//go:build darwin && cgo

// This file exercises pure-Go logic in vmm_darwin.go that does not require
// an actual running Virtualization.framework VM: config/argument validation,
// error wrapping, state-enum mapping, and lifecycle bookkeeping that returns
// before ever crossing the cgo boundary into a live *C.vz_machine_t. See
// vmm_darwin_test.go's existing tests for the same "fail closed before any
// framework call" pattern this file continues.
//
// A few functions in vmm_darwin.go (vz_machine_start/stop/state/free called
// with a *non-nil* handle) can only be reached with a VZVirtualMachine that
// successfully passed -[VZVirtualMachineConfiguration validateWithError:],
// which itself requires the com.apple.security.virtualization entitlement.
// This sandboxed test binary does not hold that entitlement, so those
// branches are intentionally left uncovered here rather than faked - see
// the per-function notes below.
package hvf

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	api "github.com/CYPT71/platform-factory/internal/microvm"
	vmruntime "github.com/CYPT71/platform-factory/internal/runtime"
)

// --- vzStateToAPI: pure enum mapping, no cgo at all ---

func TestVzStateToAPIMapsFrameworkStatesToCoarseAPIStates(t *testing.T) {
	cases := map[int]api.MachineState{
		vzStateStopped:   api.StateStopped,
		vzStateRunning:   api.StateRunning,
		vzStatePaused:    api.StateStopped,
		vzStateError:     api.StateFailed,
		vzStateStarting:  api.StateCreated,
		vzStatePausing:   api.StateRunning,
		vzStateResuming:  api.StateRunning,
		vzStateStopping:  api.StateRunning,
		vzStateSaving:    api.StateCreated,
		vzStateRestoring: api.StateCreated,
		999:              api.StateStopped, // unknown raw value falls into default
	}
	for raw, want := range cases {
		if got := vzStateToAPI(raw); got != want {
			t.Errorf("vzStateToAPI(%d) = %q, want %q", raw, got, want)
		}
	}
}

// --- darwinMachine.ID: trivial getter ---

func TestDarwinMachineIDReturnsConfiguredIdentifier(t *testing.T) {
	m := &darwinMachine{id: "my-machine"}
	if got := m.ID(); got != "my-machine" {
		t.Fatalf("ID() = %q, want %q", got, "my-machine")
	}
}

// --- darwinMachine.Logs: no cgo call at all, fully testable ---

func TestDarwinMachineLogsReadsWritesAndReportsErrors(t *testing.T) {
	t.Run("canceled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		m := &darwinMachine{logPath: filepath.Join(t.TempDir(), "serial.log")}
		var buf strings.Builder
		if err := m.Logs(ctx, &buf); !errors.Is(err, context.Canceled) {
			t.Fatalf("Logs() err = %v, want context.Canceled", err)
		}
	})
	t.Run("missing log file", func(t *testing.T) {
		m := &darwinMachine{logPath: filepath.Join(t.TempDir(), "does-not-exist.log")}
		var buf strings.Builder
		if err := m.Logs(context.Background(), &buf); err == nil ||
			!strings.Contains(err.Error(), "read logs") {
			t.Fatalf("Logs() err = %v, want a wrapped \"read logs\" error", err)
		}
	})
	t.Run("copies serial console content verbatim", func(t *testing.T) {
		dir := t.TempDir()
		logPath := filepath.Join(dir, "serial.log")
		const content = "console output line 1\nconsole output line 2\n"
		if err := os.WriteFile(logPath, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		m := &darwinMachine{logPath: logPath}
		var buf strings.Builder
		if err := m.Logs(context.Background(), &buf); err != nil {
			t.Fatalf("Logs() err = %v", err)
		}
		if buf.String() != content {
			t.Fatalf("Logs() copied %q, want %q", buf.String(), content)
		}
	})
}

// --- darwinMachine.Start/Stop/Status: ctx-canceled and already-freed
// branches return before touching the cgo handle. The success/failure
// branches that call C.vz_machine_start/stop/state on a live handle are not
// exercised here: a *C.vz_machine_t obtained only via a zero value (nil) is
// not a valid handle to pass across the cgo boundary (the Objective-C bridge
// dispatches onto bridge.queue, which would be nil and crash the process),
// and a genuinely live handle requires the virtualization entitlement this
// sandbox does not have. ---

func TestDarwinMachineStartStopStatusRejectCanceledContextAndFreedMachine(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	t.Run("Start", func(t *testing.T) {
		m := &darwinMachine{id: "s"}
		if err := m.Start(canceled); !errors.Is(err, context.Canceled) {
			t.Fatalf("Start(canceled) err = %v, want context.Canceled", err)
		}
		freed := &darwinMachine{id: "s", freed: true}
		if err := freed.Start(context.Background()); err == nil ||
			!strings.Contains(err.Error(), "already deleted") {
			t.Fatalf("Start() on freed machine err = %v, want \"already deleted\"", err)
		}
	})
	t.Run("Stop", func(t *testing.T) {
		m := &darwinMachine{id: "s"}
		if err := m.Stop(canceled); !errors.Is(err, context.Canceled) {
			t.Fatalf("Stop(canceled) err = %v, want context.Canceled", err)
		}
		freed := &darwinMachine{id: "s", freed: true}
		if err := freed.Stop(context.Background()); err == nil ||
			!strings.Contains(err.Error(), "already deleted") {
			t.Fatalf("Stop() on freed machine err = %v, want \"already deleted\"", err)
		}
	})
	t.Run("Status", func(t *testing.T) {
		m := &darwinMachine{id: "s"}
		if _, err := m.Status(canceled); !errors.Is(err, context.Canceled) {
			t.Fatalf("Status(canceled) err = %v, want context.Canceled", err)
		}
		freed := &darwinMachine{id: "freed-status", freed: true}
		status, err := freed.Status(context.Background())
		if err != nil {
			t.Fatalf("Status() on freed machine err = %v", err)
		}
		if status.ID != "freed-status" || status.State != api.StateStopped {
			t.Fatalf("Status() on freed machine = %+v, want ID=freed-status State=stopped", status)
		}
		if status.UpdatedAt.IsZero() || time.Since(status.UpdatedAt) > time.Minute {
			t.Fatalf("Status() UpdatedAt = %v, want a recent timestamp", status.UpdatedAt)
		}
	})
}

// --- darwinMachine.free: the already-freed branch returns before touching
// the cgo handle; it is exercised directly here and via DarwinVMM.Delete
// below. The branch that calls C.vz_machine_state/vz_machine_free on a live
// handle needs the same entitlement discussed above, so it is not covered.

func TestDarwinMachineFreeIsIdempotentWhenAlreadyFreed(t *testing.T) {
	m := &darwinMachine{id: "already-freed", freed: true}
	if err := m.free(); err != nil {
		t.Fatalf("free() on already-freed machine err = %v", err)
	}
	if err := m.free(); err != nil {
		t.Fatalf("second free() call err = %v, want idempotent success", err)
	}
}

// --- DarwinVMM.Name/Probe: thin wrappers, currently never called directly
// through the DarwinVMM value in any existing test.

func TestDarwinVMMNameIsStable(t *testing.T) {
	resolve, _ := dummyDigestResolver(t, "unused")
	vmm, err := NewDarwinVMM(resolve, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := vmm.Name(); got != "darwin-native" {
		t.Fatalf("Name() = %q, want %q", got, "darwin-native")
	}
}

func TestDarwinVMMProbeDelegatesToProbeNative(t *testing.T) {
	resolve, _ := dummyDigestResolver(t, "unused")
	vmm, err := NewDarwinVMM(resolve, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := vmm.Probe(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Probe(canceled) err = %v, want context.Canceled", err)
	}
	got, err := vmm.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe() err = %v", err)
	}
	want, err := ProbeNative(context.Background())
	if err != nil {
		t.Fatalf("ProbeNative() err = %v", err)
	}
	if got.Architecture != want.Architecture || got.Available != want.Available {
		t.Fatalf("Probe() = %+v, want it to delegate to ProbeNative() = %+v", got, want)
	}
}

// --- DarwinVMM.Load: map lookup only, no cgo call.

func TestDarwinVMMLoadReturnsTrackedMachineOrDescriptiveError(t *testing.T) {
	resolve, _ := dummyDigestResolver(t, "unused")
	vmm, err := NewDarwinVMM(resolve, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := vmm.Load(canceled, "anything"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Load(canceled) err = %v, want context.Canceled", err)
	}

	if _, err := vmm.Load(context.Background(), "never-created"); err == nil ||
		!strings.Contains(err.Error(), "not created by this process") {
		t.Fatalf("Load() of unknown id err = %v, want a not-created-by-this-process error", err)
	}

	tracked := &darwinMachine{id: "tracked", freed: true}
	vmm.mu.Lock()
	vmm.machines["tracked"] = tracked
	vmm.mu.Unlock()
	loaded, err := vmm.Load(context.Background(), "tracked")
	if err != nil {
		t.Fatalf("Load() of tracked id err = %v", err)
	}
	if loaded != tracked {
		t.Fatalf("Load() returned a different *darwinMachine than the one tracked")
	}
}

// --- DarwinVMM.Delete: the not-found and missing-id branches never touch
// cgo; deleting an already-freed machine reaches machine.free()'s early
// "already freed" return, which also never touches cgo. The branch where
// machine.free() itself fails (machine still running) requires a live
// handle and is not covered here for the same entitlement reason as above.

func TestDarwinVMMDeleteRemovesTrackedMachineAndIsNoopForMissingID(t *testing.T) {
	resolve, _ := dummyDigestResolver(t, "unused")
	vmm, err := NewDarwinVMM(resolve, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := vmm.Delete(canceled, "anything"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Delete(canceled) err = %v, want context.Canceled", err)
	}

	if err := vmm.Delete(context.Background(), "never-created"); err != nil {
		t.Fatalf("Delete() of unknown id err = %v, want a nil no-op", err)
	}

	tracked := &darwinMachine{id: "tracked", freed: true}
	vmm.mu.Lock()
	vmm.machines["tracked"] = tracked
	vmm.mu.Unlock()
	if err := vmm.Delete(context.Background(), "tracked"); err != nil {
		t.Fatalf("Delete() of a freed tracked machine err = %v", err)
	}
	if _, err := vmm.Load(context.Background(), "tracked"); err == nil {
		t.Fatal("deleted machine is still loadable")
	}
}

// --- NewDarwinVMM: the log-directory creation failure branch.

func TestNewDarwinVMMWrapsLogDirectoryCreationFailure(t *testing.T) {
	resolve, _ := dummyDigestResolver(t, "unused")
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker-file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// blocker is a regular file, so MkdirAll(blocker/logs) must fail.
	if _, err := NewDarwinVMM(resolve, filepath.Join(blocker, "logs")); err == nil ||
		!strings.Contains(err.Error(), "create log directory") {
		t.Fatalf("NewDarwinVMM() err = %v, want a wrapped \"create log directory\" error", err)
	}
}

// --- DarwinVMM.Create: the validation/resolution branches that run before
// the cgo boundary is crossed.

func TestDarwinVMMCreateWrapsBootBundleValidationFailure(t *testing.T) {
	resolve, _ := dummyDigestResolver(t, "unused")
	vmm, err := NewDarwinVMM(resolve, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	spec := api.MachineSpec{
		ID:        "bad-bundle",
		Bundle:    api.BootBundle{}, // zero value: wrong api_version, unpinned digests
		Resources: api.Resources{VCPUs: 1, MemoryMiB: 256},
	}
	if _, err := vmm.Create(context.Background(), spec); err == nil ||
		!strings.Contains(err.Error(), "validate boot bundle") {
		t.Fatalf("Create() err = %v, want a wrapped \"validate boot bundle\" error", err)
	}
}

func TestDarwinVMMCreateWrapsKernelResolveFailure(t *testing.T) {
	kernelDigest := "sha256:" + strings.Repeat("a", 64)
	rootfsDigest := "sha256:" + strings.Repeat("b", 64)
	rootfsResolveCalled := false
	resolve := func(_ context.Context, digest string) (string, error) {
		if digest == kernelDigest {
			return "", errors.New("boom: kernel unavailable")
		}
		rootfsResolveCalled = true
		return "/unused", nil
	}
	vmm, err := NewDarwinVMM(resolve, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := vmruntime.NewBootBundle(kernelDigest, "", rootfsDigest, []string{"console=hvc0"},
		map[string]string{darwinRootFSFormatKey: darwinRootFSFormatInitramfs})
	if err != nil {
		t.Fatal(err)
	}
	spec := api.MachineSpec{ID: "kernel-resolve-fail", Bundle: bundle, Resources: api.Resources{VCPUs: 1, MemoryMiB: 256}}
	if _, err := vmm.Create(context.Background(), spec); err == nil ||
		!strings.Contains(err.Error(), "resolve kernel") {
		t.Fatalf("Create() err = %v, want a wrapped \"resolve kernel\" error", err)
	}
	if rootfsResolveCalled {
		t.Fatal("rootfs was resolved even though the kernel resolve failed first")
	}
}

func TestDarwinVMMCreateWrapsRootFSResolveFailure(t *testing.T) {
	kernelDigest := "sha256:" + strings.Repeat("a", 64)
	rootfsDigest := "sha256:" + strings.Repeat("b", 64)
	resolve := func(_ context.Context, digest string) (string, error) {
		if digest == kernelDigest {
			return "/unused-kernel", nil
		}
		return "", errors.New("boom: rootfs unavailable")
	}
	vmm, err := NewDarwinVMM(resolve, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := vmruntime.NewBootBundle(kernelDigest, "", rootfsDigest, []string{"console=hvc0"},
		map[string]string{darwinRootFSFormatKey: darwinRootFSFormatInitramfs})
	if err != nil {
		t.Fatal(err)
	}
	spec := api.MachineSpec{ID: "rootfs-resolve-fail", Bundle: bundle, Resources: api.Resources{VCPUs: 1, MemoryMiB: 256}}
	if _, err := vmm.Create(context.Background(), spec); err == nil ||
		!strings.Contains(err.Error(), "resolve rootfs initramfs") {
		t.Fatalf("Create() err = %v, want a wrapped \"resolve rootfs initramfs\" error", err)
	}
}

// TestDarwinVMMCreateSurfacesFrameworkValidationErrorWithoutEntitlement
// exercises Create() all the way to C.vz_create_machine with real,
// resolvable (if content-free) kernel/rootfs files. Per vmm_darwin.go's
// package doc, a process without the com.apple.security.virtualization
// entitlement gets a deterministic failure out of
// -[VZVirtualMachineConfiguration validateWithError:] rather than a crash -
// verified interactively against this sandbox, where it produces "vmm:
// darwin: create machine: Invalid virtual machine configuration. The
// process doesn't have the ... entitlement." This test tolerates a signed,
// entitled test binary too (some CI configurations may have one): it only
// asserts the shape of Create()'s handling of vz_create_machine's outcome,
// not which outcome occurs.
func TestDarwinVMMCreateSurfacesFrameworkValidationErrorWithoutEntitlement(t *testing.T) {
	dir := t.TempDir()
	kernelPath := filepath.Join(dir, "kernel")
	rootfsPath := filepath.Join(dir, "rootfs")
	if err := os.WriteFile(kernelPath, []byte("not-a-real-kernel"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootfsPath, []byte("not-a-real-initramfs"), 0o600); err != nil {
		t.Fatal(err)
	}
	kernelDigest := "sha256:" + strings.Repeat("a", 64)
	rootfsDigest := "sha256:" + strings.Repeat("b", 64)
	paths := map[string]string{kernelDigest: kernelPath, rootfsDigest: rootfsPath}
	resolve := func(_ context.Context, digest string) (string, error) {
		path, ok := paths[digest]
		if !ok {
			return "", errors.New("unexpected digest requested")
		}
		return path, nil
	}
	vmm, err := NewDarwinVMM(resolve, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := vmruntime.NewBootBundle(kernelDigest, "", rootfsDigest, []string{"console=hvc0"},
		map[string]string{darwinRootFSFormatKey: darwinRootFSFormatInitramfs})
	if err != nil {
		t.Fatal(err)
	}
	spec := api.MachineSpec{
		ID:        "framework-boundary",
		Bundle:    bundle,
		Resources: api.Resources{VCPUs: 1, MemoryMiB: 256},
	}
	machine, err := vmm.Create(context.Background(), spec)
	if err != nil {
		if !strings.Contains(err.Error(), "vmm: darwin: create machine:") {
			t.Fatalf("Create() err = %v, want it wrapped as \"vmm: darwin: create machine: ...\"", err)
		}
		t.Logf("Create() failed as expected without the virtualization entitlement: %v", err)
		return
	}
	// An entitled test binary actually created a machine: clean it up and
	// confirm Create() tracked it like any other successful call.
	t.Logf("test binary appears to hold the virtualization entitlement; created %q", machine.ID())
	if machine.ID() != spec.ID {
		t.Fatalf("machine.ID() = %q, want %q", machine.ID(), spec.ID)
	}
	if err := vmm.Delete(context.Background(), machine.ID()); err != nil {
		t.Fatalf("cleanup Delete() err = %v", err)
	}
}

// --- darwinMachine.Agent: branches reachable without a live handle. Agent
// itself never crosses the cgo boundary (it only reads/writes Go fields and
// the already-adopted *os.File), so these are fully exercisable.

func TestDarwinMachineAgentRejectsCanceledContextAndFreedMachine(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	m := &darwinMachine{id: "agent-ctx"}
	if _, err := m.Agent(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Agent(canceled) err = %v, want context.Canceled", err)
	}

	freed := &darwinMachine{id: "agent-freed", freed: true}
	if _, err := freed.Agent(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "already deleted") {
		t.Fatalf("Agent() on freed machine err = %v, want \"already deleted\"", err)
	}
}

func TestDarwinMachineAgentWrapsKeyResolverError(t *testing.T) {
	host, guest, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	defer guest.Close()
	m := &darwinMachine{
		id:        "key-resolve-fail",
		agentFile: host,
		agentKey: func(context.Context, string) ([]byte, error) {
			return nil, errors.New("boom: key unavailable")
		},
	}
	if _, err := m.Agent(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "resolve guest agent key") {
		t.Fatalf("Agent() err = %v, want a wrapped \"resolve guest agent key\" error", err)
	}
	if m.agentClaimed {
		t.Fatal("a failed key resolution must not consume the native channel")
	}
}

// TestDarwinMachineAgentDetectsConcurrentClaimAfterKeyResolution simulates
// another goroutine claiming the same native agent channel while this
// call's key resolver is still running (the resolver callback itself flips
// agentClaimed, standing in for that concurrent claimant), proving the
// post-resolution re-check under the lock (vmm_darwin.go's second
// "m.freed || m.agentClaimed" guard) actually rejects the race instead of
// handing out two live agents over the same file.
func TestDarwinMachineAgentDetectsConcurrentClaimAfterKeyResolution(t *testing.T) {
	host, guest, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	defer guest.Close()
	m := &darwinMachine{id: "race"}
	m.agentFile = host
	m.agentKey = func(context.Context, string) ([]byte, error) {
		m.mu.Lock()
		m.agentClaimed = true // a concurrent Agent() call claimed it first
		m.mu.Unlock()
		return []byte("0123456789012345678901234567890123456789"), nil
	}
	if _, err := m.Agent(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "no longer available") {
		t.Fatalf("Agent() err = %v, want \"no longer available\" once a concurrent claim won the race", err)
	}
}

// --- RunLinuxHVF: input-validation branches that return before any cgo
// call, beyond what hvf_run_linux_darwin_test.go already covers (empty
// path / missing file / undersized resources / malformed MAC).

func TestRunLinuxHVFRejectsNonRegularOrEmptyKernelAndInitrd(t *testing.T) {
	t.Run("kernel path is a directory", func(t *testing.T) {
		dir := t.TempDir()
		if _, err := RunLinuxHVF(context.Background(), dir, "", "", "", 512<<20, 1, "", nil); err == nil ||
			!strings.Contains(err.Error(), "non-empty regular file") {
			t.Fatalf("err = %v, want a non-empty-regular-file error", err)
		}
	})
	t.Run("kernel file is empty", func(t *testing.T) {
		kernel := filepath.Join(t.TempDir(), "Image")
		if err := os.WriteFile(kernel, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := RunLinuxHVF(context.Background(), kernel, "", "", "", 512<<20, 1, "", nil); err == nil ||
			!strings.Contains(err.Error(), "non-empty regular file") {
			t.Fatalf("err = %v, want a non-empty-regular-file error", err)
		}
	})
	t.Run("initrd path is a directory", func(t *testing.T) {
		kernel := filepath.Join(t.TempDir(), "Image")
		if err := os.WriteFile(kernel, []byte("kernel-bytes"), 0o600); err != nil {
			t.Fatal(err)
		}
		initrdDir := t.TempDir()
		if _, err := RunLinuxHVF(context.Background(), kernel, initrdDir, "", "", 512<<20, 1, "", nil); err == nil ||
			!strings.Contains(err.Error(), "initramfs must be a non-empty regular file") {
			t.Fatalf("err = %v, want an initramfs non-empty-regular-file error", err)
		}
	})
	t.Run("initrd file is empty", func(t *testing.T) {
		kernel := filepath.Join(t.TempDir(), "Image")
		if err := os.WriteFile(kernel, []byte("kernel-bytes"), 0o600); err != nil {
			t.Fatal(err)
		}
		emptyInitrd := filepath.Join(t.TempDir(), "initrd")
		if err := os.WriteFile(emptyInitrd, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := RunLinuxHVF(context.Background(), kernel, emptyInitrd, "", "", 512<<20, 1, "", nil); err == nil ||
			!strings.Contains(err.Error(), "initramfs must be a non-empty regular file") {
			t.Fatalf("err = %v, want an initramfs non-empty-regular-file error", err)
		}
	})
}

func TestRunLinuxHVFWrapsWorkDirectoryCreationFailure(t *testing.T) {
	kernel := filepath.Join(t.TempDir(), "Image")
	if err := os.WriteFile(kernel, []byte("kernel-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Point TMPDIR at a path that does not exist so os.MkdirTemp fails
	// before RunLinuxHVF ever touches the cgo boundary.
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "does-not-exist"))
	if _, err := RunLinuxHVF(context.Background(), kernel, "", "", "", 512<<20, 1, "", nil); err == nil ||
		!strings.Contains(err.Error(), "create work directory") {
		t.Fatalf("err = %v, want a wrapped \"create work directory\" error", err)
	}
}

// --- ProbeNative: the ctx-canceled branch, not exercised by
// vmm_darwin_test.go's capabilities test.

func TestProbeNativeRejectsCanceledContext(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ProbeNative(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("ProbeNative(canceled) err = %v, want context.Canceled", err)
	}
}
