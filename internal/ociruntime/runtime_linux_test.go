//go:build linux && amd64

package ociruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/CYPT71/platform-factory/internal/hypervisor/sandbox"
)

func TestStoreLifecycleAndStartAcknowledgement(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	bundle := testBundle(t)
	state, err := store.Create(ctx, "secure-img", bundle)
	if err != nil || state.Status != "created" {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	if _, err := store.Create(ctx, "secure-img", bundle); err == nil {
		t.Fatal("duplicate create accepted")
	}
	if err := store.SetSupervisor(ctx, "secure-img", os.Getpid()); err != nil {
		t.Fatal(err)
	}
	state, _, _ = store.Get(ctx, "secure-img")
	serveTestStart(t, store, state, true, nil, false)
	if err := store.Start(ctx, "secure-img"); err != nil {
		t.Fatal(err)
	}
	state, found, err := store.Get(ctx, "secure-img")
	if err != nil || !found || state.Status != "running" {
		t.Fatalf("state=%+v found=%t err=%v", state, found, err)
	}
	if err := store.Delete(ctx, "secure-img", false); err == nil {
		t.Fatal("running delete accepted")
	}
	if err := store.SetStatus(ctx, "secure-img", "stopped"); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, "secure-img", false); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorRequestIdentityIsIndependentFromNamespacePID(t *testing.T) {
	launched := State{ID: "isolated-vmm", PID: 424242, Created: time.Unix(100, 0).UTC()}
	incarnation := stateIncarnation(launched)
	request := startResult{Command: "start", ID: launched.ID, PID: launched.PID, Incarnation: incarnation}
	if !validSupervisorRequest(request, launched, launched, incarnation, "start") {
		t.Fatal("host-visible PID should authenticate without matching os.Getpid inside the namespace")
	}
	for _, mutate := range []func(*startResult){
		func(value *startResult) { value.PID++ },
		func(value *startResult) { value.Incarnation = "stale" },
		func(value *startResult) { value.ID = "other" },
		func(value *startResult) { value.Command = "signal" },
	} {
		bad := request
		mutate(&bad)
		if validSupervisorRequest(bad, launched, launched, incarnation, "start") {
			t.Fatalf("stale request accepted: %+v", bad)
		}
	}
	current := launched
	current.PID++
	if validSupervisorRequest(request, current, launched, incarnation, "start") {
		t.Fatal("request accepted after persisted supervisor PID changed")
	}
}

func TestLaunchSupervisorPublishesHostPIDWhileChildIsNamespacePIDOne(t *testing.T) {
	if !sandbox.ProbeSandbox().Namespaces {
		t.Skip("PID namespace creation is unavailable")
	}
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Create(ctx, "isolated-vmm", testBundle(t)); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(t.TempDir(), "namespace-supervisor")
	// argv seen by the script is $1=__serve $2=--root $3=storeDir ...; store.Dir()
	// is $3, not $2 - using $2 (the literal string "--root") made the write below
	// fail under `set -eu`, so the script exited before ever printing READY,
	// which is exactly the "supervisor failed before READY: EOF" this test hit.
	script := "#!/bin/sh\nset -eu\nprintf '%s\\n' \"$$\" > \"$3/namespace-pid\"\nprintf 'READY\\n' >&3\nwhile :; do sleep 1; done\n"
	if err := os.WriteFile(helper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(t.TempDir(), "supervisor.pid")
	if err := LaunchSupervisor(ctx, store, "isolated-vmm", helper, pidFile); err != nil {
		t.Fatal(err)
	}
	state, found, err := store.readPersisted(ctx, "isolated-vmm")
	if err != nil || !found || state.PID <= 1 {
		t.Fatalf("host state=%+v found=%t err=%v", state, found, err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(state.PID, syscall.SIGKILL)
	})
	data, err := os.ReadFile(filepath.Join(store.Dir(), "namespace-pid"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != "1" {
		t.Fatalf("supervisor namespace PID=%q, want 1", strings.TrimSpace(string(data)))
	}
	data, err = os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != fmt.Sprint(state.PID) {
		t.Fatalf("published PID=%q, want host PID %d", strings.TrimSpace(string(data)), state.PID)
	}
}

func TestLaunchSupervisorTimesOutAndKillsSilentChild(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Create(ctx, "silent-vmm", testBundle(t)); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(t.TempDir(), "silent-supervisor")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nwhile :; do sleep 1; done\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	oldTimeout := supervisorReadyTimeout
	supervisorReadyTimeout = 50 * time.Millisecond
	t.Cleanup(func() { supervisorReadyTimeout = oldTimeout })
	started := time.Now()
	err = LaunchSupervisor(ctx, store, "silent-vmm", helper, "")
	if err == nil || !strings.Contains(err.Error(), "did not become ready") || time.Since(started) > 2*time.Second {
		t.Fatalf("elapsed=%s err=%v", time.Since(started), err)
	}
	state, found, getErr := store.Get(ctx, "silent-vmm")
	if getErr != nil || !found || state.PID != 0 || state.Status != "stopped" {
		t.Fatalf("state=%+v found=%v err=%v", state, found, getErr)
	}
}

func TestStoreListIsStableReconciledAndFailClosed(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	bundle := testBundle(t)
	for _, id := range []string{"z-last", "a-first"} {
		if _, err := store.Create(ctx, id, bundle); err != nil {
			t.Fatal(err)
		}
	}
	states, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 2 || states[0].ID != "a-first" || states[1].ID != "z-last" {
		t.Fatalf("states=%+v", states)
	}
	if err := os.WriteFile(filepath.Join(store.Dir(), "invalid name.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(ctx); err == nil || !strings.Contains(err.Error(), "invalid state filename") {
		t.Fatalf("invalid state filename error=%v", err)
	}
}

func TestStoreRejectsUnknownAndTrailingStateData(t *testing.T) {
	ctx := context.Background()
	for _, document := range []string{
		`{"ociVersion":"1.0.2","id":"secure-img","status":"created","pid":0,"bundle":"/tmp","created":"2026-01-01T00:00:00Z","surprise":true}`,
		`{"ociVersion":"1.0.2","id":"secure-img","status":"created","pid":0,"bundle":"/tmp","created":"2026-01-01T00:00:00Z"} {}`,
	} {
		root := t.TempDir()
		store, err := OpenStore(root)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "secure-img.json"), []byte(document), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.Get(ctx, "secure-img"); err == nil || !strings.Contains(err.Error(), "corrupt stored state") {
			t.Fatalf("document=%s err=%v", document, err)
		}
		store.Close()
	}
}

func TestStartResultSurvivesShortGuestAndReportsFailure(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct {
		name    string
		started bool
	}{
		{name: "short-guest", started: true},
		{name: "pre-kvm-failure"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, err := OpenStore(filepath.Join(t.TempDir(), "state"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			if _, err := store.Create(ctx, "secure-img", testBundle(t)); err != nil {
				t.Fatal(err)
			}
			if err := store.SetSupervisor(ctx, "secure-img", os.Getpid()); err != nil {
				t.Fatal(err)
			}
			state, _, err := store.Get(ctx, "secure-img")
			if err != nil {
				t.Fatal(err)
			}
			serveTestStart(t, store, state, test.started, errors.New("KVM_CREATE_VM denied"), true)
			err = store.Start(ctx, "secure-img")
			if test.started && err != nil {
				t.Fatalf("short guest lost acknowledgement: %v", err)
			}
			if !test.started && (err == nil || !strings.Contains(err.Error(), "KVM_CREATE_VM denied")) {
				t.Fatalf("startup failure=%v", err)
			}
		})
	}
}

func serveTestStart(t *testing.T, store *Store, state State, started bool, cause error, stopBeforeClose bool) {
	t.Helper()
	path := store.controlSocketPath(state)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		listener.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		listener.Close()
		_ = os.Remove(path)
	})
	done := make(chan struct{})
	t.Cleanup(func() { <-done })
	go func() {
		defer close(done)
		connection, acceptErr := listener.AcceptUnix()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		var request startResult
		if json.NewDecoder(io.LimitReader(connection, 4097)).Decode(&request) != nil {
			return
		}
		response := startResult{
			ID: state.ID, Incarnation: stateIncarnation(state), PID: state.PID, Started: started,
		}
		if !started {
			response.Error = cause.Error()
		}
		// Persist "running" before writing the response, mirroring
		// ServeSupervisor's own OnStarted callback
		// (supervisor_linux.go) - it calls store.SetStatus before
		// writeStartResponse for exactly this reason: a caller whose
		// Start() has returned must already observe "running" in the
		// store, never a window where the client thinks the start
		// succeeded but a concurrent store.Get sees stale state. Doing
		// it the other way around here (write the response, then set
		// the status) raced this goroutine against Start()'s own
		// caller and failed intermittently under -race.
		if started {
			_ = store.SetStatus(context.Background(), state.ID, "running")
		}
		_ = json.NewEncoder(connection).Encode(response)
		if stopBeforeClose {
			_ = store.SetStatus(context.Background(), state.ID, "stopped")
		}
	}()
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("command socket mode=%v err=%v", info.Mode().Perm(), err)
	}
}

func TestStartRejectsStaleSocketResponse(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Create(ctx, "secure-img", testBundle(t)); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSupervisor(ctx, "secure-img", os.Getpid()); err != nil {
		t.Fatal(err)
	}
	state, _, err := store.Get(ctx, "secure-img")
	if err != nil {
		t.Fatal(err)
	}
	path := store.controlSocketPath(state)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	defer os.Remove(path)
	go func() {
		connection, acceptErr := listener.AcceptUnix()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		var request startResult
		_ = json.NewDecoder(connection).Decode(&request)
		request.Started = true
		request.Incarnation = "stale"
		request.Command = ""
		_ = json.NewEncoder(connection).Encode(request)
	}()
	if err := store.Start(ctx, "secure-img"); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale response err=%v", err)
	}
}

func TestAcceptStartCommandRejectsStaleIncarnation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, err := OpenStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Create(ctx, "secure-img", testBundle(t)); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSupervisor(ctx, "secure-img", os.Getpid()); err != nil {
		t.Fatal(err)
	}
	state, _, err := store.Get(ctx, "secure-img")
	if err != nil {
		t.Fatal(err)
	}
	path := store.controlSocketPath(state)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	defer os.Remove(path)
	accepted := make(chan *net.UnixConn, 1)
	acceptErrors := make(chan error, 1)
	go func() {
		connection, acceptErr := acceptStartCommand(ctx, listener, store, state, stateIncarnation(state))
		if acceptErr != nil {
			acceptErrors <- acceptErr
			return
		}
		accepted <- connection
	}()
	send := func(incarnation string) startResult {
		connection, dialErr := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
		if dialErr != nil {
			t.Fatal(dialErr)
		}
		request := startResult{
			Command: "start", ID: state.ID, Incarnation: incarnation, PID: state.PID,
		}
		if err := json.NewEncoder(connection).Encode(request); err != nil {
			t.Fatal(err)
		}
		var response startResult
		_ = json.NewDecoder(connection).Decode(&response)
		connection.Close()
		return response
	}
	if response := send("stale"); !strings.Contains(response.Error, "stale") {
		t.Fatalf("stale command response=%+v", response)
	}
	validClient, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer validClient.Close()
	request := startResult{
		Command: "start", ID: state.ID, Incarnation: stateIncarnation(state), PID: state.PID,
	}
	if err := json.NewEncoder(validClient).Encode(request); err != nil {
		t.Fatal(err)
	}
	select {
	case connection := <-accepted:
		connection.Close()
	case err := <-acceptErrors:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("valid command was not accepted")
	}
}

func TestKillUsesValidatedSignalSocket(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, err := OpenStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Create(ctx, "secure-img", testBundle(t)); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSupervisor(ctx, "secure-img", os.Getpid()); err != nil {
		t.Fatal(err)
	}
	if err := store.SetStatus(ctx, "secure-img", "running"); err != nil {
		t.Fatal(err)
	}
	state, _, err := store.Get(ctx, "secure-img")
	if err != nil {
		t.Fatal(err)
	}
	path := store.controlSocketPath(state)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	defer os.Remove(path)
	delivered := make(chan syscall.Signal, 1)
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- serveSignalCommands(ctx, listener, store, state, stateIncarnation(state), func(_ context.Context, signal syscall.Signal) error {
			if signal == syscall.SIGINT {
				return errors.New("guest agent unavailable")
			}
			delivered <- signal
			return nil
		})
	}()

	// A stale command must be rejected without signaling the live incarnation.
	stale, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	request := startResult{
		Command: "signal", ID: state.ID, Incarnation: "stale",
		PID: state.PID, Signal: int(syscall.SIGCONT),
	}
	if err := json.NewEncoder(stale).Encode(request); err != nil {
		t.Fatal(err)
	}
	var response startResult
	if err := json.NewDecoder(stale).Decode(&response); err != nil {
		t.Fatal(err)
	}
	stale.Close()
	if !strings.Contains(response.Error, "stale") {
		t.Fatalf("stale signal response=%+v", response)
	}
	select {
	case <-delivered:
		t.Fatal("stale signal reached the guest relay")
	default:
	}

	if err := store.Kill(ctx, state.ID, syscall.SIGCONT); err == nil {
		t.Fatal("non-termination signal accepted without a guest agent")
	}
	if err := store.Kill(ctx, state.ID, syscall.SIGINT); err == nil ||
		!strings.Contains(err.Error(), "guest agent unavailable") {
		t.Fatalf("guest relay failure=%v", err)
	}
	select {
	case <-delivered:
		t.Fatal("failed guest relay acknowledged delivery")
	default:
	}
	// SIGTERM proves authenticated delivery through the socket to the guest
	// relay without signaling this test process.
	if err := store.Kill(ctx, state.ID, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case signal := <-delivered:
		if signal != syscall.SIGTERM {
			t.Fatalf("delivered signal=%v", signal)
		}
	case <-time.After(time.Second):
		t.Fatal("signal command was not relayed")
	}
	cancel()
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestStoppedStateCleansCommandSocket(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Create(ctx, "secure-img", testBundle(t)); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSupervisor(ctx, "secure-img", os.Getpid()); err != nil {
		t.Fatal(err)
	}
	state, _, _ := store.Get(ctx, "secure-img")
	path := store.controlSocketPath(state)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := store.SetStatus(ctx, "secure-img", "stopped"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("command socket survived stopped transition: %v", err)
	}
}

func TestDeadSupervisorCleansCommandSocket(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Create(ctx, "secure-img", testBundle(t)); err != nil {
		t.Fatal(err)
	}
	const deadPID = 1 << 30
	if err := store.SetSupervisor(ctx, "secure-img", deadPID); err != nil {
		t.Fatal(err)
	}
	state, found, err := store.Get(ctx, "secure-img")
	if err != nil || !found {
		t.Fatalf("state found=%t err=%v", found, err)
	}
	// Get already observes a dead process, so reconstruct its original
	// incarnation to create the stale artifact a crash could leave behind.
	state.PID = deadPID
	state.Status = "created"
	path := store.controlSocketPath(state)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.Close()
	// Persist the dead PID once more, then force crash reconciliation.
	if err := store.SetSupervisor(ctx, "secure-img", deadPID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Get(ctx, "secure-img"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("command socket survived dead supervisor reconciliation: %v", err)
	}
}

// TestSetSupervisorRecordsRealStartTicks confirms SetSupervisor captures a
// nonzero, correct PIDStartTicks for a real running process, matching an
// independently computed reading of the same /proc/<pid>/stat field - the
// value processAlive and Delete's force-kill path (see below) rely on to
// tell "still my process" from "PID reused by something else" apart.
func TestSetSupervisorRecordsRealStartTicks(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Create(ctx, "secure-img", testBundle(t)); err != nil {
		t.Fatal(err)
	}
	want, err := processStartTicks(os.Getpid())
	if err != nil {
		t.Fatalf("processStartTicks(self): %v", err)
	}
	if err := store.SetSupervisor(ctx, "secure-img", os.Getpid()); err != nil {
		t.Fatal(err)
	}
	state, found, err := store.readPersisted(ctx, "secure-img")
	if err != nil || !found {
		t.Fatalf("state found=%t err=%v", found, err)
	}
	if state.PIDStartTicks != want {
		t.Fatalf("PIDStartTicks=%d, want %d", state.PIDStartTicks, want)
	}
	if !processAlive(state.PID, state.PIDStartTicks) {
		t.Fatal("processAlive false for this test's own live process")
	}
}

// TestProcessAliveRejectsReusedPID simulates the exact scenario pidfd
// verification exists for: a PID number that is genuinely running right
// now (this test process's own PID, so the test can safely assert on it),
// but whose recorded start ticks belong to a different, earlier process
// instance - i.e. the original supervisor already exited and the kernel
// handed its old PID number to something unrelated. A bare
// kill(pid, 0)/kill(pid, sig) cannot see this; processAlive must treat it
// as not-alive rather than assume the live process it finds is the one
// that was recorded.
func TestProcessAliveRejectsReusedPID(t *testing.T) {
	realTicks, err := processStartTicks(os.Getpid())
	if err != nil {
		t.Fatalf("processStartTicks(self): %v", err)
	}
	mismatched := realTicks + 1
	if processAlive(os.Getpid(), mismatched) {
		t.Fatal("processAlive accepted a live PID with mismatched start ticks (simulated PID reuse)")
	}
	// Sanity: the same PID with its real, correctly recorded ticks must
	// still be reported alive - the mismatch above is what's rejected, not
	// the PID itself.
	if !processAlive(os.Getpid(), realTicks) {
		t.Fatal("processAlive rejected a live PID with its own correct start ticks")
	}
}

// TestDeleteForceNeverSignalsMismatchedStartTicksProcess proves the
// concrete safety property the pidfd rework exists for: Delete's
// force-kill path must never send SIGKILL to a process whose start ticks
// no longer match what was recorded - the situation a stale PID record
// reused by an unrelated process would produce. A real, live child process
// stands in for "the unrelated process that now holds this PID number" so
// this test can assert, for real, that it survives.
func TestDeleteForceNeverSignalsMismatchedStartTicksProcess(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Create(ctx, "secure-img", testBundle(t)); err != nil {
		t.Fatal(err)
	}
	child := exec.Command("sleep", "5")
	if err := child.Start(); err != nil {
		t.Fatalf("start stand-in child: %v", err)
	}
	defer func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	}()
	if err := store.SetSupervisor(ctx, "secure-img", child.Process.Pid); err != nil {
		t.Fatal(err)
	}
	if err := store.SetStatus(ctx, "secure-img", "running"); err != nil {
		t.Fatal(err)
	}
	// Corrupt the recorded start ticks in place, simulating that this PID
	// number now belongs to a process instance other than the one that was
	// actually recorded - the real child keeps running throughout.
	if err := store.update(ctx, "secure-img", func(state *State) {
		state.PIDStartTicks++
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, "secure-img", true); err != nil {
		t.Fatalf("Delete(force=true): %v", err)
	}
	if err := child.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("stand-in child did not survive Delete(force=true): %v", err)
	}
}

func TestPersistedReadDoesNotReconcileHostPID(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Create(ctx, "secure-img", testBundle(t)); err != nil {
		t.Fatal(err)
	}
	const hostPIDInvisibleFromChildNamespace = 1 << 30
	if err := store.SetSupervisor(ctx, "secure-img", hostPIDInvisibleFromChildNamespace); err != nil {
		t.Fatal(err)
	}
	state, found, err := store.readPersisted(ctx, "secure-img")
	if err != nil || !found {
		t.Fatalf("persisted state found=%t err=%v", found, err)
	}
	if state.PID != hostPIDInvisibleFromChildNamespace || state.Status != "created" {
		t.Fatalf("persisted read reconciled namespace-invisible host PID: %+v", state)
	}
	reconciled, found, err := store.Get(ctx, "secure-img")
	if err != nil || !found {
		t.Fatalf("reconciled state found=%t err=%v", found, err)
	}
	if reconciled.PID != 0 || reconciled.Status != "stopped" {
		t.Fatalf("host read did not reconcile dead PID: %+v", reconciled)
	}
}

func TestLoadConfigRejectsEscapeAndUnsupportedSemantics(t *testing.T) {
	bundle := testBundle(t)
	if _, err := LoadConfig(bundle); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/", filepath.Join(bundle, "escape")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(bundle, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	zero := 0
	config.Process.Capabilities = &Capabilities{}
	config.Process.OOMScoreAdj = &zero
	encoded, _ := json.Marshal(config)
	if err := os.WriteFile(filepath.Join(bundle, "config.json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(bundle)
	if err != nil {
		t.Fatalf("empty capabilities and neutral OOM adjustment rejected: %v", err)
	}
	if loaded.Process.OOMScoreAdj != nil {
		t.Fatal("neutral OOM adjustment was not normalized")
	}
	config.Process.Capabilities.Effective = []string{"CAP_SYS_ADMIN"}
	encoded, _ = json.Marshal(config)
	if err := os.WriteFile(filepath.Join(bundle, "config.json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(bundle); err == nil || !strings.Contains(err.Error(), "non-empty process capabilities") {
		t.Fatalf("privileged capabilities error=%v", err)
	}
	config.Process.Capabilities = nil
	config.Process.OOMScoreAdj = nil
	config.Process.Rlimits = []Rlimit{{Type: "RLIMIT_NOFILE", Soft: 1024, Hard: 2048}}
	encoded, _ = json.Marshal(config)
	if err := os.WriteFile(filepath.Join(bundle, "config.json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(bundle); err != nil {
		t.Fatalf("supported rlimit rejected: %v", err)
	}
	config.Process.Rlimits = []Rlimit{{Type: "RLIMIT_NOFILE", Soft: 2, Hard: 1}}
	encoded, _ = json.Marshal(config)
	if err := os.WriteFile(filepath.Join(bundle, "config.json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(bundle); err == nil || !strings.Contains(err.Error(), "soft value exceeds hard") {
		t.Fatalf("invalid rlimit error=%v", err)
	}
	config.Process.Rlimits = nil
	config.Root.Path = "escape/etc"
	encoded, _ = json.Marshal(config)
	if err := os.WriteFile(filepath.Join(bundle, "config.json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(bundle); err == nil {
		t.Fatal("symlink escape accepted")
	}
	config.Root.Path = "rootfs"
	config.Mounts = []Mount{{Destination: "/dev/mqueue", Type: "mqueue", Source: "mqueue"}}
	encoded, _ = json.Marshal(config)
	if err := os.WriteFile(filepath.Join(bundle, "config.json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(bundle); err != nil {
		t.Fatalf("guest-owned mqueue mount rejected: %v", err)
	}
	config.Mounts[0].Type = "bind"
	encoded, _ = json.Marshal(config)
	if err := os.WriteFile(filepath.Join(bundle, "config.json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(bundle); err == nil {
		t.Fatal("host-backed /dev/mqueue mount accepted")
	}
	config.Mounts = []Mount{{Destination: "/host", Type: "bind", Source: "/"}}
	encoded, _ = json.Marshal(config)
	_ = os.WriteFile(filepath.Join(bundle, "config.json"), encoded, 0o600)
	if _, err := LoadConfig(bundle); err == nil {
		t.Fatal("host bind mount accepted")
	}
}

func TestBuildGuestInitramfsAndPinnedBootInputs(t *testing.T) {
	bundle := testBundle(t)
	initPath := filepath.Join(t.TempDir(), "microvm-init")
	initData := []byte("init")
	if err := os.WriteFile(initPath, initData, 0o500); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(initData)
	config, err := LoadConfig(bundle)
	if err != nil {
		t.Fatal(err)
	}
	config.Annotations[annotationInitPath] = initPath
	config.Annotations[annotationInitDigest] = "sha256:" + hex.EncodeToString(sum[:])
	archive, cleanup, err := BuildGuestInitramfs(bundle, config)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if info, err := os.Stat(archive); err != nil || info.Size() == 0 {
		t.Fatalf("archive info=%v err=%v", info, err)
	}
	kernelPath := filepath.Join(t.TempDir(), "kernel")
	kernelData := []byte("kernel")
	_ = os.WriteFile(kernelPath, kernelData, 0o600)
	kernelSum := sha256.Sum256(kernelData)
	annotations := map[string]string{
		annotationKernelPath: kernelPath, annotationKernelDigest: "sha256:" + hex.EncodeToString(kernelSum[:]),
		annotationMemoryMiB: "256", annotationVCPUs: "1",
	}
	kernel, memory, err := loadPinnedBoot(annotations)
	if err != nil || string(kernel) != "kernel" || memory != 256<<20 {
		t.Fatalf("kernel=%q memory=%d err=%v", kernel, memory, err)
	}
	annotations[annotationMemoryMiB] = "bad"
	if _, _, err := loadPinnedBoot(annotations); err == nil || !strings.Contains(err.Error(), "memory") {
		t.Fatalf("bad memory err=%v", err)
	}
}

func TestReadPinnedFileRejectsOversizedAndTamperedBootInputs(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "kernel")
	payload := []byte("trusted-kernel")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	if data, err := readPinnedFile(path, digest, "kernel", int64(len(payload))); err != nil || string(data) != string(payload) {
		t.Fatalf("data=%q err=%v", data, err)
	}
	if _, err := readPinnedFile(path, digest, "kernel", int64(len(payload)-1)); err == nil || !strings.Contains(err.Error(), "byte limit") {
		t.Fatalf("oversized input err=%v", err)
	}
	if _, err := readPinnedFile(path, "sha256:"+strings.Repeat("0", 64), "kernel", 1<<20); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("tampered input err=%v", err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readPinnedFile(link, digest, "kernel", 1<<20); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("symlink input err=%v", err)
	}
}

func TestGenerateGuestSessionKeyFailsClosed(t *testing.T) {
	key, err := generateGuestSessionKey(strings.NewReader(strings.Repeat("k", 32)))
	if err != nil || len(key) != 32 || string(key) != strings.Repeat("k", 32) {
		t.Fatalf("key length=%d err=%v", len(key), err)
	}
	if _, err := generateGuestSessionKey(strings.NewReader("short")); err == nil ||
		!strings.Contains(err.Error(), "generate guest session key") {
		t.Fatalf("short entropy err=%v", err)
	}
	if _, err := generateGuestSessionKey(nil); err == nil {
		t.Fatal("nil entropy source accepted")
	}
}

func TestStorePersistsExactGuestExitStatus(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	state, err := store.Create(ctx, "exit-status", testBundle(t))
	if err != nil {
		t.Fatal(err)
	}
	exitedAt := time.Unix(123, 456).UTC()
	state.Annotations = map[string]string{"platform-factory.dev/memory-mib": "256", "platform-factory.dev/vcpus": "2"}
	state.Created = exitedAt.Add(-1500 * time.Millisecond)
	if err := store.update(ctx, state.ID, func(current *State) {
		current.Annotations = state.Annotations
		current.Created = state.Created
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetExited(ctx, state.ID, 23, exitedAt); err != nil {
		t.Fatal(err)
	}
	stored, found, err := store.Get(ctx, state.ID)
	if err != nil || !found {
		t.Fatalf("Get found=%v err=%v", found, err)
	}
	if stored.Status != "stopped" || stored.PID != 0 || stored.ExitStatus == nil || *stored.ExitStatus != 23 ||
		stored.ExitedAt == nil || !stored.ExitedAt.Equal(exitedAt) {
		t.Fatalf("state=%+v", stored)
	}
	if stored.Metrics == nil || stored.Metrics.APIVersion != "platform-factory.dev/vmm-metrics/v1" ||
		stored.Metrics.RuntimeMS != 1500 || stored.Metrics.MemoryMiB != 256 || stored.Metrics.VCPUs != 2 || stored.Metrics.ExitStatus != 23 {
		t.Fatalf("metrics=%+v", stored.Metrics)
	}
	if err := store.SetExited(ctx, state.ID, 1, time.Time{}); err == nil {
		t.Fatal("zero exit time accepted")
	}
}

func TestShutdownLogWatcherPublishesCompleteExitCode(t *testing.T) {
	var output bytes.Buffer
	var codes []uint32
	watcher := newShutdownLogWatcher(&output, func(code uint32) { codes = append(codes, code) })
	parts := []string{
		"unrelated line\nlevel=INFO component=microvm-init operation=supervise ",
		"phase=child-exit exit_",
		"code=23 action=poweroff\r\n",
		"level=INFO component=microvm-init operation=supervise phase=child-exit exit_code=7 action=poweroff\n",
	}
	for _, part := range parts {
		if _, err := watcher.Write([]byte(part)); err != nil {
			t.Fatal(err)
		}
	}
	if got := fmt.Sprint(codes); got != "[23]" {
		t.Fatalf("codes=%s output=%q", got, output.String())
	}
}
