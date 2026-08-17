package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMarketplaceExperienceFromEmptyDirectory(t *testing.T) {
	repository := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		command := exec.Command("git", args...)
		command.Dir = repository
		command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	runGit("init", "--initial-branch=main")
	runGit("config", "user.email", "publisher@example.test")
	runGit("config", "user.name", "Example Publisher")
	runGit("config", "commit.gpgsign", "false")
	runGit("config", "tag.gpgsign", "false")

	writeRelease := func(version, body string) {
		t.Helper()
		manifest := "api_version: platform-factory.dev/marketplace-manifest/v1\n" +
			"name: hello-python\nversion: " + version + "\n" +
			"description: Hello World Python plugin\nauthor: Example Publisher\n" +
			"entrypoint: plugin.py\ntags: [python, hello]\n" +
			"compatibility: [\">=v1.0.0\"]\npermissions: {}\n"
		if err := os.WriteFile(filepath.Join(repository, "plugin.yaml"), []byte(manifest), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repository, "plugin.py"), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		runGit("add", "plugin.yaml", "plugin.py")
		runGit("commit", "-m", version)
		runGit("tag", version)
	}
	writeRelease("v1.0.0", "print('hello v1')\n")

	empty := t.TempDir()
	marketplaceDir := filepath.Join(t.TempDir(), "marketplace")
	t.Setenv("PLATFORM_FACTORY_MARKETPLACE_DIR", marketplaceDir)
	oldDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(empty); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldDirectory) })

	invoke := func(args ...string) (string, string) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		if code := run(append([]string{"marketplace"}, args...), &stdout, &stderr); code != 0 {
			t.Fatalf("pf marketplace %s exited %d\nstdout: %s\nstderr: %s", strings.Join(args, " "), code, stdout.String(), stderr.String())
		}
		return stdout.String(), stderr.String()
	}

	invoke("sources", "add", repository)
	invoke("sync")
	search, _ := invoke("search", "python", "--sort", "name")
	if !strings.Contains(search, "hello-python") || !strings.Contains(search, "v1.0.0") {
		t.Fatalf("search did not expose the indexed release:\n%s", search)
	}
	invoke("install", "--allow-unsigned", "hello-python@v1.0.0")
	if data, err := os.ReadFile(filepath.Join(marketplaceDir, "plugins", "hello-python", "plugin.py")); err != nil || !strings.Contains(string(data), "hello v1") {
		t.Fatalf("installed v1 entrypoint: %q, %v", data, err)
	}

	if err := os.Chdir(repository); err != nil {
		t.Fatal(err)
	}
	writeRelease("v1.1.0", "print('hello v1.1')\n")
	if err := os.Chdir(empty); err != nil {
		t.Fatal(err)
	}
	invoke("sync")
	listing, _ := invoke("list")
	if !strings.Contains(listing, "v1.0.0") || !strings.Contains(listing, "v1.1.0") || !strings.Contains(listing, "yes") {
		t.Fatalf("list did not expose the available update:\n%s", listing)
	}
	invoke("update", "--allow-unsigned", "hello-python@v1.1.0")
	invoke("remove", "hello-python")
	listing, _ = invoke("list")
	if !strings.Contains(listing, "no plugins installed") {
		t.Fatalf("remove did not restore empty state:\n%s", listing)
	}
}
