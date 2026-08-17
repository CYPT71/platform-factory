package projectinit

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/CYPT71/platform-factory/internal/detect"
)

func TestEnrichOperationalHintsDoesNotClassifyLanguage(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\nEXPOSE 8080\n"), 0o600)
	os.WriteFile(filepath.Join(dir, ".env.example"), []byte("LOG_LEVEL=info\n"), 0o600)
	os.Mkdir(filepath.Join(dir, "data"), 0o700)

	pluginAnswer := ApplicationInspection{
		Detection:    detect.Result{Path: dir, Kind: "example-language", Profile: "example-runtime"},
		Artifact:     "app.example",
		Dependencies: DependencyState{Mode: "none", Reason: "plugin answer"},
	}
	got := EnrichOperationalHints(dir, pluginAnswer)
	if got.Detection.Kind != "example-language" || got.Artifact != "app.example" {
		t.Fatalf("host changed plugin-owned classification: %#v", got)
	}
	if !reflect.DeepEqual(got.Ports, []string{"8080:8080"}) || got.Environment["LOG_LEVEL"] != "info" || !reflect.DeepEqual(got.Storage, []string{"data"}) {
		t.Fatalf("inspection=%#v", got)
	}
}
