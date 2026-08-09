package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CYPT71/secure-oci-base/internal/plugin"
	api "github.com/CYPT71/secure-oci-base/sdk/plugin"
)

type stubPlugin struct {
	hello  plugin.HelloResult
	detect api.DetectResult
	freeze api.FreezeResult
	notes  []string
	err    error
}

func (s *stubPlugin) Hello() plugin.HelloResult { return s.hello }
func (s *stubPlugin) HasCapability(capability string) bool {
	for _, declared := range s.hello.Capabilities {
		if declared == capability {
			return true
		}
	}
	return false
}
func (s *stubPlugin) Close() error { return nil }
func (s *stubPlugin) Call(_ context.Context, method string, _, result any) error {
	if s.err != nil {
		return s.err
	}
	var payload any
	switch method {
	case "v1.detect":
		payload = s.detect
	case "v1.freeze":
		payload = s.freeze
	case "v1.plan":
		payload = api.PlanResult{Notes: s.notes}
	default:
		return errors.New("unknown method")
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, result)
}

func TestPluginHostDetectFreezeAndNotes(t *testing.T) {
	host := &pluginHost{clients: []pluginClient{
		&stubPlugin{hello: plugin.HelloResult{Name: "broken", Capabilities: []string{"detect", "freeze"}}, err: errors.New("crash")},
		&stubPlugin{
			hello:  plugin.HelloResult{Name: "zig-adapter", Capabilities: []string{"detect", "freeze", "plan"}},
			detect: api.DetectResult{Kind: "zig", Profile: "static", Evidence: []string{"build.zig"}},
			freeze: api.FreezeResult{Steps: [][]string{{"zig", "build", "--fetch"}}},
			notes:  []string{"fetches dependencies with zig build"},
		},
	}}
	result, name, found := host.detect(context.Background(), ".")
	if !found || name != "zig-adapter" || result.Kind != "zig" {
		t.Fatalf("detect=%+v name=%s found=%v", result, name, found)
	}
	steps, name, err := host.freeze(context.Background(), "zig", ".")
	if err != nil || name != "zig-adapter" || len(steps) != 1 ||
		strings.Join(steps[0].args, " ") != "zig build --fetch" {
		t.Fatalf("steps=%+v name=%s err=%v", steps, name, err)
	}
	notes := host.planNotes(context.Background(), "zig", ".")
	if len(notes) != 1 || !strings.Contains(notes[0], "zig-adapter:") {
		t.Fatalf("notes=%v", notes)
	}
}

func TestPluginHostRejectsInvalidFreezeSteps(t *testing.T) {
	for name, freeze := range map[string]api.FreezeResult{
		"empty-argv": {Steps: [][]string{{}}},
		"nul-arg":    {Steps: [][]string{{"zig", "bad\x00arg"}}},
	} {
		t.Run(name, func(t *testing.T) {
			host := &pluginHost{clients: []pluginClient{&stubPlugin{
				hello:  plugin.HelloResult{Name: "bad", Capabilities: []string{"freeze"}},
				freeze: freeze,
			}}}
			if _, _, err := host.freeze(context.Background(), "zig", "."); err == nil {
				t.Fatal("invalid freeze steps accepted")
			}
		})
	}
	empty := &pluginHost{}
	if _, _, err := empty.freeze(context.Background(), "zig", "."); !errors.Is(err, errNoPluginFreeze) {
		t.Fatalf("err=%v", err)
	}
}

func TestResolveFreezeStepsFallsBackToPlugins(t *testing.T) {
	loaded := loadProjectTest(t, "language: zig\nartifact: app\n")
	host := &pluginHost{clients: []pluginClient{&stubPlugin{
		hello:  plugin.HelloResult{Name: "zig-adapter", Capabilities: []string{"freeze"}},
		freeze: api.FreezeResult{Steps: [][]string{{"zig", "build", "--fetch"}}},
	}}}
	steps, err := resolveFreezeSteps(loaded, host)
	if err != nil || len(steps) != 1 {
		t.Fatalf("steps=%+v err=%v", steps, err)
	}
	if _, err := resolveFreezeSteps(loaded, &pluginHost{}); err == nil ||
		!strings.Contains(err.Error(), "no built-in freeze adapter") {
		t.Fatalf("err=%v", err)
	}
	builtIn := loadProjectTest(t, "language: go\nartifact: app\n")
	steps, err = resolveFreezeSteps(builtIn, host)
	if err != nil || len(steps) != 2 || steps[0].args[0] != "go" {
		t.Fatalf("built-in adapter bypassed: steps=%+v err=%v", steps, err)
	}
}

func TestProjectPlanIncludesPluginNotes(t *testing.T) {
	loaded := loadProjectTest(t, "language: zig\nartifact: app\n")
	writeProjectTestFile(t, filepath.Join(loaded.Root, "app"), "binary", 0o755)
	host := &pluginHost{clients: []pluginClient{&stubPlugin{
		hello:  plugin.HelloResult{Name: "zig-adapter", Capabilities: []string{"freeze", "plan"}},
		freeze: api.FreezeResult{Steps: [][]string{{"zig", "build", "--fetch"}}},
		notes:  []string{"fetches dependencies with zig build"},
	}}}
	var stdout, stderr bytes.Buffer
	if code := planProject(loaded, host, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	for _, want := range []string{`"plugin_notes"`, "zig-adapter:", `"--fetch"`} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout missing %q: %s", want, stdout.String())
		}
	}
}
