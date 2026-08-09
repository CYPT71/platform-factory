package provenance

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestGenerateWorkloadIdentity(t *testing.T) {
	id := "worker-1"
	identity, privateKey, err := GenerateWorkloadIdentity(id)
	if err != nil {
		t.Fatalf("failed to generate workload identity: %v", err)
	}

	if identity.ID != id {
		t.Errorf("expected ID %s, got %s", id, identity.ID)
	}

	if identity.PublicKey == "" {
		t.Error("expected non-empty public key")
	}

	if identity.IssuedAt.IsZero() {
		t.Error("expected issued at to be set")
	}

	if identity.ExpiresAt.Before(identity.IssuedAt) {
		t.Error("expected expires at to be after issued at")
	}

	if len(privateKey) != ed25519.PrivateKeySize {
		t.Errorf("expected private key size %d, got %d", ed25519.PrivateKeySize, len(privateKey))
	}
}

func TestNewWorkloadSigner(t *testing.T) {
	identity, privateKey, err := GenerateWorkloadIdentity("worker-1")
	if err != nil {
		t.Fatalf("failed to generate identity: %v", err)
	}

	signer, err := NewWorkloadSigner(identity, privateKey)
	if err != nil {
		t.Fatalf("failed to create workload signer: %v", err)
	}

	if signer == nil {
		t.Fatal("expected non-nil signer")
	}
}

func TestNewWorkloadSignerInvalid(t *testing.T) {
	tests := []struct {
		name     string
		identity WorkloadIdentity
		key      ed25519.PrivateKey
	}{
		{
			name:     "empty ID",
			identity: WorkloadIdentity{ID: "", PublicKey: "abc"},
			key:      make(ed25519.PrivateKey, ed25519.PrivateKeySize),
		},
		{
			name:     "empty public key",
			identity: WorkloadIdentity{ID: "worker-1", PublicKey: ""},
			key:      make(ed25519.PrivateKey, ed25519.PrivateKeySize),
		},
		{
			name:     "invalid private key size",
			identity: WorkloadIdentity{ID: "worker-1", PublicKey: base64.StdEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize))},
			key:      make(ed25519.PrivateKey, 10),
		},
		{
			name:     "invalid private key size",
			identity: WorkloadIdentity{ID: "worker-1", PublicKey: base64.StdEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize))},
			key:      make(ed25519.PrivateKey, 10),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewWorkloadSigner(tt.identity, tt.key)
			if err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestWorkloadSignerSign(t *testing.T) {
	identity, privateKey, err := GenerateWorkloadIdentity("worker-1")
	if err != nil {
		t.Fatalf("failed to generate identity: %v", err)
	}

	signer, err := NewWorkloadSigner(identity, privateKey)
	if err != nil {
		t.Fatalf("failed to create signer: %v", err)
	}

	record := &ProvenanceRecord{
		BuildID:    "build-123",
		ArtifactID: "sha256:abc123",
		WorkerID:   "worker-1",
		TenantID:   "tenant-1",
		Materials: []Material{
			{URI: "https://example.com/source", Digest: "sha256:def456"},
		},
		Invocation: Invocation{
			ConfigSource: ConfigSource{
				URI:    "https://example.com/config",
				Digest: "sha256:ghi789",
			},
		},
	}

	err = signer.Sign(record)
	if err != nil {
		t.Fatalf("failed to sign record: %v", err)
	}

	if record.Signature == "" {
		t.Error("expected non-empty signature")
	}

	if record.SignedBy != "worker-1" {
		t.Errorf("expected signed by worker-1, got %s", record.SignedBy)
	}

	if !record.Timestamp.Before(time.Now().Add(time.Minute)) {
		t.Error("expected timestamp to be recent")
	}
}

func TestVerify(t *testing.T) {
	identity, privateKey, err := GenerateWorkloadIdentity("worker-1")
	if err != nil {
		t.Fatalf("failed to generate identity: %v", err)
	}

	signer, err := NewWorkloadSigner(identity, privateKey)
	if err != nil {
		t.Fatalf("failed to create signer: %v", err)
	}

	record := &ProvenanceRecord{
		BuildID:    "build-123",
		ArtifactID: "sha256:abc123",
		WorkerID:   "worker-1",
		TenantID:   "tenant-1",
		Materials: []Material{
			{URI: "https://example.com/source", Digest: "sha256:def456"},
		},
		Invocation: Invocation{
			ConfigSource: ConfigSource{
				URI:    "https://example.com/config",
				Digest: "sha256:ghi789",
			},
		},
	}

	err = signer.Sign(record)
	if err != nil {
		t.Fatalf("failed to sign record: %v", err)
	}

	// Verify with the correct public key
	err = Verify(record, identity.PublicKey)
	if err != nil {
		t.Errorf("verification failed: %v", err)
	}
}

func TestVerifyInvalidSignature(t *testing.T) {
	record := &ProvenanceRecord{
		BuildID:    "build-123",
		ArtifactID: "sha256:abc123",
		WorkerID:   "worker-1",
		Signature:  "invalid-signature",
		SignedBy:   "worker-1",
	}

	err := Verify(record, "invalid-public-key")
	if err == nil {
		t.Error("expected verification to fail with invalid signature")
	}
}

func TestVerifyNoSignature(t *testing.T) {
	record := &ProvenanceRecord{
		BuildID: "build-123",
	}

	err := Verify(record, "some-key")
	if err == nil {
		t.Error("expected verification to fail with no signature")
	}
}

func TestVerifyNoSigner(t *testing.T) {
	record := &ProvenanceRecord{
		BuildID:   "build-123",
		Signature: "some-signature",
	}

	err := Verify(record, "some-key")
	if err == nil {
		t.Error("expected verification to fail with no signer")
	}
}

func TestVerifyWrongKey(t *testing.T) {
	identity1, privateKey1, _ := GenerateWorkloadIdentity("worker-1")
	identity2, _, _ := GenerateWorkloadIdentity("worker-2")

	signer, _ := NewWorkloadSigner(identity1, privateKey1)

	record := &ProvenanceRecord{
		BuildID: "build-123",
	}

	signer.Sign(record)

	// Try to verify with wrong key
	err := Verify(record, identity2.PublicKey)
	if err == nil {
		t.Error("expected verification to fail with wrong key")
	}
}

func TestSignWithWorkloadIdentity(t *testing.T) {
	record := &ProvenanceRecord{
		BuildID: "build-123",
	}

	err := SignWithWorkloadIdentity(record, "worker-1")
	if err != nil {
		t.Fatalf("failed to sign: %v", err)
	}

	if record.Signature == "" {
		t.Error("expected non-empty signature")
	}

	if record.SignedBy != "worker-1" {
		t.Errorf("expected signed by worker-1, got %s", record.SignedBy)
	}

	if record.WorkerID != "worker-1" {
		t.Errorf("expected worker ID worker-1, got %s", record.WorkerID)
	}
}

func TestNewProvenanceStore(t *testing.T) {
	store := NewProvenanceStore()

	if store == nil {
		t.Fatal("expected non-nil store")
	}

	if len(store.records) != 0 {
		t.Errorf("expected empty records, got %d", len(store.records))
	}

	if len(store.workloads) != 0 {
		t.Errorf("expected empty workloads, got %d", len(store.workloads))
	}
}

func TestProvenanceStoreStoreAndGet(t *testing.T) {
	store := NewProvenanceStore()

	record := &ProvenanceRecord{
		BuildID: "build-123",
	}

	err := store.Store(record)
	if err != nil {
		t.Fatalf("failed to store record: %v", err)
	}

	got, ok := store.Get("build-123")
	if !ok {
		t.Fatal("expected to find record")
	}

	if got != record {
		t.Error("expected same record")
	}
}

func TestProvenanceStoreGetNotFound(t *testing.T) {
	store := NewProvenanceStore()

	_, ok := store.Get("build-123")
	if ok {
		t.Error("expected not to find record")
	}
}

func TestProvenanceStoreStoreNil(t *testing.T) {
	store := NewProvenanceStore()

	err := store.Store(nil)
	if err == nil {
		t.Error("expected error when storing nil record")
	}
}

func TestProvenanceStoreStoreEmptyID(t *testing.T) {
	store := NewProvenanceStore()

	record := &ProvenanceRecord{
		BuildID: "",
	}

	err := store.Store(record)
	if err == nil {
		t.Error("expected error when storing record with empty ID")
	}
}

func TestProvenanceStoreRegisterAndGetWorkload(t *testing.T) {
	store := NewProvenanceStore()

	identity := WorkloadIdentity{
		ID:        "worker-1",
		PublicKey: "abc123",
	}

	store.RegisterWorkload(identity)

	got, ok := store.GetWorkload("worker-1")
	if !ok {
		t.Fatal("expected to find workload")
	}

	if got.ID != "worker-1" {
		t.Errorf("expected ID worker-1, got %s", got.ID)
	}
}

func TestProvenanceStoreGetWorkloadNotFound(t *testing.T) {
	store := NewProvenanceStore()

	_, ok := store.GetWorkload("worker-1")
	if ok {
		t.Error("expected not to find workload")
	}
}

func TestProvenanceRecordSerialization(t *testing.T) {
	record := &ProvenanceRecord{
		BuildID:    "build-123",
		ArtifactID: "sha256:abc123",
		WorkerID:   "worker-1",
		TenantID:   "tenant-1",
		Materials: []Material{
			{URI: "https://example.com/source", Digest: "sha256:def456", MIMEType: "application/tar+gzip"},
		},
		Invocation: Invocation{
			ConfigSource: ConfigSource{
				URI:        "https://example.com/config",
				Digest:     "sha256:ghi789",
				EntryPoint: "Dockerfile",
			},
			Environment: map[string]string{
				"GO_VERSION": "1.21",
			},
			Parameters: map[string]string{
				"target": "linux/amd64",
			},
		},
		Timestamp: time.Now(),
	}

	data, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var unmarshaled ProvenanceRecord
	err = json.Unmarshal(data, &unmarshaled)
	if err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if unmarshaled.BuildID != record.BuildID {
		t.Errorf("expected BuildID %s, got %s", record.BuildID, unmarshaled.BuildID)
	}

	if unmarshaled.ArtifactID != record.ArtifactID {
		t.Errorf("expected ArtifactID %s, got %s", record.ArtifactID, unmarshaled.ArtifactID)
	}

	if len(unmarshaled.Materials) != len(record.Materials) {
		t.Errorf("expected %d materials, got %d", len(record.Materials), len(unmarshaled.Materials))
	}

	if unmarshaled.Invocation.ConfigSource.URI != record.Invocation.ConfigSource.URI {
		t.Errorf("expected config URI %s, got %s", record.Invocation.ConfigSource.URI, unmarshaled.Invocation.ConfigSource.URI)
	}
}

func TestWorkloadIdentitySerialization(t *testing.T) {
	identity := WorkloadIdentity{
		ID:               "worker-1",
		PublicKey:        base64.StdEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize)),
		CertificateChain: "cert-chain",
		IssuedAt:         time.Now(),
		ExpiresAt:        time.Now().Add(24 * time.Hour),
	}

	data, err := json.Marshal(identity)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var unmarshaled WorkloadIdentity
	err = json.Unmarshal(data, &unmarshaled)
	if err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if unmarshaled.ID != identity.ID {
		t.Errorf("expected ID %s, got %s", identity.ID, unmarshaled.ID)
	}

	if unmarshaled.PublicKey != identity.PublicKey {
		t.Errorf("expected PublicKey %s, got %s", identity.PublicKey, unmarshaled.PublicKey)
	}
}

func TestSignAndVerifyRoundTrip(t *testing.T) {
	// Generate workload identity
	identity, privateKey, err := GenerateWorkloadIdentity("worker-1")
	if err != nil {
		t.Fatalf("failed to generate identity: %v", err)
	}

	// Create signer
	signer, err := NewWorkloadSigner(identity, privateKey)
	if err != nil {
		t.Fatalf("failed to create signer: %v", err)
	}

	// Create and sign provenance record
	record := &ProvenanceRecord{
		BuildID:    "build-456",
		ArtifactID: "sha256:def456",
		WorkerID:   "worker-1",
		TenantID:   "tenant-1",
		Materials: []Material{
			{URI: "git+https://github.com/example/repo", Digest: "sha256:abc123"},
		},
		Invocation: Invocation{
			ConfigSource: ConfigSource{
				URI:    "config.yaml",
				Digest: "sha256:ghi789",
			},
		},
	}

	err = signer.Sign(record)
	if err != nil {
		t.Fatalf("failed to sign: %v", err)
	}

	// Verify the signature
	err = Verify(record, identity.PublicKey)
	if err != nil {
		t.Fatalf("verification failed: %v", err)
	}

	// Store in provenance store
	store := NewProvenanceStore()
	store.RegisterWorkload(identity)

	err = store.Store(record)
	if err != nil {
		t.Fatalf("failed to store: %v", err)
	}

	// Retrieve and verify
	retrieved, ok := store.Get("build-456")
	if !ok {
		t.Fatal("expected to find stored record")
	}

	err = Verify(retrieved, identity.PublicKey)
	if err != nil {
		t.Fatalf("verification of stored record failed: %v", err)
	}
}

func TestConcurrentAccess(t *testing.T) {
	store := NewProvenanceStore()

	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(id int) {
			for j := 0; j < 100; j++ {
				buildID := fmt.Sprintf("build-%d-%d", id, j)
				record := &ProvenanceRecord{
					BuildID: buildID,
				}
				store.Store(record)
				store.Get(buildID)

				identity := WorkloadIdentity{
					ID:        fmt.Sprintf("worker-%d", id),
					PublicKey: fmt.Sprintf("key-%d", id),
				}
				store.RegisterWorkload(identity)
				store.GetWorkload(fmt.Sprintf("worker-%d", id))
			}
			done <- true
		}(i)
	}

	for i := 0; i < 10; i++ {
		<-done
	}
}
