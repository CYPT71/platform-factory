package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestRunEmbeddedVectors(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"vectors"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	if !strings.Contains(stdout.String(), `"failed": 0`) {
		t.Fatalf("stdout=%s", stdout.String())
	}
}

func TestRunEmbeddedBackendVectors(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("/bin/sh required")
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"backend"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	if !strings.Contains(stdout.String(), `"failed": 0`) {
		t.Fatalf("stdout=%s", stdout.String())
	}
}

func TestRunUsageErrors(t *testing.T) {
	var stdout, stderr bytes.Buffer
	for _, args := range [][]string{
		nil,
		{"unknown"},
		{"plugin"},
		{"vectors", "a", "b"},
		{"vectors", t.TempDir()},
		{"backend", "a", "b"},
		{"backend", t.TempDir()},
	} {
		if code := run(args, &stdout, &stderr); code != 2 {
			t.Fatalf("args=%v code=%d", args, code)
		}
	}
}

func TestRunPluginFailure(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"plugin", "/bin/true"}, &stdout, &stderr); code != 1 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}
