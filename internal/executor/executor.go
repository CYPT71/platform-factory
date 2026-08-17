// Package executor runs a single validated pipeline stage as a local OS
// process. It implements internal/pipeline's StageRunner interface.
//
// This is a deliberately minimal, honestly-scoped executor: it does not
// namespace, chroot or otherwise isolate the child process. It only
// supports stages whose declared network policy is "none" (there is no
// network sandbox here, so a stage requesting any other policy is refused
// rather than silently run unisolated) and it only enforces the one
// resource dimension it can safely and correctly approximate without
// cgroups or root: a memory ceiling via RLIMIT_AS, and only on Linux (the
// only OS this repo's CI runs on) — Darwin's kernel accepts the
// RLIMIT_AS constant but rejects setting it, so a memory limit request is
// refused rather than silently unenforced there. CPU rate limits
// (Stage.Resources.CPUMilli is a millicore share, not a CPU-time budget)
// and process-count limits (RLIMIT_NPROC is scoped to the whole real UID,
// not this stage's process tree) are refused rather than approximated
// incorrectly. Mounts, artifact transfer and secret injection remain the
// caller's responsibility; this package only runs a command in a
// caller-resolved working directory.
//
// A memory limit is enforced by re-execing into a fresh child process
// that sets its own RLIMIT_AS before exec'ing the real target (see
// MaybeApplyRlimitHelper in rlimit_linux.go), rather than lowering the
// calling Executor process's own rlimit around cmd.Start(). An earlier
// version did the latter; confirmed empirically against a real Linux VM
// under `go test -race` (not just reasoned about), it occasionally
// starved unrelated concurrent allocations in the calling process itself
// (observed as the race detector's own background bookkeeping hitting the
// ceiling). Any binary using Executor for memory-limited stages must call
// MaybeApplyRlimitHelper at the very start of main().
package executor

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/CYPT71/platform-factory/internal/core"
)

const maxCapturedBytes = 1 << 20

// Result is the detailed journal entry recorded for one stage run.
type Result struct {
	Stage    string
	Started  time.Time
	Duration time.Duration
	ExitCode int
	Stdout   []byte
	Stderr   []byte
	Err      string
}

// Executor runs stages under a single resolved local root directory.
// Stage.Command.WorkingDir and other abstract container-style paths are
// mapped onto that root via MapPath.
type Executor struct {
	root    string
	baseEnv []string

	sandboxed    bool
	support      SandboxSupport
	mountSources map[string]string
	secrets      SecretResolver
	dnsForwarder core.NetworkRelay

	resultsMu sync.Mutex
	results   []Result
}

// WithDNSForwarder enables the resolve-only network policy. The forwarder must
// name an explicit upstream; stages fail closed when it is absent or invalid.
func (e *Executor) WithDNSForwarder(forwarder core.NetworkRelay) *Executor {
	e.dnsForwarder = forwarder
	return e
}

// New returns an Executor that maps abstract stage paths under root. If
// baseEnv is empty, a minimal PATH is used so common tools can be found;
// stages do not inherit the host's full environment.
func New(root string, baseEnv []string) *Executor {
	if len(baseEnv) == 0 {
		baseEnv = []string{
			"PATH=/usr/bin:/bin",
			"LANG=C.UTF-8",
			"LC_ALL=C.UTF-8",
			"TZ=UTC",
			"SOURCE_DATE_EPOCH=0",
		}
	}
	return &Executor{root: root, baseEnv: append([]string(nil), baseEnv...)}
}

// WithSecretResolver equips the sandboxed executor with a secret
// source. Secrets are delivered on a tmpfs inside the stage's mount
// namespace and vanish with it; the plain executor refuses stages with
// secrets because it has no private mount namespace to confine them
// to.
func (e *Executor) WithSecretResolver(resolver SecretResolver) *Executor {
	e.secrets = resolver
	return e
}

// Results returns a copy of every stage result recorded so far, in the
// order stages completed.
func (e *Executor) Results() []Result {
	e.resultsMu.Lock()
	defer e.resultsMu.Unlock()
	return append([]Result(nil), e.results...)
}

// Run executes stage and satisfies internal/StageRunner.
func (e *Executor) Run(ctx context.Context, stage core.Stage) error {
	started := time.Now()

	var cmd *exec.Cmd
	var group *stageCgroup
	var relay net.Conn
	var redactions [][]byte
	var err error
	if e.sandboxed {
		cmd, group, relay, redactions, err = e.prepareSandboxed(ctx, stage)
	} else {
		cmd, err = e.preparePlain(ctx, stage)
	}
	if err != nil {
		return e.reject(stage.ID, started, err)
	}
	if group != nil {
		defer group.cleanup()
	}
	if relay != nil {
		defer relay.Close()
	}

	var stdout, stderr boundedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		for _, file := range cmd.ExtraFiles {
			_ = file.Close()
		}
		return e.reject(stage.ID, started, fmt.Errorf("executor: start stage %q: %w", stage.ID, err))
	}
	for _, file := range cmd.ExtraFiles {
		_ = file.Close()
	}
	cmd.ExtraFiles = nil

	waitErr := cmd.Wait()
	result := Result{
		Stage:    stage.ID,
		Started:  started,
		Duration: time.Since(started),
		Stdout:   redactSecrets(stdout.Bytes(), redactions),
		Stderr:   redactSecrets(stderr.Bytes(), redactions),
	}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	if waitErr != nil {
		result.Err = waitErr.Error()
	}
	e.record(result)
	return waitErr
}

// preparePlain builds the command for the minimal, unsandboxed
// executor, refusing every policy it cannot enforce.
func (e *Executor) preparePlain(ctx context.Context, stage core.Stage) (*exec.Cmd, error) {
	if network := effectiveNetwork(stage.Network); network != core.NetworkNone {
		return nil, fmt.Errorf(
			"executor: stage %q requests network policy %q; this executor has no network sandbox and only honors %q",
			stage.ID, network, core.NetworkNone)
	}
	if stage.Resources.CPUMilli > 0 {
		return nil, fmt.Errorf(
			"executor: stage %q requests a CPU limit; CPU millicores are a rate limit that requires cgroups, which this executor does not have",
			stage.ID)
	}
	if stage.Resources.PIDs > 0 {
		return nil, fmt.Errorf(
			"executor: stage %q requests a process-count limit; RLIMIT_NPROC is scoped to the whole user, not this stage, so it is refused rather than approximated",
			stage.ID)
	}
	if len(stage.Secrets) > 0 {
		return nil, fmt.Errorf(
			"executor: stage %q declares secrets; only the sandboxed executor can confine them to a private in-memory mount",
			stage.ID)
	}
	if stage.Sandbox.ReadOnlyRoot || stage.Sandbox.NonRoot {
		return nil, fmt.Errorf(
			"executor: stage %q requests sandbox policies; this executor has no mount or user namespace to honor them",
			stage.ID)
	}
	memoryBytes := stage.Resources.MemoryMiB << 20
	if memoryBytes > 0 && !resourceLimitsSupported() {
		return nil, fmt.Errorf(
			"executor: stage %q requests a memory limit, which is not supported on this platform", stage.ID)
	}

	cmd := exec.CommandContext(ctx, stage.Command.Executable, stage.Command.Args...)
	cmd.Dir = MapPath(e.root, stage.Command.WorkingDir)
	cmd.Env = e.stageEnv(stage)
	if memoryBytes > 0 {
		if err := wrapWithRlimitHelper(cmd, memoryBytes); err != nil {
			return nil, fmt.Errorf("executor: stage %q: %w", stage.ID, err)
		}
	}
	return cmd, nil
}

// prepareSandboxed builds the namespaced command: the stage root
// becomes the filesystem root, network none is enforced by
// CLONE_NEWNET, pids/cpu limits fail closed without cgroup support,
// and secrets are resolved into the payload for in-memory delivery.
func (e *Executor) stageEnv(stage core.Stage) []string {
	env := append([]string(nil), e.baseEnv...)
	env = append(env, "PLATFORM_FACTORY_ROOT="+e.root)
	for key, value := range stage.Env {
		env = append(env, key+"="+value)
	}
	return env
}

func (e *Executor) reject(stageID string, started time.Time, err error) error {
	e.record(Result{Stage: stageID, Started: started, Duration: time.Since(started), ExitCode: -1, Err: err.Error()})
	return err
}

func (e *Executor) record(r Result) {
	e.resultsMu.Lock()
	defer e.resultsMu.Unlock()
	e.results = append(e.results, r)
}

// MapPath maps an abstract, container-style absolute path (as used in
// Stage.Command.WorkingDir) onto a real path under root.
func MapPath(root, abstractPath string) string {
	if abstractPath == "" || abstractPath == "/" {
		return root
	}
	return filepath.Join(root, strings.TrimPrefix(abstractPath, "/"))
}

func effectiveNetwork(policy core.NetworkPolicy) core.NetworkPolicy {
	if policy == "" {
		return core.NetworkNone
	}
	return policy
}

// boundedBuffer captures at most maxCapturedBytes of output; anything
// beyond that is silently dropped so a runaway stage cannot exhaust the
// executor's memory.
type boundedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := maxCapturedBytes - b.buf.Len()
	if remaining <= 0 {
		return len(p), nil
	}
	if len(p) > remaining {
		b.buf.Write(p[:remaining])
		return len(p), nil
	}
	b.buf.Write(p)
	return len(p), nil
}

func (b *boundedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf.Bytes()...)
}
