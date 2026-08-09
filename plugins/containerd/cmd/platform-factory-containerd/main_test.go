package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunConfigAndRuntimeClass(t *testing.T) {
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"config"}, `runtime_type = "io.containerd.platform-factory.v1"`},
		{[]string{"runtimeclass", "--handler", "microvm"}, "handler: microvm"},
		{[]string{"version"}, "platform-factory-containerd"},
	} {
		var stdout, stderr bytes.Buffer
		if err := run(test.args, &stdout, &stderr); err != nil {
			t.Fatalf("run(%q): %v (%s)", test.args, err, stderr.String())
		}
		if !strings.Contains(stdout.String(), test.want) {
			t.Fatalf("run(%q) = %q; want %q", test.args, stdout.String(), test.want)
		}
	}
}

func TestRunRejectsInvalidInvocation(t *testing.T) {
	for _, args := range [][]string{{}, {"wat"}, {"config", "extra"}, {"config", "--handler", "BAD"}} {
		if err := run(args, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
			t.Fatalf("run(%q) succeeded", args)
		}
	}
}
