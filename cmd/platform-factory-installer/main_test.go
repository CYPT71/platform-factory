package main

import (
	"context"
	"errors"
	tea "github.com/charmbracelet/bubbletea"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/CYPT71/platform-factory/internal/app/installer"
)

func TestEnsurePFAliasNoopWhenPlatformFactoryNotInstalled(t *testing.T) {
	prefix := t.TempDir()
	if err := ensurePFAlias(prefix, "linux"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(prefix, "pf")); !os.IsNotExist(err) {
		t.Fatalf("pf alias created without platform-factory: err=%v", err)
	}
}

func TestEnsurePFAliasPointsAtPlatformFactory(t *testing.T) {
	prefix := t.TempDir()
	suffix := installer.BinSuffix(runtime.GOOS)
	if err := os.WriteFile(filepath.Join(prefix, "platform-factory"+suffix), []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensurePFAlias(prefix, runtime.GOOS); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(prefix, "pf"+suffix)
	if runtime.GOOS == "windows" {
		data, err := os.ReadFile(alias)
		if err != nil || string(data) != "binary" {
			t.Fatalf("pf alias content=%q err=%v, want a copy of platform-factory", data, err)
		}
		return
	}
	linkTarget, err := os.Readlink(alias)
	if err != nil {
		t.Fatalf("pf is not a symlink: %v", err)
	}
	if linkTarget != "platform-factory"+suffix {
		t.Fatalf("pf symlink target=%q, want a relative %q", linkTarget, "platform-factory"+suffix)
	}
}

func TestEnsurePFAliasReplacesAStaleAlias(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need elevated privileges on Windows by default")
	}
	prefix := t.TempDir()
	if err := os.WriteFile(filepath.Join(prefix, "platform-factory"), []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	stalePath := filepath.Join(prefix, "pf")
	if err := os.WriteFile(stalePath, []byte("leftover from a previous install"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensurePFAlias(prefix, "linux"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Readlink(stalePath); err != nil {
		t.Fatalf("stale pf file was not replaced by a symlink: %v", err)
	}
}

func TestInstallerCancellationWaitsForBuildAndNeverReportsSuccess(t *testing.T) {
	steps := []stepView{{BuildStep: installer.BuildStep{Name: "first"}}, {BuildStep: installer.BuildStep{Name: "second"}}}
	m := newBuildModel(steps, "", "", "linux", "amd64", "test")
	defer m.cancel()
	started := make(chan struct{})
	m.runBuild = func(ctx context.Context, _ stepView) ([]byte, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	result := make(chan tea.Msg, 1)
	go func() { result <- m.runStep(0)() }()
	<-started
	updated, command := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = updated.(buildModel)
	if command != nil || !m.canceled {
		t.Fatal("must wait for active build instead of quitting immediately")
	}
	select {
	case message := <-result:
		updated, command = m.Update(message)
		m = updated.(buildModel)
	case <-time.After(2 * time.Second):
		t.Fatal("build did not observe cancellation")
	}
	if !errors.Is(m.resultError(), context.Canceled) || !m.done || m.current != 0 {
		t.Fatalf("canceled state=%+v", m)
	}
	if command == nil {
		t.Fatal("completion must now quit")
	}
	if _, ok := command().(tea.QuitMsg); !ok {
		t.Fatal("expected quit after cancellation completes")
	}
}

func TestInstallerBuildReplacesBinaryOnlyAfterSuccess(t *testing.T) {
	for _, scenario := range []string{"success", "failure", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			root, prefix := t.TempDir(), t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/fixture\n\ngo 1.26.10\n"), 0600); err != nil {
				t.Fatal(err)
			}
			source := "package main\nfunc main() {}\n"
			if scenario == "failure" {
				source = "not Go source"
			}
			if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(prefix, "fixture"+installer.BinSuffix(runtime.GOOS))
			if err := os.WriteFile(path, []byte("old binary"), 0755); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if scenario == "canceled" {
				cancel()
			}
			_, err := buildBinaryContext(ctx, root, prefix, runtime.GOOS, runtime.GOARCH, "test", stepView{BuildStep: installer.BuildStep{Name: "fixture", Pkg: "."}})
			if (err == nil) != (scenario == "success") {
				t.Fatalf("build err=%v", err)
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if scenario != "success" && string(data) != "old binary" {
				t.Fatal("failed/canceled build replaced working binary")
			}
			if scenario == "success" && string(data) == "old binary" {
				t.Fatal("successful build not installed")
			}
			entries, err := os.ReadDir(prefix)
			if err != nil || len(entries) != 1 {
				t.Fatalf("staging files remain: %v %v", entries, err)
			}
		})
	}
}
