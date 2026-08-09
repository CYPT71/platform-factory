//go:build linux

package executor

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

func TestWrapWithRlimitHelperRewritesCommand(t *testing.T) {
	cmd := exec.Command("sh", "-c", "true")
	originalPath := cmd.Path
	cmd.Env = []string{"FOO=bar"}

	if err := wrapWithRlimitHelper(cmd, 256<<20); err != nil {
		t.Fatalf("wrap: %v", err)
	}
	if cmd.Path == originalPath {
		t.Fatalf("expected cmd.Path to be rewritten to the current executable, got %q", cmd.Path)
	}
	if len(cmd.Args) != 1 || cmd.Args[0] != cmd.Path {
		t.Fatalf("args=%v", cmd.Args)
	}

	var payload string
	for _, kv := range cmd.Env {
		if rest, ok := strings.CutPrefix(kv, rlimitHelperEnv+"="); ok {
			payload = rest
		}
	}
	if payload == "" {
		t.Fatal("expected the helper env var to be set")
	}
	var decoded rlimitHelperPayload
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if decoded.MemoryBytes != 256<<20 || decoded.Executable != originalPath || len(decoded.Args) != 2 {
		t.Fatalf("payload=%+v", decoded)
	}
}
