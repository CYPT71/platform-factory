package layout

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/CYPT71/platform-factory/internal/atomicfile"
)

// SetReferences replaces the index's ref-name aliases while keeping each
// verified manifest/platform descriptor content-identical. One build can thus
// publish several image names/tags without rebuilding or duplicating blobs.
func SetReferences(layoutPath string, references []string) (Report, error) {
	if err := ValidateReferences(references); err != nil {
		return Report{}, err
	}
	if _, err := Verify(layoutPath); err != nil {
		return Report{}, err
	}
	indexPath := filepath.Join(layoutPath, "index.json")
	var current index
	if err := decodeFile(indexPath, &current); err != nil {
		return Report{}, err
	}
	return setReferences(layoutPath, current, references)
}

// ValidateReferences validates a requested alias set without reading or
// mutating a layout, so CLI preflight can fail before build output exists.
func ValidateReferences(references []string) error {
	if len(references) == 0 || len(references) > 128 {
		return errors.New("layout references require between 1 and 128 values")
	}
	seenReferences := map[string]bool{}
	for _, reference := range references {
		if err := validateLocalReference(reference); err != nil {
			return err
		}
		if seenReferences[reference] {
			return fmt.Errorf("duplicate image reference %q", reference)
		}
		seenReferences[reference] = true
	}
	return nil
}

func setReferences(layoutPath string, current index, references []string) (Report, error) {
	type manifestKey struct{ digest, platform string }
	bases := map[manifestKey]descriptor{}
	for _, manifest := range current.Manifests {
		platform := ""
		if manifest.Platform != nil {
			platform = manifest.Platform.OS + "/" + manifest.Platform.Architecture
		}
		key := manifestKey{digest: manifest.Digest, platform: platform}
		if _, exists := bases[key]; !exists {
			bases[key] = manifest
		}
	}
	if len(bases)*len(references) > 1024 {
		return Report{}, errors.New("layout reference expansion exceeds 1024 descriptors")
	}
	manifests := make([]descriptor, 0, len(bases)*len(references))
	for _, base := range bases {
		for _, reference := range references {
			clone := base
			clone.Annotations = cloneStringMap(base.Annotations)
			clone.Annotations["org.opencontainers.image.ref.name"] = reference
			manifests = append(manifests, clone)
		}
	}
	sort.Slice(manifests, func(i, j int) bool { return descriptorOrder(manifests[i]) < descriptorOrder(manifests[j]) })
	encoded, err := json.Marshal(index{SchemaVersion: 2, Manifests: manifests})
	if err != nil {
		return Report{}, err
	}
	if err := atomicfile.Write(layoutPath, "index.json", append(encoded, '\n'), 0o644, true); err != nil {
		return Report{}, err
	}
	report, err := Verify(layoutPath)
	if err != nil {
		return Report{}, fmt.Errorf("verify reference-expanded layout: %w", err)
	}
	return report, nil
}

func cloneStringMap(values map[string]string) map[string]string {
	result := make(map[string]string, len(values)+1)
	for key, value := range values {
		result[key] = value
	}
	return result
}

func validateLocalReference(reference string) error {
	if reference == "" || len(reference) > 512 || strings.Contains(reference, "@") {
		return fmt.Errorf("invalid image reference %q", reference)
	}
	for _, char := range reference {
		if unicode.IsSpace(char) || unicode.IsControl(char) {
			return fmt.Errorf("invalid image reference %q", reference)
		}
	}
	lastSlash := strings.LastIndexByte(reference, '/')
	lastColon := strings.LastIndexByte(reference, ':')
	if lastColon <= lastSlash || lastColon == 0 || lastColon == len(reference)-1 {
		return fmt.Errorf("image reference %q must include an explicit tag", reference)
	}
	name, tag := reference[:lastColon], reference[lastColon+1:]
	if strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") || strings.Contains(name, "//") || strings.Contains(name, "..") {
		return fmt.Errorf("invalid image name %q", name)
	}
	if len(tag) > 128 || !validTagFirst(tag[0]) {
		return fmt.Errorf("invalid image tag %q", tag)
	}
	for _, char := range tag[1:] {
		if !(char >= 'a' && char <= 'z') && !(char >= 'A' && char <= 'Z') && !(char >= '0' && char <= '9') && char != '_' && char != '.' && char != '-' {
			return fmt.Errorf("invalid image tag %q", tag)
		}
	}
	return nil
}

func validTagFirst(value byte) bool {
	return value == '_' || value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9'
}
