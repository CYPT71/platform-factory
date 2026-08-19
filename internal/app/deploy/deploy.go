// Package deploy is the application-layer service behind `pf deploy`'s
// self-contained business rules: evaluating a deployment policy against
// existing evidence, and parsing the --config/--secret-env/--volume
// Kubernetes extension flags into their typed forms. Most of `pf
// deploy`'s remaining logic is already factored into
// internal/publicationtarget (manifest generation) or is genuine
// cross-cutting CLI infrastructure shared with publish/rollback/microvm
// (the operation-journal claim sequence, kubectl invocation) that stays
// in cmd/platform-factory/lifecycle.go; only the two self-contained
// pieces live here, where they can be tested without going through the
// CLI at all.
package deploy

import (
	"fmt"
	"strings"

	"github.com/CYPT71/platform-factory/internal/policy"
	"github.com/CYPT71/platform-factory/internal/publicationtarget"
)

// EvaluatePolicy binds existing evidence to the deployed digest and
// evaluates it against the policy rules at policyPath/evidencePath.
func EvaluatePolicy(policyPath, evidencePath, digest string) (policy.Decision, error) {
	rules, evidence, err := policy.DecodeRulesAndEvidence(policyPath, evidencePath)
	if err != nil {
		return policy.Decision{}, err
	}
	evidence.SubjectDigest = digest
	return policy.Evaluate(rules, evidence)
}

// ParseKubernetesExtensions parses the repeatable --config KEY=VALUE,
// --secret-env ENV=SECRET/KEY, and --volume MOUNT_PATH=SIZE flag values
// into publicationtarget's typed forms.
func ParseKubernetesExtensions(configValues, secretValues, volumeValues []string) ([]publicationtarget.KeyValue, []publicationtarget.SecretEnvReference, []publicationtarget.PersistentVolume, error) {
	configs := make([]publicationtarget.KeyValue, 0, len(configValues))
	for _, value := range configValues {
		key, content, ok := strings.Cut(value, "=")
		if !ok {
			return nil, nil, nil, fmt.Errorf("--config must use KEY=VALUE")
		}
		configs = append(configs, publicationtarget.KeyValue{Key: key, Value: content})
	}
	secrets := make([]publicationtarget.SecretEnvReference, 0, len(secretValues))
	for _, value := range secretValues {
		env, reference, ok := strings.Cut(value, "=")
		secret, key, slash := strings.Cut(reference, "/")
		if !ok || !slash || strings.Contains(key, "/") {
			return nil, nil, nil, fmt.Errorf("--secret-env must use ENV=SECRET/KEY")
		}
		secrets = append(secrets, publicationtarget.SecretEnvReference{Env: env, Secret: secret, Key: key})
	}
	volumes := make([]publicationtarget.PersistentVolume, 0, len(volumeValues))
	for _, value := range volumeValues {
		mount, size, ok := strings.Cut(value, "=")
		if !ok {
			return nil, nil, nil, fmt.Errorf("--volume must use MOUNT_PATH=SIZE")
		}
		volumes = append(volumes, publicationtarget.PersistentVolume{MountPath: mount, Size: size})
	}
	return configs, secrets, volumes, nil
}
