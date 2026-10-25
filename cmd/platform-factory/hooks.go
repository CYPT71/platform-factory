package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/CYPT71/platform-factory/internal/gitlabci"
	"github.com/CYPT71/platform-factory/internal/hooks"
)

// run keeps lifecycle hooks around all existing commands. Disabled means no
// buffers, log copies, callbacks, or changes to the existing execution path.
func run(args []string, stdout, stderr io.Writer) int {
	path := os.Getenv(hooks.ConfigEnv)
	if path == "" || os.Getenv(hooks.ActiveEnv) == "1" || (len(args) > 0 && args[0] == "hooks") {
		return runCommand(args, stdout, stderr)
	}
	cfg, err := hooks.Load(path)
	if err != nil {
		fmt.Fprintln(stderr, "hooks: configuration rejected")
		return 2
	}
	root, err := os.Getwd()
	if err != nil {
		return 2
	}
	command := "unknown"
	if len(args) > 0 {
		switch args[0] {
		case "build", "init", "run", "publish", "deploy", "project", "plan", "freeze", "logs", "events", "rollback", "import", "microvm", "launch", "inspect", "verify", "version", "plugin", "marketplace", "extensions", "ci", "pipeline", "doctor", "status", "diff", "sbom", "evidence", "registry", "mcp":
			command = args[0]
		}
	}
	// Arguments, stdin, application logs and credentials never enter hook events.
	dispatch := func(kind string, code int, duration time.Duration) {
		if err := hooks.Dispatch(context.Background(), cfg, hooks.NewEvent(kind, command, root, code, duration)); err != nil {
			fmt.Fprintln(stderr, "hooks: callback failed; application exit status preserved")
		}
	}
	dispatch("command.started", 0, 0)
	start := time.Now()
	code := runCommand(args, stdout, stderr)
	kind := "command.succeeded"
	if code != 0 {
		kind = "command.failed"
	}
	dispatch(kind, code, time.Since(start))
	return code
}
func runHooks(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "--help" {
		fmt.Fprintln(stdout, "usage: pf hooks validate --config FILE | emit --config FILE --event EVENT --command NAME --exit-code CODE | autofix --config FILE < incident.json | gitlab --config FILE < event.json | codex-fixer < context.json")
		return 0
	}
	if args[0] == "codex-fixer" && len(args) == 1 {
		return runPythonHook(hooks.CodexFixerScript, nil, os.Stdin, stdout, stderr)
	}
	flags := flag.NewFlagSet("hooks "+args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	config := flags.String("config", "", "absolute trusted configuration file")
	kind := flags.String("event", "command.failed", "lifecycle event")
	command := flags.String("command", "manual", "command name without arguments")
	code := flags.Int("exit-code", 1, "original application exit status")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if *config == "" || flags.NArg() != 0 {
		return 2
	}
	switch args[0] {
	case "gitlab":
		script, err := gitlabci.Assets.ReadFile("assets/trigger.py")
		if err != nil {
			return 1
		}
		return runPythonHook(script, []string{"--config", *config}, os.Stdin, stdout, stderr)
	case "validate":
		cfg, err := hooks.Load(*config)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		return extensionJSON(stdout, stderr, cfg)
	case "emit":
		cfg, err := hooks.Load(*config)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		root, err := os.Getwd()
		if err != nil {
			return 1
		}
		event := hooks.NewEvent(*kind, *command, root, *code, 0)
		if err := hooks.Dispatch(context.Background(), cfg, event); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return extensionJSON(stdout, stderr, event)
	case "autofix":
		return runPythonHook(hooks.AutofixScript, []string{"--config", *config}, os.Stdin, stdout, stderr)
	default:
		fmt.Fprintln(stderr, "unknown hooks action")
		return 2
	}
}

func runPythonHook(scriptBytes []byte, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	python, err := exec.LookPath("python3")
	if err != nil {
		fmt.Fprintln(stderr, "hooks autofix requires Python 3.11+")
		return 2
	}
	script, err := os.CreateTemp("", "pf-autofix-*.py")
	if err != nil {
		return 1
	}
	defer os.Remove(script.Name())
	if _, err := script.Write(scriptBytes); err != nil {
		script.Close()
		return 1
	}
	if err := script.Close(); err != nil {
		return 1
	}
	cmd := exec.Command(python, append([]string{"-I", script.Name()}, args...)...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return exit.ExitCode()
		}
		fmt.Fprintln(stderr, "autofix could not start")
		return 1
	}
	return 0
}
