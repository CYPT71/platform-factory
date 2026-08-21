// microvm-init is a tiny PID 1 for the scripts/microvm boot path. It execs
// exactly one child process, forwards SIGTERM/SIGINT to it, waits for it to
// exit, drains any exited descendants reparented to PID 1, and powers the
// machine off.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

type childProcess interface {
	Start() error
	Wait() (exitCode int, err error)
	Signal(os.Signal) error
}

type execChild struct {
	cmd *exec.Cmd
}

func newExecChild(path string, args []string) *execChild {
	return newConfiguredExecChild(path, args, nil, "")
}

func newConfiguredExecChild(path string, args, env []string, cwd string, identity ...processIdentity) *execChild {
	cmd := exec.Command(path, args...)
	if env != nil {
		cmd.Env = append([]string(nil), env...)
	}
	if cwd != "" {
		cmd.Dir = cwd
	}
	if len(identity) != 0 {
		applyProcessIdentity(cmd, identity[0])
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return &execChild{cmd: cmd}
}

type processConfig struct {
	Args    []string        `json:"args"`
	Env     []string        `json:"env,omitempty"`
	Cwd     string          `json:"cwd,omitempty"`
	UID     uint32          `json:"uid,omitempty"`
	GID     uint32          `json:"gid,omitempty"`
	Groups  []uint32        `json:"additional_gids,omitempty"`
	Umask   *uint32         `json:"umask,omitempty"`
	Rlimits []processRlimit `json:"rlimits,omitempty"`
}

type processRlimit struct {
	Type string `json:"type"`
	Hard uint64 `json:"hard"`
	Soft uint64 `json:"soft"`
}

type processIdentity struct {
	UID    uint32
	GID    uint32
	Groups []uint32
}

func loadProcess(path string) (processConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return processConfig{}, err
	}
	var config processConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return processConfig{}, fmt.Errorf("decode process config: %w", err)
	}
	if len(config.Args) == 0 || len(config.Args) > 128 || !filepath.IsAbs(config.Args[0]) {
		return processConfig{}, errors.New("process args must contain 1..128 values and start with an absolute path")
	}
	if config.Cwd == "" {
		config.Cwd = "/"
	}
	if !filepath.IsAbs(config.Cwd) {
		return processConfig{}, errors.New("process cwd must be absolute")
	}
	for _, value := range append(append([]string(nil), config.Args...), config.Env...) {
		if strings.ContainsRune(value, 0) {
			return processConfig{}, errors.New("process config contains NUL")
		}
	}
	for _, value := range config.Env {
		if strings.IndexByte(value, '=') <= 0 {
			return processConfig{}, errors.New("process environment entries must use KEY=value")
		}
	}
	return config, nil
}

func (c *execChild) Start() error { return c.cmd.Start() }

func (c *execChild) Wait() (int, error) {
	err := c.cmd.Wait()
	if err == nil {
		return 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return 128 + int(status.Signal()), nil
		}
		return exitErr.ExitCode(), nil
	}
	return 1, err
}

func (c *execChild) Signal(sig os.Signal) error {
	if c.cmd.Process == nil {
		return nil
	}
	return c.cmd.Process.Signal(sig)
}

// run starts child, forwards every signal received on sigCh to it, waits
// for it to exit, and returns the exit code the caller should report.
func run(child childProcess, sigCh <-chan os.Signal, stderr io.Writer) int {
	if err := child.Start(); err != nil {
		fmt.Fprintf(stderr, "level=ERROR component=microvm-init operation=supervise phase=start error=%q\n", err)
		return 1
	}

	type result struct {
		code int
		err  error
	}
	waitCh := make(chan result, 1)
	go func() {
		code, err := child.Wait()
		waitCh <- result{code, err}
	}()

	for {
		select {
		case sig := <-sigCh:
			if err := child.Signal(sig); err != nil {
				fmt.Fprintf(stderr, "level=WARN component=microvm-init operation=supervise phase=signal signal=%q error=%q\n", sig, err)
			}
		case r := <-waitCh:
			if r.err != nil {
				fmt.Fprintf(stderr, "level=ERROR component=microvm-init operation=supervise phase=wait error=%q\n", r.err)
			}
			return r.code
		}
	}
}

// parseArgs decides which binary to run as the supervised child: the OCI
// layout's standard entrypoint path by default, or an explicit override
// (and its own arguments) if given.
func parseArgs(args []string) (path string, rest []string) {
	if len(args) > 0 {
		return args[0], args[1:]
	}
	return "/app/service", nil
}

func loadArgs(args []string, configPath string) (string, []string, error) {
	if len(args) > 0 {
		path, rest := parseArgs(args)
		return path, rest, nil
	}
	encoded, err := os.ReadFile(configPath)
	if errors.Is(err, os.ErrNotExist) {
		return "/app/service", nil, nil
	}
	if err != nil {
		return "", nil, fmt.Errorf("read entrypoint config: %w", err)
	}
	var configured []string
	if err := json.Unmarshal(encoded, &configured); err != nil {
		return "", nil, fmt.Errorf("decode entrypoint config: %w", err)
	}
	if len(configured) == 0 || len(configured) > 128 || !filepath.IsAbs(configured[0]) {
		return "", nil, errors.New("entrypoint config must contain 1..128 arguments and start with an absolute path")
	}
	for _, value := range configured {
		if strings.ContainsRune(value, 0) {
			return "", nil, errors.New("entrypoint config contains NUL")
		}
	}
	return configured[0], configured[1:], nil
}

// realMain runs the child to completion and powers off, with every
// dependency injected so it is testable without ever invoking the real
// (irreversible) poweroff syscall.
func realMain(path string, args []string, sigCh <-chan os.Signal, stdout, stderr io.Writer, poweroffFn func() error) int {
	return realMainChild(newExecChild(path, args), sigCh, stdout, stderr, poweroffFn)
}

func realMainChild(child childProcess, sigCh <-chan os.Signal, stdout, stderr io.Writer, poweroffFn func() error) int {
	code := run(child, sigCh, stderr)
	reaped := reapExitedChildren(stderr)
	fmt.Fprintf(stdout, "level=INFO component=microvm-init operation=supervise phase=reap reaped=%d\n", reaped)
	fmt.Fprintf(stdout, "level=INFO component=microvm-init operation=supervise phase=child-exit exit_code=%d action=poweroff\n", code)
	if err := poweroffFn(); err != nil {
		fmt.Fprintf(stderr, "level=ERROR component=microvm-init operation=supervise phase=poweroff error=%q\n", err)
	}
	return code
}

func main() {
	if os.Getpid() != 1 {
		fmt.Fprintln(os.Stderr, "level=WARN component=microvm-init operation=supervise phase=preflight message=\"not running as PID 1\"")
	}

	// Best-effort: the kernel already wires PID 1's own fds 0..2 to
	// /dev/console when devtmpfs is mounted early enough, so a failure
	// here (e.g. no /dev/console or /dev/ttyS0 node at all) is not fatal -
	// it just means diagnostics rely on whatever fds PID 1 already has.
	if err := prepareConsole(); err != nil {
		fmt.Fprintf(os.Stderr, "level=WARN component=microvm-init operation=supervise phase=preflight message=%q\n", err)
	}
	if err := prepareProc(); err != nil {
		fmt.Fprintf(os.Stderr, "level=ERROR component=microvm-init operation=supervise phase=mount-proc error=%q\n", err)
		os.Exit(1)
	}
	if err := prepareMessageQueue(); err != nil {
		fmt.Fprintf(os.Stderr, "level=ERROR component=microvm-init operation=supervise phase=mount-mqueue error=%q\n", err)
		os.Exit(1)
	}
	if err := prepareContainerEnv(); err != nil {
		fmt.Fprintf(os.Stderr, "level=ERROR component=microvm-init operation=supervise phase=containerenv error=%q\n", err)
		os.Exit(1)
	}
	if err := prepareSharedMemory(); err != nil {
		fmt.Fprintf(os.Stderr, "level=ERROR component=microvm-init operation=supervise phase=mount-shm error=%q\n", err)
		os.Exit(1)
	}

	// Fire-and-forget: only ever does anything on a DHCP-booted guest
	// (the native HVF path), a no-op file read otherwise. Never blocks
	// the child process below.
	go reportGuestIPIfDHCP(os.Stdout)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	var child childProcess
	if len(os.Args) > 1 {
		path, args := parseArgs(os.Args[1:])
		child = newExecChild(path, args)
	} else if config, err := loadProcess("/etc/platform-factory/process.json"); err == nil {
		if err := applyProcessRlimits(config.Rlimits); err != nil {
			fmt.Fprintf(os.Stderr, "level=ERROR component=microvm-init operation=supervise phase=rlimit error=%q\n", err)
			os.Exit(1)
		}
		if config.Umask != nil {
			applyProcessUmask(*config.Umask)
		}
		child = newConfiguredExecChild(config.Args[0], config.Args[1:], config.Env, config.Cwd,
			processIdentity{UID: config.UID, GID: config.GID, Groups: config.Groups})
	} else if !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(os.Stderr, "level=ERROR component=microvm-init operation=supervise phase=config error=%q\n", err)
		os.Exit(1)
	} else {
		path, args, err := loadArgs(nil, "/etc/platform-factory/entrypoint.json")
		if err != nil {
			fmt.Fprintf(os.Stderr, "level=ERROR component=microvm-init operation=supervise phase=config error=%q\n", err)
			os.Exit(1)
		}
		child = newExecChild(path, args)
	}
	os.Exit(runWithOptionalGuestEndpoint(child, sigCh, os.Stdout, os.Stderr, poweroff,
		loadGuestEndpointConfig, loadGuestSessionKey, openGuestAgentDevice))
}
