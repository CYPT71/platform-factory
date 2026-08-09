package project

import (
	"testing"

	"github.com/CYPT71/secure-oci-base/internal/pipeline"
)

func TestLoadedPipelineTranslatesFreezeAndBuild(t *testing.T) {
	loaded := Loaded{
		Config: Config{
			Version: 1, Language: "custom", Artifact: "app",
			Image: "example/api", BuildCommand: []string{"make", "app"},
		},
		Root: t.TempDir(),
	}
	definition, err := loaded.Pipeline([][]string{{"deps", "freeze"}})
	if err != nil {
		t.Fatal(err)
	}
	graph, err := pipeline.Analyze(definition)
	if err != nil {
		t.Fatalf("translated pipeline is invalid: %v", err)
	}
	if definition.Name != "api" {
		t.Fatalf("name=%s", definition.Name)
	}
	// build must depend on the freeze stage.
	var build, freeze bool
	for _, stage := range definition.Stages {
		if stage.ID == "build" {
			build = true
			if len(stage.DependsOn) != 1 || stage.DependsOn[0] != "freeze-0" {
				t.Fatalf("build depends_on=%v", stage.DependsOn)
			}
		}
		if stage.ID == "freeze-0" {
			freeze = true
		}
	}
	if !build || !freeze {
		t.Fatalf("stages=%+v", definition.Stages)
	}
	if len(graph.Order) != 2 {
		t.Fatalf("order=%v", graph.Order)
	}
}

func TestLoadedPipelinePrebuiltArtifactIsExecutable(t *testing.T) {
	loaded := Loaded{
		Config: Config{Version: 1, Language: "compiled", Artifact: "app", Image: "svc"},
		Root:   t.TempDir(),
	}
	definition, err := loaded.Pipeline(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pipeline.Analyze(definition); err != nil {
		t.Fatalf("prebuilt pipeline is invalid: %v", err)
	}
	if len(definition.Stages) != 1 || definition.Stages[0].ID != "assemble" {
		t.Fatalf("stages=%+v", definition.Stages)
	}
}
