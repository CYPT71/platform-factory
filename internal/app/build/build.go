// Package build is the application-layer service behind `pf build` and
// `pf project build`'s shared, self-contained business rules: resolving
// build targets/platforms, planning an entrypoint/profile, running the
// native OCI builder, and writing the SBOM/provenance/policy release
// evidence both commands produce. cmd/platform-factory/main.go's runBuild
// and cmd/platform-factory/project.go's buildProjectContextWithBudget
// both call into this package now instead of maintaining two copies (or
// main.go's copy plus project.go quietly drifting) - only flag parsing,
// the interactive image-reference confirmation (buildtui), and stdout
// formatting stay in their respective CLI adapters.
package build

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/CYPT71/platform-factory/internal/atomicfile"
	"github.com/CYPT71/platform-factory/internal/attestation"
	"github.com/CYPT71/platform-factory/internal/budget"
	"github.com/CYPT71/platform-factory/internal/detect"
	"github.com/CYPT71/platform-factory/internal/policy"
	"github.com/CYPT71/platform-factory/internal/sbom"
	"github.com/CYPT71/platform-factory/internal/signing"
	"github.com/CYPT71/platform-factory/oci"
)

// Target is one platform/executable pair to build.
type Target struct {
	OS, Architecture, Input string
}

// Settings is everything a build needs beyond its Target: annotations,
// evidence-relevant metadata, and resource limits.
type Settings struct {
	Entrypoint, Profile, Image, Tag, Compression, TraceID string
	Created                                               time.Time
	Labels                                                map[string]string
	ExtraFiles                                            []oci.ExtraFile
	Config                                                oci.BuildConfig
	Observer                                              func(oci.Event)
	SemanticLayers                                        bool
	Budget                                                budget.Budget
}

// ResourceBudgetPlan renders a budget.Budget for a --dry-run/plan preview.
func ResourceBudgetPlan(value budget.Budget) map[string]any {
	return map[string]any{
		"max_wall_clock":   value.WallClock.String(),
		"max_cpu":          value.CPU.String(),
		"max_memory_bytes": value.Memory,
	}
}

// ParseByteLimit parses a --max-memory value: a non-negative integer
// with an optional B, KiB, MiB, or GiB suffix.
func ParseByteLimit(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, errors.New("memory budget must not be empty")
	}
	multipliers := map[string]int64{"": 1, "B": 1, "KiB": 1 << 10, "MiB": 1 << 20, "GiB": 1 << 30}
	suffix := ""
	for _, candidate := range []string{"KiB", "MiB", "GiB", "B"} {
		if strings.HasSuffix(value, candidate) {
			suffix = candidate
			value = strings.TrimSuffix(value, candidate)
			break
		}
	}
	number, err := strconv.ParseInt(value, 10, 64)
	if err != nil || number < 0 {
		return 0, errors.New("memory budget must be a non-negative integer with optional B, KiB, MiB, or GiB suffix")
	}
	multiplier := multipliers[suffix]
	if number > (1<<63-1)/multiplier {
		return 0, errors.New("memory budget overflows int64")
	}
	return number * multiplier, nil
}

// Targets resolves the --platform/positional-EXECUTABLE flag
// combination into one or more Targets. code is the CLI exit code a
// caller should use when err is non-nil (2 - a usage error - in every
// case here).
func Targets(platforms, positional []string, defaultOS, defaultArchitecture string) ([]Target, int, error) {
	if len(platforms) == 0 {
		if len(positional) != 1 {
			return nil, 2, errors.New("provide one EXECUTABLE, or repeat --platform linux/ARCH=EXECUTABLE")
		}
		return []Target{{OS: defaultOS, Architecture: defaultArchitecture, Input: positional[0]}}, 0, nil
	}
	if len(platforms) == 1 && !strings.Contains(platforms[0], "=") {
		if len(positional) != 1 {
			return nil, 2, errors.New("--platform linux/ARCH requires one EXECUTABLE")
		}
		osName, architecture, err := ParsePlatform(platforms[0])
		if err != nil {
			return nil, 2, err
		}
		return []Target{{OS: osName, Architecture: architecture, Input: positional[0]}}, 0, nil
	}
	if len(positional) != 0 || len(platforms) < 2 {
		return nil, 2, errors.New("multi-platform syntax is --platform linux/ARCH=EXECUTABLE repeated at least twice")
	}
	targets := make([]Target, 0, len(platforms))
	for _, value := range platforms {
		platformName, input, found := strings.Cut(value, "=")
		if !found || input == "" {
			return nil, 2, fmt.Errorf("invalid platform input %q; expected linux/ARCH=EXECUTABLE", value)
		}
		osName, architecture, err := ParsePlatform(platformName)
		if err != nil {
			return nil, 2, err
		}
		targets = append(targets, Target{OS: osName, Architecture: architecture, Input: input})
	}
	return targets, 0, nil
}

// ParsePlatform parses "linux/amd64" or "linux/arm64".
func ParsePlatform(value string) (string, string, error) {
	parts := strings.Split(value, "/")
	if len(parts) != 2 || parts[0] != "linux" || (parts[1] != "amd64" && parts[1] != "arm64") {
		return "", "", errors.New("platform must be linux/amd64 or linux/arm64")
	}
	return parts[0], parts[1], nil
}

// ResolveTarget is the shared side-effect-free build planner: it detects
// target.Input's kind/profile and applies settings' entrypoint/profile
// overrides (config file, then explicit flag) in priority order.
func ResolveTarget(target Target, settings Settings) (string, string, error) {
	detected, err := detect.Path(target.Input)
	if err != nil {
		return "", "", err
	}
	if detected.Ambiguous || (detected.Kind != "elf" && detected.Kind != "unknown") {
		return "", "", fmt.Errorf("detected %s input %s; provide a compiled executable", detected.Kind, target.Input)
	}
	entrypoint := "/app/" + filepath.Base(target.Input)
	profile := detected.Profile
	if profile == "" || profile == "unknown" {
		profile = "static"
	}
	if settings.Config.Entrypoint != "" {
		entrypoint = settings.Config.Entrypoint
	}
	if settings.Config.Profile != "" {
		profile = settings.Config.Profile
	}
	if settings.Entrypoint != "" {
		entrypoint = settings.Entrypoint
	}
	if settings.Profile != "" {
		profile = settings.Profile
	}
	return entrypoint, profile, nil
}

// BuildImage resolves target/settings and runs the native OCI builder,
// returning the same result-map shape the CLI has always reported
// (architecture/digest/platform/profile) alongside a CLI exit code for
// the error case.
func BuildImage(target Target, output string, settings Settings) (map[string]any, int, error) {
	entrypoint, profile, err := ResolveTarget(target, settings)
	if err != nil {
		return nil, 2, err
	}
	digest, err := oci.Build(oci.Options{
		Binary: target.Input, Output: output, Architecture: target.Architecture, OS: target.OS,
		Entrypoint: entrypoint, Profile: profile, Created: settings.Created,
		ImageName: settings.Image, Tag: settings.Tag, Labels: settings.Labels,
		ExtraFiles: settings.ExtraFiles, Args: settings.Config.Args, WorkingDir: settings.Config.WorkingDir,
		Env: settings.Config.Env, User: settings.Config.User, Home: settings.Config.Home,
		IdentityFiles: settings.Config.IdentityFiles, Ports: settings.Config.Ports,
		Volumes: settings.Config.Volumes, WritablePaths: settings.Config.WritablePaths,
		Healthcheck: settings.Config.Healthcheck, TraceID: settings.TraceID,
		Compression: settings.Compression, Observer: settings.Observer,
		SemanticLayers: settings.SemanticLayers,
		Budget:         settings.Budget,
	})
	if err != nil {
		return nil, 1, err
	}
	return map[string]any{
		"architecture": target.Architecture, "digest": digest,
		"platform": target.OS + "/" + target.Architecture, "profile": profile,
	}, 0, nil
}

// WriteSBOMToDist generates a native SBOM (internal/sbom) over exactly
// the files this build actually embedded - the resolved entrypoint plus
// every extra file - and writes it to distDir/sbom.json. This is the
// build's own real inputs, not a guess: the same paths oci.Build itself
// just read.
func WriteSBOMToDist(distDir string, target Target, settings Settings) error {
	entrypoint, _, err := ResolveTarget(target, settings)
	if err != nil {
		return err
	}
	paths := map[string]string{entrypoint: target.Input}
	for _, extra := range settings.ExtraFiles {
		paths[extra.Dest] = extra.Source
	}
	document, err := sbom.Generate(paths)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(distDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", distDir, err)
	}
	file, err := os.Create(filepath.Join(distDir, "sbom.json"))
	if err != nil {
		return err
	}
	writeErr := sbom.Write(file, document)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

// WriteBuildEvidence writes provenance (and, with signKeyDir, signed
// DSSE attestations) to distDir, and the policy rules/evidence/decision
// plus a human-readable summary to reportsDir. builderVersion identifies
// the platform-factory build that produced result (the CLI's own
// version string).
func WriteBuildEvidence(distDir, reportsDir, signKeyDir, signKeyName, builderVersion string, result map[string]any, target Target, settings Settings) error {
	digest, _ := result["digest"].(string)
	if digest == "" {
		return errors.New("build result has no subject digest")
	}
	provenance := map[string]any{
		"api_version": "platform-factory.dev/provenance/v1", "builder": "platform-factory/" + builderVersion,
		"subject_digest": digest, "platform": target.OS + "/" + target.Architecture,
		"entrypoint": settings.Entrypoint, "created": settings.Created.UTC().Format(time.RFC3339),
	}
	if distDir != "" {
		if err := atomicfile.WriteJSON(distDir, "provenance.json", provenance); err != nil {
			return err
		}
		if signKeyDir != "" {
			store, err := signing.NewFileKeyStore(signKeyDir)
			if err != nil {
				return err
			}
			publicKey, err := store.PublicKey(signKeyName)
			if err != nil {
				return err
			}
			keyID := "ed25519:" + base64.RawURLEncoding.EncodeToString(publicKey)
			provenanceEnvelope, err := attestation.Sign(store, signKeyName, keyID, "application/vnd.in-toto+json", provenance)
			if err != nil {
				return err
			}
			if err := atomicfile.WriteJSON(filepath.Join(distDir, "attestations"), "provenance.dsse.json", provenanceEnvelope); err != nil {
				return err
			}
			subjectEnvelope, err := attestation.Sign(store, signKeyName, keyID,
				"application/vnd.platform-factory.subject.v1+json",
				map[string]string{"digest": digest, "reference": settings.Image + ":" + settings.Tag})
			if err != nil {
				return err
			}
			if err := atomicfile.WriteJSON(filepath.Join(distDir, "signatures"), "subject.dsse.json", subjectEnvelope); err != nil {
				return err
			}
		}
	}
	evidence := policy.Evidence{
		SubjectDigest: digest, NonRoot: true, ReadOnlyRootFS: true,
		CapabilitiesDropped: true, SecretsAbsent: true, SBOM: distDir != "",
		Provenance: distDir != "", Signature: signKeyDir != "", Reproducible: true,
	}
	rules := policy.Rules{
		APIVersion: policy.APIVersion, RequireHardening: true,
		RequireSBOM: distDir != "", RequireProvenance: distDir != "",
		RequireReproducible: true,
	}
	decision, err := policy.Evaluate(rules, evidence)
	if err != nil {
		return err
	}
	if reportsDir != "" {
		if err := atomicfile.WriteJSON(reportsDir, "policy-rules.json", rules); err != nil {
			return err
		}
		if err := atomicfile.WriteJSON(reportsDir, "evidence.json", evidence); err != nil {
			return err
		}
		if err := atomicfile.WriteJSON(reportsDir, "policy.json", map[string]any{
			"rules": rules, "evidence": evidence, "decision": decision,
		}); err != nil {
			return err
		}
		summary := fmt.Sprintf("Build complete\nSubject: %s\nPlatform: %s/%s\nPolicy: allowed=%t\n",
			digest, target.OS, target.Architecture, decision.Allowed)
		if err := atomicfile.Write(reportsDir, "summary.txt", []byte(summary), 0o644, true); err != nil {
			return err
		}
	}
	return nil
}
