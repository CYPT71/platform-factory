package provenance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	appmigration "github.com/CYPT71/secure-oci-base/internal/app/migration"
	"github.com/CYPT71/secure-oci-base/internal/core"
	domainmigration "github.com/CYPT71/secure-oci-base/internal/migration"
)

const (
	MigrationExecutionFormatVersion = "platform-factory.dev/migration-execution/v1"
	MigrationArtifactFormatVersion  = "platform-factory.dev/migration-artifact/v1"
	maxMigrationExecutionRecords    = 100000
	maxMigrationExecutionRecordSize = 1 << 20
)

var errMigrationExecutionExists = errors.New("migration provenance record exists")

// MigrationExecutionRecord is the durable representation of facts observed by
// the migration use case. Provider-native payloads and error text are excluded.
type MigrationExecutionRecord struct {
	FormatVersion        string                             `json:"format_version"`
	TraceID              string                             `json:"trace_id"`
	CanonicalGraphDigest string                             `json:"canonical_graph_digest"`
	PlanDigest           string                             `json:"plan_digest"`
	OperationID          core.OperationID                   `json:"operation_id"`
	ResourceID           string                             `json:"resource_id"`
	RequestedCapability  string                             `json:"requested_capability"`
	CapabilityVersion    string                             `json:"capability_version"`
	TargetPluginID       string                             `json:"target_plugin_id,omitempty"`
	TargetPluginDigest   string                             `json:"target_plugin_digest,omitempty"`
	ResolvedCapability   string                             `json:"resolved_capability,omitempty"`
	VerifiedCapability   string                             `json:"verified_capability,omitempty"`
	Compatibility        appmigration.Compatibility         `json:"compatibility,omitempty"`
	Gaps                 []domainmigration.CompatibilityGap `json:"gaps,omitempty"`
	ObservationCount     uint32                             `json:"observation_count"`
	VerificationCount    uint32                             `json:"verification_count"`
	Verified             bool                               `json:"verified"`
	Applied              bool                               `json:"applied"`
	Indeterminate        bool                               `json:"indeterminate"`
	FinalStatus          appmigration.StepStatus            `json:"final_status"`
}

// MigrationExecutionStore is an append-only, crash-safe implementation of the
// application ProvenanceSink. One immutable record is stored per trace and
// operation; an exact replay is idempotent and conflicting evidence fails.
type MigrationExecutionStore struct {
	mu        sync.Mutex
	root      *os.File
	records   map[string]MigrationExecutionRecord
	artifacts map[string]MigrationArtifactRecord
}

func NewMigrationExecutionStore(root string) (*MigrationExecutionStore, error) {
	if root == "" {
		return nil, errors.New("migration provenance: root directory is required")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("migration provenance: create root: %w", err)
	}
	rootFile, err := openMigrationProvenanceRoot(root)
	if err != nil {
		return nil, err
	}
	store := &MigrationExecutionStore{root: rootFile, records: make(map[string]MigrationExecutionRecord), artifacts: make(map[string]MigrationArtifactRecord)}
	if err := store.load(); err != nil {
		_ = rootFile.Close()
		return nil, err
	}
	return store, nil
}

// MigrationArtifactRecord contains only host-observed artifact transfer facts.
// It deliberately excludes artifact bytes and provider diagnostics.
type MigrationArtifactRecord struct {
	FormatVersion       string           `json:"format_version"`
	TraceID             string           `json:"trace_id"`
	OperationID         core.OperationID `json:"operation_id"`
	ResourceID          string           `json:"resource_id"`
	Digest              string           `json:"digest,omitempty"`
	Size                int64            `json:"size"`
	Format              string           `json:"format,omitempty"`
	SourcePluginID      string           `json:"source_plugin_id,omitempty"`
	SourcePluginDigest  string           `json:"source_plugin_digest,omitempty"`
	TargetPluginID      string           `json:"target_plugin_id,omitempty"`
	TargetPluginDigest  string           `json:"target_plugin_digest,omitempty"`
	ArtifactVerified    bool             `json:"artifact_verified"`
	Imported            bool             `json:"imported"`
	ObservedAfterImport bool             `json:"observed_after_import"`
	Verified            bool             `json:"verified"`
}

func (s *MigrationExecutionStore) RecordArtifact(ctx context.Context, evidence appmigration.ArtifactEvidence) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	record := MigrationArtifactRecord{
		FormatVersion: MigrationArtifactFormatVersion, TraceID: evidence.TraceID,
		OperationID: evidence.OperationID, ResourceID: evidence.ResourceID,
		Digest: evidence.Digest, Size: evidence.Size, Format: evidence.Format,
		SourcePluginID: evidence.SourcePluginID, SourcePluginDigest: evidence.SourcePluginDigest,
		TargetPluginID: evidence.TargetPluginID, TargetPluginDigest: evidence.TargetPluginDigest,
		ArtifactVerified: evidence.ArtifactVerified, Imported: evidence.Imported,
		ObservedAfterImport: evidence.ObservedAfterImport, Verified: evidence.Verified,
	}
	if err := validateMigrationArtifactRecord(record); err != nil {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("migration provenance: encode artifact record: %w", err)
	}
	key := migrationArtifactKey(record.TraceID, record.OperationID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.artifacts[key]; ok {
		existingData, marshalErr := json.Marshal(existing)
		if marshalErr != nil || !bytes.Equal(existingData, data) {
			return fmt.Errorf("migration provenance: conflicting artifact evidence for operation %q", record.OperationID)
		}
		return nil
	}
	if len(s.records)+len(s.artifacts) >= maxMigrationExecutionRecords {
		return errors.New("migration provenance: record limit reached")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.append(key, data); err != nil {
		if !errors.Is(err, errMigrationExecutionExists) {
			return err
		}
		existing, readErr := readMigrationProvenanceRecord(s.root, key, maxMigrationExecutionRecordSize)
		if readErr != nil || !bytes.Equal(existing, data) {
			return fmt.Errorf("migration provenance: conflicting concurrent artifact evidence for operation %q", record.OperationID)
		}
	}
	s.artifacts[key] = record
	return nil
}

func (s *MigrationExecutionStore) ArtifactRecords() []MigrationArtifactRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]MigrationArtifactRecord, 0, len(s.artifacts))
	for _, record := range s.artifacts {
		result = append(result, record)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].TraceID != result[j].TraceID {
			return result[i].TraceID < result[j].TraceID
		}
		return result[i].OperationID < result[j].OperationID
	})
	return result
}

func (s *MigrationExecutionStore) RecordExecution(ctx context.Context, evidence appmigration.ExecutionEvidence) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	record := recordFromEvidence(evidence)
	if err := validateMigrationExecutionRecord(record); err != nil {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("migration provenance: encode record: %w", err)
	}
	if len(data) > maxMigrationExecutionRecordSize {
		return errors.New("migration provenance: record exceeds size limit")
	}
	key := migrationExecutionKey(record.TraceID, record.OperationID)

	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.records[key]; ok {
		existingData, marshalErr := json.Marshal(existing)
		if marshalErr != nil || !bytes.Equal(existingData, data) {
			return fmt.Errorf("migration provenance: conflicting evidence for operation %q", record.OperationID)
		}
		return nil
	}
	if len(s.records) >= maxMigrationExecutionRecords {
		return errors.New("migration provenance: record limit reached")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.append(key, data); err != nil {
		if !errors.Is(err, errMigrationExecutionExists) {
			return err
		}
		existing, readErr := readMigrationProvenanceRecord(s.root, key, maxMigrationExecutionRecordSize)
		if readErr != nil || !bytes.Equal(existing, data) {
			return fmt.Errorf("migration provenance: conflicting concurrent evidence for operation %q", record.OperationID)
		}
	}
	s.records[key] = record
	return nil
}

// Close releases the pinned root descriptor. The store must not be used after
// Close. Composition owns its lifetime.
func (s *MigrationExecutionStore) Close() error {
	if s == nil || s.root == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.root.Close()
	s.root = nil
	return err
}

// Records returns a deterministic snapshot suitable for verification or
// export. Reload validation has already rejected malformed persistent state.
func (s *MigrationExecutionStore) Records() []MigrationExecutionRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]MigrationExecutionRecord, 0, len(s.records))
	for _, record := range s.records {
		record.Gaps = append([]domainmigration.CompatibilityGap(nil), record.Gaps...)
		result = append(result, record)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].TraceID != result[j].TraceID {
			return result[i].TraceID < result[j].TraceID
		}
		return result[i].OperationID < result[j].OperationID
	})
	return result
}

func recordFromEvidence(e appmigration.ExecutionEvidence) MigrationExecutionRecord {
	gaps := append([]domainmigration.CompatibilityGap(nil), e.Gaps...)
	sort.Slice(gaps, func(i, j int) bool {
		a, b := gaps[i], gaps[j]
		return a.ResourceID+"\x00"+a.Requirement+"\x00"+string(a.Compatibility)+"\x00"+a.Reason+"\x00"+a.LostGuarantee <
			b.ResourceID+"\x00"+b.Requirement+"\x00"+string(b.Compatibility)+"\x00"+b.Reason+"\x00"+b.LostGuarantee
	})
	return MigrationExecutionRecord{
		FormatVersion: MigrationExecutionFormatVersion, TraceID: e.TraceID,
		CanonicalGraphDigest: e.InputDigest, PlanDigest: e.PlanDigest,
		OperationID: e.OperationID, ResourceID: e.ResourceID,
		RequestedCapability: e.Capability, CapabilityVersion: e.CapabilityVersion,
		TargetPluginID: e.CandidateID, TargetPluginDigest: e.CandidateDigest,
		ResolvedCapability: e.ResolvedCapability, VerifiedCapability: e.VerifiedCapability,
		Compatibility: e.Compatibility, Gaps: gaps, ObservationCount: e.ObservationCount,
		VerificationCount: e.VerificationCount, Verified: e.Verified, Applied: e.Applied,
		Indeterminate: e.Indeterminate, FinalStatus: e.Status,
	}
}

func validateMigrationExecutionRecord(r MigrationExecutionRecord) error {
	if r.FormatVersion != MigrationExecutionFormatVersion {
		return errors.New("migration provenance: unsupported format version")
	}
	values := []string{r.TraceID, r.CanonicalGraphDigest, r.PlanDigest, string(r.OperationID), r.ResourceID, r.RequestedCapability, r.CapabilityVersion,
		r.TargetPluginID, r.TargetPluginDigest, r.ResolvedCapability, r.VerifiedCapability, string(r.Compatibility), string(r.FinalStatus)}
	for _, value := range values {
		if len(value) > 4096 || !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 || containsSecretValue(value) {
			return errors.New("migration provenance: invalid or secret-like field value")
		}
	}
	if r.TraceID == "" || r.ResourceID == "" || r.RequestedCapability == "" || r.CapabilityVersion == "" || !core.ValidOperationID(r.OperationID) {
		return errors.New("migration provenance: required identity is missing or invalid")
	}
	if !validDigest(r.CanonicalGraphDigest) || !validDigest(r.PlanDigest) || (r.TargetPluginDigest != "" && !validDigest(r.TargetPluginDigest)) {
		return errors.New("migration provenance: invalid digest")
	}
	if r.FinalStatus != appmigration.StepConverged && r.FinalStatus != appmigration.StepFailed && r.FinalStatus != appmigration.StepBlocked {
		return errors.New("migration provenance: invalid final status")
	}
	if r.FinalStatus == appmigration.StepConverged && (!r.Verified || r.VerificationCount == 0) {
		return errors.New("migration provenance: convergence requires observed verification")
	}
	if r.FinalStatus == appmigration.StepBlocked && (r.Applied || r.ObservationCount != 0 || r.VerificationCount != 0) {
		return errors.New("migration provenance: blocked operation contains execution claims")
	}
	if len(r.Gaps) > domainmigration.MaxGaps {
		return errors.New("migration provenance: too many compatibility gaps")
	}
	for _, gap := range r.Gaps {
		for _, value := range []string{gap.ResourceID, gap.Requirement, string(gap.Compatibility), gap.Reason, gap.LostGuarantee} {
			if containsSecretValue(value) || len(value) > 4096 || !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 {
				return errors.New("migration provenance: invalid or secret-like compatibility gap")
			}
		}
	}
	return nil
}

func validateMigrationArtifactRecord(r MigrationArtifactRecord) error {
	if r.FormatVersion != MigrationArtifactFormatVersion {
		return errors.New("migration provenance: unsupported artifact format version")
	}
	values := []string{r.TraceID, string(r.OperationID), r.ResourceID, r.Digest, r.Format,
		r.SourcePluginID, r.SourcePluginDigest, r.TargetPluginID, r.TargetPluginDigest}
	for _, value := range values {
		if len(value) > 4096 || !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 || containsSecretValue(value) {
			return errors.New("migration provenance: invalid or secret-like artifact field value")
		}
	}
	if r.TraceID == "" || r.ResourceID == "" || !core.ValidOperationID(r.OperationID) {
		return errors.New("migration provenance: artifact identity is missing or invalid")
	}
	if r.Size < 0 || r.Size > appmigration.MaxPortableArtifactBytes {
		return errors.New("migration provenance: artifact size is invalid")
	}
	if r.Digest != "" && !validDigest(r.Digest) {
		return errors.New("migration provenance: artifact digest is invalid")
	}
	if (r.Digest == "") != (r.Format == "") {
		return errors.New("migration provenance: artifact digest and format must be recorded together")
	}
	for _, digest := range []string{r.SourcePluginDigest, r.TargetPluginDigest} {
		if digest != "" && !validDigest(digest) {
			return errors.New("migration provenance: plugin digest is invalid")
		}
	}
	if (r.SourcePluginID == "") != (r.SourcePluginDigest == "") || (r.TargetPluginID == "") != (r.TargetPluginDigest == "") {
		return errors.New("migration provenance: plugin ID and digest must be recorded together")
	}
	if r.ArtifactVerified && (r.Digest == "" || r.Format == "") {
		return errors.New("migration provenance: artifact verification requires identity facts")
	}
	if r.ArtifactVerified && (r.SourcePluginID == "" || r.TargetPluginID == "") {
		return errors.New("migration provenance: artifact verification requires source and target identities")
	}
	if r.ObservedAfterImport && !r.ArtifactVerified {
		return errors.New("migration provenance: post-import observation requires host-verified artifact")
	}
	if r.Imported && (!r.ArtifactVerified || !r.ObservedAfterImport || !r.Verified) {
		return errors.New("migration provenance: import success requires host verification and observed final state")
	}
	if r.Verified && (!r.ArtifactVerified || !r.Imported || !r.ObservedAfterImport) {
		return errors.New("migration provenance: final verification requires observed verified import")
	}
	return nil
}

func containsSecretValue(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	for _, marker := range []string{"secret-sentinel", "password=", "passwd=", "secret=", "access_token=", "api_key=", "private_key=", "-----begin private key", "-----begin rsa private key"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func validDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func migrationExecutionKey(traceID string, operationID core.OperationID) string {
	sum := sha256.Sum256([]byte("platform-factory/migration-provenance-key/v1\x00" + traceID + "\x00" + string(operationID)))
	return hex.EncodeToString(sum[:])
}

func migrationArtifactKey(traceID string, operationID core.OperationID) string {
	sum := sha256.Sum256([]byte("platform-factory/migration-artifact-provenance-key/v1\x00" + traceID + "\x00" + string(operationID)))
	return hex.EncodeToString(sum[:])
}

func (s *MigrationExecutionStore) load() error {
	entries, err := readMigrationProvenanceDir(s.root)
	if err != nil {
		return fmt.Errorf("migration provenance: read root: %w", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".migration-") {
			continue // unpublished temp file left by a crash
		}
		if entry.IsDir() || len(entry.Name()) != 64 || strings.Trim(entry.Name(), "0123456789abcdef") != "" {
			return fmt.Errorf("migration provenance: unexpected entry %q", entry.Name())
		}
		if len(s.records)+len(s.artifacts) >= maxMigrationExecutionRecords {
			return errors.New("migration provenance: record limit reached while loading")
		}
		data, err := readMigrationProvenanceRecord(s.root, entry.Name(), maxMigrationExecutionRecordSize)
		if err != nil {
			return fmt.Errorf("migration provenance: read record %q: %w", entry.Name(), err)
		}
		var header struct {
			FormatVersion string `json:"format_version"`
		}
		if err := json.Unmarshal(data, &header); err != nil {
			return fmt.Errorf("migration provenance: corrupt record %q", entry.Name())
		}
		switch header.FormatVersion {
		case MigrationExecutionFormatVersion:
			var record MigrationExecutionRecord
			if err := decodeStrictMigrationRecord(data, &record); err != nil {
				return fmt.Errorf("migration provenance: corrupt record %q", entry.Name())
			}
			if err := validateMigrationExecutionRecord(record); err != nil {
				return fmt.Errorf("migration provenance: validate record %q: %w", entry.Name(), err)
			}
			key := migrationExecutionKey(record.TraceID, record.OperationID)
			if key != entry.Name() {
				return fmt.Errorf("migration provenance: record %q identity mismatch", entry.Name())
			}
			s.records[key] = record
		case MigrationArtifactFormatVersion:
			var record MigrationArtifactRecord
			if err := decodeStrictMigrationRecord(data, &record); err != nil {
				return fmt.Errorf("migration provenance: corrupt artifact record %q", entry.Name())
			}
			if err := validateMigrationArtifactRecord(record); err != nil {
				return fmt.Errorf("migration provenance: validate artifact record %q: %w", entry.Name(), err)
			}
			key := migrationArtifactKey(record.TraceID, record.OperationID)
			if key != entry.Name() {
				return fmt.Errorf("migration provenance: artifact record %q identity mismatch", entry.Name())
			}
			s.artifacts[key] = record
		default:
			return fmt.Errorf("migration provenance: unsupported record format %q", header.FormatVersion)
		}
	}
	return nil
}

func decodeStrictMigrationRecord(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("trailing record content")
	}
	return nil
}

func (s *MigrationExecutionStore) append(key string, data []byte) error {
	return appendMigrationProvenanceRecord(s.root, key, data)
}

var _ appmigration.ProvenanceSink = (*MigrationExecutionStore)(nil)
