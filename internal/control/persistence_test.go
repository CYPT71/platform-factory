package control

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSaveLoadReclaimsAssignedLeaseAndPreservesCompleted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "control.json")
	plane := NewControlPlane(time.Minute)
	if err := plane.RegisterWorker("worker-a", "linux/amd64"); err != nil {
		t.Fatal(err)
	}
	assignedID, _ := plane.SubmitLease("retry me", "linux/amd64")
	completedID, _ := plane.SubmitLease("keep me", "")
	assigned, ok, err := plane.NextLease("worker-a")
	if err != nil || !ok || assigned.ID != assignedID {
		t.Fatalf("assigned=%+v ok=%v err=%v", assigned, ok, err)
	}
	completed, ok, err := plane.NextLease("worker-a")
	if err != nil || !ok || completed.ID != completedID {
		t.Fatalf("completed=%+v ok=%v err=%v", completed, ok, err)
	}
	if err := plane.CompleteLease("worker-a", completedID, "durable result"); err != nil {
		t.Fatal(err)
	}
	if err := plane.Save(path); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot info=%v err=%v", info, err)
	}

	restarted, err := LoadControlPlane(time.Minute, path)
	if err != nil {
		t.Fatal(err)
	}
	if workers := restarted.WorkerStatuses(); len(workers) != 0 {
		t.Fatalf("stale workers survived restart: %+v", workers)
	}
	status, _ := restarted.LeaseStatus(completedID)
	if status.State != LeaseCompleted || status.Result != "durable result" ||
		status.CompletedBy != "worker-a" || status.CompletedAt.IsZero() {
		t.Fatalf("completed state lost: %+v", status)
	}
	status, _ = restarted.LeaseStatus(assignedID)
	if status.State != LeasePending || status.Worker != "" || status.Attempt != 1 {
		t.Fatalf("assigned lease was not reclaimed: %+v", status)
	}
	if err := restarted.RegisterWorker("worker-b", "linux/amd64"); err != nil {
		t.Fatal(err)
	}
	retry, ok, err := restarted.NextLease("worker-b")
	if err != nil || !ok || retry.ID != assignedID || retry.Attempt != 2 {
		t.Fatalf("retry=%+v ok=%v err=%v", retry, ok, err)
	}
	newID, err := restarted.SubmitLease("new", "")
	if err != nil || newID != "lease-3" {
		t.Fatalf("new id=%q err=%v", newID, err)
	}
}

func TestLoadRejectsCorruptAndInconsistentSnapshots(t *testing.T) {
	for name, content := range map[string]string{
		"unknown-field": `{"version":1,"next_id":0,"leases":[],"pending":[],"extra":true}`,
		"trailing":      `{"version":1,"next_id":0,"leases":[],"pending":[]} {}`,
		"unknown-queue": `{"version":1,"next_id":1,"leases":[],"pending":["lease-1"]}`,
		"duplicate":     `{"version":1,"next_id":1,"leases":[{"id":"lease-1","payload":"x","state":"pending"}],"pending":["lease-1","lease-1"]}`,
		"bad-state":     `{"version":1,"next_id":1,"leases":[{"id":"lease-1","payload":"x","state":"mystery"}],"pending":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadControlPlane(time.Minute, path); err == nil {
				t.Fatal("invalid snapshot loaded")
			}
		})
	}
}

func TestLoadMigratesVersionOneCompletionIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	legacy := `{"version":1,"next_id":1,"leases":[{"id":"lease-1","payload":"x","state":"completed","worker":"worker-old","attempt":1,"result":"done"}],"pending":[]}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	plane, err := LoadControlPlane(time.Minute, path)
	if err != nil {
		t.Fatal(err)
	}
	lease, ok := plane.LeaseStatus("lease-1")
	if !ok || lease.CompletedBy != "worker-old" {
		t.Fatalf("legacy completion identity not migrated: %+v ok=%v", lease, ok)
	}
}

func TestSaveLoadPreservesSchedulingRequirements(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	plane := NewControlPlane(time.Minute)
	digest := "sha256:" + strings.Repeat("c", 64)
	id, err := plane.SubmitLeaseWithRequirements("work", "linux/arm64", []string{"kvm", "network"}, digest)
	if err != nil {
		t.Fatal(err)
	}
	if err := plane.Save(path); err != nil {
		t.Fatal(err)
	}
	restarted, err := LoadControlPlane(time.Minute, path)
	if err != nil {
		t.Fatal(err)
	}
	lease, ok := restarted.LeaseStatus(id)
	if !ok || lease.RequiredPlatform != "linux/arm64" || lease.PreferredContent != digest ||
		len(lease.RequiredCapabilities) != 2 || lease.RequiredCapabilities[0] != "kvm" || lease.RequiredCapabilities[1] != "network" {
		t.Fatalf("requirements not preserved: %+v ok=%v", lease, ok)
	}
}

func TestSaveLoadPreservesCanceledLeaseAsTerminal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	plane := NewControlPlane(time.Minute)
	if err := plane.RegisterWorker("worker-a", "linux/amd64"); err != nil {
		t.Fatal(err)
	}
	id, err := plane.SubmitLease("stop me", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := plane.NextLease("worker-a"); err != nil || !ok {
		t.Fatalf("assign: ok=%v err=%v", ok, err)
	}
	if changed, err := plane.CancelLease("operator-a", id); err != nil || !changed {
		t.Fatalf("cancel: changed=%v err=%v", changed, err)
	}
	if err := plane.Save(path); err != nil {
		t.Fatal(err)
	}

	restarted, err := LoadControlPlane(time.Minute, path)
	if err != nil {
		t.Fatal(err)
	}
	lease, ok := restarted.LeaseStatus(id)
	if !ok || lease.State != LeaseCanceled || lease.CanceledBy != "operator-a" || lease.CanceledAt.IsZero() {
		t.Fatalf("cancellation not preserved: %+v ok=%v", lease, ok)
	}
	if err := restarted.RegisterWorker("worker-b", "linux/amd64"); err != nil {
		t.Fatal(err)
	}
	if lease, ok, err := restarted.NextLease("worker-b"); err != nil || ok {
		t.Fatalf("canceled lease was requeued: lease=%+v ok=%v err=%v", lease, ok, err)
	}
}

func TestSaveRejectsSymlinkTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "state")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	err := NewControlPlane(time.Minute).Save(link)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("err=%v", err)
	}
	data, _ := os.ReadFile(target)
	if string(data) != "keep" {
		t.Fatalf("symlink target changed: %q", data)
	}
}
