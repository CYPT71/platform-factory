package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunDispatchHelpVersionAndAliases(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"help"},
		{"-h"},
		{"--help"},
		{"version"},
		{"--version"},
		{"image"},
		{"container"},
		{"inspect", "-h"},
		{"project", "-h"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 0 {
			t.Fatalf("args=%v code=%d stderr=%s", args, code, stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"version"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "platform-factory") {
		t.Fatalf("version stdout=%s", stdout.String())
	}
	if code := run([]string{"nonexistent-command"}, &stdout, &stderr); code != 2 {
		t.Fatalf("unknown command code=%d", code)
	}
	if code := run([]string{"vm"}, &stdout, &stderr); code == 0 {
		t.Fatal("bare vm alias should require a subcommand")
	}
	if !strings.Contains(stderr.String(), `"platform-factory vm" is deprecated`) {
		t.Fatalf("expected a deprecation warning for the vm alias, stderr=%s", stderr.String())
	}
}

func TestDeprecatedAliasesWarnOnStderr(t *testing.T) {
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"vm", "status"}, `"platform-factory vm" is deprecated, use "platform-factory microvm" instead`},
		{[]string{"container", "run", "--help"}, `"platform-factory container run" is deprecated, use "platform-factory run" instead`},
		{[]string{"image", "verify", "--help"}, `"platform-factory image verify" is deprecated, use "platform-factory verify" instead`},
		{[]string{"plan", "nonexistent-directory-xyz"}, `"platform-factory plan" is deprecated, use "platform-factory project plan" instead`},
		{[]string{"freeze", "nonexistent-directory-xyz"}, `"platform-factory freeze" is deprecated, use "platform-factory project freeze" instead`},
	} {
		var stdout, stderr bytes.Buffer
		run(test.args, &stdout, &stderr)
		if !strings.Contains(stderr.String(), test.want) {
			t.Fatalf("args=%v: stderr=%q does not contain %q", test.args, stderr.String(), test.want)
		}
	}

	// "plan --help" short-circuits straight to printUsage before the
	// deprecation notice is printed - documented so a future refactor
	// that changes this ordering has to do so deliberately.
	var stdout, stderr bytes.Buffer
	run([]string{"plan", "--help"}, &stdout, &stderr)
	if strings.Contains(stderr.String(), "is deprecated") {
		t.Fatalf(`"plan --help" unexpectedly printed a deprecation warning: stderr=%s`, stderr.String())
	}
}

func TestRunPipelineTextRunAndMissingArgs(t *testing.T) {
	work := t.TempDir()
	name := writePipelineFile(t, t.TempDir())
	var stdout, stderr bytes.Buffer
	if code := run([]string{"pipeline", "run", "--sandbox", "off", "--format", "text", "--workdir", work, name}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "resolve: succeeded") {
		t.Fatalf("stdout=%s", stdout.String())
	}
}
