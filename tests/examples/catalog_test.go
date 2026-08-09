package examples_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CYPT71/secure-oci-base/internal/policy"
	"go.yaml.in/yaml/v3"
)

// examplesDir is the source-tree examples/ directory, relative to this
// test's own directory, since the catalog this test audits lives there,
// not alongside this out-of-tree test.
const examplesDir = "../../examples"

func examplesPath(parts ...string) string {
	return filepath.Join(append([]string{examplesDir}, parts...)...)
}

func TestMajorFeatureCatalogIsComplete(t *testing.T) {
	required := []string{
		"README.md",
		"QUICKSTART.md",
		"platform-factory.json",
		"project-config/.config_image.yaml",
		"pipeline.json",
		"sdk/README.md", // covers Go, Python, JavaScript, TypeScript, and C# plugin examples
		"supply-chain/README.md",
		"microvm/README.md",
		"podman-microvm/README.md",
		"docker-microvm/README.md",
		"containerd-kubernetes/README.md",
		"containerd-kubernetes/runtimeclass.yaml",
		"kubevirt-microvm.json",
		"distributed/README.md",
		"reproducible-build/README.md",
		"observability/README.md",
	}
	for _, name := range required {
		info, err := os.Stat(examplesPath(filepath.FromSlash(name)))
		if err != nil || info.IsDir() || info.Size() == 0 {
			t.Errorf("major feature example %q is missing or empty: info=%v err=%v", name, info, err)
		}
	}
}

func TestMajorExamplesHaveExecutableStandaloneEntrypoints(t *testing.T) {
	directories := []string{
		"project-config", "supply-chain", "microvm", "podman-microvm",
		"docker-microvm", "containerd-kubernetes", "distributed",
		"reproducible-build", "observability", "sdk",
	}
	for _, directory := range directories {
		script := examplesPath(directory, "run.sh")
		info, err := os.Stat(script)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
			t.Errorf("%s must be a non-empty executable entrypoint: info=%v err=%v", script, info, err)
			continue
		}
		if output, err := exec.Command("bash", "-n", script).CombinedOutput(); err != nil {
			t.Errorf("%s is not valid shell: %v: %s", script, err, output)
		}
	}
}

func TestRuntimeClassExampleKeepsContainerAndMicroVMOptInExplicit(t *testing.T) {
	data, err := os.ReadFile(examplesPath("containerd-kubernetes", "runtimeclass.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	var runtimeClass, pod map[string]any
	if err := decoder.Decode(&runtimeClass); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(&pod); err != nil {
		t.Fatal(err)
	}
	if runtimeClass["kind"] != "RuntimeClass" || runtimeClass["handler"] != "platform-factory" {
		t.Fatalf("runtimeClass=%+v", runtimeClass)
	}
	spec, ok := pod["spec"].(map[string]any)
	if !ok || spec["runtimeClassName"] != "platform-factory" || spec["restartPolicy"] != "Never" {
		t.Fatalf("pod spec=%+v", pod["spec"])
	}
}

func TestJSONFixturesAreSyntacticallyValid(t *testing.T) {
	fixtures, err := filepath.Glob(examplesPath("**", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	rootFixtures, err := filepath.Glob(examplesPath("*.json"))
	if err != nil {
		t.Fatal(err)
	}
	fixtures = append(fixtures, rootFixtures...)
	for _, name := range fixtures {
		t.Run(strings.TrimPrefix(name, examplesDir+string(filepath.Separator)), func(t *testing.T) {
			data, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			if !json.Valid(data) {
				t.Fatal("invalid JSON")
			}
		})
	}
}

func TestSupplyChainPolicyAcceptsItsDemonstrationEvidence(t *testing.T) {
	var rules policy.Rules
	decodeFixture(t, examplesPath("supply-chain", "policy.json"), &rules)
	var evidence policy.Evidence
	decodeFixture(t, examplesPath("supply-chain", "evidence.json"), &evidence)
	decision, err := policy.Evaluate(rules, evidence)
	if err != nil || !decision.Allowed {
		t.Fatalf("decision=%+v err=%v", decision, err)
	}
}

func decodeFixture(t *testing.T, name string, target any) {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatal(err)
	}
}
