package migration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"testing"

	"github.com/CYPT71/secure-oci-base/internal/core"
	domainmigration "github.com/CYPT71/secure-oci-base/internal/migration"
	"github.com/CYPT71/secure-oci-base/internal/observability"
)

type artifactExporter struct {
	a   ExportedArtifact
	err error
}

func (artifactExporter) Identity() (string, string) {
	return "source", "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
}

func (x artifactExporter) Export(context.Context, domainmigration.Resource) (ExportedArtifact, error) {
	return x.a, x.err
}

type artifactImporter struct {
	calls     int
	converged bool
	explicit  bool
	artifact  VerifiedArtifact
}

func (*artifactImporter) Identity() (string, string) {
	return "target", "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
}

func (x *artifactImporter) Import(_ context.Context, _ core.OperationID, _ domainmigration.Resource, v VerifiedArtifact) error {
	x.calls++
	x.artifact = v
	return nil
}
func (x *artifactImporter) ObserveArtifact(_ context.Context, r domainmigration.Resource) (ArtifactObservation, error) {
	return ArtifactObservation{Found: true, Resource: r, NativeBinding: "native", Attestation: []byte("proof")}, nil
}

type installationVerifier struct{ fail bool }
type installedReadback struct{ data []byte }

func (r installedReadback) ReadInstalledArtifact(context.Context, domainmigration.Resource, string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(r.data)), nil
}

func (v installationVerifier) VerifyInstallation(_ context.Context, _ domainmigration.Resource, _ VerifiedArtifact, o ArtifactObservation) error {
	if v.fail || !o.Found || o.NativeBinding == "" || len(o.Attestation) == 0 {
		return errors.New("installation proof rejected")
	}
	return nil
}

type nonSensitiveClassifier struct{ classification string }

func (c nonSensitiveClassifier) ClassifyArtifact(_ context.Context, _ string, r io.Reader) (ArtifactClassificationEvidence, error) {
	data, _ := io.ReadAll(r)
	sum := sha256.Sum256(data)
	classification := c.classification
	if classification == "" {
		classification = "non-sensitive"
	}
	digest := "sha256:" + hex.EncodeToString(sum[:])
	return ArtifactClassificationEvidence{Classification: classification, AuthorityID: "test-policy-authority", AuthorityDigest: digest, PolicyDigest: digest}, nil
}
func transferForTest(store core.CacheStore, verifier ArtifactStructureVerifier, sink ArtifactEvidenceSink) ArtifactTransfer {
	return ArtifactTransfer{Store: store, Verifier: verifier, Sink: sink, Policy: nonSensitiveClassifier{}, Defense: SentinelSecretDefense{}, InstallationVerifier: installationVerifier{}}
}

type artifactVerifier struct{ err error }

func (x artifactVerifier) VerifyArtifact(context.Context, string, io.Reader) error { return x.err }

type artifactSink struct{ items []ArtifactEvidence }

func (x *artifactSink) RecordArtifact(_ context.Context, e ArtifactEvidence) error {
	x.items = append(x.items, e)
	return nil
}

type artifactStore struct {
	data              []byte
	verifyErr         error
	mutateAfterVerify bool
}

func (s *artifactStore) Put(r io.Reader) (core.Descriptor, error) {
	s.data, _ = io.ReadAll(r)
	h := sha256.Sum256(s.data)
	return core.Descriptor{Digest: "sha256:" + hex.EncodeToString(h[:]), Size: int64(len(s.data))}, nil
}
func (s *artifactStore) Get(string) (io.ReadCloser, error) {
	if s.mutateAfterVerify && len(s.data) > 0 {
		s.data[0] ^= 0xff
	}
	return io.NopCloser(bytes.NewReader(s.data)), nil
}
func (s *artifactStore) Verify(string) error                             { return s.verifyErr }
func (*artifactStore) StageKey(core.CacheStageKeyInputs) (string, error) { return "", nil }
func (*artifactStore) GetRecord(string, any) (bool, error)               { return false, nil }
func (*artifactStore) PutRecord(string, any) error                       { return nil }
func artifactPayload(data []byte) ExportedArtifact {
	h := sha256.Sum256(data)
	return ExportedArtifact{Digest: "sha256:" + hex.EncodeToString(h[:]), Size: int64(len(data)), Format: OCIArchiveFormat, Data: data}
}
func TestArtifactTransferVerifiesBeforeImport(t *testing.T) {
	store := &artifactStore{}
	sink := &artifactSink{}
	importer := &artifactImporter{}
	u := transferForTest(store, artifactVerifier{}, sink)
	ctx := observability.ContextWithTraceID(context.Background(), "trace")
	e, err := u.Transfer(ctx, "operation", domainmigration.Resource{ID: "r"}, artifactExporter{a: artifactPayload([]byte("archive"))}, importer)
	if err != nil || !e.ArtifactVerified || !e.Verified || !e.Imported || importer.calls != 1 {
		t.Fatalf("e=%+v calls=%d err=%v", e, importer.calls, err)
	}
}
func TestArtifactTransferNeverImportsBeforeEveryVerification(t *testing.T) {
	valid := artifactPayload([]byte("archive"))
	cases := []struct {
		name     string
		a        ExportedArtifact
		verifier error
		storeErr error
	}{{"digest", func() ExportedArtifact { x := valid; x.Digest = "sha256:" + string(make([]byte, 64)); return x }(), nil, nil}, {"size", func() ExportedArtifact { x := valid; x.Size++; return x }(), nil, nil}, {"format", func() ExportedArtifact { x := valid; x.Format = "zip"; return x }(), nil, nil}, {"structure", valid, errors.New("invalid"), nil}, {"cas verify", valid, nil, errors.New("corrupt")}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			imp := &artifactImporter{}
			_, err := transferForTest(&artifactStore{verifyErr: tc.storeErr}, artifactVerifier{tc.verifier}, &artifactSink{}).Transfer(observability.ContextWithTraceID(context.Background(), "trace"), "operation", domainmigration.Resource{ID: "r"}, artifactExporter{a: tc.a}, imp)
			if err == nil || imp.calls != 0 {
				t.Fatalf("err=%v calls=%d", err, imp.calls)
			}
		})
	}
}
func TestArtifactTransferCancellationPreventsExportAndImport(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	imp := &artifactImporter{}
	_, err := (ArtifactTransfer{}).Transfer(ctx, "operation", domainmigration.Resource{}, artifactExporter{}, imp)
	if !errors.Is(err, context.Canceled) || imp.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, imp.calls)
	}
}

func TestArtifactTransferDetectsCASChangeAfterVerify(t *testing.T) {
	valid := artifactPayload([]byte("archive"))
	imp := &artifactImporter{}
	_, err := transferForTest(&artifactStore{mutateAfterVerify: true}, artifactVerifier{}, &artifactSink{}).Transfer(observability.ContextWithTraceID(context.Background(), "trace"), "operation", domainmigration.Resource{ID: "r"}, artifactExporter{a: valid}, imp)
	if err == nil || imp.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, imp.calls)
	}
}
func TestArtifactTransferRejectsAcceptedWithoutObservedEffect(t *testing.T) {
	valid := artifactPayload([]byte("archive"))
	imp := &artifactImporter{explicit: true}
	u := transferForTest(&artifactStore{}, artifactVerifier{}, &artifactSink{})
	u.InstallationVerifier = installationVerifier{fail: true}
	e, err := u.Transfer(observability.ContextWithTraceID(context.Background(), "trace"), "operation", domainmigration.Resource{ID: "r"}, artifactExporter{a: valid}, imp)
	if err == nil || e.Imported || e.Verified || !e.ArtifactVerified {
		t.Fatalf("e=%+v err=%v", e, err)
	}
}
func TestArtifactTransferRejectsSecretBeforePersistence(t *testing.T) {
	a := artifactPayload([]byte("secret-sentinel"))
	store := &artifactStore{}
	imp := &artifactImporter{}
	_, err := transferForTest(store, artifactVerifier{}, &artifactSink{}).Transfer(observability.ContextWithTraceID(context.Background(), "trace"), "operation", domainmigration.Resource{ID: "r"}, artifactExporter{a: a}, imp)
	if err == nil || len(store.data) != 0 || imp.calls != 0 {
		t.Fatalf("persisted=%d calls=%d err=%v", len(store.data), imp.calls, err)
	}
}
func TestArtifactTransferRejectsUnknownSensitivityBeforePersistence(t *testing.T) {
	a := artifactPayload([]byte("archive"))
	store := &artifactStore{}
	imp := &artifactImporter{}
	u := transferForTest(store, artifactVerifier{}, &artifactSink{})
	u.Policy = nonSensitiveClassifier{classification: "unknown"}
	_, err := u.Transfer(observability.ContextWithTraceID(context.Background(), "trace"), "operation", domainmigration.Resource{ID: "r"}, artifactExporter{a: a}, imp)
	if err == nil || len(store.data) != 0 || imp.calls != 0 {
		t.Fatalf("persisted=%d calls=%d err=%v", len(store.data), imp.calls, err)
	}
}
func TestArtifactTransferNeverAutoClassifiesOpaqueCredentials(t *testing.T) {
	for name, payload := range map[string]string{"jwt": "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJhZG1pbiJ9.signature", "aws": "AKIAIOSFODNN7EXAMPLE", "random": "dGhpcy1pcy1vcGFxdWUtY3JlZGVudGlhbC1tYXRlcmlhbA=="} {
		t.Run(name, func(t *testing.T) {
			a := artifactPayload([]byte(payload))
			store := &artifactStore{}
			u := transferForTest(store, artifactVerifier{}, &artifactSink{})
			u.Policy = nonSensitiveClassifier{classification: "unknown"}
			_, err := u.Transfer(observability.ContextWithTraceID(context.Background(), "trace"), "operation", domainmigration.Resource{ID: "r"}, artifactExporter{a: a}, &artifactImporter{})
			if err == nil || len(store.data) != 0 {
				t.Fatalf("payload persisted: err=%v", err)
			}
		})
	}
}
func TestReadbackInstallationVerifierRejectsTargetEchoWithoutBytes(t *testing.T) {
	expected := artifactPayload([]byte("expected"))
	err := (ReadbackInstallationVerifier{Readback: installedReadback{data: []byte("different")}}).VerifyInstallation(context.Background(), domainmigration.Resource{ID: "r"}, VerifiedArtifact{Digest: expected.Digest, Size: expected.Size, Format: expected.Format}, ArtifactObservation{Found: true, NativeBinding: "native/r", Attestation: []byte(expected.Digest)})
	if err == nil {
		t.Fatal("target echo accepted without matching installed bytes")
	}
}
