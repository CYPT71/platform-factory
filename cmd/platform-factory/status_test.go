package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStatusGuidesAnEmptyDirectoryWithoutWriting(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := runStatus([]string{"--format", "json", root}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"initialized": false`) || !strings.Contains(stdout.String(), `"next_action": "pf init"`) {
		t.Fatalf("status=%s", stdout.String())
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("status mutated empty directory: entries=%v err=%v", entries, err)
	}
}

func TestStatusReportsInitializedProjectNextBuild(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "pf.yaml"), []byte(
		"version: 1\nlanguage: compiled\nprofile: static\nartifact: app\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := runStatus([]string{root}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Project     ready") || !strings.Contains(stdout.String(), "Next        pf build") {
		t.Fatalf("status=%s", stdout.String())
	}
}

func TestTopLevelInspectWithoutLayoutInspectsCurrentProject(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "pf.yaml"), []byte(
		"version: 1\nlanguage: compiled\nprofile: static\nartifact: app\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"inspect"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"initialized": true`) || !strings.Contains(stdout.String(), `"next_action": "pf build"`) {
		t.Fatalf("inspect=%s", stdout.String())
	}
}

func TestExplainGivesOneReasonedNextActionFromEmptyDirectory(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := runExplain([]string{root}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Next: pf init") || !strings.Contains(stdout.String(), "Why: the directory has no") {
		t.Fatalf("explain=%s", stdout.String())
	}
}
