package cache

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"sort"
	"strings"

	api "github.com/CYPT71/platform-factory/internal/core"
)

// StageKeyInputs are the components combined into a stage cache key.
type StageKeyInputs struct {
	EngineVersion string
	Stage         api.Stage
	BaseDigest    string
	// InputDigests must be supplied in the same order as Stage.Inputs. The
	// reference/digest pairs are canonicalized together.
	InputDigests []string
	Platform     string
}

// StageKey derives the deterministic cache key for a stage:
//
//	SHA256(engine-version + canonical-stage-definition + pinned-base-digest
//	       + input-digests + platform + declared-environment
//	       + secret-identities-without-secret-values)
//
// Stage.ID and Stage.DependsOn are excluded: they describe graph placement,
// not content, so structurally identical stages share a cache entry
// regardless of where they sit in the pipeline. Stage.Env and
// Stage.Secrets already carry no secret values, only identities, so they
// need no additional stripping.
func StageKey(in StageKeyInputs) (string, error) {
	if in.EngineVersion == "" || strings.ContainsRune(in.EngineVersion, 0) {
		return "", errors.New("cache: engine version must be non-empty and NUL-free")
	}
	if _, err := parseDigest(in.BaseDigest); err != nil {
		return "", fmt.Errorf("cache: invalid base digest: %w", err)
	}
	if in.Platform != "linux/amd64" && in.Platform != "linux/arm64" {
		return "", errors.New("cache: platform must be linux/amd64 or linux/arm64")
	}
	if len(in.InputDigests) != len(in.Stage.Inputs) {
		return "", errors.New("cache: input digest count must match stage inputs")
	}
	type inputDigest struct {
		Reference api.ArtifactReference `json:"reference"`
		Digest    string                `json:"digest"`
	}
	inputs := make([]inputDigest, len(in.InputDigests))
	for index, digest := range in.InputDigests {
		if _, err := parseDigest(digest); err != nil {
			return "", fmt.Errorf("cache: invalid input digest %d: %w", index, err)
		}
		inputs[index] = inputDigest{Reference: in.Stage.Inputs[index], Digest: digest}
	}
	sort.Slice(inputs, func(i, j int) bool {
		if inputs[i].Reference.Stage == inputs[j].Reference.Stage {
			if inputs[i].Reference.Name == inputs[j].Reference.Name {
				return inputs[i].Digest < inputs[j].Digest
			}
			return inputs[i].Reference.Name < inputs[j].Reference.Name
		}
		return inputs[i].Reference.Stage < inputs[j].Reference.Stage
	})

	hasher := sha256.New()
	writePart(hasher, in.EngineVersion)
	writePart(hasher, in.BaseDigest)
	writePart(hasher, in.Platform)
	inputData, err := json.Marshal(inputs)
	if err != nil {
		return "", fmt.Errorf("cache: encode inputs: %w", err)
	}
	writePart(hasher, string(inputData))

	data, err := canonicalStageJSON(in.Stage)
	if err != nil {
		return "", err
	}
	writePart(hasher, string(data))

	return "sha256:" + hex.EncodeToString(hasher.Sum(nil)), nil
}

func canonicalStageJSON(stage api.Stage) ([]byte, error) {
	canonical := stage
	canonical.ID = ""
	canonical.DependsOn = nil
	canonical.Inputs = nil
	canonical.Mounts = append([]api.Mount(nil), stage.Mounts...)
	sort.Slice(canonical.Mounts, func(i, j int) bool {
		if canonical.Mounts[i].Target == canonical.Mounts[j].Target {
			return canonical.Mounts[i].Source < canonical.Mounts[j].Source
		}
		return canonical.Mounts[i].Target < canonical.Mounts[j].Target
	})
	canonical.Secrets = append([]api.SecretReference(nil), stage.Secrets...)
	sort.Slice(canonical.Secrets, func(i, j int) bool {
		if canonical.Secrets[i].Target == canonical.Secrets[j].Target {
			return canonical.Secrets[i].ID < canonical.Secrets[j].ID
		}
		return canonical.Secrets[i].Target < canonical.Secrets[j].Target
	})
	canonical.Caches = append([]api.CacheMount(nil), stage.Caches...)
	sort.Slice(canonical.Caches, func(i, j int) bool {
		if canonical.Caches[i].Target == canonical.Caches[j].Target {
			return canonical.Caches[i].ID < canonical.Caches[j].ID
		}
		return canonical.Caches[i].Target < canonical.Caches[j].Target
	})
	canonical.Outputs = append([]api.ArtifactDeclaration(nil), stage.Outputs...)
	sort.Slice(canonical.Outputs, func(i, j int) bool {
		return canonical.Outputs[i].Name < canonical.Outputs[j].Name
	})
	data, err := json.Marshal(canonical)
	if err != nil {
		return nil, fmt.Errorf("cache: encode stage: %w", err)
	}
	return data, nil
}

func writePart(h hash.Hash, value string) {
	writeUint(h, uint64(len(value)))
	_, _ = io.WriteString(h, value)
}

func writeUint(h hash.Hash, value uint64) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], value)
	_, _ = h.Write(length[:])
}
