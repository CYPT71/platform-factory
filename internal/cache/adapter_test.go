package cache

import (
	"strings"
	"testing"
)

func TestOpenAdapterOpensAStoreAndReturnsAnAdapter(t *testing.T) {
	adapter, err := OpenAdapter(t.TempDir())
	if err != nil {
		t.Fatalf("OpenAdapter: %v", err)
	}
	key := "sha256:" + strings.Repeat("a", 64)
	if err := adapter.PutRecord(key, map[string]string{"a": "b"}); err != nil {
		t.Fatalf("PutRecord: %v", err)
	}
	var out map[string]string
	found, err := adapter.GetRecord(key, &out)
	if err != nil || !found || out["a"] != "b" {
		t.Fatalf("GetRecord: found=%v err=%v out=%v", found, err, out)
	}
}

func TestOpenAdapterRejectsAnUnusableRoot(t *testing.T) {
	if _, err := OpenAdapter(""); err == nil {
		t.Fatal("expected an error for an empty root")
	}
}
