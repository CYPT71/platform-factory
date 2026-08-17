package layout

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CYPT71/platform-factory/internal/oci"
)

func buildDiffLayout(t *testing.T, mutate func(*oci.Options)) string {
	t.Helper()
	root := t.TempDir()
	binary := filepath.Join(root, "service")
	if err := os.WriteFile(binary, []byte("payload"), 0o755); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "layout")
	options := oci.Options{Binary: binary, Output: output, Architecture: "amd64",
		ImageName: "example/service", Tag: "v1"}
	if mutate != nil {
		mutate(&options)
	}
	if _, err := oci.Build(options); err != nil {
		t.Fatal(err)
	}
	return output
}

func TestDiffIdenticalLayouts(t *testing.T) {
	a := buildDiffLayout(t, nil)
	b := buildDiffLayout(t, nil)
	report, err := Diff(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Equal || len(report.Platforms) != 1 || !report.Platforms[0].Equal {
		t.Fatalf("report=%+v", report)
	}
}

func TestDiffExplainsConfigDivergence(t *testing.T) {
	a := buildDiffLayout(t, nil)
	b := buildDiffLayout(t, func(options *oci.Options) {
		options.Env = map[string]string{"MODE": "debug"}
	})
	report, err := Diff(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if report.Equal || len(report.Platforms) != 1 {
		t.Fatalf("report=%+v", report)
	}
	var envDiff *FieldDiff
	for index, field := range report.Platforms[0].Config {
		if field.Field == "env" {
			envDiff = &report.Platforms[0].Config[index]
		}
	}
	if envDiff == nil || !strings.Contains(envDiff.B, "MODE=debug") {
		t.Fatalf("config diff=%+v", report.Platforms[0].Config)
	}
}

func TestDiffExplainsLayerDivergence(t *testing.T) {
	extra := filepath.Join(t.TempDir(), "extra.txt")
	if err := os.WriteFile(extra, []byte("extra content"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := buildDiffLayout(t, nil)
	b := buildDiffLayout(t, func(options *oci.Options) {
		options.ExtraFiles = []oci.ExtraFile{{Dest: "/opt/extra.txt", Source: extra, Mode: 0o444}}
	})
	report, err := Diff(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if report.Equal {
		t.Fatalf("report=%+v", report)
	}
	layers := report.Platforms[0].Layers
	if len(layers) != 1 || layers[0].Added == 0 || layers[0].FirstDivergence == nil {
		t.Fatalf("layers=%+v", layers)
	}
	if !strings.Contains(layers[0].FirstDivergence.Path, "extra.txt") &&
		layers[0].FirstDivergence.Path != "opt/" && layers[0].FirstDivergence.Path != "opt" {
		t.Fatalf("first divergence=%+v", layers[0].FirstDivergence)
	}
	var text bytes.Buffer
	report.WriteText(&text)
	if !strings.Contains(text.String(), "divergent") || !strings.Contains(text.String(), "layer 0") {
		t.Fatalf("text=%s", text.String())
	}
}

func TestDiffExplainsCompressionOnlyDivergence(t *testing.T) {
	a := buildDiffLayout(t, func(options *oci.Options) { options.Compression = "best" })
	b := buildDiffLayout(t, func(options *oci.Options) { options.Compression = "fast" })
	report, err := Diff(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if report.Equal {
		t.Fatal("expected compressed bytes to differ")
	}
	layer := report.Platforms[0].Layers[0]
	if layer.Added+layer.Removed+layer.Changed != 0 || layer.FirstDivergence == nil ||
		!strings.Contains(layer.FirstDivergence.Detail, "compression") {
		t.Fatalf("layer=%+v", layer)
	}
}

func TestDiffExplainsChangedFileModeAndContent(t *testing.T) {
	extra := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(extra, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := buildDiffLayout(t, func(options *oci.Options) {
		options.ExtraFiles = []oci.ExtraFile{{Dest: "/opt/file.txt", Source: extra, Mode: 0o644}}
	})
	other := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(other, []byte("changed content longer"), 0o755); err != nil {
		t.Fatal(err)
	}
	b := buildDiffLayout(t, func(options *oci.Options) {
		options.ExtraFiles = []oci.ExtraFile{{Dest: "/opt/file.txt", Source: other, Mode: 0o755}}
	})
	report, err := Diff(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if report.Equal {
		t.Fatal("expected divergence")
	}
	layer := report.Platforms[0].Layers[0]
	if layer.Changed == 0 || layer.FirstDivergence == nil {
		t.Fatalf("layer=%+v", layer)
	}
	detail := layer.FirstDivergence.Detail
	if !strings.Contains(detail, "mode") && !strings.Contains(detail, "content") && !strings.Contains(detail, "size") {
		t.Fatalf("detail=%q", detail)
	}
}

func TestDiffReportsMissingTargets(t *testing.T) {
	a := buildNamedLayout(t, "example/service", "v1", "amd64", "payload")
	b := buildNamedLayout(t, "example/service", "v2", "amd64", "payload")
	report, err := Diff(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if report.Equal || len(report.Notes) != 2 {
		t.Fatalf("report=%+v", report)
	}
}

func TestDiffTextRenderingCoversAllSections(t *testing.T) {
	a := buildNamedLayout(t, "example/service", "v1", "amd64", "payload")
	b := buildDiffLayout(t, func(options *oci.Options) {
		options.ImageName = "example/service"
		options.Tag = "v1"
		options.Env = map[string]string{"MODE": "debug"}
		options.Labels = map[string]string{"team": "core"}
		options.Ports = []string{"8080/tcp"}
	})
	report, err := Diff(a, b)
	if err != nil {
		t.Fatal(err)
	}
	var text bytes.Buffer
	report.WriteText(&text)
	rendered := text.String()
	if !strings.Contains(rendered, "config") || !strings.Contains(rendered, "divergent") {
		t.Fatalf("text=%s", rendered)
	}
	// An identical pair renders the identical line.
	c := buildNamedLayout(t, "example/service", "v1", "amd64", "payload")
	same, err := Diff(a, c)
	if err != nil {
		t.Fatal(err)
	}
	var identical bytes.Buffer
	same.WriteText(&identical)
	if !strings.Contains(identical.String(), "identical") {
		t.Fatalf("identical text=%s", identical.String())
	}
}

func TestDiffRejectsInvalidLayouts(t *testing.T) {
	valid := buildDiffLayout(t, nil)
	if _, err := Diff(t.TempDir(), valid); err == nil || !strings.Contains(err.Error(), "layout A") {
		t.Fatalf("err=%v", err)
	}
	if _, err := Diff(valid, t.TempDir()); err == nil || !strings.Contains(err.Error(), "layout B") {
		t.Fatalf("err=%v", err)
	}
}
