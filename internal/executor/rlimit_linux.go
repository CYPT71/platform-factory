//go:build linux

package executor

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

func resourceLimitsSupported() bool { return true }

const rlimitHelperEnv = "PLATFORM_FACTORY_EXECUTOR_RLIMIT_HELPER"

type rlimitHelperPayload struct {
	MemoryBytes int64    `json:"memory_bytes"`
	Executable  string   `json:"executable"`
	Args        []string `json:"args"`
}

// wrapWithRlimitHelper rewrites cmd to re-exec the current binary with a
// hidden env-var payload instead of running the target directly.
// MaybeApplyRlimitHelper, which must be called at the very start of the
// consuming binary's main(), intercepts this on the freshly exec'd
// process — single-threaded, with no concurrent work from the original
// process that a lowered RLIMIT_AS could starve — sets the requested
// memory ceiling on itself, and execs the real target. cmd.Path/cmd.Args
// must already be resolved to the real target before calling this.
func wrapWithRlimitHelper(cmd *exec.Cmd, memoryBytes int64) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve current executable: %w", err)
	}
	payload, err := json.Marshal(rlimitHelperPayload{
		MemoryBytes: memoryBytes,
		Executable:  cmd.Path,
		Args:        cmd.Args[1:],
	})
	if err != nil {
		return err
	}
	cmd.Path = self
	cmd.Args = []string{self}
	cmd.Env = append(cmd.Env, rlimitHelperEnv+"="+string(payload))
	return nil
}

// MaybeApplyRlimitHelper must be called at the very start of main() by any
// binary that uses Executor for memory-limited stages. If this process was
// re-exec'd by wrapWithRlimitHelper, it applies the requested RLIMIT_AS to
// itself and execs the real target command, never returning. Otherwise it
// returns immediately and normal main() execution continues.
//
// This exists because RLIMIT_AS is process-wide: an earlier design lowered
// it directly on the running, multi-goroutine Executor process around
// cmd.Start(), which — confirmed empirically against a real Linux VM, not
// just reasoned about — occasionally starved unrelated concurrent
// allocations in that same process (observed as the race detector's own
// background bookkeeping hitting the ceiling under `go test -race`).
// Re-execing into a fresh process before applying the limit avoids that
// entire risk class.
func MaybeApplyRlimitHelper() {
	raw := os.Getenv(rlimitHelperEnv)
	if raw == "" {
		return
	}
	os.Unsetenv(rlimitHelperEnv)

	var payload rlimitHelperPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		fmt.Fprintln(os.Stderr, "executor: invalid rlimit helper payload:", err)
		os.Exit(1)
	}
	limit := syscall.Rlimit{Cur: uint64(payload.MemoryBytes), Max: uint64(payload.MemoryBytes)}
	if err := syscall.Setrlimit(syscall.RLIMIT_AS, &limit); err != nil {
		fmt.Fprintln(os.Stderr, "executor: set RLIMIT_AS:", err)
		os.Exit(1)
	}
	resolved, err := exec.LookPath(payload.Executable)
	if err != nil {
		fmt.Fprintln(os.Stderr, "executor: resolve executable:", err)
		os.Exit(1)
	}
	argv := append([]string{payload.Executable}, payload.Args...)
	if err := syscall.Exec(resolved, argv, os.Environ()); err != nil {
		fmt.Fprintln(os.Stderr, "executor: exec:", err)
		os.Exit(1)
	}
}
