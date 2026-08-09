package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

type fakeChild struct {
	startErr  error
	started   bool
	signals   []os.Signal
	onSignal  func(os.Signal)
	exitAfter chan struct{}
	exitCode  int
	exitErr   error
}

func newFakeChild() *fakeChild {
	return &fakeChild{exitAfter: make(chan struct{})}
}

func (f *fakeChild) Start() error {
	f.started = true
	return f.startErr
}

func (f *fakeChild) Signal(sig os.Signal) error {
	f.signals = append(f.signals, sig)
	if f.onSignal != nil {
		f.onSignal(sig)
	}
	return nil
}

func (f *fakeChild) Wait() (int, error) {
	<-f.exitAfter
	return f.exitCode, f.exitErr
}

func TestRunPropagatesChildExitCode(t *testing.T) {
	f := newFakeChild()
	f.exitCode = 7
	close(f.exitAfter)

	got := run(f, make(chan os.Signal), io.Discard)
	if !f.started {
		t.Fatal("child was never started")
	}
	if got != 7 {
		t.Fatalf("exit code = %d, want 7", got)
	}
}

func TestRunForwardsSignalsToChild(t *testing.T) {
	f := newFakeChild()
	f.onSignal = func(os.Signal) { close(f.exitAfter) }

	sigCh := make(chan os.Signal, 1)
	sigCh <- syscall.SIGTERM

	got := run(f, sigCh, io.Discard)
	if got != 0 {
		t.Fatalf("exit code = %d, want 0", got)
	}
	if len(f.signals) != 1 || f.signals[0] != syscall.SIGTERM {
		t.Fatalf("signals forwarded = %v, want [SIGTERM]", f.signals)
	}
}

func TestRunReturnsOneWhenChildFailsToStart(t *testing.T) {
	f := newFakeChild()
	f.startErr = errors.New("no such file")
	close(f.exitAfter)

	got := run(f, make(chan os.Signal), io.Discard)
	if got != 1 {
		t.Fatalf("exit code = %d, want 1", got)
	}
}

func TestRunReturnsChildsReportedCodeEvenWhenWaitAlsoErrors(t *testing.T) {
	f := newFakeChild()
	f.exitCode = 3
	f.exitErr = errors.New("wait: no child processes")
	close(f.exitAfter)

	got := run(f, make(chan os.Signal), io.Discard)
	if got != 3 {
		t.Fatalf("exit code = %d, want 3 (run() trusts the code childProcess.Wait reports, logging the error separately)", got)
	}
}

// The following exercise the real execChild against actual subprocesses (not
// the fake above), on whatever host runs `go test` - never inside a VM and
// never as PID 1, so this is safe on any developer machine or CI runner.

func TestExecChildRunsRealProcessAndReportsExitCode(t *testing.T) {
	c := newExecChild("sh", []string{"-c", "exit 5"})
	if err := c.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	code, err := c.Wait()
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if code != 5 {
		t.Fatalf("exit code = %d, want 5", code)
	}
}

func TestExecChildReportsSuccessAsZero(t *testing.T) {
	c := newExecChild("sh", []string{"-c", "exit 0"})
	if err := c.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	code, err := c.Wait()
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
}

func TestExecChildForwardsSignalToRealProcess(t *testing.T) {
	c := newExecChild("sleep", []string{"30"})
	if err := c.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := c.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal: %v", err)
	}
	code, err := c.Wait()
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if code != 128+int(syscall.SIGTERM) {
		t.Fatalf("exit code = %d, want shell/OCI SIGTERM status", code)
	}
}

func TestExecChildSignalBeforeStartIsNoop(t *testing.T) {
	c := newExecChild("sh", []string{"-c", "exit 0"})
	if err := c.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal before start should be a no-op, got: %v", err)
	}
}

func TestExecChildStartErrorForMissingBinary(t *testing.T) {
	c := newExecChild("/no/such/binary-xyz", nil)
	if err := c.Start(); err == nil {
		t.Fatal("expected an error starting a nonexistent binary")
	}
}

func TestParseArgsDefaultsToAppService(t *testing.T) {
	path, rest := parseArgs(nil)
	if path != "/app/service" || len(rest) != 0 {
		t.Fatalf("got (%q, %v), want (/app/service, [])", path, rest)
	}
}

func TestParseArgsUsesFirstArgAsPathAndForwardsTheRest(t *testing.T) {
	path, rest := parseArgs([]string{"/bin/foo", "-x", "1"})
	if path != "/bin/foo" {
		t.Fatalf("path = %q, want /bin/foo", path)
	}
	if len(rest) != 2 || rest[0] != "-x" || rest[1] != "1" {
		t.Fatalf("rest = %v, want [-x 1]", rest)
	}
}

func TestLoadProcessValidatesProjectedOCIContract(t *testing.T) {
	write := func(t *testing.T, document string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "process.json")
		if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	valid := write(t, `{
		"args":["/app/service","--serve"],
		"env":["A=B"],
		"uid":1000,
		"gid":1001,
		"additional_gids":[1002],
		"umask":23
	}`)
	config, err := loadProcess(valid)
	if err != nil {
		t.Fatal(err)
	}
	if config.Cwd != "/" || config.Umask == nil || *config.Umask != 0o027 ||
		config.UID != 1000 || config.GID != 1001 || len(config.Groups) != 1 {
		t.Fatalf("config=%+v", config)
	}

	for _, test := range []struct {
		name     string
		document string
		want     string
	}{
		{name: "invalid json", document: `{`, want: "decode process config"},
		{name: "missing args", document: `{}`, want: "1..128"},
		{name: "relative executable", document: `{"args":["app"]}`, want: "absolute path"},
		{name: "relative cwd", document: `{"args":["/app"],"cwd":"work"}`, want: "cwd must be absolute"},
		{name: "nul argument", document: "{\"args\":[\"/app\\u0000bad\"]}", want: "contains NUL"},
		{name: "invalid environment", document: `{"args":["/app"],"env":["INVALID"]}`, want: "KEY=value"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := loadProcess(write(t, test.document))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v want substring %q", err, test.want)
			}
		})
	}
	if _, err := loadProcess(filepath.Join(t.TempDir(), "missing.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file error=%v", err)
	}
}

func TestLoadArgsFromConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "entrypoint.json")
	if err := os.WriteFile(path, []byte(`["/opt/service","--listen=:8080"]`), 0o600); err != nil {
		t.Fatal(err)
	}
	gotPath, gotArgs, err := loadArgs(nil, path)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/opt/service" || len(gotArgs) != 1 || gotArgs[0] != "--listen=:8080" {
		t.Fatalf("got (%q, %v)", gotPath, gotArgs)
	}
}

func TestLoadArgsRejectsInvalidConfig(t *testing.T) {
	for _, content := range []string{`{}`, `[]`, `["relative"]`, `["/app","bad\u0000arg"]`} {
		path := filepath.Join(t.TempDir(), "entrypoint.json")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := loadArgs(nil, path); err == nil {
			t.Fatalf("accepted %s", content)
		}
	}
}

// realMain is exercised against a real short-lived subprocess, but with
// poweroff injected as a fake - it must never call the real, irreversible
// poweroff() during a test run.

func TestRealMainRunsChildAndCallsPoweroff(t *testing.T) {
	var poweroffCalled bool
	poweroffFn := func() error { poweroffCalled = true; return nil }
	var stdout, stderr bytes.Buffer

	code := realMain("sh", []string{"-c", "exit 5"}, make(chan os.Signal), &stdout, &stderr, poweroffFn)

	if code != 5 {
		t.Fatalf("code = %d, want 5", code)
	}
	if !poweroffCalled {
		t.Fatal("poweroff was not called after the child exited")
	}
	if !strings.Contains(stdout.String(), "code=5") {
		t.Fatalf("stdout = %q, want it to report the child's exit code", stdout.String())
	}
	if !strings.Contains(stdout.String(), "phase=reap reaped=") {
		t.Fatalf("stdout = %q, want a zombie-reaping result", stdout.String())
	}
}

func TestRealMainLogsAndStillReturnsCodeWhenPoweroffFails(t *testing.T) {
	poweroffFn := func() error { return errors.New("poweroff not supported here") }
	var stdout, stderr bytes.Buffer

	code := realMain("sh", []string{"-c", "exit 0"}, make(chan os.Signal), &stdout, &stderr, poweroffFn)

	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	if !strings.Contains(stderr.String(), "poweroff not supported here") {
		t.Fatalf("stderr = %q, want it to mention the poweroff error", stderr.String())
	}
}
