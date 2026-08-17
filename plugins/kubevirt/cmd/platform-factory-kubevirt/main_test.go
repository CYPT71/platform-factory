package main

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func withStubExecutor(t *testing.T, stub executor) {
	t.Helper()
	previous := execCommand
	execCommand = stub
	t.Cleanup(func() { execCommand = previous })
}

func TestHandleLifecycleDrivesVirtctl(t *testing.T) {
	var calls [][]string
	withStubExecutor(t, func(name string, args []string, stdin []byte) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		return []byte("ok"), nil
	})
	params, _ := json.Marshal(specParams{Name: "demo", Namespace: "production"})
	for _, action := range []string{"start", "stop", "restart"} {
		value, err := handleLifecycle(action)(context.Background(), params)
		if err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		if value.(commandResult).Output != "ok" {
			t.Fatalf("%s: unexpected result %+v", action, value)
		}
	}
	if len(calls) != 3 || !reflect.DeepEqual(calls[0], []string{"virtctl", "start", "--namespace", "production", "demo"}) {
		t.Fatalf("calls=%v", calls)
	}
}

func TestHandleStatusAndLogs(t *testing.T) {
	var calls [][]string
	withStubExecutor(t, func(name string, args []string, stdin []byte) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		return []byte(`{"kind":"VirtualMachine"}`), nil
	})
	params, _ := json.Marshal(specParams{Name: "demo", Namespace: "production"})

	statusValue, err := handleStatus(context.Background(), params)
	if err != nil || statusValue.(commandResult).Output != `{"kind":"VirtualMachine"}` {
		t.Fatalf("status value=%+v err=%v", statusValue, err)
	}
	if !reflect.DeepEqual(calls[0], []string{"kubectl", "get", "virtualmachine", "--namespace", "production", "demo", "-o", "json"}) {
		t.Fatalf("status call=%v", calls[0])
	}

	logsValue, err := handleLogs(context.Background(), params)
	if err != nil {
		t.Fatalf("logs: %v", err)
	}
	if !reflect.DeepEqual(calls[1], []string{"virtctl", "console", "--namespace", "production", "demo"}) {
		t.Fatalf("logs call=%v", calls[1])
	}
	_ = logsValue
}

func TestHandleDelete(t *testing.T) {
	var calls [][]string
	withStubExecutor(t, func(name string, args []string, stdin []byte) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		return nil, nil
	})
	params, _ := json.Marshal(specParams{Name: "demo", Namespace: "production"})
	if _, err := handleDelete(context.Background(), params); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls[0], []string{"kubectl", "delete", "virtualmachine", "--namespace", "production", "demo"}) {
		t.Fatalf("call=%v", calls[0])
	}
}

func TestHandleCreateIsDryRunByDefault(t *testing.T) {
	withStubExecutor(t, func(string, []string, []byte) ([]byte, error) {
		t.Fatal("executor called without apply")
		return nil, nil
	})
	params, _ := json.Marshal(specParams{
		Name: "demo", Namespace: "default",
		Image: "registry.example/boot@sha256:" + strings.Repeat("b", 64),
	})
	value, err := handleCreate(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	result := value.(manifestResult)
	if result.Applied || !strings.Contains(result.Manifest, `"kind": "VirtualMachine"`) {
		t.Fatalf("result=%+v", result)
	}
}

func TestHandleCreateAppliesThroughKubectl(t *testing.T) {
	var command string
	var args []string
	var piped []byte
	withStubExecutor(t, func(name string, commandArgs []string, stdin []byte) ([]byte, error) {
		command, args, piped = name, commandArgs, stdin
		return []byte("applied"), nil
	})
	params, _ := json.Marshal(specParams{
		Name: "demo", Namespace: "default", Apply: true,
		Image: "registry.example/boot@sha256:" + strings.Repeat("b", 64),
	})
	value, err := handleCreate(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	if command != "kubectl" || !reflect.DeepEqual(args, []string{"apply", "-f", "-"}) {
		t.Fatalf("command=%s args=%v", command, args)
	}
	if !strings.Contains(string(piped), `"kind": "VirtualMachine"`) {
		t.Fatalf("piped manifest=%s", piped)
	}
	if !value.(manifestResult).Applied {
		t.Fatalf("result=%+v", value)
	}
}

func TestHandleCreatePropagatesApplyFailure(t *testing.T) {
	withStubExecutor(t, func(string, []string, []byte) ([]byte, error) {
		return nil, errors.New("apply failed")
	})
	params, _ := json.Marshal(specParams{
		Name: "demo", Namespace: "default", Apply: true,
		Image: "registry.example/boot@sha256:" + strings.Repeat("b", 64),
	})
	if _, err := handleCreate(context.Background(), params); err == nil {
		t.Fatal("expected error to propagate")
	}
}

func TestHandleRBACIsDryRunByDefaultAndApplies(t *testing.T) {
	var applyCalled bool
	withStubExecutor(t, func(name string, args []string, stdin []byte) ([]byte, error) {
		applyCalled = true
		if name != "kubectl" || !reflect.DeepEqual(args, []string{"apply", "-f", "-"}) {
			t.Fatalf("command=%s args=%v", name, args)
		}
		if !strings.Contains(string(stdin), `"kind": "Role"`) {
			t.Fatalf("piped manifest missing Role: %s", stdin)
		}
		return []byte("applied"), nil
	})
	dryRunParams, _ := json.Marshal(specParams{Name: "demo", Namespace: "production"})
	value, err := handleRBAC(context.Background(), dryRunParams)
	if err != nil {
		t.Fatal(err)
	}
	if applyCalled || value.(manifestResult).Applied {
		t.Fatalf("rbac applied without --apply: %+v", value)
	}
	if !strings.Contains(value.(manifestResult).Manifest, `"kind": "Role"`) {
		t.Fatalf("manifest missing Role: %+v", value)
	}

	applyParams, _ := json.Marshal(specParams{Name: "demo", Namespace: "production", Apply: true})
	value, err = handleRBAC(context.Background(), applyParams)
	if err != nil {
		t.Fatal(err)
	}
	if !applyCalled || !value.(manifestResult).Applied {
		t.Fatalf("rbac did not apply: %+v", value)
	}
}

func TestHandlersRejectInvalidConfigurationWithoutExecuting(t *testing.T) {
	withStubExecutor(t, func(string, []string, []byte) ([]byte, error) {
		t.Fatal("executor called for invalid configuration")
		return nil, nil
	})
	invalid, _ := json.Marshal(specParams{Name: "demo", Namespace: "Bad Namespace"})
	missingImage, _ := json.Marshal(specParams{Name: "demo", Namespace: "default"})
	missingIdentity, _ := json.Marshal(specParams{})

	for _, handler := range []func(context.Context, json.RawMessage) (any, error){
		handleCreate, handleStatus, handleLogs, handleDelete, handleRBAC,
		handleLifecycle("start"), handleLifecycle("stop"), handleLifecycle("restart"),
	} {
		if _, err := handler(context.Background(), missingIdentity); err == nil {
			t.Fatal("accepted params with no name/namespace")
		}
		if _, err := handler(context.Background(), invalid); err == nil {
			t.Fatal("accepted an invalid namespace")
		}
	}
	if _, err := handleCreate(context.Background(), missingImage); err == nil {
		t.Fatal("accepted create with no image")
	}
}
