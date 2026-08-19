package languageplugin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFreezeStepsCoverEveryLanguageWithFixtures(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "requirements.txt"), []byte("example==1.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, language := range []string{"go", "node", "python", "dotnet", "rust", "ruby", "php"} {
		steps, err := FreezeSteps(language, root)
		if err != nil {
			t.Fatalf("%s: %v", language, err)
		}
		if len(steps) == 0 {
			t.Fatalf("%s returned no steps", language)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "pom.xml"), []byte("<project/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := FreezeSteps("java", root); err != nil {
		t.Fatalf("java: %v", err)
	}
}

func TestFreezeStepsSelectsLockfilesAndRejectsUnsupportedProjects(t *testing.T) {
	root := t.TempDir()
	write := func(name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte("fixture"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("package-lock.json")
	steps, err := FreezeSteps("node", root)
	if err != nil || len(steps) != 1 {
		t.Fatalf("node steps=%+v err=%v", steps, err)
	}
	write("mvnw")
	steps, err = FreezeSteps("java", root)
	if err != nil || steps[0][0] != "./mvnw" {
		t.Fatalf("java steps=%+v err=%v", steps, err)
	}
	empty := t.TempDir()
	for _, language := range []string{"python", "java", "unknown"} {
		if _, err := FreezeSteps(language, empty); err == nil {
			t.Fatalf("%s unexpectedly supported", language)
		}
	}
}

func TestProfile(t *testing.T) {
	cases := map[string]string{
		"go": "static", "rust": "static", "node": "node", "typescript": "node",
		"dotnet": "dotnet", "csharp": "dotnet", "python": "python", "ruby": "ruby",
	}
	for language, want := range cases {
		if got := Profile(language); got != want {
			t.Fatalf("Profile(%q)=%q, want %q", language, got, want)
		}
	}
}
