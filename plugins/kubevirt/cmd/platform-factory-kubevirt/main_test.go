package main

import (
	"bytes"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestRunLifecycleDrivesVirtctlAndKubectl(t *testing.T) {
	var calls [][]string
	execute := func(name string, args []string, _ io.Reader, _, _ io.Writer) error {
		calls = append(calls, append([]string{name}, args...))
		return nil
	}
	for _, action := range []string{"start", "stop", "restart", "status", "logs", "delete"} {
		var stdout, stderr bytes.Buffer
		code := run([]string{
			action, "--name=demo", "--namespace=production",
		}, &stdout, &stderr, execute)
		if code != 0 {
			t.Fatalf("%s code=%d stderr=%s", action, code, stderr.String())
		}
	}
	if len(calls) != 6 || !reflect.DeepEqual(calls[0], []string{"virtctl", "start", "--namespace", "production", "demo"}) {
		t.Fatalf("calls=%v", calls)
	}
	if !reflect.DeepEqual(calls[3], []string{"kubectl", "get", "virtualmachine", "--namespace", "production", "demo", "-o", "json"}) {
		t.Fatalf("status call=%v", calls[3])
	}
}

func TestRunCreateIsDryRunByDefault(t *testing.T) {
	execute := func(string, []string, io.Reader, io.Writer, io.Writer) error {
		t.Fatal("executor called without --apply")
		return nil
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"create", "--name=demo",
		"--image=registry.example/boot@sha256:" + strings.Repeat("b", 64),
	}, &stdout, &stderr, execute)
	if code != 0 || !strings.Contains(stdout.String(), `"kind": "VirtualMachine"`) {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

func TestRunCreateAppliesThroughKubectl(t *testing.T) {
	var command string
	var args []string
	var piped []byte
	execute := func(name string, commandArgs []string, stdin io.Reader, _, _ io.Writer) error {
		command, args = name, commandArgs
		piped, _ = io.ReadAll(stdin)
		return nil
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"create", "--name=demo", "--apply",
		"--image=registry.example/boot@sha256:" + strings.Repeat("b", 64),
	}, &stdout, &stderr, execute)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if command != "kubectl" || !reflect.DeepEqual(args, []string{"apply", "-f", "-"}) {
		t.Fatalf("command=%s args=%v", command, args)
	}
	if !strings.Contains(string(piped), `"kind": "VirtualMachine"`) {
		t.Fatalf("piped manifest=%s", piped)
	}
}

func TestRunRejectsInvalidConfigurationWithoutExecuting(t *testing.T) {
	execute := func(string, []string, io.Reader, io.Writer, io.Writer) error {
		t.Fatal("executor called for invalid configuration")
		return nil
	}
	for _, args := range [][]string{
		nil,
		{"create", "--image=latest"},
		{"create", "--name=BAD", "--image=registry.example/boot@sha256:" + strings.Repeat("b", 64)},
		{"start", "--name=BAD"},
		{"wat", "--name=demo"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr, execute); code == 0 {
			t.Fatalf("args=%v unexpectedly succeeded", args)
		}
	}
}

func TestRunPropagatesExecutorExitCode(t *testing.T) {
	execute := func(string, []string, io.Reader, io.Writer, io.Writer) error {
		return exitError{code: 7}
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"start", "--name=demo"}, &stdout, &stderr, execute)
	if code != 7 {
		t.Fatalf("code=%d", code)
	}
}

type exitError struct{ code int }

func (e exitError) Error() string { return "exit error" }
func (e exitError) ExitCode() int { return e.code }
