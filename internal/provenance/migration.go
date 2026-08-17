package provenance

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	appmigration "github.com/CYPT71/platform-factory/internal/app/migration"
	"github.com/CYPT71/platform-factory/internal/core"
	domainmigration "github.com/CYPT71/platform-factory/internal/migration"
	"github.com/CYPT71/platform-factory/internal/strictjson"
)

const (
	MigrationExecutionFormatVersion = "platform-factory.dev/migration-execution/v1"
	MigrationArtifactFormatVersion  = "platform-factory.dev/migration-artifact/v1"
	MigrationWorkflowFormatVersion  = "platform-factory.dev/migration-workflow/v1"
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
	workflows map[string]MigrationWorkflowRecord
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
	store := &MigrationExecutionStore{root: rootFile, records: make(map[string]MigrationExecutionRecord), artifacts: make(map[string]MigrationArtifactRecord), workflows: make(map[string]MigrationWorkflowRecord)}
	if err := store.load(); err != nil {
		_ = rootFile.Close()
		return nil, err
	}
	return store, nil
}

type MigrationWorkflowRecord struct {
	FormatVersion         string                               `json:"format_version"`
	TraceID               string                               `json:"trace_id"`
	SourcePluginID        string                               `json:"source_plugin_id"`
	SourcePluginDigest    string                               `json:"source_plugin_digest"`
	SourceCapability      string                               `json:"source_capability"`
	CanonicalGraphDigest  string                               `json:"canonical_graph_digest"`
	PlanDigest            string                               `json:"plan_digest"`
	SourceResourceIDs     []string                             `json:"source_resource_ids"`
	TargetResourceIDs     []string                             `json:"target_resource_ids"`
	TargetPluginIDs       []string                             `json:"target_plugin_ids"`
	TargetPluginDigests   []string                             `json:"target_plugin_digests"`
	RequestedCapabilities []string                             `json:"requested_capabilities"`
	ResolvedCapabilities  []string                             `json:"resolved_capabilities"`
	VerifiedCapabilities  []string                             `json:"verified_capabilities"`
	OperationIDs          []string                             `json:"operation_ids"`
	CompatibilityGaps     []domainmigration.CompatibilityGap   `json:"compatibility_gaps,omitempty"`
	ExternalDependencies  []domainmigration.ExternalDependency `json:"external_dependencies,omitempty"`
	Transformations       []domainmigration.Transformation     `json:"transformations,omitempty"`
	ObservationCount      uint32                               `json:"observation_count"`
	VerificationCount     uint32                               `json:"verification_count"`
	FinalState            string                               `json:"final_state"`
}

func (s *MigrationExecutionStore) RecordWorkflow(ctx context.Context, evidence appmigration.WorkflowEvidence) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	record := MigrationWorkflowRecord{
		FormatVersion: MigrationWorkflowFormatVersion, TraceID: evidence.TraceID,
		SourcePluginID: evidence.SourcePluginID, SourcePluginDigest: evidence.SourcePluginDigest, SourceCapability: evidence.SourceCapability,
		CanonicalGraphDigest: evidence.CanonicalGraphDigest, PlanDigest: evidence.PlanDigest,
		SourceResourceIDs: append([]string(nil), evidence.SourceResourceIDs...), TargetResourceIDs: append([]string(nil), evidence.TargetResourceIDs...),
		TargetPluginIDs: append([]string(nil), evidence.TargetPluginIDs...), TargetPluginDigests: append([]string(nil), evidence.TargetPluginDigests...),
		RequestedCapabilities: append([]string(nil), evidence.RequestedCapabilities...), ResolvedCapabilities: append([]string(nil), evidence.ResolvedCapabilities...), VerifiedCapabilities: append([]string(nil), evidence.VerifiedCapabilities...),
		OperationIDs: append([]string(nil), evidence.OperationIDs...), CompatibilityGaps: append([]domainmigration.CompatibilityGap(nil), evidence.CompatibilityGaps...), ExternalDependencies: append([]domainmigration.ExternalDependency(nil), evidence.ExternalDependencies...), Transformations: append([]domainmigration.Transformation(nil), evidence.Transformations...),
		ObservationCount: evidence.ObservationCount, VerificationCount: evidence.VerificationCount, FinalState: evidence.FinalState,
	}
	canonicalizeWorkflowRecord(&record)
	if err := validateMigrationWorkflowRecord(record); err != nil {
		return err
	}
	key := migrationWorkflowKey(record.TraceID, record.PlanDigest)
	return persistMigrationRecord(ctx, s, s.workflows, key, record,
		"workflow", "migration provenance: conflicting workflow evidence",
		"migration provenance: conflicting concurrent workflow evidence")
}

func (s *MigrationExecutionStore) WorkflowRecords() []MigrationWorkflowRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]MigrationWorkflowRecord, 0, len(s.workflows))
	for _, record := range s.workflows {
		record.SourceResourceIDs = append([]string(nil), record.SourceResourceIDs...)
		record.TargetResourceIDs = append([]string(nil), record.TargetResourceIDs...)
		record.TargetPluginIDs = append([]string(nil), record.TargetPluginIDs...)
		record.TargetPluginDigests = append([]string(nil), record.TargetPluginDigests...)
		record.RequestedCapabilities = append([]string(nil), record.RequestedCapabilities...)
		record.ResolvedCapabilities = append([]string(nil), record.ResolvedCapabilities...)
		record.VerifiedCapabilities = append([]string(nil), record.VerifiedCapabilities...)
		record.OperationIDs = append([]string(nil), record.OperationIDs...)
		record.CompatibilityGaps = append([]domainmigration.CompatibilityGap(nil), record.CompatibilityGaps...)
		record.ExternalDependencies = append([]domainmigration.ExternalDependency(nil), record.ExternalDependencies...)
		record.Transformations = append([]domainmigration.Transformation(nil), record.Transformations...)
		result = append(result, record)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].TraceID != result[j].TraceID {
			return result[i].TraceID < result[j].TraceID
		}
		return result[i].PlanDigest < result[j].PlanDigest
	})
	return result
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
	key := migrationArtifactKey(record.TraceID, record.OperationID)
	return persistMigrationRecord(ctx, s, s.artifacts, key, record,
		"artifact record",
		fmt.Sprintf("migration provenance: conflicting artifact evidence for operation %q", record.OperationID),
		fmt.Sprintf("migration provenance: conflicting concurrent artifact evidence for operation %q", record.OperationID))
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
	key := migrationExecutionKey(record.TraceID, record.OperationID)
	return persistMigrationRecord(ctx, s, s.records, key, record, "record",
		fmt.Sprintf("migration provenance: conflicting evidence for operation %q", record.OperationID),
		fmt.Sprintf("migration provenance: conflicting concurrent evidence for operation %q", record.OperationID))
}

func persistMigrationRecord[T any](ctx context.Context, s *MigrationExecutionStore,
	records map[string]T, key string, record T, kind, conflict, concurrentConflict string,
) error {
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("migration provenance: encode %s: %w", kind, err)
	}
	if len(data) > maxMigrationExecutionRecordSize {
		return errors.New("migration provenance: record exceeds size limit")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := records[key]; ok {
		existingData, marshalErr := json.Marshal(existing)
		if marshalErr != nil || !bytes.Equal(existingData, data) {
			return errors.New(conflict)
		}
		return nil
	}
	if len(s.records)+len(s.artifacts)+len(s.workflows) >= maxMigrationExecutionRecords {
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
			return errors.New(concurrentConflict)
		}
	}
	records[key] = record
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

func canonicalizeWorkflowRecord(r *MigrationWorkflowRecord) {
	for _, values := range [][]string{r.SourceResourceIDs, r.TargetResourceIDs, r.TargetPluginIDs, r.TargetPluginDigests, r.RequestedCapabilities, r.ResolvedCapabilities, r.VerifiedCapabilities, r.OperationIDs} {
		sort.Strings(values)
	}
	sort.Slice(r.CompatibilityGaps, func(i, j int) bool {
		a, b := r.CompatibilityGaps[i], r.CompatibilityGaps[j]
		return a.ResourceID+"\x00"+a.Requirement+"\x00"+a.Reason < b.ResourceID+"\x00"+b.Requirement+"\x00"+b.Reason
	})
	sort.Slice(r.ExternalDependencies, func(i, j int) bool {
		a, b := r.ExternalDependencies[i], r.ExternalDependencies[j]
		return a.ResourceID+"\x00"+a.Kind+"\x00"+a.Reference < b.ResourceID+"\x00"+b.Kind+"\x00"+b.Reference
	})
	sort.Slice(r.Transformations, func(i, j int) bool {
		a, b := r.Transformations[i], r.Transformations[j]
		return a.ResourceID+"\x00"+a.Field+"\x00"+a.From+"\x00"+a.To+"\x00"+a.Reason < b.ResourceID+"\x00"+b.Field+"\x00"+b.From+"\x00"+b.To+"\x00"+b.Reason
	})
}

func validateMigrationWorkflowRecord(r MigrationWorkflowRecord) error {
	if r.FormatVersion != MigrationWorkflowFormatVersion {
		return errors.New("migration provenance: unsupported workflow format version")
	}
	if r.TraceID == "" || r.SourcePluginID == "" || r.SourceCapability == "" || !validDigest(r.SourcePluginDigest) || !validDigest(r.CanonicalGraphDigest) || !validDigest(r.PlanDigest) {
		return errors.New("migration provenance: workflow identity is missing or invalid")
	}
	if r.FinalState != "verified" && r.FinalState != "failed" {
		return errors.New("migration provenance: invalid workflow final state")
	}
	if len(r.SourceResourceIDs) == 0 || len(r.SourceResourceIDs) != len(r.TargetResourceIDs) || len(r.OperationIDs) == 0 {
		return errors.New("migration provenance: workflow resource or operation evidence is incomplete")
	}
	if len(r.TargetPluginIDs) == 0 || len(r.TargetPluginIDs) != len(r.TargetPluginDigests) {
		return errors.New("migration provenance: target plugin evidence is incomplete")
	}
	if r.FinalState == "verified" && (r.VerificationCount == 0 || len(r.VerifiedCapabilities) == 0) {
		return errors.New("migration provenance: verified workflow lacks host verification")
	}
	for _, values := range [][]string{{r.TraceID, r.SourcePluginID, r.SourceCapability, r.FinalState}, r.SourceResourceIDs, r.TargetResourceIDs, r.TargetPluginIDs, r.TargetPluginDigests, r.RequestedCapabilities, r.ResolvedCapabilities, r.VerifiedCapabilities, r.OperationIDs} {
		seen := map[string]struct{}{}
		for _, value := range values {
			if value == "" || len(value) > 4096 || !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 || containsSecretValue(value) {
				return errors.New("migration provenance: invalid or secret-like workflow field")
			}
			if _, ok := seen[value]; ok {
				return errors.New("migration provenance: duplicate workflow field")
			}
			seen[value] = struct{}{}
		}
	}
	for _, digest := range r.TargetPluginDigests {
		if !validDigest(digest) {
			return errors.New("migration provenance: invalid target plugin digest")
		}
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
	return core.DeriveID("platform-factory/migration-provenance-key/v1", traceID, string(operationID))
}

func migrationArtifactKey(traceID string, operationID core.OperationID) string {
	return core.DeriveID("platform-factory/migration-artifact-provenance-key/v1", traceID, string(operationID))
}

func migrationWorkflowKey(traceID, planDigest string) string {
	return core.DeriveID("platform-factory/migration-workflow-provenance-key/v1", traceID, planDigest)
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
		if len(s.records)+len(s.artifacts)+len(s.workflows) >= maxMigrationExecutionRecords {
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
		case MigrationWorkflowFormatVersion:
			var record MigrationWorkflowRecord
			if err := decodeStrictMigrationRecord(data, &record); err != nil {
				return fmt.Errorf("migration provenance: corrupt workflow record %q", entry.Name())
			}
			if err := validateMigrationWorkflowRecord(record); err != nil {
				return fmt.Errorf("migration provenance: validate workflow record %q: %w", entry.Name(), err)
			}
			key := migrationWorkflowKey(record.TraceID, record.PlanDigest)
			if key != entry.Name() {
				return fmt.Errorf("migration provenance: workflow record %q identity mismatch", entry.Name())
			}
			s.workflows[key] = record
		default:
			return fmt.Errorf("migration provenance: unsupported record format %q", header.FormatVersion)
		}
	}
	return nil
}

func decodeStrictMigrationRecord(data []byte, target any) error {
	return strictjson.Decode(data, target)
}

func (s *MigrationExecutionStore) append(key string, data []byte) error {
	return appendMigrationProvenanceRecord(s.root, key, data)
}

var _ appmigration.ProvenanceSink = (*MigrationExecutionStore)(nil)
var _ appmigration.WorkflowProvenanceSink = (*MigrationExecutionStore)(nil)
