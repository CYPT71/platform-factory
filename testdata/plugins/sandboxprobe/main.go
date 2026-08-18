// Command secure-oci-sandboxprobe is a separate Go module, importing only
// the public sdk/plugin SDK, used solely to prove from inside a plugin
// subprocess that internal/plugin.Start's namespace sandbox actually took
// effect: that outbound network access is cut off, and that no_new_privs is
// set. It reports observations rather than asserting them itself so the
// real assertions live in the host-side Go test, next to the rest of the
// project's test suite.
package main

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	plugin "github.com/CYPT71/platform-factory/sdk/plugin"
)

// rlimitNPROC is RLIMIT_NPROC (include/uapi/asm-generic/resource.h); see
// internal/plugin/sandbox_linux.go's identical constant for why Go's
// syscall package has no exported name for it.
const rlimitNPROC = 6

func handleNetProbe(_ context.Context, _ json.RawMessage) (any, error) {
	conn, err := net.DialTimeout("tcp", "8.8.8.8:53", 3*time.Second)
	if err != nil {
		return map[string]string{"result": "denied", "detail": err.Error()}, nil
	}
	_ = conn.Close()
	return map[string]string{"result": "reached"}, nil
}

func handleIsolationProbe(_ context.Context, _ json.RawMessage) (any, error) {
	limits := map[string]string{}
	for name, resource := range map[string]int{
		"core": syscall.RLIMIT_CORE, "file_size": syscall.RLIMIT_FSIZE,
		"open_files": syscall.RLIMIT_NOFILE, "cpu": syscall.RLIMIT_CPU,
		"processes": rlimitNPROC, "address_space": syscall.RLIMIT_AS,
	} {
		var limit syscall.Rlimit
		if err := syscall.Getrlimit(resource, &limit); err != nil {
			return nil, err
		}
		limits[name] = strconv.FormatUint(limit.Cur, 10)
	}
	return limits, nil
}

// tmpProbeParams names a file the host created directly under the real,
// shared /tmp before this plugin started. Isolation here means the
// plugin's own scratch use ($TMPDIR) is private and empty - not that /tmp
// itself is sealed off, since detect/freeze/plan take a real caller-chosen
// project path over RPC that routinely lives under /tmp (see
// internal/plugin/sandbox_linux.go's isolateTempDirectory doc comment) - so
// this file living directly under /tmp (not under $TMPDIR) must still be
// visible: that's the regression this probe also guards against.
type tmpProbeParams struct {
	HostCanaryPath string `json:"host_canary_path"`
}

func handleTmpProbe(_ context.Context, raw json.RawMessage) (any, error) {
	var params tmpProbeParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, err
	}
	_, statErr := os.Stat(params.HostCanaryPath)
	scratch := os.Getenv("TMPDIR")
	if scratch == "" {
		// isolateTempDirectory (internal/plugin/sandbox_linux.go) is
		// documented best-effort: on a host that denies the mount
		// namespace operations it needs, TMPDIR is left unset rather
		// than the plugin silently trusting an unisolated value.
		// Report that plainly instead of failing open with os.ReadDir(""),
		// which returns a confusing "open : no such file or directory"
		// that looks like a probe bug rather than a host capability gap.
		return map[string]string{
			"tmpdir":               "",
			"scratch_entry_count":  "",
			"shared_tmp_reachable": strconv.FormatBool(statErr == nil),
		}, nil
	}
	entries, err := os.ReadDir(scratch)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"tmpdir":               scratch,
		"scratch_entry_count":  strconv.Itoa(len(entries)),
		"shared_tmp_reachable": strconv.FormatBool(statErr == nil),
	}, nil
}

// handleNetnsProbe reports which network namespace this plugin is actually
// running in, by reading the /proc/self/ns/net symlink target (a stable
// "netns:[<inode>]" identifier the kernel assigns per namespace). The host
// test compares this against its own /proc/self/ns/net to prove
// hostNetworkGranted's effect directly, rather than depending on real
// outbound connectivity being available in whatever environment the test
// happens to run in.
func handleNetnsProbe(_ context.Context, _ json.RawMessage) (any, error) {
	target, err := os.Readlink("/proc/self/ns/net")
	if err != nil {
		return nil, err
	}
	return map[string]string{"netns": target}, nil
}

// memoryBombParams.Bytes is how much memory handleMemoryBomb actively
// tries to commit - the caller decides whether that is comfortably under
// or comfortably over the profile's RLIMIT_AS ceiling; this handler does
// not know or care which.
type memoryBombParams struct {
	Bytes int64 `json:"bytes"`
}

// handleMemoryBomb is a genuinely hostile probe: it does not ask what the
// memory ceiling is (that's handleIsolationProbe), it tries to violate it,
// by allocating and then touching (forcing the kernel to actually commit)
// params.Bytes of memory. If params.Bytes exceeds the process's real
// RLIMIT_AS, Go's own runtime allocator hits ENOMEM on the underlying
// mmap/brk syscall and fatally crashes the whole process (runtime: out of
// memory) - not a recoverable Go error, so this handler cannot itself
// report failure; the host observes enforcement by the RPC call failing
// because the plugin process died attempting it, not by a normal response.
func handleMemoryBomb(_ context.Context, raw json.RawMessage) (any, error) {
	var params memoryBombParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, err
	}
	data := make([]byte, params.Bytes)
	for i := 0; i < len(data); i += 4096 {
		data[i] = 1
	}
	return map[string]int64{"committed_bytes": int64(len(data))}, nil
}

// forkBombParams.Attempts is how many child processes handleForkBomb
// actively tries to have alive at once.
type forkBombParams struct {
	Attempts int `json:"attempts"`
}

// handleForkBomb is the RLIMIT_NPROC analogue of handleMemoryBomb: it
// tries to have params.Attempts child processes alive simultaneously
// (each a long-enough-lived "sleep" so they overlap), not just asking what
// the limit is. Unlike a memory-ceiling violation, exceeding RLIMIT_NPROC
// does not crash the plugin process itself - each individual fork/clone
// past the ceiling just fails with EAGAIN - so this handler can report
// results normally rather than dying; the host asserts on the returned
// counts instead of on the RPC call itself failing.
func handleForkBomb(_ context.Context, raw json.RawMessage) (any, error) {
	var params forkBombParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, err
	}
	started := make([]*exec.Cmd, 0, params.Attempts)
	succeeded, failed := 0, 0
	for i := 0; i < params.Attempts; i++ {
		cmd := exec.Command("sleep", "5")
		if err := cmd.Start(); err != nil {
			failed++
			continue
		}
		succeeded++
		started = append(started, cmd)
	}
	for _, cmd := range started {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
	return map[string]int{"attempted": params.Attempts, "succeeded": succeeded, "failed": failed}, nil
}

func handlePrivProbe(_ context.Context, _ json.RawMessage) (any, error) {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if name, value, found := strings.Cut(line, ":"); found && name == "NoNewPrivs" {
			return map[string]string{"no_new_privs": strings.TrimSpace(value)}, nil
		}
	}
	return map[string]string{"no_new_privs": "absent"}, nil
}

func main() {
	server := plugin.NewServer("sandboxprobe", "v0.1.0")
	server.Handle("observe.net-probe", handleNetProbe)
	server.Handle("observe.priv-probe", handlePrivProbe)
	server.Handle("observe.isolation-probe", handleIsolationProbe)
	server.Handle("observe.tmp-probe", handleTmpProbe)
	server.Handle("observe.netns-probe", handleNetnsProbe)
	server.Handle("observe.memory-bomb", handleMemoryBomb)
	server.Handle("observe.fork-bomb", handleForkBomb)
	if err := server.Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
		os.Exit(1)
	}
}
