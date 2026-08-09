package executor

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRedactSecrets(t *testing.T) {
	secrets := [][]byte{[]byte("swordfish"), []byte("hunter2")}
	cases := map[string]struct{ in, want string }{
		"exact":      {"token=swordfish done", "token=[secret] done"},
		"multiple":   {"a swordfish b hunter2", "a [secret] b [secret]"},
		"none":       {"nothing secret here", "nothing secret here"},
		"prefix-cut": {"leading swordfi", "leading [secret]"},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			got := redactSecrets([]byte(test.in), secrets)
			if string(got) != test.want {
				t.Fatalf("redact(%q)=%q want %q", test.in, got, test.want)
			}
		})
	}
	if got := redactSecrets([]byte("unchanged"), nil); string(got) != "unchanged" {
		t.Fatalf("no-secret redaction changed data: %q", got)
	}
}

func TestEnvResolver(t *testing.T) {
	t.Setenv("PLATFORM_FACTORY_SECRET_REGISTRY_TOKEN", "abc123")
	value, err := (EnvResolver{}).Resolve(context.Background(), "registry-token")
	if err != nil || string(value) != "abc123" {
		t.Fatalf("value=%q err=%v", value, err)
	}
	if _, err := (EnvResolver{}).Resolve(context.Background(), "missing-secret"); err == nil {
		t.Fatal("missing secret accepted")
	}
	if _, err := (EnvResolver{}).Resolve(context.Background(), "Bad Id"); err == nil {
		t.Fatal("invalid id accepted")
	}
}

func TestDirResolver(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "token"), []byte("filesecret"), 0o400); err != nil {
		t.Fatal(err)
	}
	value, err := (DirResolver{Dir: dir}).Resolve(context.Background(), "token")
	if err != nil || !bytes.Equal(value, []byte("filesecret")) {
		t.Fatalf("value=%q err=%v", value, err)
	}
	if _, err := (DirResolver{Dir: dir}).Resolve(context.Background(), "absent"); err == nil {
		t.Fatal("absent secret accepted")
	}
	if err := os.Mkdir(filepath.Join(dir, "adir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := (DirResolver{Dir: dir}).Resolve(context.Background(), "adir"); err == nil {
		t.Fatal("directory accepted as secret")
	}
	if _, err := (DirResolver{Dir: dir}).Resolve(context.Background(), "bad id"); err == nil {
		t.Fatal("invalid id accepted")
	}
}

func TestNewSandboxedRejectsUnsupportedHost(t *testing.T) {
	_, err := NewSandboxed(t.TempDir(), nil, SandboxSupport{
		Details: map[string]string{"user-namespaces": "unavailable in this test"},
	}, nil)
	if err == nil {
		t.Fatal("sandbox construction accepted without user namespace support")
	}
}
