package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"sort"
	"strings"

	appmigration "github.com/CYPT71/platform-factory/internal/app/migration"
	"github.com/CYPT71/platform-factory/internal/core"
	domainmigration "github.com/CYPT71/platform-factory/internal/migration"
)

const migrationImportCapability = "migration.import"
const migrationExportCapability = "migration.export"
const migrationArtifactObserveCapability = "migration.artifact-observe"

type EffectiveArtifactGrant struct {
	PluginID, PluginDigest, Capability, ResourceID     string
	FilesystemScopes, NetworkScopes, CredentialScopes  []string
	EffectiveIsolation, EvidenceSource, EvidenceDigest string
}
type RuntimeGrantEvidence struct {
	FilesystemScopes, NetworkScopes, CredentialScopes  []string
	EffectiveIsolation, EvidenceSource, EvidenceDigest string
}

func newRuntimeGrantEvidence(isolation string, filesystem, network, credentials []string, source string) *RuntimeGrantEvidence {
	sum := sha256.Sum256([]byte(isolation + "\x00" + strings.Join(filesystem, "\x00") + "\x01" + strings.Join(network, "\x00") + "\x01" + strings.Join(credentials, "\x00") + "\x00" + source))
	return &RuntimeGrantEvidence{FilesystemScopes: append([]string(nil), filesystem...), NetworkScopes: append([]string(nil), network...), CredentialScopes: append([]string(nil), credentials...), EffectiveIsolation: isolation, EvidenceSource: source, EvidenceDigest: "sha256:" + hex.EncodeToString(sum[:])}
}

type MigrationArtifactFactory struct{ Registry *Registry }

func (f MigrationArtifactFactory) OpenExporter(ctx context.Context, resolved appmigration.ResolvedCapability, resourceID string) (appmigration.ArtifactExporter, error) {
	if err := f.check(ctx, resolved, migrationExportCapability, resourceID); err != nil {
		return nil, err
	}
	return &migrationArtifactRPC{registry: f.Registry, id: resolved.CandidateID, digest: resolved.Digest, resourceID: resourceID}, nil
}
func (f MigrationArtifactFactory) OpenImporter(ctx context.Context, resolved appmigration.ResolvedCapability, resourceID string) (appmigration.ArtifactImporter, error) {
	if err := f.check(ctx, resolved, migrationImportCapability, resourceID); err != nil {
		return nil, err
	}
	if _, err := f.Registry.verifiedClient(resolved.CandidateID, resolved.Digest, migrationArtifactObserveCapability); err != nil {
		return nil, err
	}
	return &migrationArtifactRPC{registry: f.Registry, id: resolved.CandidateID, digest: resolved.Digest, resourceID: resourceID}, nil
}
func (f MigrationArtifactFactory) check(ctx context.Context, r appmigration.ResolvedCapability, capability, resourceID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := f.Registry.verifiedClient(r.CandidateID, r.Digest, capability)
	if err != nil {
		return err
	}
	grant, err := f.Registry.effectiveArtifactGrant(ctx, r.CandidateID, r.Digest, capability, resourceID)
	if err != nil {
		return err
	}
	if grant.PluginID != r.CandidateID || grant.PluginDigest != r.Digest || grant.Capability != capability || grant.ResourceID != resourceID || grant.EffectiveIsolation == "" || grant.EvidenceSource == "" || !validWireDigest(grant.EvidenceDigest) {
		return errors.New("migration artifact: effective grant scope or evidence is invalid")
	}
	if len(grant.FilesystemScopes) > 0 || len(grant.NetworkScopes) > 0 || len(grant.CredentialScopes) > 0 {
		return errors.New("migration artifact: privileged scopes lack demonstrated enforcement")
	}
	return nil
}
func (r *Registry) effectiveArtifactGrant(ctx context.Context, id, digest, capability, resourceID string) (EffectiveArtifactGrant, error) {
	if resourceID == "" {
		return EffectiveArtifactGrant{}, errors.New("migration artifact: resource scope is required")
	}
	client, err := r.verifiedClient(id, digest, capability)
	if err != nil {
		return EffectiveArtifactGrant{}, err
	}
	effective := client.runtimeGrantEvidence
	if effective == nil || effective.EffectiveIsolation == "" || effective.EffectiveIsolation == "none" || !validWireDigest(effective.EvidenceDigest) {
		return EffectiveArtifactGrant{}, errors.New("migration artifact: runtime isolation was not observed")
	}
	r.mu.RLock()
	manifest := r.states[id].verifiedManifest
	r.mu.RUnlock()
	if !sameStrings(manifest.Permissions.Filesystem, effective.FilesystemScopes) || !sameStrings(manifest.Permissions.Network, effective.NetworkScopes) || !sameStrings(manifest.Permissions.Secrets, effective.CredentialScopes) {
		return EffectiveArtifactGrant{}, errors.New("migration artifact: requested permissions do not match applied runtime scopes")
	}
	sum := sha256.Sum256([]byte(id + "\x00" + digest + "\x00" + capability + "\x00" + resourceID + "\x00" + effective.EvidenceDigest))
	return EffectiveArtifactGrant{PluginID: id, PluginDigest: digest, Capability: capability, ResourceID: resourceID, FilesystemScopes: append([]string(nil), effective.FilesystemScopes...), NetworkScopes: append([]string(nil), effective.NetworkScopes...), CredentialScopes: append([]string(nil), effective.CredentialScopes...), EffectiveIsolation: effective.EffectiveIsolation, EvidenceSource: effective.EvidenceSource, EvidenceDigest: "sha256:" + hex.EncodeToString(sum[:])}, ctx.Err()
}
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	aa := append([]string(nil), a...)
	bb := append([]string(nil), b...)
	sort.Strings(aa)
	sort.Strings(bb)
	return reflect.DeepEqual(aa, bb)
}
func validWireDigest(v string) bool {
	if len(v) != 71 || v[:7] != "sha256:" {
		return false
	}
	_, err := hex.DecodeString(v[7:])
	return err == nil
}

type migrationArtifactRPC struct {
	registry   *Registry
	id, digest string
	resourceID string
}

func (a *migrationArtifactRPC) Identity() (string, string) { return a.id, a.digest }

func (a *migrationArtifactRPC) Export(ctx context.Context, r domainmigration.Resource) (appmigration.ExportedArtifact, error) {
	if r.ID != a.resourceID {
		return appmigration.ExportedArtifact{}, errors.New("migration artifact: resource exceeds effective grant scope")
	}
	c, err := a.registry.verifiedClient(a.id, a.digest, migrationExportCapability)
	if err != nil {
		return appmigration.ExportedArtifact{}, err
	}
	var out migrationExportResult
	if err := c.Call(ctx, "v1."+migrationExportCapability, migrationExportParams{Resource: resourceToWire(r)}, &out); err != nil {
		return appmigration.ExportedArtifact{}, err
	}
	x := out.Artifact
	return appmigration.ExportedArtifact{Digest: x.Digest, Size: x.Size, Format: x.Format, Data: append([]byte(nil), x.Data...)}, nil
}
func (a *migrationArtifactRPC) Import(ctx context.Context, operationID core.OperationID, r domainmigration.Resource, v appmigration.VerifiedArtifact) error {
	if r.ID != a.resourceID {
		return errors.New("migration artifact: resource exceeds effective grant scope")
	}
	c, err := a.registry.verifiedClient(a.id, a.digest, migrationImportCapability)
	if err != nil {
		return err
	}
	var out migrationImportResult
	err = c.CallWithIdempotency(ctx, operationID, "v1."+migrationImportCapability, migrationImportParams{Resource: resourceToWire(r), Artifact: migrationWireArtifact{Digest: v.Digest, Size: v.Size, Format: v.Format, Data: append([]byte(nil), v.Data...)}}, &out)
	if err == nil && !out.Accepted {
		return errors.New("migration artifact: import not accepted")
	}
	return err
}
func (a *migrationArtifactRPC) ObserveArtifact(ctx context.Context, r domainmigration.Resource) (appmigration.ArtifactObservation, error) {
	if r.ID != a.resourceID {
		return appmigration.ArtifactObservation{}, errors.New("migration artifact: resource exceeds effective grant scope")
	}
	c, err := a.registry.verifiedClient(a.id, a.digest, migrationArtifactObserveCapability)
	if err != nil {
		return appmigration.ArtifactObservation{}, err
	}
	var out migrationArtifactObserveResult
	params := migrationArtifactObserveParams{Resource: resourceToWire(r)}
	if err := c.Call(ctx, "v1."+migrationArtifactObserveCapability, params, &out); err != nil {
		return appmigration.ArtifactObservation{}, err
	}
	observation := appmigration.ArtifactObservation{Found: out.Found, NativeBinding: out.NativeBinding, Attestation: append([]byte(nil), out.Attestation...)}
	if out.Resource != nil {
		observation.Resource = resourceFromWire(*out.Resource)
	}
	return observation, nil
}
