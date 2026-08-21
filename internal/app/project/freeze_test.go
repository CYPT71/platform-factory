package project

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWrapperCommand(t *testing.T) {
	got := wrapperCommand("dotnet")
	if runtime.GOOS == "windows" {
		if got != "dotnet.bat" {
			t.Fatalf("windows: wrapperCommand = %q", got)
		}
		return
	}
	if got != "dotnet" {
		t.Fatalf("non-windows: wrapperCommand = %q", got)
	}
}

func writeFreezeMarkerFile(t *testing.T, name, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func TestFreezeStepsCoverBuiltInAndCustomLanguages(t *testing.T) {
	tests := map[string]string{
		"go":       "go.mod",
		"node":     "package-lock.json",
		"python":   "requirements.txt",
		"java":     "pom.xml",
		"dotnet":   "project.csproj",
		"rust":     "Cargo.toml",
		"ruby":     "Gemfile",
		"php":      "composer.json",
		"compiled": "artifact",
	}
	for language, marker := range tests {
		t.Run(language, func(t *testing.T) {
			loaded := loadTestProject(t, "language: "+language+"\nartifact: app\n")
			writeFreezeMarkerFile(t, filepath.Join(loaded.Root, marker), "x", 0o644)
			if _, err := FreezeSteps(loaded); err != nil {
				t.Fatal(err)
			}
		})
	}
	loaded := loadTestProject(t, "language: custom\nartifact: app\nfreeze_command: [tool, lock]\n")
	steps, err := FreezeSteps(loaded)
	if err != nil || len(steps) != 1 || strings.Join(steps[0].Args, " ") != "tool lock" {
		t.Fatalf("steps=%+v err=%v", steps, err)
	}
}

func TestFreezeStepVariants(t *testing.T) {
	for _, test := range []struct {
		name, language, marker, first string
	}{
		{"node-without-lock", "node", "package.json", "npm install"},
		{"python-lock", "python", "requirements.lock", "python -m pip install"},
		{"maven-wrapper", "java", "mvnw", "./mvnw"},
		{"gradle-wrapper", "java", "gradlew", "./gradlew"},
	} {
		t.Run(test.name, func(t *testing.T) {
			loaded := loadTestProject(t, "language: "+test.language+"\nartifact: app\n")
			writeFreezeMarkerFile(t, filepath.Join(loaded.Root, test.marker), "x", 0o755)
			steps, err := FreezeSteps(loaded)
			if err != nil || len(steps) == 0 ||
				!strings.HasPrefix(strings.Join(steps[0].Args, " "), test.first) {
				t.Fatalf("steps=%v err=%v", steps, err)
			}
		})
	}
}

func TestFreezeStepsJavaAndCustomWithoutMarkersFail(t *testing.T) {
	javaLoaded := loadTestProject(t, "language: java\nartifact: app\n")
	if _, err := FreezeSteps(javaLoaded); err == nil {
		t.Fatal("expected java freeze without Maven/Gradle files to fail")
	}
	customLoaded := loadTestProject(t, "language: custom\nartifact: app\n")
	if _, err := FreezeSteps(customLoaded); err == nil {
		t.Fatal("expected custom language without freeze_command to fail")
	}
}
