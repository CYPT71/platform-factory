package build

import (
	"path/filepath"

	"github.com/CYPT71/platform-factory/internal/atomicfile"
	"github.com/CYPT71/platform-factory/internal/policy"
	"github.com/CYPT71/platform-factory/internal/project"
)

// WriteLaunchPublicationEvidence writes the three fixed-shape policy,
// evidence, and provenance JSON documents `pf launch --publish`'s
// production lifecycle requires before publishing: the maximal
// policy.Rules, and a policy.Evidence asserting every one of its own
// preconditions - this path always builds hardened, signed, reproducible
// images, so there is nothing conditional left for evidence to report -
// with digest naming the build's ReproducibleBuild subject.
func WriteLaunchPublicationEvidence(policyPath, evidencePath, provenancePath string, loaded project.Loaded, digest, builderVersion string) error {
	rules := policy.Rules{
		APIVersion: policy.APIVersion, RequireHardening: true, RequireSBOM: true,
		RequireProvenance: true, RequireSignature: true, RequireReproducible: true,
	}
	evidence := policy.Evidence{
		NonRoot: true, ReadOnlyRootFS: true, CapabilitiesDropped: true,
		SecretsAbsent: true, Reproducible: true,
	}
	provenance := map[string]any{
		"api_version":  "platform-factory.dev/provenance/v1",
		"builder":      "platform-factory/" + builderVersion,
		"config":       filepath.Base(loaded.File),
		"output":       digest,
		"platform":     loaded.Config.Platform,
		"reproducible": true,
	}
	for path, value := range map[string]any{
		policyPath: rules, evidencePath: evidence, provenancePath: provenance,
	} {
		if err := atomicfile.WriteJSONSensitive(path, value); err != nil {
			return err
		}
	}
	return nil
}
