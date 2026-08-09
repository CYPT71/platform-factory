package migration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/CYPT71/secure-oci-base/internal/core"
	domainmigration "github.com/CYPT71/secure-oci-base/internal/migration"
	"github.com/CYPT71/secure-oci-base/internal/observability"
)

// Base64 expands payloads by 4/3; 380 KiB leaves bounded room for the JSON-RPC
// envelope beneath the protocol's hard 512 KiB frame limit.
const MaxPortableArtifactBytes = 380 << 10
const OCIArchiveFormat = "oci-layout.tar.gz"

type ExportedArtifact struct {
	Digest string
	Size   int64
	Format string
	Data   []byte
}
type VerifiedArtifact struct {
	Digest string
	Size   int64
	Format string
	Data   []byte
}
type ArtifactExporter interface {
	Identity() (string, string)
	Export(context.Context, domainmigration.Resource) (ExportedArtifact, error)
}
type ArtifactImporter interface {
	Identity() (string, string)
	Import(context.Context, core.OperationID, domainmigration.Resource, VerifiedArtifact) error
	ObserveArtifact(context.Context, domainmigration.Resource) (ArtifactObservation, error)
}
type ArtifactObservation struct {
	Found         bool
	Resource      domainmigration.Resource
	NativeBinding string
	Attestation   []byte
}
type ArtifactInstallationVerifier interface {
	VerifyInstallation(context.Context, domainmigration.Resource, VerifiedArtifact, ArtifactObservation) error
}
type InstalledArtifactReadback interface {
	ReadInstalledArtifact(context.Context, domainmigration.Resource, string) (io.ReadCloser, error)
}
type ReadbackInstallationVerifier struct{ Readback InstalledArtifactReadback }

func (v ReadbackInstallationVerifier) VerifyInstallation(ctx context.Context, r domainmigration.Resource, a VerifiedArtifact, o ArtifactObservation) error {
	if v.Readback == nil || !o.Found || o.NativeBinding == "" {
		return errors.New("migration artifact: independent installation readback is required")
	}
	reader, err := v.Readback.ReadInstalledArtifact(ctx, r, o.NativeBinding)
	if err != nil {
		return err
	}
	data, readErr := io.ReadAll(io.LimitReader(reader, MaxPortableArtifactBytes+1))
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || int64(len(data)) != a.Size {
		return errors.Join(readErr, closeErr, errors.New("migration artifact: installed readback size mismatch"))
	}
	sum := sha256.Sum256(data)
	if "sha256:"+hex.EncodeToString(sum[:]) != a.Digest {
		return errors.New("migration artifact: installed readback digest mismatch")
	}
	return nil
}

type ArtifactStructureVerifier interface {
	VerifyArtifact(context.Context, string, io.Reader) error
}
type ArtifactEvidence struct {
	TraceID, ResourceID, Digest, Format                                    string
	OperationID                                                            core.OperationID
	SourcePluginID, SourcePluginDigest, TargetPluginID, TargetPluginDigest string
	Size                                                                   int64
	ArtifactVerified, Verified, Imported, ObservedAfterImport              bool
}
type ArtifactEvidenceSink interface {
	RecordArtifact(context.Context, ArtifactEvidence) error
}

type ArtifactTransfer struct {
	Store                core.CacheStore
	Verifier             ArtifactStructureVerifier
	Sink                 ArtifactEvidenceSink
	Policy               ArtifactPersistencePolicy
	Defense              ArtifactSentinelDefense
	InstallationVerifier ArtifactInstallationVerifier
}
type ArtifactClassificationEvidence struct{ Classification, AuthorityID, AuthorityDigest, PolicyDigest string }
type ArtifactPersistencePolicy interface {
	ClassifyArtifact(context.Context, string, io.Reader) (ArtifactClassificationEvidence, error)
}
type ArtifactSentinelDefense interface{ ScanSentinels([]byte) error }
type SentinelSecretDefense struct{}

func (SentinelSecretDefense) ScanSentinels(data []byte) error {
	lower := strings.ToLower(string(data))
	for _, m := range []string{"secret-sentinel", "password=", "secret=", "access_token=", "api_key=", "private_key=", "-----begin private key"} {
		if strings.Contains(lower, m) {
			return errors.New("migration artifact: secret sentinel detected")
		}
	}
	return nil
}

func (u ArtifactTransfer) Transfer(ctx context.Context, operationID core.OperationID, resource domainmigration.Resource, exporter ArtifactExporter, importer ArtifactImporter) (ArtifactEvidence, error) {
	e := ArtifactEvidence{TraceID: observability.TraceIDFromContext(ctx), ResourceID: resource.ID, OperationID: operationID}
	if err := ctx.Err(); err != nil {
		return e, err
	}
	if e.TraceID == "" || !core.ValidOperationID(operationID) || u.Store == nil || u.Verifier == nil || u.Sink == nil || u.Policy == nil || u.Defense == nil || u.InstallationVerifier == nil || exporter == nil || importer == nil {
		return e, errors.New("migration artifact: complete verified dependencies and identifiers are required")
	}
	e.SourcePluginID, e.SourcePluginDigest = exporter.Identity()
	e.TargetPluginID, e.TargetPluginDigest = importer.Identity()
	if e.SourcePluginID == "" || e.SourcePluginDigest == "" || e.TargetPluginID == "" || e.TargetPluginDigest == "" {
		return u.record(ctx, e, errors.New("migration artifact: verified plugin identities are required"))
	}
	a, err := exporter.Export(ctx, resource)
	if err != nil {
		return u.record(ctx, e, err)
	}
	e.Digest, e.Size, e.Format = a.Digest, a.Size, a.Format
	if a.Format != OCIArchiveFormat {
		return u.record(ctx, e, fmt.Errorf("migration artifact: unsupported format %q", a.Format))
	}
	if a.Size < 0 || a.Size > MaxPortableArtifactBytes || int64(len(a.Data)) != a.Size {
		return u.record(ctx, e, errors.New("migration artifact: size mismatch or limit exceeded"))
	}
	sum := sha256.Sum256(a.Data)
	if a.Digest != "sha256:"+hex.EncodeToString(sum[:]) {
		return u.record(ctx, e, errors.New("migration artifact: digest mismatch"))
	}
	if err := u.Verifier.VerifyArtifact(ctx, a.Format, bytes.NewReader(a.Data)); err != nil {
		return u.record(ctx, e, fmt.Errorf("migration artifact: structure verification: %w", err))
	}
	if err := u.Defense.ScanSentinels(a.Data); err != nil {
		return u.record(ctx, e, err)
	}
	classification, err := u.Policy.ClassifyArtifact(ctx, a.Format, bytes.NewReader(a.Data))
	if err != nil {
		return u.record(ctx, e, err)
	}
	if classification.Classification != "non-sensitive" || classification.AuthorityID == "" || !validSHA256Digest(classification.AuthorityDigest) || !validSHA256Digest(classification.PolicyDigest) {
		return u.record(ctx, e, errors.New("migration artifact: plaintext persistence requires proven non-sensitive classification"))
	}
	d, err := u.Store.Put(bytes.NewReader(a.Data))
	if err != nil {
		return u.record(ctx, e, err)
	}
	if d.Digest != a.Digest || d.Size != a.Size {
		return u.record(ctx, e, errors.New("migration artifact: CAS descriptor mismatch"))
	}
	if err := u.Store.Verify(a.Digest); err != nil {
		return u.record(ctx, e, fmt.Errorf("migration artifact: CAS verification: %w", err))
	}
	r, err := u.Store.Get(a.Digest)
	if err != nil {
		return u.record(ctx, e, err)
	}
	verifiedData, readErr := io.ReadAll(io.LimitReader(r, MaxPortableArtifactBytes+1))
	closeErr := r.Close()
	if readErr != nil || closeErr != nil || len(verifiedData) != len(a.Data) {
		return u.record(ctx, e, errors.Join(readErr, closeErr, errors.New("migration artifact: CAS read mismatch")))
	}
	verifiedSum := sha256.Sum256(verifiedData)
	if "sha256:"+hex.EncodeToString(verifiedSum[:]) != a.Digest {
		return u.record(ctx, e, errors.New("migration artifact: CAS bytes changed after verification"))
	}
	e.ArtifactVerified = true
	verifiedArtifact := VerifiedArtifact{Digest: a.Digest, Size: a.Size, Format: a.Format, Data: verifiedData}
	err = importer.Import(ctx, operationID, resource, verifiedArtifact)
	if err == nil {
		var observed ArtifactObservation
		observed, err = importer.ObserveArtifact(ctx, resource)
		if err == nil {
			e.ObservedAfterImport = true
			err = u.InstallationVerifier.VerifyInstallation(ctx, resource, verifiedArtifact, observed)
			if err == nil {
				e.Imported, e.Verified = true, true
			}
		}
	}
	return u.record(ctx, e, err)
}
func (u ArtifactTransfer) record(ctx context.Context, e ArtifactEvidence, operationErr error) (ArtifactEvidence, error) {
	if err := u.Sink.RecordArtifact(ctx, e); err != nil {
		return e, errors.Join(operationErr, err)
	}
	return e, operationErr
}
