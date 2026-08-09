// Package provenance provides workload identity-based provenance signing.
// This package implements signing of provenance records with workload identities
// for the distributed build system.
package provenance

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// WorkloadIdentity represents a workload's cryptographic identity.
type WorkloadIdentity struct {
	// ID is the unique identifier for the workload (e.g., from SPIFFE/SPIRE)
	ID string `json:"id"`
	// PublicKey is the workload's Ed25519 public key in base64
	PublicKey string `json:"public_key"`
	// CertificateChain is the base64-encoded certificate chain for the workload
	CertificateChain string `json:"certificate_chain,omitempty"`
	// IssuedAt is when the identity was issued
	IssuedAt time.Time `json:"issued_at"`
	// ExpiresAt is when the identity expires
	ExpiresAt time.Time `json:"expires_at"`
}

// WorkloadSigner signs provenance records with a workload identity.
type WorkloadSigner struct {
	identity   WorkloadIdentity
	privateKey ed25519.PrivateKey
}

// NewWorkloadSigner creates a new WorkloadSigner with the given identity and private key.
// The caller is responsible for ensuring the private key matches the public key in the identity.
func NewWorkloadSigner(identity WorkloadIdentity, privateKey ed25519.PrivateKey) (*WorkloadSigner, error) {
	if identity.ID == "" {
		return nil, errors.New("workload identity ID cannot be empty")
	}
	if identity.PublicKey == "" {
		return nil, errors.New("workload identity public key cannot be empty")
	}
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, errors.New("invalid Ed25519 private key size")
	}

	return &WorkloadSigner{
		identity:   identity,
		privateKey: privateKey,
	}, nil
}

// GenerateWorkloadIdentity creates a new workload identity with a new key pair.
func GenerateWorkloadIdentity(id string) (WorkloadIdentity, ed25519.PrivateKey, error) {
	if id == "" {
		return WorkloadIdentity{}, nil, errors.New("workload identity ID cannot be empty")
	}

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return WorkloadIdentity{}, nil, fmt.Errorf("failed to generate key pair: %w", err)
	}

	return WorkloadIdentity{
		ID:        id,
		PublicKey: base64.StdEncoding.EncodeToString(publicKey),
		IssuedAt:  time.Now(),
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}, privateKey, nil
}

// ProvenanceRecord represents a provenance record for a build artifact.
type ProvenanceRecord struct {
	// BuildID is the unique identifier for the build
	BuildID string `json:"build_id"`
	// ArtifactID is the identifier for the artifact (e.g., digest)
	ArtifactID string `json:"artifact_id"`
	// WorkerID is the identifier of the worker that produced the artifact
	WorkerID string `json:"worker_id"`
	// TenantID is the tenant that owns the build
	TenantID string `json:"tenant_id"`
	// Materials is the list of input materials for the build
	Materials []Material `json:"materials"`
	// Invocation is the build invocation configuration
	Invocation Invocation `json:"invocation"`
	// Timestamp is when the provenance was recorded
	Timestamp time.Time `json:"timestamp"`
	// Signature is the base64-encoded signature of the provenance record
	Signature string `json:"signature,omitempty"`
	// SignedBy is the workload identity that signed the record
	SignedBy string `json:"signed_by,omitempty"`
}

// Material represents an input material for a build.
type Material struct {
	// URI is the location of the material
	URI string `json:"uri"`
	// Digest is the content digest of the material
	Digest string `json:"digest"`
	// MIMEType is the MIME type of the material
	MIMEType string `json:"mime_type,omitempty"`
}

// Invocation represents the build invocation configuration.
type Invocation struct {
	// ConfigSource is the source of the configuration
	ConfigSource ConfigSource `json:"config_source"`
	// Environment is the build environment
	Environment map[string]string `json:"environment,omitempty"`
	// Parameters are the build parameters
	Parameters map[string]string `json:"parameters,omitempty"`
}

// ConfigSource represents the source of the build configuration.
type ConfigSource struct {
	// URI is the location of the configuration
	URI string `json:"uri"`
	// Digest is the content digest of the configuration
	Digest string `json:"digest"`
	// EntryPoint is the entry point for the configuration
	EntryPoint string `json:"entry_point,omitempty"`
}

// Identity returns the workload identity this signer signs as.
func (ws *WorkloadSigner) Identity() WorkloadIdentity {
	return ws.identity
}

// Sign signs the provenance record with the workload's private key.
func (ws *WorkloadSigner) Sign(record *ProvenanceRecord) error {
	if record.BuildID == "" {
		return errors.New("build ID cannot be empty")
	}
	if record.Timestamp.IsZero() {
		record.Timestamp = time.Now()
	}

	// Serialize the provenance record (excluding signature and signed_by)
	recordToSign := ProvenanceRecord{
		BuildID:    record.BuildID,
		ArtifactID: record.ArtifactID,
		WorkerID:   record.WorkerID,
		TenantID:   record.TenantID,
		Materials:  record.Materials,
		Invocation: record.Invocation,
		Timestamp:  record.Timestamp,
	}

	data, err := json.Marshal(recordToSign)
	if err != nil {
		return fmt.Errorf("failed to marshal provenance record: %w", err)
	}

	// Sign the data
	signature := ed25519.Sign(ws.privateKey, data)
	record.Signature = base64.StdEncoding.EncodeToString(signature)
	record.SignedBy = ws.identity.ID

	return nil
}

// Verify verifies the signature on a provenance record using the workload's public key.
func Verify(record *ProvenanceRecord, workloadPublicKey string) error {
	if record.Signature == "" {
		return errors.New("provenance record has no signature")
	}
	if record.SignedBy == "" {
		return errors.New("provenance record has no signer")
	}

	publicKeyBytes, err := base64.StdEncoding.DecodeString(workloadPublicKey)
	if err != nil {
		return fmt.Errorf("failed to decode public key: %w", err)
	}

	// Reconstruct the record without signature and signed_by
	recordToVerify := ProvenanceRecord{
		BuildID:    record.BuildID,
		ArtifactID: record.ArtifactID,
		WorkerID:   record.WorkerID,
		TenantID:   record.TenantID,
		Materials:  record.Materials,
		Invocation: record.Invocation,
		Timestamp:  record.Timestamp,
	}

	data, err := json.Marshal(recordToVerify)
	if err != nil {
		return fmt.Errorf("failed to marshal provenance record for verification: %w", err)
	}

	signatureBytes, err := base64.StdEncoding.DecodeString(record.Signature)
	if err != nil {
		return fmt.Errorf("failed to decode signature: %w", err)
	}

	if !ed25519.Verify(publicKeyBytes, data, signatureBytes) {
		return errors.New("provenance signature verification failed")
	}

	return nil
}

// SignWithWorkloadIdentity signs a provenance record using a pre-configured workload identity.
// This is a convenience function that generates an ephemeral key pair for signing.
// In production, this would integrate with a secure identity provider (e.g., SPIFFE/SPIRE).
func SignWithWorkloadIdentity(record *ProvenanceRecord, workloadID string) error {
	if record == nil {
		return errors.New("provenance record cannot be nil")
	}

	// Set the worker ID from the workload ID if not already set
	if record.WorkerID == "" {
		record.WorkerID = workloadID
	}

	// Generate an ephemeral key pair for signing
	// In production, this would use the workload's persistent identity
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("failed to generate key pair: %w", err)
	}

	// Serialize the provenance record (excluding signature and signed_by)
	recordToSign := ProvenanceRecord{
		BuildID:    record.BuildID,
		ArtifactID: record.ArtifactID,
		WorkerID:   record.WorkerID,
		TenantID:   record.TenantID,
		Materials:  record.Materials,
		Invocation: record.Invocation,
		Timestamp:  record.Timestamp,
	}

	data, err := json.Marshal(recordToSign)
	if err != nil {
		return fmt.Errorf("failed to marshal record: %w", err)
	}

	// Sign with the ephemeral key
	signature := ed25519.Sign(privateKey, data)
	record.Signature = base64.StdEncoding.EncodeToString(signature)
	record.SignedBy = workloadID

	// Store the public key in the record's materials for verification
	// In production, the public key would come from the workload's identity certificate
	record.Materials = append(record.Materials, Material{
		URI:    fmt.Sprintf("workload-identity://%s/public-key", workloadID),
		Digest: base64.StdEncoding.EncodeToString(publicKey),
	})

	return nil
}

// ProvenanceStore stores and retrieves provenance records.
type ProvenanceStore struct {
	mu        sync.RWMutex
	records   map[string]*ProvenanceRecord
	workloads map[string]WorkloadIdentity
}

// NewProvenanceStore creates a new ProvenanceStore.
func NewProvenanceStore() *ProvenanceStore {
	return &ProvenanceStore{
		records:   make(map[string]*ProvenanceRecord),
		workloads: make(map[string]WorkloadIdentity),
	}
}

// Store saves a provenance record.
func (ps *ProvenanceStore) Store(record *ProvenanceRecord) error {
	if record == nil {
		return errors.New("provenance record cannot be nil")
	}
	if record.BuildID == "" {
		return errors.New("build ID cannot be empty")
	}

	ps.mu.Lock()
	defer ps.mu.Unlock()

	ps.records[record.BuildID] = record
	return nil
}

// Get retrieves a provenance record by build ID.
func (ps *ProvenanceStore) Get(buildID string) (*ProvenanceRecord, bool) {
	ps.mu.RLock()
	defer ps.mu.RUnlock()

	record, ok := ps.records[buildID]
	return record, ok
}

// RegisterWorkload registers a workload identity.
func (ps *ProvenanceStore) RegisterWorkload(identity WorkloadIdentity) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.workloads[identity.ID] = identity
}

// GetWorkload retrieves a workload identity by ID.
func (ps *ProvenanceStore) GetWorkload(id string) (WorkloadIdentity, bool) {
	ps.mu.RLock()
	defer ps.mu.RUnlock()

	identity, ok := ps.workloads[id]
	return identity, ok
}
