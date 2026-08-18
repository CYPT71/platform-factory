// Package status is the application-layer service behind `pf status`
// and `pf explain`: inspecting a project directory's on-disk artifacts
// (build layout, release evidence, publication/deployment records) and
// deriving exactly one safe next command. cmd/platform-factory/status.go
// only parses flags and formats the result (text vs JSON, and the
// "Next/Why" pairing) - every filesystem inspection and state-machine
// step lives here, testable without going through the CLI at all.
package status

import (
	"os"
	"path/filepath"

	"github.com/CYPT71/platform-factory/internal/app/observe"
	"github.com/CYPT71/platform-factory/internal/layout"
	"github.com/CYPT71/platform-factory/internal/project"
	"github.com/CYPT71/platform-factory/internal/publicationtarget"
	"github.com/CYPT71/platform-factory/internal/strictjson"
)

// Status is a project's build/evidence/publication/deployment state,
// plus the one safe next command it implies - the exact shape `pf
// status --format json` outputs (cmd/platform-factory/status.go wraps
// it with a top-level api_version field, the same pattern used
// throughout this CLI's other JSON output).
type Status struct {
	Initialized        bool   `json:"initialized"`
	Config             string `json:"config,omitempty"`
	Built              bool   `json:"built"`
	BuildDigest        string `json:"build_digest,omitempty"`
	EvidenceComplete   bool   `json:"evidence_complete"`
	Published          bool   `json:"published"`
	PublishedReference string `json:"published_reference,omitempty"`
	Deployed           bool   `json:"deployed"`
	Deployment         string `json:"deployment,omitempty"`
	NextAction         string `json:"next_action"`
}

// Compute inspects the project rooted at start (or its nearest
// ancestor) and derives its Status. A directory with no discoverable
// project is not an error - it is reported as Status{NextAction: "pf
// init"}, the same as every other stage this checks progressively.
func Compute(start string) Status {
	result := Status{NextAction: "pf init"}
	loaded, err := project.Discover(start, "")
	if err != nil {
		return result
	}
	result.Initialized = true
	result.Config = loaded.File
	result.NextAction = "pf build"
	if report, verifyErr := layout.Verify(loaded.Output()); verifyErr == nil && len(report.Platforms) > 0 {
		result.Built = true
		result.BuildDigest = report.Platforms[0].Digest
		result.NextAction = "pf publish <registry/image:tag>"
	}
	releaseDir := filepath.Join(loaded.Root, ".platform-factory", "release")
	result.EvidenceComplete = regularFiles(releaseDir,
		"sbom.json", "provenance.json", "reports/build.json", "reports/policy.json",
		"reports/policy-rules.json", "reports/evidence.json", "reports/summary.txt")
	var published struct {
		APIVersion string `json:"api_version"`
		Digest     string `json:"digest"`
		Reference  string `json:"reference"`
		Repository string `json:"repository"`
		Scheme     string `json:"scheme"`
	}
	if strictjson.DecodeFile(filepath.Join(loaded.Root, ".platform-factory", "published.json"), &published) == nil &&
		published.APIVersion == "platform-factory.dev/publication/v1" &&
		published.Reference == published.Repository+"@"+published.Digest {
		result.Published = true
		result.PublishedReference = published.Reference
		result.NextAction = "pf deploy --dry-run"
	}
	var deployed observe.DeployedProject
	if strictjson.DecodeFile(filepath.Join(loaded.Root, ".platform-factory", "deployed.json"), &deployed) == nil &&
		deployed.APIVersion == "platform-factory.dev/deployment/v1" &&
		publicationtarget.ValidKubernetesName(deployed.Name) && publicationtarget.ValidKubernetesName(deployed.Namespace) &&
		(deployed.Workload == "job" || deployed.Workload == "service") && publicationtarget.ValidDigestReference(deployed.Image) {
		result.Deployed = true
		result.Deployment = deployed.Namespace + "/" + deployed.Name
		result.NextAction = "pf logs"
	}
	return result
}

// ExplainReason gives the one-sentence reason behind status.NextAction -
// `pf explain`'s whole contract, factored out of Compute since it's a
// pure function of a Status a caller may already have (`pf explain`
// itself gets one from a `pf status --format json` round trip today,
// but the reasoning itself doesn't need to).
func ExplainReason(s Status) string {
	switch {
	case s.Deployed:
		return "the immutable release is deployed; inspect its bounded workload logs next"
	case s.Published:
		return "the signed release is published by digest but has not been deployed"
	case s.Built && s.EvidenceComplete:
		return "the verified release bundle is complete and ready for a registry target"
	case s.Built:
		return "the OCI layout exists but its release evidence is incomplete"
	case s.Initialized:
		return "the project is initialized but has no verified OCI build"
	default:
		return "the directory has no Platform Factory project yet"
	}
}

func regularFiles(root string, names ...string) bool {
	for _, name := range names {
		info, err := os.Lstat(filepath.Join(root, name))
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return false
		}
	}
	return true
}
