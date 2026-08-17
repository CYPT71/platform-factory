package conformance

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"

	"github.com/CYPT71/platform-factory/internal/publicationtarget"
	"github.com/CYPT71/platform-factory/internal/registry"
)

//go:embed vectors-publication/*.json
var embeddedPublicationVectors embed.FS

func EmbeddedPublicationVectors() fs.FS { return embeddedPublicationVectors }

type PublicationVector struct {
	Name       string                            `json:"name"`
	Registry   *RegistryPublicationInput         `json:"registry,omitempty"`
	Kubernetes *publicationtarget.KubernetesSpec `json:"kubernetes,omitempty"`
	Expect     PublicationExpect                 `json:"expect"`
}

type RegistryPublicationInput struct {
	Reference string `json:"reference"`
}
type PublicationExpect struct {
	Valid      bool   `json:"valid"`
	Target     string `json:"target,omitempty"`
	Repository string `json:"repository,omitempty"`
	Tag        string `json:"tag,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
}

func RunPublication(fsys fs.FS) ([]Result, error) {
	names, err := fs.Glob(fsys, "vectors-publication/*.json")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil, errors.New("conformance: no publication vectors found")
	}
	results := make([]Result, 0, len(names))
	for _, name := range names {
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, err
		}
		var vector PublicationVector
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&vector); err != nil {
			return nil, fmt.Errorf("conformance: %s: %w", name, err)
		}
		results = append(results, evaluatePublication(vector))
	}
	return results, nil
}

func evaluatePublication(v PublicationVector) Result {
	actual := PublicationExpect{}
	if (v.Registry == nil) == (v.Kubernetes == nil) {
		return Result{Name: v.Name, Passed: false, Detail: "vector must select exactly one target"}
	}
	if v.Registry != nil {
		parsed, err := registry.ParseReference(v.Registry.Reference)
		actual.Valid = err == nil
		if err == nil {
			actual.Target = "registry"
			actual.Repository = parsed.Registry + "/" + parsed.Repository
			actual.Tag = parsed.Tag
		}
	} else {
		manifest, err := publicationtarget.KubernetesManifest(*v.Kubernetes)
		actual.Valid = err == nil
		if err == nil {
			sum := sha256.Sum256(manifest)
			actual.Target = "kubernetes"
			actual.SHA256 = "sha256:" + hex.EncodeToString(sum[:])
		}
	}
	expectedJSON, _ := json.Marshal(v.Expect)
	actualJSON, _ := json.Marshal(actual)
	if bytes.Equal(expectedJSON, actualJSON) {
		return Result{Name: v.Name, Passed: true}
	}
	return Result{Name: v.Name, Passed: false, Detail: fmt.Sprintf("expected %s, got %s", expectedJSON, actualJSON)}
}
