package registry

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestPushAndPullAgainstARealLocalOCIRegistry proves push against a real,
// generic OCI Distribution server - not Docker Hub or GHCR's read-only
// pull path (TestPullImageRootfsAgainstRealDockerHub/GHCR live in
// internal/app/provisionruntime), and not the httptest fakes every other
// test in this package uses. PushLayout's blob upload sessions, manifest
// PUT, and tag move all run against a genuine registry:2 instance, then
// GetManifest reads the result back from that same real server.
//
// set PLATFORM_FACTORY_TEST_LIVE_LOCAL_REGISTRY=1 to run it; requires
// podman (or docker) on PATH, pulling and running registry:2 locally.
func TestPushAndPullAgainstARealLocalOCIRegistry(t *testing.T) {
	if os.Getenv("PLATFORM_FACTORY_TEST_LIVE_LOCAL_REGISTRY") != "1" {
		t.Skip("set PLATFORM_FACTORY_TEST_LIVE_LOCAL_REGISTRY=1 to push/pull against a real local registry:2 container")
	}
	runtime := containerRuntime(t)

	containerName := "pf-live-registry-test"
	_ = exec.Command(runtime, "rm", "-f", containerName).Run()
	runOut, err := exec.Command(runtime, "run", "-d", "--rm", "--name", containerName,
		"-p", "5000", "registry:2").CombinedOutput()
	if err != nil {
		t.Fatalf("start registry:2: %v\n%s", err, runOut)
	}
	t.Cleanup(func() { _ = exec.Command(runtime, "rm", "-f", containerName).Run() })

	port := hostPort(t, runtime, containerName)
	target := Reference{Registry: "127.0.0.1:" + port, Repository: "live/service", Tag: "v1"}
	client := &Client{Scheme: "http", HTTP: &http.Client{}}

	waitForRegistryReady(t, client, target)

	layoutDir := testLayout(t)
	result, err := client.PushLayout(context.Background(), layoutDir, target, "")
	if err != nil {
		t.Fatalf("PushLayout against a real registry:2: %v", err)
	}
	if result.Digest == "" {
		t.Fatal("PushLayout returned an empty digest")
	}
	t.Logf("pushed digest: %s", result.Digest)

	body, contentType, err := client.GetManifest(context.Background(), target, target.Tag)
	if err != nil {
		t.Fatalf("GetManifest against a real registry:2: %v", err)
	}
	if len(body) == 0 || contentType == "" {
		t.Fatalf("GetManifest returned empty body/content-type: body=%d bytes contentType=%q", len(body), contentType)
	}

	byDigest, _, err := client.GetManifest(context.Background(), target, result.Digest)
	if err != nil {
		t.Fatalf("GetManifest by digest against a real registry:2: %v", err)
	}
	if string(byDigest) != string(body) {
		t.Fatal("manifest fetched by tag and by digest disagree")
	}
}

func containerRuntime(t *testing.T) string {
	t.Helper()
	for _, candidate := range []string{"podman", "docker"} {
		if _, err := exec.LookPath(candidate); err == nil {
			return candidate
		}
	}
	t.Skip("neither podman nor docker is on PATH")
	return ""
}

func hostPort(t *testing.T, runtime, containerName string) string {
	t.Helper()
	out, err := exec.Command(runtime, "port", containerName, "5000/tcp").CombinedOutput()
	if err != nil {
		t.Fatalf("inspect published port: %v\n%s", err, out)
	}
	// Output looks like "0.0.0.0:54321" (and, on podman, sometimes one line
	// per address family) - the port is whatever follows the last ':'.
	line := strings.TrimSpace(strings.Split(strings.TrimSpace(string(out)), "\n")[0])
	idx := strings.LastIndex(line, ":")
	if idx < 0 {
		t.Fatalf("could not parse published port from %q", line)
	}
	port := line[idx+1:]
	if _, err := strconv.Atoi(port); err != nil {
		t.Fatalf("could not parse published port from %q", line)
	}
	return port
}

func waitForRegistryReady(t *testing.T, client *Client, target Reference) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		if _, _, err := client.GetManifest(context.Background(), target, "does-not-exist"); err == nil || isRegistryReachable(err) {
			return
		} else {
			lastErr = err
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("registry:2 did not become reachable in time: %v", lastErr)
}

// isRegistryReachable distinguishes "the server answered (even with a 404
// for a tag that doesn't exist)" from "the connection itself failed" while
// the container is still starting.
func isRegistryReachable(err error) bool {
	if err == nil {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "HTTP 404") || strings.Contains(msg, "NAME_UNKNOWN") || strings.Contains(msg, "MANIFEST_UNKNOWN")
}
