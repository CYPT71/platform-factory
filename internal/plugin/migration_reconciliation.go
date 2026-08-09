package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	appmigration "github.com/CYPT71/secure-oci-base/internal/app/migration"
	domainmigration "github.com/CYPT71/secure-oci-base/internal/migration"
)

const migrationDifferenceDomain = "platform-factory/plugin-migration-difference/v1\x00"

// Difference measures normalized canonical state in the host. Plugin output is
// never trusted to declare convergence or progress, and no provider-native
// value is retained in the fingerprint.
func (*migrationTarget) Difference(ctx context.Context, desired domainmigration.Resource, observation appmigration.TargetObservation) (appmigration.TargetDifference, error) {
	if err := ctx.Err(); err != nil {
		return appmigration.TargetDifference{}, err
	}
	normalized, ok := observation.Native.(normalizedObservation)
	if !ok {
		return appmigration.TargetDifference{}, errors.New("migration difference: invalid host observation")
	}
	desired, err := canonicalMigrationResource(desired)
	if err != nil {
		return appmigration.TargetDifference{}, fmt.Errorf("migration difference: invalid desired resource: %w", err)
	}

	var observed *domainmigration.Resource
	distance := absentResourceDistance(desired)
	if normalized.found {
		canonical, err := canonicalMigrationResource(normalized.resource)
		if err != nil {
			return appmigration.TargetDifference{}, fmt.Errorf("migration difference: invalid observed resource: %w", err)
		}
		observed = &canonical
		distance = resourceDistance(desired, canonical)
	}
	fingerprint, err := migrationDifferenceFingerprint(desired, observed)
	if err != nil {
		return appmigration.TargetDifference{}, err
	}
	return appmigration.TargetDifference{Known: true, Distance: distance, Fingerprint: fingerprint}, nil
}

func canonicalMigrationResource(resource domainmigration.Resource) (domainmigration.Resource, error) {
	aggregate := domainmigration.Aggregate{Discovery: domainmigration.DiscoveryComplete, Resources: []domainmigration.Resource{resource}}
	if err := aggregate.Validate(); err != nil {
		return domainmigration.Resource{}, err
	}
	canonical := aggregate.Canonical().Resources[0]
	// nil and empty collections have the same migration meaning. Collapse both
	// so wire round-trips cannot manufacture a false difference.
	if len(canonical.Attributes) == 0 {
		canonical.Attributes = nil
	}
	if len(canonical.Requirements) == 0 {
		canonical.Requirements = nil
	}
	return canonical, nil
}

func absentResourceDistance(desired domainmigration.Resource) uint64 {
	// Five identity fields plus every desired property must be established.
	return uint64(5 + len(desired.Attributes) + len(desired.Requirements))
}

func resourceDistance(desired, observed domainmigration.Resource) uint64 {
	var distance uint64
	for _, equal := range []bool{
		desired.ID == observed.ID,
		desired.Kind == observed.Kind,
		desired.Origin.Source == observed.Origin.Source,
		desired.Origin.NativeType == observed.Origin.NativeType,
		desired.Origin.NativeID == observed.Origin.NativeID,
	} {
		if !equal {
			distance++
		}
	}
	distance += stringMapDistance(desired.Attributes, observed.Attributes)
	distance += requirementDistance(desired.Requirements, observed.Requirements)
	return distance
}

func stringMapDistance(desired, observed map[string]string) uint64 {
	var distance uint64
	for key, want := range desired {
		if got, ok := observed[key]; !ok || got != want {
			distance++
		}
	}
	for key := range observed {
		if _, ok := desired[key]; !ok {
			distance++
		}
	}
	return distance
}

func requirementDistance(desired, observed []domainmigration.Requirement) uint64 {
	want := make(map[string]struct{}, len(desired))
	got := make(map[string]struct{}, len(observed))
	for _, requirement := range desired {
		want[requirement.Capability+"\x00"+requirement.Version] = struct{}{}
	}
	for _, requirement := range observed {
		got[requirement.Capability+"\x00"+requirement.Version] = struct{}{}
	}
	var distance uint64
	for key := range want {
		if _, ok := got[key]; !ok {
			distance++
		}
	}
	for key := range got {
		if _, ok := want[key]; !ok {
			distance++
		}
	}
	return distance
}

func migrationDifferenceFingerprint(desired domainmigration.Resource, observed *domainmigration.Resource) (string, error) {
	encoded, err := json.Marshal(struct {
		Desired  domainmigration.Resource  `json:"desired"`
		Observed *domainmigration.Resource `json:"observed"`
	}{Desired: desired, Observed: observed})
	if err != nil {
		return "", fmt.Errorf("migration difference: canonical encoding: %w", err)
	}
	sum := sha256.Sum256(append([]byte(migrationDifferenceDomain), encoded...))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
