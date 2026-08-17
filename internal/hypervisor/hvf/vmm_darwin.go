//go:build darwin && cgo

// Package vmm's darwin backend drives Apple's Virtualization.framework
// directly (vz_bridge_darwin.{h,m}) - no QEMU, no other third-party VMM.
// Creating and starting a virtual machine requires the calling binary to
// hold the com.apple.security.virtualization entitlement; without it,
// VZVirtualMachineConfiguration.validateWithError: fails and Create
// returns that error verbatim.
package hvf

/*
#cgo CFLAGS: -fobjc-arc
#cgo LDFLAGS: -framework Virtualization -framework Foundation
#include <stdlib.h>
#include "vz_bridge_darwin.h"
*/
import "C"

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/CYPT71/platform-factory/internal/guest"
	api "github.com/CYPT71/platform-factory/internal/microvm"
	vmruntime "github.com/CYPT71/platform-factory/internal/runtime"
)

const errorBufferSize = 512

const (
	darwinRootFSFormatKey       = "platform-factory.dev/rootfs-format"
	darwinRootFSFormatInitramfs = "initramfs"
)

// HVFLinuxRunResult is the observable result of a native macOS Linux boot.
// Virtualization.framework supplies the Linux boot protocol and virtio
// console while using Hypervisor.framework for hardware execution.
type HVFLinuxRunResult struct {
	Serial        []byte
	Stopped       bool
	SerialMatched bool
}

// RunLinuxHVF boots a host-architecture Linux kernel and optional initramfs
// through Apple's native virtualization stack. It is the macOS counterpart
// of RunLinux: no QEMU or third-party VMM participates in the boot.
//
// macAddress, when non-empty ("xx:xx:xx:xx:xx:xx", locally administered),
// attaches a single NAT-backed virtio-net device. Empty disables networking.
//
// liveWriter, when non-nil, receives each newly-appended chunk of serial
// output as soon as a poll iteration observes it - unlike the Serial
// field on the final result, which a caller only ever sees once RunLinuxHVF
// itself returns (i.e. after the guest has already stopped, or ctx was
// cancelled). A caller that needs to react to something the guest prints
// mid-boot (cmd/platform-factory's darwin native run path watches for
// cmd/microvm-init's DHCP-address report line) has no other hook to do
// that from. May be nil.
func RunLinuxHVF(ctx context.Context, kernelPath, initrdPath, commandLine, serialReady string, memoryBytes uint64, vcpus uint32, macAddress string, liveWriter io.Writer) (result HVFLinuxRunResult, err error) {
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if kernelPath == "" {
		return result, errors.New("vmm: hvf: Linux kernel path is required")
	}
	if info, statErr := os.Stat(kernelPath); statErr != nil {
		return result, fmt.Errorf("vmm: hvf: inspect Linux kernel: %w", statErr)
	} else if !info.Mode().IsRegular() || info.Size() == 0 {
		return result, errors.New("vmm: hvf: Linux kernel must be a non-empty regular file")
	}
	if initrdPath != "" {
		if info, statErr := os.Stat(initrdPath); statErr != nil {
			return result, fmt.Errorf("vmm: hvf: inspect initramfs: %w", statErr)
		} else if !info.Mode().IsRegular() || info.Size() == 0 {
			return result, errors.New("vmm: hvf: initramfs must be a non-empty regular file")
		}
	}
	if memoryBytes < 64<<20 || vcpus == 0 {
		return result, errors.New("vmm: hvf: at least one vCPU and 64 MiB of memory are required")
	}
	workDir, err := os.MkdirTemp("", "platform-factory-hvf-linux-")
	if err != nil {
		return result, fmt.Errorf("vmm: hvf: create work directory: %w", err)
	}
	defer os.RemoveAll(workDir)
	logPath := filepath.Join(workDir, "serial.log")

	cKernel := C.CString(kernelPath)
	defer C.free(unsafe.Pointer(cKernel))
	var cInitrd *C.char
	if initrdPath != "" {
		cInitrd = C.CString(initrdPath)
		defer C.free(unsafe.Pointer(cInitrd))
	}
	cCommandLine := C.CString(commandLine)
	defer C.free(unsafe.Pointer(cCommandLine))
	cLogPath := C.CString(logPath)
	defer C.free(unsafe.Pointer(cLogPath))
	var cMAC *C.char
	if macAddress != "" {
		cMAC = C.CString(macAddress)
		defer C.free(unsafe.Pointer(cMAC))
	}
	errBuf := make([]C.char, errorBufferSize)
	handle := C.vz_create_machine(
		cKernel, cInitrd, cCommandLine, cLogPath,
		C.ulonglong(memoryBytes), C.uint(vcpus),
		cMAC,
		nil,
		&errBuf[0], C.size_t(len(errBuf)),
	)
	if handle == nil {
		return result, fmt.Errorf("vmm: hvf: create Linux machine: %s", C.GoString(&errBuf[0]))
	}
	defer C.vz_machine_free(handle)
	if rc := C.vz_machine_start(handle, &errBuf[0], C.size_t(len(errBuf))); rc != 0 {
		return result, fmt.Errorf("vmm: hvf: start Linux machine: %s", C.GoString(&errBuf[0]))
	}
	defer func() {
		if state := int(C.vz_machine_state(handle)); state != vzStateStopped {
			if rc := C.vz_machine_stop(handle, &errBuf[0], C.size_t(len(errBuf))); err == nil && rc != 0 {
				err = fmt.Errorf("vmm: hvf: stop Linux machine: %s", C.GoString(&errBuf[0]))
			}
		}
		if serial, readErr := os.ReadFile(logPath); readErr == nil {
			result.Serial = serial
		} else if err == nil {
			err = fmt.Errorf("vmm: hvf: read serial console: %w", readErr)
		}
	}()

	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	var liveWritten int
	for {
		switch int(C.vz_machine_state(handle)) {
		case vzStateStopped:
			result.Stopped = true
			return result, nil
		case vzStateError:
			return result, errors.New("vmm: hvf: Linux machine entered the framework error state")
		}
		if serial, readErr := os.ReadFile(logPath); readErr == nil {
			result.Serial = serial
			if liveWriter != nil && len(serial) > liveWritten {
				_, _ = liveWriter.Write(serial[liveWritten:])
				liveWritten = len(serial)
			}
			if bytes.Contains(serial, []byte("Kernel panic - not syncing")) {
				return result, errors.New("vmm: hvf: Linux guest kernel panicked")
			}
			if serialReady != "" && bytes.Contains(serial, []byte(serialReady)) {
				result.SerialMatched = true
				return result, nil
			}
		} else if !errors.Is(readErr, os.ErrNotExist) {
			return result, fmt.Errorf("vmm: hvf: monitor serial console: %w", readErr)
		}
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-ticker.C:
		}
	}
}

// ContentResolver resolves a boot bundle's content digest (as recorded in
// api.BootBundle) to a local, already content-verified file path. The vmm
// package deliberately does not import internal/cache to avoid coupling
// the VMM backends to one specific store implementation.
type ContentResolver func(ctx context.Context, digest string) (string, error)

// ProbeNative reports whether Virtualization.framework can run a VM on
// this host. It does not by itself prove the process holds the required
// entitlement - that only surfaces as a Create() failure.
func ProbeNative(ctx context.Context) (api.Capabilities, error) {
	result := api.Capabilities{
		Architecture: runtime.GOARCH,
		Features: map[string]bool{
			"create-vm":         true,
			"rootfs":            true,
			"network":           true,
			"dns":               false,
			"port-forwarding":   true,
			"volumes":           false,
			"guest-environment": false,
		},
		Details: map[string]string{
			"backend":            "darwin-native-virtualization",
			"rootfs-format":      darwinRootFSFormatInitramfs,
			"guest-agent-device": "/dev/hvc1",
			"network-caveat": "NAT-attached virtio-net via RunLinuxHVF; guest DHCPs its own " +
				"address (ip=dhcp), host relays each --publish port to it. Unverified on real " +
				"hardware - see docs/legacy-vm-disk-boot.md.",
		},
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if C.vz_is_supported() == 0 {
		result.Details["unavailable"] = "VZVirtualMachine.isSupported is false on this host"
		return result, nil
	}
	result.Available = true
	result.Features["kernel-boot"] = true
	result.Features["initrd"] = true
	result.Features["vcpu-memory"] = true
	result.Features["serial-console"] = true
	result.Features["guest-agent-channel"] = true
	result.Features["entropy"] = true
	return result, nil
}

// DarwinVMM implements the internal microVM VMM port over Virtualization.framework. It only
// tracks machines created by this process: Load cannot resume a machine
// across a process restart (the framework does not support attaching to
// another process's VZVirtualMachine), which is a real, documented
// limitation, not an oversight - use a StateStore for status across
// restarts instead.
type DarwinVMM struct {
	Resolve        ContentResolver
	LogDir         string
	AgentConnector guest.GuestAgentConnector
	// AgentKey resolves the per-boot key already provisioned inside the
	// guest initramfs. The key is never passed to Objective-C or persisted
	// by the VMM. AgentConnector remains an injectable override for tests.
	AgentKey GuestAgentKeyResolver

	mu       sync.Mutex
	machines map[string]*darwinMachine
}

// NewDarwinVMM returns a DarwinVMM. logDir is created if missing and holds
// one serial-console log file per machine ID.
func NewDarwinVMM(resolve ContentResolver, logDir string) (*DarwinVMM, error) {
	if resolve == nil {
		return nil, errors.New("vmm: darwin backend requires a ContentResolver")
	}
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return nil, fmt.Errorf("vmm: darwin: create log directory: %w", err)
	}
	return &DarwinVMM{Resolve: resolve, LogDir: logDir, machines: map[string]*darwinMachine{}}, nil
}

func (v *DarwinVMM) Name() string { return "darwin-native" }

func (v *DarwinVMM) Probe(ctx context.Context) (api.Capabilities, error) {
	return ProbeNative(ctx)
}

func (v *DarwinVMM) Create(ctx context.Context, spec api.MachineSpec) (api.Machine, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !vmruntime.ValidMachineID(spec.ID) {
		return nil, fmt.Errorf("vmm: darwin: invalid machine id %q", spec.ID)
	}
	v.mu.Lock()
	_, exists := v.machines[spec.ID]
	v.mu.Unlock()
	if exists {
		return nil, fmt.Errorf("vmm: darwin: machine %q already exists", spec.ID)
	}
	if spec.Resources.VCPUs == 0 || spec.Resources.MemoryMiB == 0 ||
		spec.Resources.MemoryMiB > ^uint64(0)/(1024*1024) {
		return nil, errors.New("vmm: darwin: positive, non-overflowing vCPU and memory limits are required")
	}
	if err := vmruntime.ValidateBootBundle(spec.Bundle); err != nil {
		return nil, fmt.Errorf("vmm: darwin: validate boot bundle: %w", err)
	}
	if err := validateDarwinMachineSpecSupport(spec); err != nil {
		return nil, err
	}
	kernelPath, err := v.Resolve(ctx, spec.Bundle.Kernel)
	if err != nil {
		return nil, fmt.Errorf("vmm: darwin: resolve kernel: %w", err)
	}
	// The first supported rootfs transport is the deterministic initramfs
	// produced by internal/rootfs. RootFS remains independently pinned in the
	// bundle and is always resolved; Initrd must be absent so no pinned input
	// is silently ignored or confused with the application filesystem.
	initrdPath, err := v.Resolve(ctx, spec.Bundle.RootFS)
	if err != nil {
		return nil, fmt.Errorf("vmm: darwin: resolve rootfs initramfs: %w", err)
	}

	logPath := filepath.Join(v.LogDir, spec.ID+".log")
	cKernel := C.CString(kernelPath)
	defer C.free(unsafe.Pointer(cKernel))
	var cInitrd *C.char
	if initrdPath != "" {
		cInitrd = C.CString(initrdPath)
		defer C.free(unsafe.Pointer(cInitrd))
	}
	cCmdline := C.CString(strings.Join(spec.Bundle.CommandLine, " "))
	defer C.free(unsafe.Pointer(cCmdline))
	cLogPath := C.CString(logPath)
	defer C.free(unsafe.Pointer(cLogPath))

	errBuf := make([]C.char, errorBufferSize)
	agentFD := C.int(-1)
	// MachineSpec has no network intent; direct-boot callers configure networking.
	handle := C.vz_create_machine(
		cKernel, cInitrd, cCmdline, cLogPath,
		C.ulonglong(spec.Resources.MemoryMiB*1024*1024), C.uint(spec.Resources.VCPUs),
		nil,
		&agentFD,
		&errBuf[0], C.size_t(len(errBuf)),
	)
	if handle == nil {
		return nil, fmt.Errorf("vmm: darwin: create machine: %s", C.GoString(&errBuf[0]))
	}

	if agentFD < 0 {
		C.vz_machine_free(handle)
		return nil, errors.New("vmm: darwin: framework did not return the guest agent channel")
	}
	agentFile := os.NewFile(uintptr(agentFD), "platform-factory-hvc1-"+spec.ID)
	if agentFile == nil {
		C.vz_machine_free(handle)
		return nil, errors.New("vmm: darwin: adopt guest agent channel")
	}
	machine := &darwinMachine{
		id: spec.ID, handle: handle, logPath: logPath,
		agentConnector: v.AgentConnector, agentKey: v.AgentKey, agentFile: agentFile,
	}
	v.mu.Lock()
	if _, exists := v.machines[spec.ID]; exists {
		v.mu.Unlock()
		_ = machine.free()
		return nil, fmt.Errorf("vmm: darwin: machine %q already exists", spec.ID)
	}
	v.machines[spec.ID] = machine
	v.mu.Unlock()
	return machine, nil
}

func validateDarwinMachineSpecSupport(spec api.MachineSpec) error {
	var unsupported []string
	if spec.Bundle.Metadata[darwinRootFSFormatKey] != darwinRootFSFormatInitramfs {
		unsupported = append(unsupported, "rootfs format (only initramfs is supported)")
	}
	if spec.Bundle.Initrd != "" {
		unsupported = append(unsupported, "separate initrd")
	}
	if len(spec.Ports) != 0 {
		unsupported = append(unsupported, "port forwarding")
	}
	if len(spec.Volumes) != 0 {
		unsupported = append(unsupported, "volume attachment")
	}
	if len(spec.DNS) != 0 {
		unsupported = append(unsupported, "DNS configuration")
	}
	if len(spec.Env) != 0 {
		unsupported = append(unsupported, "guest environment injection")
	}
	if len(unsupported) != 0 {
		return fmt.Errorf("vmm: darwin: unsupported machine features are not implemented: %s", strings.Join(unsupported, ", "))
	}
	return nil
}

func (v *DarwinVMM) Load(ctx context.Context, id string) (api.Machine, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	v.mu.Lock()
	machine, ok := v.machines[id]
	v.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("vmm: darwin: machine %q was not created by this process; Virtualization.framework does not support attaching to another process's VM", id)
	}
	return machine, nil
}

func (v *DarwinVMM) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	v.mu.Lock()
	machine, ok := v.machines[id]
	if !ok {
		v.mu.Unlock()
		return nil
	}
	if err := machine.free(); err != nil {
		v.mu.Unlock()
		return err
	}
	delete(v.machines, id)
	v.mu.Unlock()
	return nil
}

var _ api.VMM = (*DarwinVMM)(nil)

// GuestAgentKeyResolver returns the secret provisioned into this machine's
// initramfs. Implementations should keep it in memory and return at least 32
// bytes; the Darwin backend never logs or persists it.
type GuestAgentKeyResolver func(context.Context, string) ([]byte, error)

type darwinMachine struct {
	id             string
	logPath        string
	agentConnector guest.GuestAgentConnector
	agentKey       GuestAgentKeyResolver
	agentFile      *os.File
	agentClaimed   bool

	mu     sync.Mutex
	handle *C.vz_machine_t
	freed  bool
}

func (m *darwinMachine) ID() string { return m.id }

func (m *darwinMachine) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.freed {
		return errors.New("vmm: darwin: machine already deleted")
	}
	errBuf := make([]C.char, errorBufferSize)
	if rc := C.vz_machine_start(m.handle, &errBuf[0], C.size_t(len(errBuf))); rc != 0 {
		return fmt.Errorf("vmm: darwin: start: %s", C.GoString(&errBuf[0]))
	}
	return nil
}

func (m *darwinMachine) Stop(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.freed {
		return errors.New("vmm: darwin: machine already deleted")
	}
	errBuf := make([]C.char, errorBufferSize)
	if rc := C.vz_machine_stop(m.handle, &errBuf[0], C.size_t(len(errBuf))); rc != 0 {
		return fmt.Errorf("vmm: darwin: stop: %s", C.GoString(&errBuf[0]))
	}
	return nil
}

func (m *darwinMachine) Status(ctx context.Context) (api.MachineStatus, error) {
	if err := ctx.Err(); err != nil {
		return api.MachineStatus{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.freed {
		return api.MachineStatus{ID: m.id, State: api.StateStopped, UpdatedAt: time.Now()}, nil
	}
	state := vzStateToAPI(int(C.vz_machine_state(m.handle)))
	return api.MachineStatus{ID: m.id, State: state, UpdatedAt: time.Now()}, nil
}

func (m *darwinMachine) Logs(ctx context.Context, w io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := os.Open(m.logPath)
	if err != nil {
		return fmt.Errorf("vmm: darwin: read logs: %w", err)
	}
	defer file.Close()
	_, err = io.Copy(w, file)
	return err
}

// Agent claims the native hvc1 attachment exactly once and authenticates it
// with the per-boot key provisioned by the caller.
func (m *darwinMachine) Agent(ctx context.Context) (api.GuestAgent, error) {
	if m.agentConnector != nil {
		return guest.OpenAgent(ctx, m.id, m.agentConnector)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	if m.freed {
		m.mu.Unlock()
		return nil, errors.New("vmm: darwin: machine already deleted")
	}
	if m.agentFile == nil {
		m.mu.Unlock()
		return nil, errors.New("vmm: darwin: native guest agent channel is not configured")
	}
	if m.agentClaimed {
		m.mu.Unlock()
		return nil, errors.New("vmm: darwin: native guest agent channel was already claimed")
	}
	keyResolver := m.agentKey
	m.mu.Unlock()
	if keyResolver == nil {
		return nil, errors.New("vmm: darwin: guest agent key is not configured")
	}
	resolvedKey, err := keyResolver(ctx, m.id)
	if err != nil {
		return nil, fmt.Errorf("vmm: darwin: resolve guest agent key: %w", err)
	}
	key := append([]byte(nil), resolvedKey...)
	defer clear(key)
	m.mu.Lock()
	if m.freed || m.agentClaimed {
		m.mu.Unlock()
		return nil, errors.New("vmm: darwin: native guest agent channel is no longer available")
	}
	m.agentClaimed = true
	file := m.agentFile
	m.mu.Unlock()
	return guest.OpenAgent(ctx, m.id, func(context.Context, string) (io.ReadWriteCloser, []byte, error) {
		return file, key, nil
	})
}

func (m *darwinMachine) free() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.freed {
		return nil
	}
	if state := int(C.vz_machine_state(m.handle)); state == vzStateRunning || state == vzStateStarting {
		return errors.New("vmm: darwin: stop the machine before deleting it")
	}
	C.vz_machine_free(m.handle)
	if m.agentFile != nil {
		_ = m.agentFile.Close()
		m.agentFile = nil
	}
	m.freed = true
	return nil
}

// VZVirtualMachineState raw values (Virtualization.framework, stable
// across SDK versions per Apple's header comments).
const (
	vzStateStopped = iota
	vzStateRunning
	vzStatePaused
	vzStateError
	vzStateStarting
	vzStatePausing
	vzStateResuming
	vzStateStopping
	vzStateSaving
	vzStateRestoring
)

// vzStateToAPI maps Virtualization.framework's ten-value state enum onto
// api.MachineState's coarser four values. Transitional states map to
// whichever side of the transition they are closer to; this is a
// best-effort compression, not a lossless mapping.
func vzStateToAPI(state int) api.MachineState {
	switch state {
	case vzStateRunning, vzStatePausing, vzStateResuming, vzStateStopping:
		return api.StateRunning
	case vzStateError:
		return api.StateFailed
	case vzStateStarting, vzStateSaving, vzStateRestoring:
		return api.StateCreated
	default: // Stopped, Paused
		return api.StateStopped
	}
}
