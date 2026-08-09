//go:build linux

package main

import (
	"bytes"
	"strings"
	"syscall"
	"testing"
)

func TestReapExitedChildrenDrainsEveryExitedChild(t *testing.T) {
	results := []struct {
		pid    int
		status syscall.WaitStatus
		err    error
	}{
		{pid: 41, status: 0},
		{pid: 42, status: 7 << 8},
		{pid: 0},
	}
	index := 0
	wait := func() (int, syscall.WaitStatus, error) {
		result := results[index]
		index++
		return result.pid, result.status, result.err
	}
	var stderr bytes.Buffer
	if got := reapExitedChildrenWith(wait, &stderr); got != 2 {
		t.Fatalf("reaped = %d, want 2", got)
	}
	logs := stderr.String()
	for _, field := range []string{"phase=reap pid=41 status=0", "phase=reap pid=42 status=1792"} {
		if !strings.Contains(logs, field) {
			t.Fatalf("logs = %q, missing %q", logs, field)
		}
	}
}
