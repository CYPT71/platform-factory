package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	api "github.com/CYPT71/secure-oci-base/internal/core"
)

// CanonicalJSON validates and serializes a pipeline with semantically
// unordered collections sorted. The result is suitable as a cache-key input,
// but does not include source bytes beyond their declared digests.
func CanonicalJSON(definition api.Pipeline) ([]byte, error) {
	if _, err := Analyze(definition); err != nil {
		return nil, err
	}
	normalized := definition
	normalized.RequiredCapabilities = sortedStrings(definition.RequiredCapabilities)
	if len(normalized.RequiredCapabilities) == 0 {
		normalized.RequiredCapabilities = nil
	}
	normalized.Inputs = append([]api.Input(nil), definition.Inputs...)
	sort.Slice(normalized.Inputs, func(i, j int) bool { return normalized.Inputs[i].ID < normalized.Inputs[j].ID })
	normalized.Outputs = append([]api.Output(nil), definition.Outputs...)
	sort.Slice(normalized.Outputs, func(i, j int) bool { return normalized.Outputs[i].Name < normalized.Outputs[j].Name })
	normalized.Stages = make([]api.Stage, len(definition.Stages))
	for index, stage := range definition.Stages {
		normalized.Stages[index] = normalizeStage(stage)
	}
	sort.Slice(normalized.Stages, func(i, j int) bool { return normalized.Stages[i].ID < normalized.Stages[j].ID })
	return json.Marshal(normalized)
}

// Fingerprint returns the SHA-256 of CanonicalJSON.
func Fingerprint(definition api.Pipeline) (string, error) {
	data, err := CanonicalJSON(definition)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func normalizeStage(stage api.Stage) api.Stage {
	normalized := stage
	if normalized.Network == "" {
		normalized.Network = api.NetworkNone
	}
	normalized.DependsOn = sortedStrings(stage.DependsOn)
	normalized.Mounts = append([]api.Mount(nil), stage.Mounts...)
	sort.Slice(normalized.Mounts, func(i, j int) bool {
		if normalized.Mounts[i].Target == normalized.Mounts[j].Target {
			return normalized.Mounts[i].Source < normalized.Mounts[j].Source
		}
		return normalized.Mounts[i].Target < normalized.Mounts[j].Target
	})
	normalized.Secrets = append([]api.SecretReference(nil), stage.Secrets...)
	sort.Slice(normalized.Secrets, func(i, j int) bool {
		if normalized.Secrets[i].Target == normalized.Secrets[j].Target {
			return normalized.Secrets[i].ID < normalized.Secrets[j].ID
		}
		return normalized.Secrets[i].Target < normalized.Secrets[j].Target
	})
	normalized.Caches = append([]api.CacheMount(nil), stage.Caches...)
	sort.Slice(normalized.Caches, func(i, j int) bool {
		if normalized.Caches[i].Target == normalized.Caches[j].Target {
			return normalized.Caches[i].ID < normalized.Caches[j].ID
		}
		return normalized.Caches[i].Target < normalized.Caches[j].Target
	})
	normalized.Inputs = append([]api.ArtifactReference(nil), stage.Inputs...)
	sort.Slice(normalized.Inputs, func(i, j int) bool {
		if normalized.Inputs[i].Stage == normalized.Inputs[j].Stage {
			return normalized.Inputs[i].Name < normalized.Inputs[j].Name
		}
		return normalized.Inputs[i].Stage < normalized.Inputs[j].Stage
	})
	normalized.Outputs = append([]api.ArtifactDeclaration(nil), stage.Outputs...)
	sort.Slice(normalized.Outputs, func(i, j int) bool { return normalized.Outputs[i].Name < normalized.Outputs[j].Name })
	normalized.Command.Args = append([]string(nil), stage.Command.Args...)
	if stage.Env != nil {
		normalized.Env = make(map[string]string, len(stage.Env))
		for key, value := range stage.Env {
			normalized.Env[key] = value
		}
	}
	return normalized
}

func sortedStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}
