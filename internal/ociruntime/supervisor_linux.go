// This file's supervisor calls directly into internal/hypervisor/kvm's
// native run loop (RunLinuxWithOptions), which is currently implemented
// for linux/amd64 only - see kvm_run_linux_amd64.go. Restricting the
// build tag to match is what actually reflects that: an unrestricted
// "linux" tag here compiles but fails to link on linux/arm64, breaking
// `go build ./...` for the whole repository on that platform, not just
// this package.
//go:build linux && amd64

package ociruntime

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/CYPT71/secure-oci-base/internal/guesttransport"
	"github.com/CYPT71/secure-oci-base/internal/hypervisor/kvm"
	"github.com/CYPT71/secure-oci-base/internal/hypervisor/sandbox"
)

const (
	annotationKernelPath   = "platform-factory.dev/kernel-path"
	annotationKernelDigest = "platform-factory.dev/kernel-digest"
	annotationInitrdPath   = "platform-factory.dev/initramfs-path"
	annotationInitrdDigest = "platform-factory.dev/initramfs-digest"
	annotationMemoryMiB    = "platform-factory.dev/memory-mib"
	annotationVCPUs        = "platform-factory.dev/vcpus"

	// annotationSandboxCgroups/annotationSandboxNamespaces opt this
	// supervisor into cgroup v2 resource limits and NET/UTS/IPC namespace
	// isolation (see internal/hypervisor/sandbox.Config.NamespaceList and
	// .CgroupParent). Both default to off, unlike seccomp and capability
	// dropping below: they need a host that has actually delegated a
	// cgroup subtree, or granted CAP_SYS_ADMIN, to the account this
	// supervisor runs as - requirements a bare "go test"/first-boot
	// environment won't generally satisfy, whereas a real deployment
	// under systemd or a container orchestrator generally will. Turning
	// them on unconditionally would make the runtime unable to start any
	// container at all on a host that hasn't set that up. Seccomp and
	// capability dropping have no equivalent host precondition, so they
	// are not gated the same way - see sandboxConfigForSupervisor.
	annotationSandboxCgroups    = "platform-factory.dev/sandbox-cgroups"
	annotationSandboxNamespaces = "platform-factory.dev/sandbox-namespaces"

	// annotationBlockDevicePath/annotationBlockDeviceReadonly attach one
	// virtio-blk device backed by a host file. Unlike
	// annotationSandbox{Cgroups,Namespaces} above, an explicit request
	// here that can't be satisfied fails guest launch rather than
	// silently booting without the device: a missing disk is a
	// functional gap the caller asked for and would otherwise have no
	// way to notice, not a hardening nice-to-have with a safe fallback.
	annotationBlockDevicePath     = "platform-factory.dev/block-device-path"
	annotationBlockDeviceReadonly = "platform-factory.dev/block-device-readonly"

	// annotationNetworkTAP opts this container into one virtio-net
	// device backed by a host TAP interface (internal/hypervisor/kvm's
	// OpenTAP) - see that package's own doc comment on ProbeTAPSupport
	// for exactly what "rootless" does and doesn't mean here: a process
	// with real CAP_NET_ADMIN in its actual network namespace (typically
	// root, or a deployment that granted the capability explicitly) gets
	// a real TAP device; anything else fails closed with a clear reason
	// rather than silently booting a guest with no network. Turning real
	// outside connectivity into something available to a fully
	// unprivileged rootless Podman invocation would additionally need a
	// userspace NAT layer (the same job slirp4netns/pasta do for
	// Podman's own container networking), which is out of scope here -
	// TAP creation alone does not provide that, regardless of privilege.
	annotationNetworkTAP = "platform-factory.dev/network-tap"
)

func LaunchSupervisor(ctx context.Context, store *Store, id, executable, pidFile string) error {
	readyReader, readyWriter, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("oci runtime: create supervisor readiness pipe: %w", err)
	}
	defer readyReader.Close()
	command := exec.CommandContext(ctx, executable, "__serve", "--root", store.Dir(), "--ready-fd", "3", id)
	command.Stdin = nil
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.ExtraFiles = []*os.File{readyWriter}
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	// IPC and UTS namespace isolation is applied here, at process
	// creation, rather than self-applied later in ServeSupervisor via
	// internal/hypervisor/sandbox.applyNamespaces, for one consistent
	// isolation boundary established before this process runs any code
	// at all.
	//
	// CLONE_NEWPID is deliberately NOT requested, even though it was at
	// one point: this whole function's and ServeSupervisor's PID
	// bookkeeping assumes parent and child share one PID namespace -
	// store.SetSupervisor below records command.Process.Pid, the
	// host-visible PID as this parent sees it, and ServeSupervisor's own
	// startup loop waits for state.PID == os.Getpid() inside the child.
	// Under CLONE_NEWPID the child becomes PID 1 of a *new* namespace, so
	// os.Getpid() there is always 1, that comparison never succeeds, and
	// every single container launch fails closed with "supervisor PID
	// mismatch". Isolating the PID namespace would need the state store
	// to stop keying on a raw host PID first; that's separate work, not
	// something to bolt on by only gating the clone flag.
	//
	// Requesting even IPC/UTS without CLONE_NEWUSER needs CAP_SYS_ADMIN
	// in the caller's own namespace - the process running this function
	// (platform-factory-runtime create, invoked by Podman/Docker/containerd as
	// the OCI runtime), not the child being launched. That privilege is
	// not guaranteed by this codebase's own contract, so this probes for
	// it first: without it, clone() itself would fail and command.Start()
	// below would return an error, turning an isolation improvement into
	// a complete guest-launch regression on any host that lacks it.
	// Falling back to Setsid-only here reproduces this function's exact
	// pre-existing behavior on such a host.
	//
	// Mount and network namespaces are deliberately excluded even when
	// available: this process needs continued access to the store
	// directory tree and, for TAP networking, the host network
	// namespace. An isolated mount namespace could hide a device or
	// path a caller supplied by host path, so the safer default is to
	// leave those alone until the paths this process actually needs
	// are enumerated and can be bind-mounted in explicitly.
	if sandbox.ProbeSandbox().Namespaces {
		command.SysProcAttr.Cloneflags = syscall.CLONE_NEWIPC | syscall.CLONE_NEWUTS
	}
	if err := command.Start(); err != nil {
		readyWriter.Close()
		return fmt.Errorf("oci runtime: launch supervisor: %w", err)
	}
	readyWriter.Close()
	if err := store.SetSupervisor(ctx, id, command.Process.Pid); err != nil {
		_ = command.Process.Kill()
		return err
	}
	if pidFile != "" {
		if err := os.WriteFile(pidFile, []byte(strconv.Itoa(command.Process.Pid)), 0o600); err != nil {
			_ = command.Process.Kill()
			return fmt.Errorf("oci runtime: write pid file: %w", err)
		}
	}
	ready := make(chan error, 1)
	go func() {
		var response [6]byte
		_, err := io.ReadFull(readyReader, response[:])
		if err == nil && string(response[:]) != "READY\n" {
			err = errors.New("unexpected readiness response")
		}
		ready <- err
	}()
	select {
	case err := <-ready:
		if err != nil {
			_ = command.Process.Kill()
			return fmt.Errorf("oci runtime: supervisor failed before READY: %w", err)
		}
	case <-ctx.Done():
		_ = command.Process.Kill()
		return ctx.Err()
	case <-time.After(5 * time.Second):
		_ = command.Process.Kill()
		return errors.New("oci runtime: supervisor did not become ready within 5 seconds")
	}
	return nil
}

// applyVMMSandbox hardens the __serve process (the VMM host) against
// the consequences of a KVM or guest-transport bug being exploited
// from inside. PID/IPC/UTS namespace isolation is already in place by
// the time this runs - see LaunchSupervisor's Cloneflags - so this
// only applies what must be self-applied from inside the process
// itself:
//
//   - PR_SET_NO_NEW_PRIVS, unconditionally: it needs no privilege and
//     cannot fail for lack of it, and this process never execs a
//     setuid or file-capability binary, so its permanent, process-wide
//     nature has no downside here.
//   - A delegated cgroup with a PID ceiling, only when ProbeSandbox
//     confirms the parent cgroup has actually delegated a controller
//     to children - routinely unavailable outside a delegated scope
//     (systemd Delegate=yes, or root), so this is skipped rather than
//     treated as fatal on a host without it. A CPU or memory ceiling is
//     deliberately not requested: guest resource needs are set by the
//     annotation-driven memory-mib/vcpus configuration already
//     enforced elsewhere, and constraining the host cgroup on top of
//     that risks starving KVM itself rather than the guest.
//   - A capability bounding-set drop of every capability guest
//     execution does not need, only when ProbeSandbox confirms
//     CAP_SETPCAP is held. The accompanying setuid-to-nobody step
//     dropPrivileges would otherwise perform is deliberately skipped:
//     this process needs continued access to an already-open /dev/kvm,
//     and this change cannot verify locally (developed without a
//     Linux host) whether this deployment's DAC access to that device
//     depends on UID rather than group membership - see
//     sandbox.DropBoundingCapabilities's doc comment.
//
// This has been cross-compiled for linux/amd64 and reviewed against
// each primitive's documented kernel behavior, but has not been
// exercised against a real /dev/kvm by this change locally.
// ci-sandbox.yml and ci-microvm.yml, which run on real Linux hardware,
// are the actual proof this does not break guest boot.
func applyVMMSandbox() error {
	sb := sandbox.NewSandbox(sandbox.Config{DropCapabilities: sandbox.AllCapabilities, PIDsLimit: 256})
	if err := sb.ApplySeccomp(); err != nil {
		return fmt.Errorf("oci runtime: apply no_new_privs: %w", err)
	}
	support := sandbox.ProbeSandbox()
	if support.Cgroups {
		if err := sb.ApplyCgroups(); err != nil {
			return fmt.Errorf("oci runtime: apply cgroup limits: %w", err)
		}
	}
	if support.CapabilityBoundingDrop {
		if err := sb.DropBoundingCapabilities(); err != nil {
			return fmt.Errorf("oci runtime: drop capability bounding set: %w", err)
		}
	}
	return nil
}

func ServeSupervisor(ctx context.Context, store *Store, id string, readyFD int) (err error) {
	startAcknowledged := false
	var command *net.UnixConn
	var state State
	deadline := time.Now().Add(2 * time.Second)
	for {
		var found bool
		state, found, err = store.Get(ctx, id)
		if err != nil || !found {
			return fmt.Errorf("oci runtime: load supervisor state: %w", err)
		}
		if state.PID == os.Getpid() {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("oci runtime: supervisor PID mismatch: state=%d process=%d", state.PID, os.Getpid())
		}
		time.Sleep(5 * time.Millisecond)
	}
	incarnation := stateIncarnation(state)
	socketPath := store.controlSocketPath(state)
	_ = os.Remove(socketPath)
	address := &net.UnixAddr{Name: socketPath, Net: "unix"}
	listener, err := net.ListenUnix("unix", address)
	if err != nil {
		return fmt.Errorf("oci runtime: listen supervisor command socket: %w", err)
	}
	defer listener.Close()
	defer os.Remove(socketPath)
	if err := os.Chmod(socketPath, 0o600); err != nil {
		return fmt.Errorf("oci runtime: secure supervisor command socket: %w", err)
	}
	if readyFD < 3 {
		return errors.New("oci runtime: supervisor readiness descriptor is invalid")
	}
	ready := os.NewFile(uintptr(readyFD), "platform-factory-supervisor-ready")
	if ready == nil {
		return errors.New("oci runtime: open supervisor readiness descriptor")
	}
	if _, err := ready.Write([]byte("READY\n")); err != nil {
		ready.Close()
		return fmt.Errorf("oci runtime: publish supervisor readiness: %w", err)
	}
	if err := ready.Close(); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT)
	defer stop()
	defer func() {
		if err != nil && !startAcknowledged && command != nil {
			_ = writeStartResponse(command, state, incarnation, false, err)
		}
		_ = store.SetStatus(context.Background(), id, "stopped")
	}()
	command, err = acceptStartCommand(ctx, listener, store, state, incarnation)
	if err != nil {
		return err
	}
	defer command.Close()
	if err := applyVMMSandbox(); err != nil {
		return err
	}
	config, err := LoadConfig(state.Bundle)
	if err != nil {
		return err
	}
	// change_profile confines the calling OS thread, not the Go process as
	// a whole - a goroutine the Go scheduler later migrates elsewhere would
	// carry on unconfined. LockOSThread pins this goroutine for the rest of
	// its life first, so vmm.RunLinuxWithOptions's own KVM_RUN loop (locked
	// to a thread for its own, unrelated reason: vcpu fds are thread-
	// affine) ends up on this same, now-confined thread rather than a
	// fresh one. Never unlocked on purpose: if this goroutine ever exits
	// without a matching Unlock, Go terminates the underlying OS thread
	// instead of returning it to the pool, which is exactly what should
	// happen to one a security profile was permanently applied to.
	//
	// This still only protects this goroutine's own call path - reading
	// the bundle's rootfs into the guest initramfs, the pinned kernel/init
	// binaries, and every /dev/kvm ioctl RunLinuxWithOptions makes - not
	// the separate signal-forwarding goroutine ServeSupervisor spawns
	// below. That's a deliberate scope limit, not an oversight: full-
	// process confinement of an already-running, already-multi-threaded Go
	// binary (every GC/netpoller/sysmon thread the runtime started before
	// this function ever ran, plus every future goroutine that might land
	// on any of them) is a substantially larger problem than confining the
	// one goroutine that does the security-sensitive work here.
	//
	// Unconditional now, unlike the AppArmor transition below it staying
	// profile-gated: sandbox.Sandbox.Apply's seccomp filter (installed a
	// few lines down) applies to whichever OS thread happens to be current
	// when it's installed, same scoping rule, and unlike an AppArmor
	// profile it is not optional here - every container gets one.
	runtime.LockOSThread()
	if err := applyApparmorProfile(config.Process.ApparmorProfile); err != nil {
		return err
	}
	sandboxConfig, err := sandboxConfigForSupervisor(state.Annotations)
	if err != nil {
		return err
	}
	guestSandbox := sandbox.NewSandbox(sandboxConfig)
	if err := guestSandbox.Apply(); err != nil {
		return fmt.Errorf("oci runtime: apply VMM sandbox: %w", err)
	}
	defer func() { _ = guestSandbox.Cleanup() }()
	// Last of all, on this now-permanently-locked thread: the real
	// classic-BPF filter (see sandbox.Sandbox.ApplyStrictSeccomp's doc
	// comment for why it must run here and not from applyVMMSandbox above,
	// which runs before LockOSThread and would install it on a thread this
	// goroutine is about to leave). Everything from here through
	// RunLinuxWithOptions - initramfs assembly, kernel/init loading, the
	// KVM_RUN loop itself - executes under it.
	if err := guestSandbox.ApplyStrictSeccomp(); err != nil {
		return fmt.Errorf("oci runtime: apply strict VMM seccomp filter: %w", err)
	}
	sessionKey, err := generateGuestSessionKey(rand.Reader)
	if err != nil {
		return err
	}
	defer clear(sessionKey)
	initrdPath, cleanup, err := buildGuestInitramfs(state.Bundle, config, sessionKey)
	if err != nil {
		return err
	}
	defer cleanup()
	initrd, err := os.ReadFile(initrdPath)
	if err != nil {
		return err
	}
	defer clear(initrd)
	kernel, memoryBytes, err := loadPinnedBoot(state.Annotations)
	if err != nil {
		return err
	}
	virtioOptions, cleanupBlockDevice, err := virtioDevicesForSupervisor(state.Annotations)
	if err != nil {
		return err
	}
	defer cleanupBlockDevice()
	hostChannel, guestChannel := net.Pipe()
	agent, err := guesttransport.NewAgent(hostChannel, sessionKey)
	if err != nil {
		hostChannel.Close()
		guestChannel.Close()
		return fmt.Errorf("oci runtime: create authenticated guest transport: %w", err)
	}
	defer agent.Close()
	runContext, cancelRun := context.WithCancel(ctx)
	signalServerDone := make(chan error, 1)
	go func() {
		signalServerDone <- serveSignalCommands(
			runContext, listener, store, state, incarnation,
			func(ctx context.Context, signal syscall.Signal) error {
				signalContext, cancel := context.WithTimeout(ctx, 5*time.Second)
				defer cancel()
				err := agent.Signal(signalContext, guestSignalName(signal))
				if err == nil {
					return nil
				}
				// The workload may exit quickly enough that COM1 reports its
				// terminal status and cancels KVM while the authenticated signal
				// response is still draining from COM2. In that race the requested
				// outcome has succeeded even though the transport returns EOF.
				for attempts := 0; attempts < 25; attempts++ {
					terminal, found, stateErr := store.Get(context.Background(), id)
					if stateErr == nil && found && terminal.Status == "stopped" {
						return nil
					}
					select {
					case <-ctx.Done():
						terminal, found, stateErr := store.Get(context.Background(), id)
						if stateErr == nil && found && terminal.Status == "stopped" {
							return nil
						}
						return err
					case <-time.After(10 * time.Millisecond):
					}
				}
				return err
			},
		)
	}()
	defer func() {
		cancelRun()
		<-signalServerDone
	}()
	commandLine := "console=ttyS0,115200 earlycon=uart,io,0x3f8,115200 ignore_loglevel panic=0 rdinit=/sbin/init 8250.nr_uarts=2 platform_factory.guest_transport=ttyS1 platform_factory.guest_config=/etc/platform-factory/guest-transport.json"
	result, err := kvm.RunLinuxWithOptions(
		runContext,
		memoryBytes,
		kernel,
		initrd,
		commandLine,
		1<<20,
		kvm.LinuxRunOptions{
			GuestChannel:   guestChannel,
			BlockDevices:   virtioOptions.BlockDevices,
			NetworkDevices: virtioOptions.NetworkDevices,
			// A relayed termination signal makes microvm-init log this exact
			// line right before it calls poweroff(). Without ACPI, poweroff
			// fails and the kernel falls back to its idle halt loop, which
			// runs with interrupts enabled - a normal, wakeable park, not
			// the unwakeable halt KVM reports as KVM_EXIT_HLT. That leaves
			// RunLinuxWithOptions no exit event to notice, so without this
			// watcher the run would block inside KVM_RUN until an external
			// SIGKILL arrives. Once the guest itself has said there's
			// nothing left to run, host-side cancellation is what actually
			// ends the VM (interrupting the blocked KVM_RUN via the
			// existing ctx-cancellation path), not any guest cooperation
			// after this point - the guest's own scheduler is already gone
			// by the time it reaches machine_halt(), so nothing in it can
			// answer a request over the guest-transport channel anymore.
			SerialWriter: newShutdownLogWatcher(os.Stdout, func(exitCode uint32) {
				_ = store.SetExited(context.Background(), id, exitCode, time.Now())
				cancelRun()
			}),
			OnStarted: func() error {
				if err := store.SetStatus(ctx, id, "running"); err != nil {
					return err
				}
				if err := writeStartResponse(command, state, incarnation, true, nil); err != nil {
					return err
				}
				startAcknowledged = true
				return nil
			},
		},
	)
	_ = result
	return err
}

func generateGuestSessionKey(source io.Reader) ([]byte, error) {
	if source == nil {
		return nil, errors.New("oci runtime: guest session entropy source is required")
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(source, key); err != nil {
		clear(key)
		return nil, fmt.Errorf("oci runtime: generate guest session key: %w", err)
	}
	return key, nil
}

func acceptStartCommand(ctx context.Context, listener *net.UnixListener, store *Store, state State, incarnation string) (*net.UnixConn, error) {
	for {
		if err := listener.SetDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
			return nil, err
		}
		connection, err := listener.AcceptUnix()
		if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("oci runtime: accept supervisor command: %w", err)
		}
		var request startResult
		decodeErr := json.NewDecoder(io.LimitReader(connection, 4097)).Decode(&request)
		current, found, stateErr := store.Get(ctx, state.ID)
		valid := decodeErr == nil && stateErr == nil && found &&
			request.Command == "start" && request.ID == state.ID &&
			request.PID == os.Getpid() && current.PID == os.Getpid() &&
			request.Incarnation == incarnation && stateIncarnation(current) == incarnation
		if valid {
			return connection, nil
		}
		cause := errors.New("invalid or stale supervisor start command")
		if decodeErr != nil {
			cause = fmt.Errorf("decode supervisor start command: %w", decodeErr)
		} else if stateErr != nil {
			cause = stateErr
		}
		_ = writeStartResponse(connection, state, incarnation, false, cause)
		connection.Close()
	}
}

func writeStartResponse(connection io.Writer, state State, incarnation string, started bool, cause error) error {
	response := startResult{
		ID: state.ID, Incarnation: incarnation, PID: state.PID, Started: started,
	}
	if cause != nil {
		response.Error = cause.Error()
		if len(response.Error) > 1024 {
			response.Error = response.Error[:1024]
		}
	}
	if err := json.NewEncoder(connection).Encode(response); err != nil {
		return fmt.Errorf("oci runtime: publish supervisor start response: %w", err)
	}
	return nil
}

func serveSignalCommands(
	ctx context.Context,
	listener *net.UnixListener,
	store *Store,
	state State,
	incarnation string,
	deliver func(context.Context, syscall.Signal) error,
) error {
	for {
		if err := listener.SetDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
			return err
		}
		connection, err := listener.AcceptUnix()
		if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
			if ctx.Err() != nil {
				return nil
			}
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("oci runtime: accept supervisor signal command: %w", err)
		}
		var request startResult
		decodeErr := json.NewDecoder(io.LimitReader(connection, 4097)).Decode(&request)
		current, found, stateErr := store.Get(ctx, state.ID)
		valid := decodeErr == nil && stateErr == nil && found &&
			request.Command == "signal" && request.ID == state.ID &&
			request.PID == os.Getpid() && current.PID == os.Getpid() &&
			request.Incarnation == incarnation && stateIncarnation(current) == incarnation &&
			guestTerminationSignal(syscall.Signal(request.Signal))
		if !valid {
			cause := errors.New("invalid or stale supervisor signal command")
			if decodeErr != nil {
				cause = fmt.Errorf("decode supervisor signal command: %w", decodeErr)
			} else if stateErr != nil {
				cause = stateErr
			}
			_ = writeSignalResponse(connection, state, incarnation, request.Signal, false, cause)
			connection.Close()
			continue
		}
		if deliver == nil {
			cause := errors.New("guest signal relay is unavailable")
			_ = writeSignalResponse(connection, state, incarnation, request.Signal, false, cause)
			connection.Close()
			continue
		}
		if err := deliver(ctx, syscall.Signal(request.Signal)); err != nil {
			_ = writeSignalResponse(connection, state, incarnation, request.Signal, false, err)
			connection.Close()
			continue
		}
		if err := writeSignalResponse(connection, state, incarnation, request.Signal, true, nil); err != nil {
			connection.Close()
			return err
		}
		connection.Close()
		return nil
	}
}

func writeSignalResponse(connection io.Writer, state State, incarnation string, signal int, signaled bool, cause error) error {
	response := startResult{
		Command: "signal", ID: state.ID, Incarnation: incarnation,
		PID: state.PID, Signal: signal, Signaled: signaled,
	}
	if cause != nil {
		response.Error = cause.Error()
		if len(response.Error) > 1024 {
			response.Error = response.Error[:1024]
		}
	}
	if err := json.NewEncoder(connection).Encode(response); err != nil {
		return fmt.Errorf("oci runtime: publish supervisor signal response: %w", err)
	}
	return nil
}

// shutdownMarker is the fixed portion of the line cmd/microvm-init's
// realMainChild logs over COM1 right before it attempts poweroff, once the
// supervised child has exited. See newShutdownLogWatcher.
const shutdownMarker = "component=microvm-init operation=supervise phase=child-exit"

// newShutdownLogWatcher wraps a serial console writer to notice, purely from
// the host side, the one moment a guest-initiated shutdown becomes
// observable: cmd/microvm-init logging that its child has exited and it's
// about to call poweroff. It cannot wait for anything from the guest after
// that - see the call site's comment - so it calls cancel as soon as the
// marker appears in the byte stream, which handleLinuxSerialIO delivers one
// byte at a time.
func newShutdownLogWatcher(underlying io.Writer, exited func(uint32)) io.Writer {
	return &shutdownLogWatcher{underlying: underlying, exited: exited}
}

type shutdownLogWatcher struct {
	underlying io.Writer
	exited     func(uint32)
	tail       []byte
	triggered  bool
}

func (w *shutdownLogWatcher) Write(p []byte) (int, error) {
	n, err := w.underlying.Write(p)
	if !w.triggered {
		w.tail = append(w.tail, p...)
		if excess := len(w.tail) - 2*len(shutdownMarker); excess > 0 {
			w.tail = w.tail[excess:]
		}
		if lineEnd := bytes.LastIndexByte(w.tail, '\n'); lineEnd >= 0 {
			if match := shutdownExitPattern.FindSubmatch(w.tail[:lineEnd]); len(match) == 2 {
				code, parseErr := strconv.ParseUint(string(match[1]), 10, 8)
				if parseErr == nil {
					w.triggered = true
					w.exited(uint32(code))
				}
			}
		}
	}
	return n, err
}

var shutdownExitPattern = regexp.MustCompile(regexp.QuoteMeta(shutdownMarker) + ` exit_code=([0-9]{1,3})(?: |$)`)

// sandboxConfigForSupervisor builds the sandbox.Config this supervisor
// applies to itself before touching the bundle's rootfs or /dev/kvm.
// Seccomp is unconditional - this process needs no ambient privilege to
// install it, and the filter's syscall allow-list was derived from what
// this exact call path actually does (see sandbox.DefaultSeccompProfile's
// doc comment). Capability-bounding-set dropping is handled separately, by
// applyVMMSandbox above (probe-gated, and deliberately without a paired
// setuid - see that function's doc comment); DropPrivileges is
// deliberately left false here for the same reason: setuid(65534) can
// succeed inside a rootless Podman invocation (which maps the invoking
// user to UID 0 in its own user namespace, satisfying dropPrivileges'
// euid==0 check) and then break /dev/kvm access outright on a host where
// that device was chowned to a specific UID rather than a shared group.
// Cgroups and namespaces stay annotation-gated - see
// annotationSandboxCgroups/annotationSandboxNamespaces.
func sandboxConfigForSupervisor(annotations map[string]string) (sandbox.Config, error) {
	config := sandbox.Config{
		Seccomp: true,
	}
	cgroups, err := annotationBool(annotations, annotationSandboxCgroups)
	if err != nil {
		return sandbox.Config{}, err
	}
	config.Cgroups = cgroups
	namespaces, err := annotationBool(annotations, annotationSandboxNamespaces)
	if err != nil {
		return sandbox.Config{}, err
	}
	config.Namespaces = namespaces
	return config, nil
}

func annotationBool(annotations map[string]string, key string) (bool, error) {
	raw, ok := annotations[key]
	if !ok || raw == "" {
		return false, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("oci runtime: %s annotation is invalid: %w", key, err)
	}
	return value, nil
}

// virtioDevicesForSupervisor builds the optional virtio-blk/virtio-net
// devices this container's annotations request (see
// annotationBlockDevicePath/annotationNetworkTAP's own doc comments for
// why an explicit, unsatisfiable request fails guest launch here instead
// of silently degrading the way the sandbox annotations do).
//
// Ownership is asymmetric between the two device kinds, matching
// kvm.BlockDeviceOptions/NetworkDeviceOptions' own doc comments: the
// block device backend file is owned by this function's caller for the
// guest's whole lifetime (kvm.RunLinuxWithOptions never closes it), so the
// returned cleanup closes it once, whenever the caller is done - but the
// TAP descriptor's ownership transfers to RunLinuxWithOptions the moment
// it is successfully placed into the returned options.NetworkDevices:
// RunLinuxWithOptions's own shutdown path depends on being the one to
// close it (that Close is what unblocks linux_virtio_net.go's blocked
// TAP Read on guest shutdown, not just a resource release), so this
// function must never also schedule a close for it once that hand-off
// has happened - every error path below that occurs after opening TAP
// closes it directly, right there, instead of leaving it for the
// returned cleanup to find.
func virtioDevicesForSupervisor(annotations map[string]string) (options kvm.LinuxRunOptions, cleanupBlockDevice func(), err error) {
	cleanupBlockDevice = func() {}
	if path := annotations[annotationBlockDevicePath]; path != "" {
		readOnly, err := annotationBool(annotations, annotationBlockDeviceReadonly)
		if err != nil {
			return options, cleanupBlockDevice, err
		}
		flags := os.O_RDWR
		if readOnly {
			flags = os.O_RDONLY
		}
		backend, err := os.OpenFile(path, flags, 0)
		if err != nil {
			return options, cleanupBlockDevice, fmt.Errorf("oci runtime: open %s backend %q: %w", annotationBlockDevicePath, path, err)
		}
		cleanupBlockDevice = func() { _ = backend.Close() }
		info, err := backend.Stat()
		if err != nil {
			return options, cleanupBlockDevice, fmt.Errorf("oci runtime: stat %s backend %q: %w", annotationBlockDevicePath, path, err)
		}
		capacity := uint64(info.Size())
		if capacity == 0 || capacity%512 != 0 {
			return options, cleanupBlockDevice, fmt.Errorf("oci runtime: %s backend %q size %d must be a positive multiple of 512", annotationBlockDevicePath, path, capacity)
		}
		options.BlockDevices = []kvm.BlockDeviceOptions{{Backend: backend, Capacity: capacity, ReadOnly: readOnly}}
	}

	wantTAP, err := annotationBool(annotations, annotationNetworkTAP)
	if err != nil {
		return options, cleanupBlockDevice, err
	}
	if wantTAP {
		support := kvm.ProbeTAPSupport()
		if !support.Available {
			return options, cleanupBlockDevice, fmt.Errorf("oci runtime: %s was requested but is unavailable: %s", annotationNetworkTAP, support.Reason)
		}
		tap, _, err := kvm.OpenTAP("")
		if err != nil {
			return options, cleanupBlockDevice, fmt.Errorf("oci runtime: open TAP device: %w", err)
		}
		mac := make(net.HardwareAddr, 6)
		if _, err := rand.Read(mac); err != nil {
			_ = tap.Close()
			return options, cleanupBlockDevice, fmt.Errorf("oci runtime: generate guest MAC address: %w", err)
		}
		mac[0] = (mac[0] &^ 0x01) | 0x02 // locally administered, unicast
		options.NetworkDevices = []kvm.NetworkDeviceOptions{{TAP: tap, MAC: mac}}
	}

	return options, cleanupBlockDevice, nil
}

func loadPinnedBoot(annotations map[string]string) ([]byte, uint64, error) {
	kernel, err := readPinnedFile(annotations[annotationKernelPath], annotations[annotationKernelDigest], "kernel")
	if err != nil {
		return nil, 0, err
	}
	memoryMiB := uint64(128)
	if raw := annotations[annotationMemoryMiB]; raw != "" {
		memoryMiB, err = strconv.ParseUint(raw, 10, 64)
		if err != nil || memoryMiB < 64 || memoryMiB > 1<<20 {
			return nil, 0, errors.New("oci runtime: memory-mib annotation is invalid")
		}
	}
	vcpus := uint64(1)
	if raw := annotations[annotationVCPUs]; raw != "" {
		vcpus, err = strconv.ParseUint(raw, 10, 32)
		if err != nil || vcpus != 1 {
			return nil, 0, errors.New("oci runtime: current KVM backend requires exactly one vCPU")
		}
	}
	return kernel, memoryMiB << 20, nil
}

func guestSignalName(signal syscall.Signal) string {
	switch signal {
	case syscall.SIGINT:
		return "INT"
	case syscall.SIGQUIT:
		return "QUIT"
	case syscall.SIGKILL:
		return "KILL"
	case syscall.SIGTERM:
		return "TERM"
	default:
		return ""
	}
}

func readPinnedFile(path, digest, label string) ([]byte, error) {
	if path == "" || !strings.HasPrefix(digest, "sha256:") || len(digest) != 71 {
		return nil, fmt.Errorf("oci runtime: %s path and sha256 digest annotations are required", label)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("oci runtime: %s must be a real regular file", label)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	actual := "sha256:" + hex.EncodeToString(sum[:])
	if actual != digest {
		return nil, fmt.Errorf("oci runtime: %s digest mismatch: got %s", label, actual)
	}
	return data, nil
}
