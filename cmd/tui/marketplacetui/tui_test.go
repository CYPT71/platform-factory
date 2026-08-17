package marketplacetui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/CYPT71/platform-factory/internal/marketplace"
)

func sampleIndexForTUI() *marketplace.Index {
	idx := &marketplace.Index{}
	idx.Upsert(marketplace.PluginEntry{
		Name: "python", Description: "Python support", Author: "Platform Factory",
		Releases:      []marketplace.ReleaseEntry{{Version: "v1.0.0", Tag: "v1.0.0"}},
		LatestVersion: "v1.0.0",
	})
	idx.Upsert(marketplace.PluginEntry{
		Name: "zzz-analyzer", Description: "Static analysis", Author: "Someone",
		Releases:      []marketplace.ReleaseEntry{{Version: "v2.0.0", Tag: "v2.0.0"}},
		LatestVersion: "v2.0.0",
	})
	return idx
}

func newTestModel(t *testing.T, index *marketplace.Index, pluginsDir string) *model {
	t.Helper()
	search := textinput.New()
	search.Focus()
	m := &model{
		index:     index,
		manager:   &marketplace.Manager{Dir: pluginsDir, AllowUnsigned: true},
		search:    search,
		list:      list.New(nil, list.NewDefaultDelegate(), 80, 20),
		installed: map[string]marketplace.InstalledPlugin{},
		sortBy:    marketplace.SortRelevance,
		width:     100, height: 30,
	}
	m.list.SetShowTitle(false)
	m.list.SetFilteringEnabled(false)
	m.list.SetShowHelp(false)
	m.list.SetShowStatusBar(false)
	m.refresh()
	return m
}

func TestModelSearchFiltersList(t *testing.T) {
	m := newTestModel(t, sampleIndexForTUI(), t.TempDir())
	if got := len(m.list.Items()); got != 2 {
		t.Fatalf("initial list = %d items, want 2", got)
	}
	for _, r := range []rune("python") {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(*model)
	}
	items := m.list.Items()
	if len(items) != 1 {
		t.Fatalf("filtered list = %d items, want 1: %+v", len(items), items)
	}
	if got := items[0].(pluginItem).entry.Name; got != "python" {
		t.Fatalf("filtered item = %q, want python", got)
	}
}

func TestModelEnterOpensDetailAndEscGoesBack(t *testing.T) {
	m := newTestModel(t, sampleIndexForTUI(), t.TempDir())
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(*model)
	if m.state != viewDetail {
		t.Fatalf("state after enter = %v, want viewDetail", m.state)
	}
	if m.detail.Name == "" {
		t.Fatal("detail should be populated after enter")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(*model)
	if m.state != viewList {
		t.Fatalf("state after esc = %v, want viewList", m.state)
	}
}

func TestModelViewRendersWithoutPanicking(t *testing.T) {
	m := newTestModel(t, sampleIndexForTUI(), t.TempDir())
	if out := m.View(); out == "" {
		t.Fatal("list view should render something")
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(*model)
	if out := m.View(); out == "" {
		t.Fatal("detail view should render something")
	}
}

func TestModelEmptyAndNoResultsGuidance(t *testing.T) {
	m := newTestModel(t, &marketplace.Index{}, t.TempDir())
	if out := m.View(); !strings.Contains(out, "No plugins indexed yet") {
		t.Fatalf("empty index lacks onboarding: %q", out)
	}
	m = newTestModel(t, sampleIndexForTUI(), t.TempDir())
	m.search.SetValue("missing")
	m.refresh()
	if out := m.View(); !strings.Contains(out, "No plugins match") {
		t.Fatalf("empty search lacks recovery guidance: %q", out)
	}
}

func TestModelBlocksUntrustedInstallBeforeGit(t *testing.T) {
	m := newTestModel(t, sampleIndexForTUI(), t.TempDir())
	m.manager.AllowUnsigned = false
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(*model)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	m = updated.(*model)
	if cmd != nil || m.working || !m.statusIsError || !strings.Contains(m.status, "unverified") {
		t.Fatalf("untrusted install should be blocked with guidance: working=%v status=%q", m.working, m.status)
	}
}

// newTUIFixtureRepo builds a minimal real Git repository with one tagged
// release, so the install/update/remove flow below exercises real Git
// operations end to end, same as internal/marketplace's own tests.
func newTUIFixtureRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(context.Background(), "git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "--initial-branch=main")
	run("config", "user.email", "fixture@example.com")
	run("config", "user.name", "Fixture")
	run("config", "commit.gpgsign", "false")
	run("config", "tag.gpgsign", "false")
	manifest := "api_version: " + marketplace.ManifestAPIVersion + "\n" +
		"name: acme\nversion: v1.0.0\nentrypoint: plugin.py\n"
	if err := os.WriteFile(filepath.Join(dir, "plugin.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.py"), []byte("print('hi')"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-m", "v1.0.0")
	run("tag", "v1.0.0")
	return dir
}

func TestModelInstallAndRemoveFlow(t *testing.T) {
	repo := newTUIFixtureRepo(t)
	result, err := marketplace.SyncSource(context.Background(), repo, marketplace.PluginEntry{})
	if err != nil {
		t.Fatal(err)
	}
	index := &marketplace.Index{}
	index.Upsert(result.Plugin)

	pluginsDir := t.TempDir()
	m := newTestModel(t, index, pluginsDir)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(*model)
	if m.state != viewDetail {
		t.Fatal("expected to be in detail view")
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	m = updated.(*model)
	if !m.working || cmd == nil {
		t.Fatal("expected install to start a background command and set working=true")
	}
	msg := cmd()
	updated, _ = m.Update(msg)
	m = updated.(*model)
	if m.working {
		t.Fatal("expected working=false once the install result arrived")
	}
	if m.statusIsError {
		t.Fatalf("install should have succeeded, status=%q", m.status)
	}
	if _, ok := m.installed["acme"]; !ok {
		t.Fatalf("acme should be recorded as installed: %+v", m.installed)
	}
	if _, err := os.Stat(filepath.Join(pluginsDir, "acme", "plugin.py")); err != nil {
		t.Fatalf("installed entrypoint should exist on disk: %v", err)
	}

	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	m = updated.(*model)
	if !m.confirmRemove || cmd != nil {
		t.Fatal("first remove key must request confirmation without deleting")
	}
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m = updated.(*model)
	if !m.working || cmd == nil {
		t.Fatal("expected remove to start a background command")
	}
	msg = cmd()
	updated, _ = m.Update(msg)
	m = updated.(*model)
	if m.statusIsError {
		t.Fatalf("remove should have succeeded, status=%q", m.status)
	}
	if _, ok := m.installed["acme"]; ok {
		t.Fatal("acme should no longer be recorded as installed")
	}
	if _, err := os.Stat(filepath.Join(pluginsDir, "acme")); !os.IsNotExist(err) {
		t.Fatalf("installed directory should be gone, stat err=%v", err)
	}
}
