package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/CYPT71/platform-factory/internal/atomicfile"
	"github.com/CYPT71/platform-factory/internal/policy"
	"github.com/CYPT71/platform-factory/internal/project"
	"github.com/CYPT71/platform-factory/internal/signing"
	"github.com/CYPT71/platform-factory/internal/strictjson"
)

type projectBuildPolicy struct {
	configured bool
	rules      policy.Rules
	pins       policy.Evidence
	keyDir     string
}

func preflightProjectBuildPolicy(loaded project.Loaded) (projectBuildPolicy, error) {
	filename := filepath.Join(loaded.Root, "policies", "build.json")
	info, err := os.Lstat(filename)
	if errors.Is(err, os.ErrNotExist) {
		return projectBuildPolicy{}, nil
	}
	if err != nil {
		return projectBuildPolicy{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return projectBuildPolicy{}, errors.New("policies/build.json must be a regular non-symlink file")
	}
	var rules policy.Rules
	if err := strictjson.DecodeFile(filename, &rules); err != nil {
		return projectBuildPolicy{}, fmt.Errorf("decode policies/build.json: %w", err)
	}
	state := projectBuildPolicy{configured: true, rules: rules}
	if lock, lockErr := project.LoadLock(loaded.AdjacentLockPath()); lockErr == nil && lock.Version == project.CurrentLockVersion {
		state.pins.SourcesPinned = len(lock.Sources) > 0 || lock.GitCommit != ""
		state.pins.BasePinned = len(lock.Bases) > 0
		state.pins.ToolchainPinned = len(lock.Toolchains) > 0
	}
	// No plugin is a vacuously closed plugin set. A language plugin must
	// provide its verified digest to this policy surface before RequirePins
	// can pass; do not infer it from the plugin name.
	state.pins.PluginsPinned = !loaded.Config.LanguagePlugin
	if rules.RequirePins && (!state.pins.SourcesPinned || !state.pins.BasePinned || !state.pins.ToolchainPinned || !state.pins.PluginsPinned) {
		return projectBuildPolicy{}, errors.New("build policy requires source, base, toolchain and plugin pins not all present in verified inputs")
	}
	if rules.RequireHardening {
		return projectBuildPolicy{}, errors.New("build policy requires runtime hardening, but pf.yaml v1 cannot prove read-only rootfs and dropped capabilities")
	}
	if rules.RequireReproducible {
		return projectBuildPolicy{}, errors.New("build policy requires a reproducibility proof; project build currently performs one build only")
	}
	if rules.RequireSignature {
		state.keyDir = filepath.Join(loaded.Root, ".platform-factory", "keys")
		keyPath := filepath.Join(state.keyDir, "release.key")
		keyInfo, keyErr := os.Lstat(keyPath)
		if keyErr != nil || !keyInfo.Mode().IsRegular() || keyInfo.Mode()&os.ModeSymlink != 0 {
			return projectBuildPolicy{}, errors.New("build policy requires signature but .platform-factory/keys/release.key is missing or unsafe")
		}
		store, keyErr := signing.NewFileKeyStore(state.keyDir)
		if keyErr != nil {
			return projectBuildPolicy{}, keyErr
		}
		if _, keyErr := store.PublicKey("release"); keyErr != nil {
			return projectBuildPolicy{}, fmt.Errorf("validate release signing key: %w", keyErr)
		}
	}
	// Validate api_version even when every boolean is false.
	if _, err := policy.Evaluate(rules, policy.Evidence{SubjectDigest: "validation"}); err != nil {
		return projectBuildPolicy{}, err
	}
	return state, nil
}

func persistProjectBuildPolicy(state projectBuildPolicy, releaseDir, reportsDir, digest string) error {
	if !state.configured {
		return nil
	}
	evidence := state.pins
	evidence.SubjectDigest = digest
	evidence.SBOM = regularFile(filepath.Join(releaseDir, "sbom.json"))
	evidence.Provenance = regularFile(filepath.Join(releaseDir, "provenance.json"))
	evidence.Signature = regularFile(filepath.Join(releaseDir, "attestations", "provenance.dsse.json")) && regularFile(filepath.Join(releaseDir, "signatures", "subject.dsse.json"))
	decision, err := policy.Evaluate(state.rules, evidence)
	if err != nil {
		return err
	}
	if !decision.Allowed {
		return fmt.Errorf("build policy denied produced evidence: %s", strings.Join(decision.Reasons, "; "))
	}
	if err := atomicfile.WriteJSON(reportsDir, "policy-rules.json", state.rules); err != nil {
		return err
	}
	if err := atomicfile.WriteJSON(reportsDir, "evidence.json", evidence); err != nil {
		return err
	}
	return atomicfile.WriteJSON(reportsDir, "policy.json", map[string]any{"rules": state.rules, "evidence": evidence, "decision": decision})
}

func regularFile(filename string) bool {
	info, err := os.Lstat(filename)
	return err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0
}

type directBuildPolicy struct {
	configured bool
	rules      policy.Rules
}

func preflightDirectBuildPolicy(filename, distDir, reportsDir, signKeyDir, signKeyName string, rebuilds int, requireIdentical bool) (directBuildPolicy, error) {
	if filename == "" {
		return directBuildPolicy{}, nil
	}
	info, err := os.Lstat(filename)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return directBuildPolicy{}, errors.New("--policy must name a regular non-symlink file")
	}
	var rules policy.Rules
	if err := strictjson.DecodeFile(filename, &rules); err != nil {
		return directBuildPolicy{}, fmt.Errorf("decode build policy: %w", err)
	}
	if _, err := policy.Evaluate(rules, policy.Evidence{SubjectDigest: "validation"}); err != nil {
		return directBuildPolicy{}, err
	}
	if reportsDir == "" {
		return directBuildPolicy{}, errors.New("--reports is required with --policy so the decision is persisted")
	}
	if rules.RequirePins {
		return directBuildPolicy{}, errors.New("low-level build policy cannot prove source/base/toolchain/plugin pins; use a locked project build")
	}
	if rules.RequireHardening {
		return directBuildPolicy{}, errors.New("low-level build policy cannot prove read-only rootfs and dropped runtime capabilities")
	}
	if rules.RequireSBOM || rules.RequireProvenance {
		if distDir == "" {
			return directBuildPolicy{}, errors.New("build policy requires SBOM/provenance; use --dist")
		}
	}
	if rules.RequireSignature {
		if signKeyDir == "" || distDir == "" {
			return directBuildPolicy{}, errors.New("build policy requires signature; use --dist and --sign-key-dir")
		}
		keyPath := filepath.Join(signKeyDir, signKeyName+".key")
		keyInfo, keyErr := os.Lstat(keyPath)
		if keyErr != nil || !keyInfo.Mode().IsRegular() || keyInfo.Mode()&os.ModeSymlink != 0 {
			return directBuildPolicy{}, errors.New("configured signing key is missing or unsafe")
		}
		store, keyErr := signing.NewFileKeyStore(signKeyDir)
		if keyErr != nil {
			return directBuildPolicy{}, keyErr
		}
		if _, keyErr := store.PublicKey(signKeyName); keyErr != nil {
			return directBuildPolicy{}, fmt.Errorf("validate signing key: %w", keyErr)
		}
	}
	if rules.RequireReproducible && (rebuilds < 2 || !requireIdentical) {
		return directBuildPolicy{}, errors.New("build policy requires reproducibility; use --rebuild 2 or greater with --require-identical")
	}
	if rebuilds > 1 && (rules.RequireSBOM || rules.RequireProvenance || rules.RequireSignature) {
		return directBuildPolicy{}, errors.New("rebuild mode does not yet emit signed release evidence; split reproducibility verification and release build")
	}
	return directBuildPolicy{configured: true, rules: rules}, nil
}

func persistDirectBuildPolicy(state directBuildPolicy, distDir, reportsDir, digest string, reproducible bool) error {
	if !state.configured {
		return nil
	}
	evidence := policy.Evidence{
		SubjectDigest: digest,
		SBOM:          regularFile(filepath.Join(distDir, "sbom.json")),
		Provenance:    regularFile(filepath.Join(distDir, "provenance.json")),
		Signature: regularFile(filepath.Join(distDir, "attestations", "provenance.dsse.json")) &&
			regularFile(filepath.Join(distDir, "signatures", "subject.dsse.json")),
		Reproducible: reproducible,
	}
	decision, err := policy.Evaluate(state.rules, evidence)
	if err != nil {
		return err
	}
	if !decision.Allowed {
		return fmt.Errorf("build policy denied produced evidence: %s", strings.Join(decision.Reasons, "; "))
	}
	if err := atomicfile.WriteJSON(reportsDir, "policy-rules.json", state.rules); err != nil {
		return err
	}
	if err := atomicfile.WriteJSON(reportsDir, "evidence.json", evidence); err != nil {
		return err
	}
	return atomicfile.WriteJSON(reportsDir, "policy.json", map[string]any{"rules": state.rules, "evidence": evidence, "decision": decision})
}
