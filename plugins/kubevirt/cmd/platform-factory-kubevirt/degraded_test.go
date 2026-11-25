package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestDegradedConditionsClassifyRBACAPIOutageAndInterruption(t *testing.T) {
	if err := classifyClusterError("kubectl", errors.New("exit status 1"), "Error from server (Forbidden): virtualmachines.kubevirt.io is forbidden"); !strings.Contains(err.Error(), "rbac_forbidden") {
		t.Fatalf("expected rbac_forbidden, got %v", err)
	}
	if err := classifyClusterError("kubectl", errors.New("exit status 1"), "The connection to the server 127.0.0.1:6443 was refused - did you specify the right host or port?"); !strings.Contains(err.Error(), "api_unavailable") {
		t.Fatalf("expected api_unavailable, got %v", err)
	}
	if err := classifyClusterError("kubectl", context.Canceled, ""); !strings.Contains(err.Error(), "interrupted") {
		t.Fatalf("expected interrupted on context.Canceled, got %v", err)
	}

	orig := execCommand
	defer func() { execCommand = orig }()

	// Simulate insufficient RBAC during apply -> surfaces rbac_forbidden cleanly.
	execCommand = func(ctx context.Context, name string, args []string, stdin []byte) ([]byte, error) {
		return nil, classifyClusterError(name, errors.New("exit status 1"), "Error from server (Forbidden): cannot create virtualmachines")
	}
	raw, _ := json.Marshal(specParams{
		Name:      "vm-rbac",
		Namespace: "default",
		Image:     "registry.example/vm/boot@sha256:" + strings.Repeat("a", 64),
		Arch:      "amd64",
		Apply:     true,
	})
	if _, err := handleCreate(context.Background(), raw); err == nil || !strings.Contains(err.Error(), "rbac_forbidden") {
		t.Fatalf("expected rbac_forbidden from handleCreate, got %v", err)
	}

	// Simulate API disconnect during delete -> surfaces api_unavailable cleanly.
	execCommand = func(ctx context.Context, name string, args []string, stdin []byte) ([]byte, error) {
		return nil, classifyClusterError(name, errors.New("exit status 1"), "Unable to connect to the server: EOF")
	}
	if _, err := handleDelete(context.Background(), raw); err == nil || !strings.Contains(err.Error(), "api_unavailable") {
		t.Fatalf("expected api_unavailable from handleDelete, got %v", err)
	}
}
