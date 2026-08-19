// Command platform-factory-runtime is the OCI Runtime command facade selected by
// Podman. The initial vertical slice owns validated create/state/delete state;
// start/kill are reserved for the persistent native-VMM monitor.
//
// linux/amd64 only: it calls straight into internal/ociruntime's
// KVM-backed supervisor, which internal/hypervisor/kvm currently only
// implements for amd64.
//go:build linux && amd64

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/CYPT71/platform-factory/internal/ociruntime"
)

// waitPollInterval is how often `wait` re-reads the store while a
// container is still running. The OCI runtime CLI spec has no `wait`
// verb of its own - conmon/Podman normally waitpid()/pidfd-poll the PID
// this runtime already writes to --pid-file, which already terminates
// exactly when the guest does (ServeSupervisor's own process exits once
// the guest stops, see supervisor_linux.go) - but a poll-based `wait` is
// a convenience worth having for direct/scripted use of this binary
// without a process-supervision layer above it.
const waitPollInterval = 200 * time.Millisecond

var version = "dev"
var launchSupervisor = ociruntime.LaunchSupervisor

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "platform-factory-runtime:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	return runWithIO(ctx, args, os.Stdout, os.Stderr)
}

func runWithIO(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: platform-factory-runtime <create|start|state|kill|delete|wait|--version>")
	}
	args, err := normalizeInvocation(args)
	if err != nil {
		return err
	}
	if args[0] == "__serve" {
		return runSupervisor(ctx, args[1:])
	}
	if args[0] == "--version" || args[0] == "version" {
		fmt.Fprintf(stdout, "platform-factory-runtime %s\nociVersion: %s\n", version, ociruntime.OCIVersion)
		return nil
	}
	if args[0] == "features" {
		// This reports what LaunchSupervisor/ServeSupervisor (this
		// package's __serve child, see internal/ociruntime/supervisor_linux.go)
		// apply to their own process, not a per-container sandbox
		// negotiated from the bundle config. PID/IPC/UTS are created together
		// when supported. OCI state retains the host-visible PID while the
		// child authenticates with its persisted incarnation, so PID 1 inside
		// the namespace no longer conflicts with lifecycle state. Namespaces
		// and "cgroup.v2" are still probe-gated - both need
		// CAP_SYS_ADMIN or a delegated cgroup the caller isn't
		// guaranteed to have, and are silently skipped rather than
		// failing guest launch when unavailable (see the probes in
		// LaunchSupervisor and applyVMMSandbox). "seccomp.enabled" is a
		// real classic-BPF filter (internal/hypervisor/sandbox's
		// ApplyStrictSeccomp), not just PR_SET_NO_NEW_PRIVS.
		_, err := fmt.Fprintln(stdout, `{"ociVersionMin":"1.0.0","ociVersionMax":"1.2.0","hooks":[],"mountOptions":[],"linux":{"namespaces":["pid","ipc","uts"],"capabilities":["CAP_CHOWN","CAP_DAC_OVERRIDE","CAP_DAC_READ_SEARCH","CAP_FOWNER","CAP_FSETID","CAP_KILL","CAP_SETGID","CAP_SETUID","CAP_SETPCAP","CAP_LINUX_IMMUTABLE","CAP_NET_BIND_SERVICE","CAP_NET_BROADCAST","CAP_NET_ADMIN","CAP_NET_RAW","CAP_IPC_LOCK","CAP_IPC_OWNER","CAP_SYS_MODULE","CAP_SYS_RAWIO","CAP_SYS_CHROOT","CAP_SYS_PTRACE","CAP_SYS_PACCT","CAP_SYS_ADMIN","CAP_SYS_BOOT","CAP_SYS_NICE","CAP_SYS_RESOURCE","CAP_SYS_TIME","CAP_SYS_TTY_CONFIG","CAP_MKNOD","CAP_LEASE","CAP_AUDIT_WRITE","CAP_AUDIT_CONTROL","CAP_SETFCAP","CAP_MAC_OVERRIDE","CAP_MAC_ADMIN","CAP_SYSLOG","CAP_WAKE_ALARM","CAP_BLOCK_SUSPEND","CAP_AUDIT_READ","CAP_PERFMON","CAP_BPF","CAP_CHECKPOINT_RESTORE"],"cgroup":{"v1":false,"v2":true,"systemd":false},"seccomp":{"enabled":true}},"annotations":{"io.github.platform-factory.runtime":"native-kvm"}}`)
		return err
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", defaultStateRoot(), "runtime state root")
	bundle := flags.String("bundle", "", "OCI bundle directory")
	force := flags.Bool("force", false, "force deletion")
	pidFile := flags.String("pid-file", "", "write supervisor PID")
	consoleSocket := flags.String("console-socket", "", "OCI console socket (unsupported)")
	preserveFDs := flags.Int("preserve-fds", 0, "additional inherited descriptors (unsupported)")
	noPivot := flags.Bool("no-pivot", false, "disable pivot_root (unsupported)")
	noNewKeyring := flags.Bool("no-new-keyring", false, "disable session keyring")
	all := flags.Bool("all", false, "signal all processes (the MicroVM supervisor already owns the whole guest)")
	format := flags.String("format", "json", "list output format (json)")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *consoleSocket != "" || *preserveFDs != 0 || *noPivot {
		return errors.New("terminal console sockets, preserved descriptors, and --no-pivot are not supported")
	}
	_ = noNewKeyring
	if *all && args[0] != "kill" {
		return errors.New("--all is valid only for kill")
	}
	positionals := flags.Args()
	required := 1
	if args[0] == "kill" {
		required = 2
	} else if args[0] == "list" {
		required = 0
	}
	if len(positionals) != required {
		if args[0] == "list" {
			return errors.New("list does not accept a container ID")
		}
		return fmt.Errorf("%s requires exactly one container ID", args[0])
	}
	store, err := ociruntime.OpenStore(*root)
	if err != nil {
		return err
	}
	defer store.Close()
	switch args[0] {
	case "list":
		if *format != "json" {
			return fmt.Errorf("unsupported list format %q", *format)
		}
		states, err := store.List(ctx)
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(states)
	case "create":
		id := positionals[0]
		if *bundle == "" {
			return errors.New("create requires --bundle")
		}
		if _, err := store.Create(ctx, id, *bundle); err != nil {
			return err
		}
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		return launchSupervisor(ctx, store, id, executable, *pidFile)
	case "state":
		id := positionals[0]
		state, found, err := store.Get(ctx, id)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("container %q does not exist", id)
		}
		return json.NewEncoder(stdout).Encode(state)
	case "delete":
		id := positionals[0]
		return store.Delete(ctx, id, *force)
	case "start":
		id := positionals[0]
		return store.Start(ctx, id)
	case "kill":
		id := positionals[0]
		if len(positionals) != 2 {
			return errors.New("kill requires an ID and signal")
		}
		signal, err := parseSignal(positionals[1])
		if err != nil {
			return err
		}
		return store.Kill(ctx, id, signal)
	case "wait":
		id := positionals[0]
		return waitForExit(ctx, store, id, stdout)
	default:
		return fmt.Errorf("unsupported OCI runtime command %q", args[0])
	}
}

// normalizeInvocation accepts the runc-style global options conmon places
// before the command and moves --root into the command FlagSet. Logging is
// inherited through conmon, so --log/--log-format are accepted but not used
// as a second source of truth.
func normalizeInvocation(args []string) ([]string, error) {
	var root string
	for len(args) != 0 && strings.HasPrefix(args[0], "-") && args[0] != "--version" {
		switch args[0] {
		case "--root":
			if len(args) < 2 {
				return nil, errors.New("--root requires a value")
			}
			root, args = args[1], args[2:]
		case "--log", "--log-format":
			if len(args) < 2 {
				return nil, fmt.Errorf("%s requires a value", args[0])
			}
			args = args[2:]
		case "--systemd-cgroup":
			// Deliberately accepted and discarded, not wired to anything:
			// this flag tells a container runtime which cgroup *manager*
			// (systemd vs. cgroupfs) to place a container's own cgroup
			// tree under - a distinction with nothing to apply to here.
			// This runtime never creates a per-container cgroup at all;
			// internal/hypervisor/sandbox's cgroup primitive confines the
			// VMM supervisor process itself, gated by the separate
			// secure-oci.dev/sandbox-cgroups annotation
			// (sandboxConfigForSupervisor, supervisor_linux.go), not by
			// Podman's own cgroup-manager choice. Accepting the flag
			// keeps Podman's own invocation happy; there is no cgroup
			// manager selection left to make.
			args = args[1:]
		case "--debug":
			args = args[1:]
		default:
			return nil, fmt.Errorf("unsupported global runtime option %q", args[0])
		}
	}
	if len(args) == 0 {
		return nil, errors.New("missing OCI runtime command")
	}
	if root != "" {
		args = append([]string{args[0], "--root", root}, args[1:]...)
	}
	return args, nil
}

func runSupervisor(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("__serve", flag.ContinueOnError)
	root := flags.String("root", defaultStateRoot(), "runtime state root")
	readyFD := flags.Int("ready-fd", -1, "inherited supervisor readiness descriptor")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("__serve requires one container ID")
	}
	store, err := ociruntime.OpenStore(*root)
	if err != nil {
		return err
	}
	defer store.Close()
	return ociruntime.ServeSupervisor(ctx, store, flags.Arg(0), *readyFD)
}

// waitForExit polls the store for id until its status leaves "running"
// (or the container was never started at all - "created" also blocks,
// since nothing has run yet to wait for), then writes the terminal state
// as JSON, the same shape `state` already reports. Reconciliation
// happens for free: Store.Get (via getUnlocked, internal/ociruntime/runtime.go)
// already detects a crashed supervisor's dead PID and flips status to
// "stopped" on every read, so a killed guest is noticed by the very next
// poll, not just a graceful one.
func waitForExit(ctx context.Context, store *ociruntime.Store, id string, stdout io.Writer) error {
	for {
		state, found, err := store.Get(ctx, id)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("container %q does not exist", id)
		}
		if state.Status == "stopped" {
			return json.NewEncoder(stdout).Encode(state)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(waitPollInterval):
		}
	}
}

func defaultStateRoot() string {
	if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); filepath.IsAbs(runtimeDir) {
		return filepath.Join(runtimeDir, "platform-factory-runtime")
	}
	return filepath.Join("/run/user", strconv.Itoa(os.Geteuid()), "platform-factory-runtime")
}

func parseSignal(value string) (syscall.Signal, error) {
	value = strings.TrimPrefix(strings.ToUpper(value), "SIG")
	if number, err := strconv.Atoi(value); err == nil && number > 0 {
		return syscall.Signal(number), nil
	}
	signals := map[string]syscall.Signal{
		"TERM": syscall.SIGTERM, "KILL": syscall.SIGKILL, "INT": syscall.SIGINT,
		"QUIT": syscall.SIGQUIT, "HUP": syscall.SIGHUP,
	}
	signal, ok := signals[value]
	if !ok {
		return 0, fmt.Errorf("unsupported signal %q", value)
	}
	return signal, nil
}
