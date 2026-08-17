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

	"github.com/CYPT71/platform-factory/internal/guesttransport"
	"github.com/CYPT71/platform-factory/internal/hypervisor/kvm"
	"github.com/CYPT71/platform-factory/internal/hypervisor/sandbox"
	"github.com/CYPT71/platform-factory/internal/observability"
)

const (
	maxKernelBytes = int64(256 << 20)
	maxInitBytes   = int64(64 << 20)

	annotationKernelPath   = "platform-factory.dev/kernel-path"
	annotationKernelDigest = "platform-factory.dev/kernel-digest"
	annotationInitrdPath   = "platform-factory.dev/initramfs-path"
	annotationInitrdDigest = "platform-factory.dev/initramfs-digest"
	annotationMemoryMiB    = "platform-factory.dev/memory-mib"
	annotationVCPUs        = "platform-factory.dev/vcpus"

	// Cgroups and namespaces require explicit host delegation.
	annotationSandboxCgroups    = "platform-factory.dev/sandbox-cgroups"
	annotationSandboxNamespaces = "platform-factory.dev/sandbox-namespaces"

	// Requested devices fail closed when unavailable.
	annotationBlockDevicePath     = "platform-factory.dev/block-device-path"
	annotationBlockDeviceReadonly = "platform-factory.dev/block-device-readonly"

	// TAP requires CAP_NET_ADMIN in the active network namespace.
	annotationNetworkTAP = "platform-factory.dev/network-tap"
)

var supervisorReadyTimeout = 5 * time.Second

func LaunchSupervisor(ctx context.Context, store *Store, id, executable, pidFile string) error {
	readyReader, readyWriter, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("oci runtime: create supervisor readiness pipe: %w", err)
	}
	defer readyReader.Close()
	// The supervisor outlives this process (Setsid below detaches it from
	// our session entirely), but inheriting our os.Stdout/os.Stderr would
	// leave it writing to whatever pipe our own short-lived CLI invocation
	// was given - typically conmon's per-invocation log pipe for `create`,
	// not a descriptor conmon keeps open for the container's full
	// lifetime. Once that pipe's read end goes away, any write the
	// long-lived supervisor makes to it (e.g. the guest's serial console
	// output piped to stdout) raises SIGPIPE; Go's runtime terminates the
	// process outright for fd 1/2 rather than surfacing EPIPE, silently
	// killing the supervisor before it can answer a pending `start`
	// command - see supervisorLogPath's callers for the resulting
	// diagnostic. A dedicated on-disk log file has no such lifetime
	// coupling and survives the crash for later inspection.
	logPath := supervisorLogPath(store, id)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("oci runtime: open supervisor log: %w", err)
	}
	defer logFile.Close()
	command := exec.CommandContext(ctx, executable, "__serve", "--root", store.Dir(), "--ready-fd", "3", id)
	command.Stdin = nil
	command.Stdout = logFile
	command.Stderr = logFile
	command.ExtraFiles = []*os.File{readyWriter}
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	// PID/IPC/UTS isolation is established before child code executes. Mount
	// and network namespaces remain shared for bundle and TAP access.
	if sandbox.ProbeSandbox().Namespaces {
		command.SysProcAttr.Cloneflags = syscall.CLONE_NEWPID | syscall.CLONE_NEWIPC | syscall.CLONE_NEWUTS
	}
	if err := command.Start(); err != nil {
		readyWriter.Close()
		return fmt.Errorf("oci runtime: launch supervisor: %w", err)
	}
	readyWriter.Close()
	if err := store.SetSupervisor(ctx, id, command.Process.Pid); err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return err
	}
	if pidFile != "" {
		if err := os.WriteFile(pidFile, []byte(strconv.Itoa(command.Process.Pid)), 0o600); err != nil {
			_ = command.Process.Kill()
			_ = command.Wait()
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
			_ = command.Wait()
			return fmt.Errorf("oci runtime: supervisor failed before READY: %w", err)
		}
	case <-ctx.Done():
		_ = command.Process.Kill()
		_ = command.Wait()
		return ctx.Err()
	case <-time.After(supervisorReadyTimeout):
		// Kill alone leaves a zombie: PidfdOpen (processAlive's liveness
		// check, used by Store.Get's reconciliation) succeeds for a zombie
		// until its parent reaps it, so the persisted state would be stuck
		// showing this dead supervisor as still running forever. Wait
		// reaps it so the next Store.Get sees the process is really gone.
		_ = command.Process.Kill()
		_ = command.Wait()
		return fmt.Errorf("oci runtime: supervisor did not become ready within %s", supervisorReadyTimeout)
	}
	return nil
}

// applyVMMSandbox sets no-new-privileges and applies supported cgroup and
// capability limits without changing the UID that owns /dev/kvm access.
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

var debugFD, _ = syscall.Open("/tmp/debug-trace.txt", syscall.O_CREAT|syscall.O_WRONLY|syscall.O_APPEND, 0o600)

func dbg(s string) { _, _ = syscall.Write(debugFD, []byte(s+"\n")) }

func ServeSupervisor(ctx context.Context, store *Store, id string, readyFD int) (err error) {
	dbg("ENTER")
	startAcknowledged := false
	var command *net.UnixConn
	var state State
	deadline := time.Now().Add(2 * time.Second)
	for {
		var found bool
		state, found, err = store.readPersisted(ctx, id)
		if err != nil || !found {
			return fmt.Errorf("oci runtime: load supervisor state: %w", err)
		}
		if state.PID > 0 {
			break
		}
		if time.Now().After(deadline) {
			return errors.New("oci runtime: supervisor host PID was not published")
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
	// AppArmor and seccomp are thread-scoped; pin the KVM execution path and
	// never return its confined thread to the runtime pool.
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
	// Apply the thread-scoped filter after LockOSThread.
	if err := guestSandbox.ApplyStrictSeccomp(); err != nil {
		return fmt.Errorf("oci runtime: apply strict VMM seccomp filter: %w", err)
	}
	dbg("ApplyStrictSeccomp OK")
	sessionKey, err := generateGuestSessionKey(rand.Reader)
	if err != nil {
		return err
	}
	dbg("generateGuestSessionKey OK")
	defer clear(sessionKey)
	initrdPath, cleanup, err := buildGuestInitramfs(state.Bundle, config, sessionKey)
	if err != nil {
		dbg("buildGuestInitramfs err=" + err.Error())
		return err
	}
	dbg("buildGuestInitramfs OK")
	defer cleanup()
	initrd, err := os.ReadFile(initrdPath)
	if err != nil {
		dbg("ReadFile initrd err=" + err.Error())
		return err
	}
	dbg("ReadFile initrd OK")
	defer clear(initrd)
	kernel, memoryBytes, err := loadPinnedBoot(state.Annotations)
	if err != nil {
		dbg("loadPinnedBoot err=" + err.Error())
		return err
	}
	dbg("loadPinnedBoot OK")
	virtioOptions, cleanupBlockDevice, err := virtioDevicesForSupervisor(state.Annotations)
	if err != nil {
		dbg("virtioDevicesForSupervisor err=" + err.Error())
		return err
	}
	dbg("virtioDevicesForSupervisor OK")
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
					terminal, found, stateErr := store.readPersisted(context.Background(), id)
					if stateErr == nil && found && terminal.Status == "stopped" {
						return nil
					}
					select {
					case <-ctx.Done():
						terminal, found, stateErr := store.readPersisted(context.Background(), id)
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
	// Best-effort liveness observability, not a correctness mechanism: a
	// stuck guest is logged, never acted on automatically (no forced
	// kill/restart) - see guesttransport.RunHeartbeat's own doc comment
	// for why a single missed probe isn't "confirmed" stuck. Stops on its
	// own once runContext is canceled above.
	go guesttransport.RunHeartbeat(runContext, agent, 5*time.Second, 3, func(stuck bool, consecutiveMisses int) {
		fields := observability.Fields{"container_id": id, "consecutive_misses": consecutiveMisses}
		if stuck {
			observability.Warn("guest heartbeat missed repeatedly; guest may be stuck", fields)
			return
		}
		observability.Info("guest heartbeat recovered", fields)
	})
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
		current, found, stateErr := store.readPersisted(ctx, state.ID)
		valid := decodeErr == nil && stateErr == nil && found &&
			validSupervisorRequest(request, current, state, incarnation, "start")
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
		current, found, stateErr := store.readPersisted(ctx, state.ID)
		valid := decodeErr == nil && stateErr == nil && found &&
			validSupervisorRequest(request, current, state, incarnation, "signal") &&
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

const shutdownMarker = "component=microvm-init operation=supervise phase=child-exit"

// newShutdownLogWatcher observes the guest's final child-exit record.
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

// sandboxConfigForSupervisor always enables seccomp; host-dependent controls
// require explicit annotations. UID changes are excluded to preserve /dev/kvm.
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
	kernel, err := readPinnedFile(annotations[annotationKernelPath], annotations[annotationKernelDigest], "kernel", maxKernelBytes)
	if err != nil {
		return nil, 0, err
	}
	memoryMiB := uint64(128)
	if raw := annotations[annotationMemoryMiB]; raw != "" {
		memoryMiB, err = strconv.ParseUint(raw, 10, 64)
		if err != nil || memoryMiB < 64 || memoryMiB > 64<<10 {
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

func readPinnedFile(path, digest, label string, maxBytes int64) ([]byte, error) {
	if path == "" || !strings.HasPrefix(digest, "sha256:") || len(digest) != 71 {
		return nil, fmt.Errorf("oci runtime: %s path and sha256 digest annotations are required", label)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("oci runtime: %s must be a real regular file", label)
	}
	if maxBytes <= 0 || info.Size() > maxBytes {
		return nil, fmt.Errorf("oci runtime: %s exceeds the %d byte limit", label, maxBytes)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		clear(data)
		return nil, fmt.Errorf("oci runtime: %s exceeds the %d byte limit", label, maxBytes)
	}
	sum := sha256.Sum256(data)
	actual := "sha256:" + hex.EncodeToString(sum[:])
	if actual != digest {
		return nil, fmt.Errorf("oci runtime: %s digest mismatch: got %s", label, actual)
	}
	return data, nil
}
