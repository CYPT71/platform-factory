package oci

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadBuildConfigStrict(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "platform-factory.json")
	data := `{
	  "entrypoint": "/app/service",
	  "args": ["serve"],
	  "working_dir": "/app",
	  "env": {"MODE": "production"},
	  "user": "10001:10001",
	  "home": "/home/nonroot",
	  "identity_files": true,
	  "system_files": {"ca_certificates": "/host/ca.pem"},
	  "ports": ["8080/tcp"],
	  "volumes": ["/data"],
	  "writable_paths": ["/tmp", "/data"],
	  "healthcheck": {"command": ["/app/service", "health"], "interval": "30s", "timeout": "2s", "retries": 3}
	}`
	if err := os.WriteFile(filename, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := LoadBuildConfig(filename)
	if err != nil {
		t.Fatal(err)
	}
	if config.User != "10001:10001" || config.Ports[0] != "8080/tcp" ||
		config.Healthcheck.Retries != 3 || config.SystemFiles.CACertificates != "/host/ca.pem" {
		t.Fatalf("config = %+v", config)
	}
}

func TestLoadBuildConfigRejectsUnknownAndUnsafeValues(t *testing.T) {
	for _, input := range []string{
		`{"entrypoint":"/app/service","unknown":true}`,
		`{"entrypoint":"relative"}`,
		`{"entrypoint":"/app/service","writable_paths":["/tmp/../etc"]}`,
		`{"entrypoint":"/app/service","ports":["99999/tcp"]}`,
		`{"entrypoint":"/app/service","healthcheck":{"command":[]}}`,
	} {
		filename := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(filename, []byte(input), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadBuildConfig(filename); err == nil {
			t.Fatalf("unsafe config accepted: %s", input)
		}
	}
}

func TestSortedEnvIsDeterministic(t *testing.T) {
	got := strings.Join(sortedEnv(map[string]string{"Z": "last", "A": "first"}), ",")
	if got != "A=first,Z=last" {
		t.Fatalf("sorted env = %q", got)
	}
}

func TestBuildConfigValidationEdges(t *testing.T) {
	for _, config := range []BuildConfig{
		{Entrypoint: "/app/service", User: "bad user"},
		{Entrypoint: "/app/service", Env: map[string]string{"BAD=KEY": "x"}},
		{Entrypoint: "/app/service", Volumes: []string{"/data", "/data"}},
		{Entrypoint: "/app/service", Healthcheck: &Healthcheck{Command: []string{"true"}, Interval: "never"}},
		{Entrypoint: "/app/service", Healthcheck: &Healthcheck{Command: []string{"true"}, Retries: -1}},
	} {
		if err := config.Validate(); err == nil {
			t.Fatalf("invalid config accepted: %+v", config)
		}
	}
}

func TestInterpretedProductionProfiles(t *testing.T) {
	tests := []BuildConfig{
		{Entrypoint: "/usr/local/bin/python3", Profile: "python", Args: []string{"/app/main.py"}},
		{Entrypoint: "/usr/local/bin/node", Profile: "node", Args: []string{"/app/server.js"}},
		{Entrypoint: "/usr/bin/java", Profile: "java", Args: []string{"-jar", "/app/service.jar"}},
		{Entrypoint: "/usr/bin/dotnet", Profile: "dotnet", Args: []string{"/app/service.dll"}},
		{Entrypoint: "/usr/local/bin/ruby", Profile: "ruby", Args: []string{"/app/main.rb"}},
		{Entrypoint: "/usr/local/bin/php", Profile: "php", Args: []string{"/app/index.php"}},
	}
	for _, config := range tests {
		if err := config.Validate(); err != nil {
			t.Fatalf("valid profile rejected: %+v: %v", config, err)
		}
	}
}

func TestInterpretedProfilesRequireMatchingRuntimeAndApplication(t *testing.T) {
	for _, config := range []BuildConfig{
		{Entrypoint: "/usr/bin/node", Profile: "python", Args: []string{"/app/main.py"}},
		{Entrypoint: "/usr/bin/java", Profile: "java"},
		{Entrypoint: "/usr/bin/python3", Profile: "ruby", Args: []string{"/app/main.rb"}},
		{Entrypoint: "/usr/bin/ruby", Profile: "php", Args: []string{"/app/index.php"}},
		{Entrypoint: "/usr/bin/php", Profile: "php"},
		{Entrypoint: "/app/service", Profile: "cobol"},
	} {
		if err := config.Validate(); err == nil {
			t.Fatalf("invalid interpreted profile accepted: %+v", config)
		}
	}
}
