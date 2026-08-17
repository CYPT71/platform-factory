package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPFInitHelloWorldExperience(t *testing.T) {
	root := t.TempDir()
	mainSource := `package main
import "fmt"
func main() { fmt.Println("hello world") }
`
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(mainSource), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "pf")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = "."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build pf: %v\n%s", err, output)
	}
	pluginDir := buildInitLanguagePlugin(t, "go")
	run := func(args ...string) string {
		t.Helper()
		command := exec.Command(binary, args...)
		command.Dir = root
		command.Env = append(os.Environ(), "PLATFORM_FACTORY_LANG_PLUGIN_DIR="+pluginDir)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("pf %s: %v\n%s", strings.Join(args, " "), err, output)
		}
		return string(output)
	}
	dry := run("init", "--dry-run", ".")
	if !strings.Contains(dry, "pf.yaml") || !strings.Contains(dry, "pf.lock") || !strings.Contains(dry, "language go") || !strings.Contains(dry, "dependencies none") || !strings.Contains(dry, "recommended runtime container") || strings.Contains(dry, "unknown connections") {
		t.Fatalf("unexpected review:\n%s", dry)
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 1 {
		t.Fatalf("dry-run mutated project: entries=%v err=%v", entries, err)
	}
	run("init", "--yes", ".")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	wantEntries := []string{".git", ".gitignore", ".pf", "deploy", "dist", "main.go", "pf.lock", "pf.yaml", "policies", "reports"}
	if got := entryNames(entries); strings.Join(got, "\x00") != strings.Join(wantEntries, "\x00") {
		t.Fatalf("init scaffold=%v want=%v", got, wantEntries)
	}
	config, err := os.ReadFile(filepath.Join(root, "pf.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"language: go", "artifact: \"app\"", "- \"main.go\"", "mode: none", "isolation: container"} {
		if !strings.Contains(string(config), want) {
			t.Fatalf("pf.yaml missing %q:\n%s", want, config)
		}
	}
	lock, err := os.ReadFile(filepath.Join(root, "pf.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(lock)) != "{\n  \"version\": 1\n}" {
		t.Fatalf("non-minimal pf.lock:\n%s", lock)
	}
	plan := run("build", "--dry-run")
	if !strings.Contains(plan, `"valid": true`) || !strings.Contains(plan, "main.go") {
		t.Fatalf("invalid build handoff:\n%s", plan)
	}
	run("build")
	if info, err := os.Stat(filepath.Join(root, "app")); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("build did not produce app: %v", err)
	}
}

func TestJuniorDeploysHelloWorldToRealLocalDocker(t *testing.T) {
	testJuniorDeploysHelloWorldToRealLocalRuntime(t, "docker")
}

func TestJuniorDeploysHelloWorldToRealLocalPodman(t *testing.T) {
	testJuniorDeploysHelloWorldToRealLocalRuntime(t, "podman")
}

func testJuniorDeploysHelloWorldToRealLocalRuntime(t *testing.T, engine string) {
	t.Helper()
	if _, err := exec.LookPath(engine); err != nil {
		if os.Getenv("PF_REQUIRE_REAL_RUNTIME") == "1" {
			t.Fatalf("%s is required for this acceptance test: %v", engine, err)
		}
		t.Skipf("%s unavailable: %v", engine, err)
	}
	if output, err := exec.Command(engine, "info").CombinedOutput(); err != nil {
		if os.Getenv("PF_REQUIRE_REAL_RUNTIME") == "1" {
			t.Fatalf("%s daemon is required for this acceptance test: %v: %s", engine, err, output)
		}
		t.Skipf("%s daemon unavailable: %v: %s", engine, err, output)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nimport \"fmt\"\nfunc main(){fmt.Println(\"hello from pf junior\")}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pfBinary := filepath.Join(t.TempDir(), "pf")
	if runtime.GOOS == "windows" {
		pfBinary += ".exe"
	}
	build := exec.Command("go", "build", "-o", pfBinary, ".")
	build.Dir = "."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build pf: %v\n%s", err, output)
	}
	pluginDir := buildInitLanguagePlugin(t, "go")
	environment := append(os.Environ(), "PLATFORM_FACTORY_LANG_PLUGIN_DIR="+pluginDir, "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	runPF := func(args ...string) string {
		t.Helper()
		command := exec.Command(pfBinary, args...)
		command.Dir = root
		command.Env = environment
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("pf %s: %v\n%s", strings.Join(args, " "), err, output)
		}
		return string(output)
	}
	runPF("init", "--yes", "--engine", engine, ".")
	output := runPF("launch")
	if !strings.Contains(output, "hello from pf junior") {
		t.Fatalf("container output=%s", output)
	}
	if _, err := os.Stat(filepath.Join(root, ".platform-factory", "image", "index.json")); err != nil {
		t.Fatalf("launch did not retain the verified local OCI image: %v", err)
	}
	imageName := filepath.Base(root) + ":latest"
	t.Cleanup(func() { _ = exec.Command(engine, "image", "rm", imageName).Run() })
}

func TestPFInitPythonNodeAndDotnetExperience(t *testing.T) {
	pfBinary := filepath.Join(t.TempDir(), "pf")
	if runtime.GOOS == "windows" {
		pfBinary += ".exe"
	}
	build := exec.Command("go", "build", "-o", pfBinary, ".")
	build.Dir = "."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build pf: %v\n%s", err, output)
	}
	tests := []struct {
		name                           string
		files                          map[string]string
		language, artifact, dependency string
		execute                        []string
		want                           string
	}{
		{name: "python", files: map[string]string{"app.py": "print('hello from python')\n"}, language: "python", artifact: "app.py", dependency: "none", execute: []string{"python3", "app.py"}, want: "hello from python"},
		{name: "node", files: map[string]string{"index.js": "console.log('hello from node')\n"}, language: "node", artifact: "index.js", dependency: "none", execute: []string{"node", "index.js"}, want: "hello from node"},
		{name: "dotnet", files: map[string]string{"Hello.csproj": "<Project Sdk=\"Microsoft.NET.Sdk\"><PropertyGroup><OutputType>Exe</OutputType><TargetFramework>net10.0</TargetFramework><ImplicitUsings>enable</ImplicitUsings></PropertyGroup></Project>\n", "Program.cs": "Console.WriteLine(\"hello from dotnet\");\n"}, language: "dotnet", artifact: ".platform-factory/build/Hello.dll", dependency: "manifest", execute: []string{"dotnet", "run", "--project", "Hello.csproj"}, want: "hello from dotnet"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pluginDir := buildInitLanguagePlugin(t, test.language)
			for _, tool := range []string{test.execute[0]} {
				if _, err := exec.LookPath(tool); err != nil {
					t.Skipf("%s capability unavailable: %v", tool, err)
				}
			}
			root := t.TempDir()
			for name, content := range test.files {
				if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			runPF := func(args ...string) string {
				t.Helper()
				command := exec.Command(pfBinary, args...)
				command.Dir = root
				command.Env = append(os.Environ(), "PLATFORM_FACTORY_LANG_PLUGIN_DIR="+pluginDir)
				output, err := command.CombinedOutput()
				if err != nil {
					t.Fatalf("pf %s: %v\n%s", strings.Join(args, " "), err, output)
				}
				return string(output)
			}
			review := runPF("init", "--dry-run", ".")
			for _, want := range []string{"language " + test.language, "dependencies " + test.dependency, "pf.yaml", "pf.lock"} {
				if !strings.Contains(review, want) {
					t.Fatalf("review missing %q:\n%s", want, review)
				}
			}
			runPF("init", "--yes", ".")
			config, err := os.ReadFile(filepath.Join(root, "pf.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"language: " + test.language, "artifact: \"" + test.artifact + "\"", "mode: " + test.dependency} {
				if !strings.Contains(string(config), want) {
					t.Fatalf("pf.yaml missing %q:\n%s", want, config)
				}
			}
			command := exec.Command(pfBinary, "build", "--dry-run")
			command.Dir = root
			command.Env = append(os.Environ(), "PLATFORM_FACTORY_LANG_PLUGIN_DIR="+pluginDir)
			plan, buildErr := command.CombinedOutput()
			if buildErr == nil || !strings.Contains(string(plan), `"valid": false`) || !strings.Contains(string(plan), "pf.yaml has no runtime field set") {
				t.Fatalf("interpreted build preflight must fail safely: err=%v\n%s", buildErr, plan)
			}
			if test.language == "python" {
				buildAttempt := exec.Command(pfBinary, "build")
				buildAttempt.Dir = root
				buildAttempt.Env = append(os.Environ(), "PLATFORM_FACTORY_LANG_PLUGIN_DIR="+pluginDir)
				buildOutput, err := buildAttempt.CombinedOutput()
				if err == nil || !strings.Contains(string(buildOutput), "capability preflight failed") || !strings.Contains(string(buildOutput), "pf doctor") {
					t.Fatalf("real build must stop at the same actionable preflight: err=%v\n%s", err, buildOutput)
				}
				publish := exec.Command(pfBinary, "publish", "--dry-run", "--allow-incomplete-evidence", "registry.example/hello:v1")
				publish.Dir = root
				publish.Env = append(os.Environ(), "PLATFORM_FACTORY_LANG_PLUGIN_DIR="+pluginDir)
				publishOutput, err := publish.CombinedOutput()
				if err == nil || !strings.Contains(string(publishOutput), ".platform-factory/image") {
					t.Fatalf("publish must discover and report the missing project layout: err=%v\n%s", err, publishOutput)
				}
				deploy := exec.Command(pfBinary, "deploy", "--dry-run", "registry.example/hello@sha256:"+strings.Repeat("a", 64))
				deploy.Dir = root
				deploy.Env = append(os.Environ(), "PLATFORM_FACTORY_LANG_PLUGIN_DIR="+pluginDir)
				manifest, err := deploy.CombinedOutput()
				if err != nil || !strings.Contains(string(manifest), `"kind": "Job"`) || strings.Contains(string(manifest), "containerPort") {
					t.Fatalf("python deployment review must be a no-port Job: err=%v\n%s", err, manifest)
				}
			}
			command = exec.Command(test.execute[0], test.execute[1:]...)
			command.Dir = root
			output, err := command.CombinedOutput()
			if err != nil || !strings.Contains(string(output), test.want) {
				t.Fatalf("run app: %v\n%s", err, output)
			}
		})
	}
}

func buildInitLanguagePlugin(t *testing.T, language string) string {
	t.Helper()
	dir := t.TempDir()
	name := "platform-factory-lang-" + language
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	command := exec.Command("go", "build", "-o", filepath.Join(dir, name), ".")
	command.Dir = filepath.Join("..", "..", "plugins", "lang-"+language)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build %s plugin: %v\n%s", language, err, output)
	}
	return dir
}

func entryNames(entries []os.DirEntry) []string {
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name()
	}
	return names
}
