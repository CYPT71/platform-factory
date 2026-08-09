package control

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAuditJournalAppendsVerifiableHashChainAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit", "events.jsonl")
	journal, err := OpenAuditJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	journal.now = func() time.Time { return time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC) }
	first, err := journal.Append("worker-a", "lease.assigned", "lease-1")
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := OpenAuditJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := restarted.Append("worker-a", "lease.completed", "lease-1")
	if err != nil {
		t.Fatal(err)
	}
	if first.Sequence != 1 || second.Sequence != 2 || second.PreviousHash != first.Hash {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	events, err := VerifyAuditJournal(path)
	if err != nil || len(events) != 2 {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("info=%v err=%v", info, err)
	}
}

func TestAuditJournalDetectsTamperingAndTruncation(t *testing.T) {
	for name, mutate := range map[string]func([]byte) []byte{
		"tamper": func(data []byte) []byte {
			return []byte(strings.Replace(string(data), "lease.assigned", "lease.deleted", 1))
		},
		"truncate": func(data []byte) []byte { return data[:len(data)-1] },
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "audit.jsonl")
			journal, _ := OpenAuditJournal(path)
			if _, err := journal.Append("worker-a", "lease.assigned", "lease-1"); err != nil {
				t.Fatal(err)
			}
			data, _ := os.ReadFile(path)
			if err := os.WriteFile(path, mutate(data), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := OpenAuditJournal(path); err == nil {
				t.Fatal("corrupt journal accepted")
			}
		})
	}
}

func TestAuditJournalRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "audit")
	if err := os.Symlink(target, link); err != nil {
		t.Skip(err)
	}
	if _, err := OpenAuditJournal(link); err == nil {
		t.Fatal("symlink journal accepted")
	}
}

func TestAuditJournalRejectsInvalidInputsAndUnsafeDestinations(t *testing.T) {
	if _, err := OpenAuditJournal(""); err == nil {
		t.Fatal("empty path accepted")
	}
	journal, err := OpenAuditJournal(filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, actor, action, subject string }{
		{"actor", "bad\nactor", "lease.assigned", "lease-1"},
		{"action", "worker-a", "", "lease-1"},
		{"subject", "worker-a", "lease.assigned", strings.Repeat("x", 257)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := journal.Append(test.actor, test.action, test.subject); err == nil {
				t.Fatal("invalid audit text accepted")
			}
		})
	}

	parentFile := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parentFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	blocked, err := OpenAuditJournal(filepath.Join(parentFile, "audit.jsonl"))
	if err != nil && !os.IsNotExist(err) {
		// An OS may reject the path during Lstat rather than during Append.
		return
	}
	if err == nil {
		if _, err := blocked.Append("worker-a", "lease.assigned", "lease-1"); err == nil {
			t.Fatal("journal below a regular file accepted")
		}
	}
}

func TestAuditJournalRejectsMalformedChains(t *testing.T) {
	validTime := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	valid := AuditEvent{Version: auditVersion, Sequence: 1, Time: validTime,
		Actor: "worker-a", Action: "lease.assigned", Subject: "lease-1"}
	valid.Hash = auditEventHash(valid)
	encoded, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string][]byte{
		"unknown-field": append(append([]byte{}, encoded[:len(encoded)-1]...), []byte(",\"extra\":true}\n")...),
		"two-events":    append(append(append([]byte{}, encoded...), encoded...), '\n'),
		"bad-hash":      []byte(strings.Replace(string(encoded), valid.Hash, "sha256:"+strings.Repeat("0", 64), 1) + "\n"),
		"long-line":     []byte(strings.Repeat("x", maxAuditLineBytes) + "\n"),
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "audit.jsonl")
			if err := os.WriteFile(path, content, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := VerifyAuditJournal(path); err == nil {
				t.Fatalf("malformed journal accepted: %s", fmt.Sprintf("%.80s", content))
			}
		})
	}
}

func TestAuditJournalRejectsOversizedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxAuditFileBytes + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyAuditJournal(path); err == nil {
		t.Fatal("oversized journal accepted")
	}
}
