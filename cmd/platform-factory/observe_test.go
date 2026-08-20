package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CYPT71/platform-factory/internal/atomicfile"
	api "github.com/CYPT71/platform-factory/sdk/plugin"
)

// TestDispatchObservationSelectsLogsOrEvents drives dispatchObservation
// directly against a stub deployment plugin (the same separation
// TestDeployToClusterAppliesThenObservesPerWorkload/
// TestRollbackClusterUndoesThenObservesRolloutStatus use in
// lifecycle_test.go), covering what a real `pf logs`/`pf events` cannot
// exercise without a live cluster: which observe Kind/Tail/Follow get
// requested for each command.
func TestDispatchObservationSelectsLogsOrEvents(t *testing.T) {
	stub := &stubDeploymentPlugin{
		capabilities:  allDeploymentCapabilities(),
		observeResult: api.DeploymentObserveResult{Output: "log/event output", Ready: true},
	}
	host := &pluginHost{clients: []pluginClient{stub}}

	output, err := dispatchObservation(context.Background(), host, "logs", "prod", "hello", 50, true)
	if err != nil || output != "log/event output" {
		t.Fatalf("output=%q err=%v", output, err)
	}
	var logsParams api.DeploymentObserveParams
	if decodeErr := json.Unmarshal(stub.lastParams["v1."+api.CapabilityDeploymentObserve], &logsParams); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if logsParams.Kind != "logs" || logsParams.Tail != 50 || !logsParams.Follow || logsParams.Namespace != "prod" || logsParams.Name != "hello" {
		t.Fatalf("logsParams=%+v", logsParams)
	}

	output, err = dispatchObservation(context.Background(), host, "events", "prod", "hello", 0, false)
	if err != nil || output != "log/event output" {
		t.Fatalf("output=%q err=%v", output, err)
	}
	var eventsParams api.DeploymentObserveParams
	if decodeErr := json.Unmarshal(stub.lastParams["v1."+api.CapabilityDeploymentObserve], &eventsParams); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if eventsParams.Kind != "events" || eventsParams.Namespace != "prod" || eventsParams.Name != "hello" {
		t.Fatalf("eventsParams=%+v", eventsParams)
	}
}

func TestDispatchObservationSurfacesMissingCapabilityAndErrors(t *testing.T) {
	empty := &pluginHost{}
	if _, err := dispatchObservation(context.Background(), empty, "logs", "prod", "hello", 0, false); err == nil ||
		!strings.Contains(err.Error(), api.CapabilityDeploymentObserve) {
		t.Fatalf("err=%v", err)
	}
	failing := &pluginHost{clients: []pluginClient{&stubDeploymentPlugin{capabilities: allDeploymentCapabilities(), err: errors.New("observe refused")}}}
	if _, err := dispatchObservation(context.Background(), failing, "events", "prod", "hello", 0, false); err == nil ||
		!strings.Contains(err.Error(), "observe refused") {
		t.Fatalf("err=%v", err)
	}
}

// TestProjectLogsAndEventsWithoutAConfiguredPluginFailCleanly proves the
// CLI wiring: `pf logs`/`pf events` now dispatch through the deployment
// plugin (see dispatchObservation) rather than shelling to kubectl, so
// without --plugin-dir pointing at one, both fail closed with a clear
// "no installed plugin" message. execute is asserted never called: it is
// retained on runProjectObservation's signature only for CLI-boundary
// stability.
func TestProjectLogsAndEventsWithoutAConfiguredPluginFailCleanly(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "pf.yaml"), []byte(
		"version: 1\nlanguage: compiled\nprofile: static\nartifact: app\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := atomicfile.WriteJSONSensitive(filepath.Join(root, ".platform-factory", "deployed.json"), map[string]any{
		"api_version": "platform-factory.dev/deployment/v1",
		"image":       "registry.example/app@sha256:" + strings.Repeat("a", 64),
		"name":        "hello", "namespace": "prod", "workload": "job",
	}); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	var stdout, stderr bytes.Buffer
	if code := runProjectObservation(context.Background(), "logs", []string{"--tail", "50", "--follow"}, &stdout, &stderr, nil); code != 1 ||
		!strings.Contains(stderr.String(), "no installed plugin provides "+api.CapabilityDeploymentObserve) {
		t.Fatalf("logs code=%d stderr=%s", code, stderr.String())
	}
	stderr.Reset()
	if code := runProjectObservation(context.Background(), "events", nil, &stdout, &stderr, nil); code != 1 ||
		!strings.Contains(stderr.String(), "no installed plugin provides "+api.CapabilityDeploymentObserve) {
		t.Fatalf("events code=%d stderr=%s", code, stderr.String())
	}
}

func TestProjectLogsFailWithOneSafeNextAction(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "pf.yaml"), []byte(
		"version: 1\nlanguage: compiled\nprofile: static\nartifact: app\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	var stdout, stderr bytes.Buffer
	if code := runProjectObservation(context.Background(), "logs", nil, &stdout, &stderr, nil); code != 1 ||
		!strings.Contains(stderr.String(), "run `pf deploy` first") {
		t.Fatalf("code/status=%s", stderr.String())
	}
}

// TestRunProjectObservationFlagHandling covers runProjectObservation's
// own flag.FlagSet plumbing: the -h/--help early return, a rejected
// unknown flag, and the three usage-validation cases (unwanted
// positional args, a non-positive --tail, and --follow combined with
// "events" which has no notion of following) - none of which reach
// observeapp.LoadDeployedProject, so they need no deployed-project
// fixture at all.
func TestRunProjectObservationFlagHandling(t *testing.T) {
	t.Run("help flag returns 0 without an error", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runProjectObservation(context.Background(), "logs", []string{"--help"}, &stdout, &stderr, nil)
		if code != 0 {
			t.Fatalf("code=%d stderr=%s", code, stderr.String())
		}
	})
	t.Run("unknown flag returns 2", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runProjectObservation(context.Background(), "logs", []string{"--bogus"}, &stdout, &stderr, nil)
		if code != 2 {
			t.Fatalf("code=%d stderr=%s", code, stderr.String())
		}
	})
	t.Run("unexpected positional argument", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runProjectObservation(context.Background(), "logs", []string{"extra"}, &stdout, &stderr, nil)
		if code != 2 || !strings.Contains(stderr.String(), "usage: platform-factory logs") {
			t.Fatalf("code=%d stderr=%s", code, stderr.String())
		}
		if !strings.Contains(stderr.String(), "--follow") {
			t.Fatalf("expected the logs usage line to mention --follow: %s", stderr.String())
		}
	})
	t.Run("non-positive tail is rejected", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runProjectObservation(context.Background(), "logs", []string{"--tail", "0"}, &stdout, &stderr, nil)
		if code != 2 || !strings.Contains(stderr.String(), "usage:") {
			t.Fatalf("code=%d stderr=%s", code, stderr.String())
		}
	})
	t.Run("events does not support --follow", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runProjectObservation(context.Background(), "events", []string{"--follow"}, &stdout, &stderr, nil)
		if code != 2 || !strings.Contains(stderr.String(), "usage: platform-factory events") {
			t.Fatalf("code=%d stderr=%s", code, stderr.String())
		}
		if strings.Contains(stderr.String(), "--follow]") {
			t.Fatalf("the events usage line must not advertise --follow: %s", stderr.String())
		}
	})
}

// TestRunProjectObservationSurfacesAPluginStartFailure covers
// runProjectObservation's pluginFlags.start(ctx) error branch - reached
// after a deployed-project state is found (so it must get past
// observeapp.LoadDeployedProject first), triggered here the same way
// plugins_flags_test.go's TestPluginOptionsStartRejectsMissingKey does:
// a --plugin-key that does not resolve to a readable file.
func TestRunProjectObservationSurfacesAPluginStartFailure(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "pf.yaml"), []byte(
		"version: 1\nlanguage: compiled\nprofile: static\nartifact: app\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := atomicfile.WriteJSONSensitive(filepath.Join(root, ".platform-factory", "deployed.json"), map[string]any{
		"api_version": "platform-factory.dev/deployment/v1",
		"image":       "registry.example/app@sha256:" + strings.Repeat("a", 64),
		"name":        "hello", "namespace": "prod", "workload": "job",
	}); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	var stdout, stderr bytes.Buffer
	code := runProjectObservation(context.Background(), "logs",
		[]string{"--plugin-dir", t.TempDir(), "--plugin-key", "/does/not/exist.pem"},
		&stdout, &stderr, nil)
	if code != 1 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "platform-factory logs:") {
		t.Fatalf("expected the plugin start failure to be reported, stderr=%q", stderr.String())
	}
}

func TestRollbackUsesPersistedServiceAndRejectsJob(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "pf.yaml"), []byte(
		"version: 1\nlanguage: compiled\nprofile: static\nartifact: app\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(root, ".platform-factory", "deployed.json")
	state := map[string]any{
		"api_version": "platform-factory.dev/deployment/v1",
		"image":       "registry.example/app@sha256:" + strings.Repeat("a", 64),
		"name":        "hello", "namespace": "prod", "workload": "service",
	}
	if err := atomicfile.WriteJSONSensitive(statePath, state); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	var stdout, stderr bytes.Buffer
	if code := runRollback(context.Background(), []string{"--dry-run"}, &stdout, &stderr, nil); code != 0 {
		t.Fatalf("rollback code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "deployment/hello") || !strings.Contains(stdout.String(), "--namespace prod") {
		t.Fatalf("rollback plan=%s", stdout.String())
	}
	state["workload"] = "job"
	if err := atomicfile.WriteJSONSensitive(statePath, state); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := runRollback(context.Background(), []string{"--dry-run"}, &stdout, &stderr, nil); code != 1 ||
		!strings.Contains(stderr.String(), "Jobs have no rollout history") {
		t.Fatalf("job rollback code=%d stderr=%s", code, stderr.String())
	}
}
