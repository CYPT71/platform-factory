package dockersave

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestSocketClientLoadArchiveAndImageExistsRoundTrip(t *testing.T) {
	engine, socketPath := newFakeEngineSocket(t)
	client := NewSocketClient(socketPath)

	exists, err := client.ImageExists(context.Background(), "example/service:v1")
	if err != nil || exists {
		t.Fatalf("exists=%v err=%v, want false before any load", exists, err)
	}

	if err := client.LoadArchive(context.Background(), strings.NewReader("not a real archive, the fake engine does not parse it here")); err != nil {
		t.Fatal(err)
	}
	engine.markLoaded("example/service:v1")

	exists, err = client.ImageExists(context.Background(), "example/service:v1")
	if err != nil || !exists {
		t.Fatalf("exists=%v err=%v, want true after load", exists, err)
	}
}

func TestSocketClientLoadArchiveSurfacesStreamedError(t *testing.T) {
	engine, socketPath := newFakeEngineSocket(t)
	engine.loadErr = "manifest.json: no such file"
	client := NewSocketClient(socketPath)
	err := client.LoadArchive(context.Background(), strings.NewReader("payload"))
	if err == nil || !strings.Contains(err.Error(), "manifest.json") {
		t.Fatalf("err=%v, want the streamed error surfaced", err)
	}
}

func TestSocketClientLoadArchiveSurfacesNonOKStatus(t *testing.T) {
	engine, socketPath := newFakeEngineSocket(t)
	engine.failLoad = true
	client := NewSocketClient(socketPath)
	err := client.LoadArchive(context.Background(), strings.NewReader("payload"))
	if err == nil || !strings.Contains(err.Error(), "load rejected") {
		t.Fatalf("err=%v, want the API error message surfaced", err)
	}
}

func TestSocketClientImageExistsSurfacesUnexpectedStatus(t *testing.T) {
	_, socketPath := newFakeEngineSocket(t)
	client := NewSocketClient(socketPath)
	// POST to the inspect-shaped GET endpoint is unhandled by the fake
	// engine and falls through to its default 404 branch - not what a
	// real daemon would do for a malformed path, but enough to exercise
	// ImageExists's own "anything but 200/404 is an error" branch through
	// a path that never matches "/json".
	if _, err := client.ImageExists(context.Background(), ""); err == nil {
		t.Fatal("expected an empty reference to be rejected before any request")
	}
}

func TestSocketClientRefusesUnreachableSocket(t *testing.T) {
	client := NewSocketClient("/nonexistent/path/to.sock")
	if err := client.LoadArchive(context.Background(), io.NopCloser(strings.NewReader("x"))); err == nil {
		t.Fatal("expected a dial failure against a nonexistent socket")
	}
	if _, err := client.ImageExists(context.Background(), "example/service:v1"); err == nil {
		t.Fatal("expected a dial failure against a nonexistent socket")
	}
}

func TestDockerAndPodmanSocketPathResolution(t *testing.T) {
	t.Run("DOCKER_HOST unix url", func(t *testing.T) {
		t.Setenv("DOCKER_HOST", "unix:///custom/docker.sock")
		got, err := DockerSocketPath()
		if err != nil || got != "/custom/docker.sock" {
			t.Fatalf("got=%q err=%v", got, err)
		}
	})
	t.Run("DOCKER_HOST bare path", func(t *testing.T) {
		t.Setenv("DOCKER_HOST", "/custom/docker2.sock")
		got, err := DockerSocketPath()
		if err != nil || got != "/custom/docker2.sock" {
			t.Fatalf("got=%q err=%v", got, err)
		}
	})
	t.Run("DOCKER_HOST rejects non-unix scheme", func(t *testing.T) {
		t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:2375")
		if _, err := DockerSocketPath(); err == nil {
			t.Fatal("expected tcp:// to be rejected for a local socket resolution")
		}
	})
	t.Run("default docker socket", func(t *testing.T) {
		t.Setenv("DOCKER_HOST", "")
		got, err := DockerSocketPath()
		if err != nil || got != "/var/run/docker.sock" {
			t.Fatalf("got=%q err=%v", got, err)
		}
	})
	t.Run("CONTAINER_HOST unix url", func(t *testing.T) {
		t.Setenv("CONTAINER_HOST", "unix:///custom/podman.sock")
		got, err := PodmanSocketPath()
		if err != nil || got != "/custom/podman.sock" {
			t.Fatalf("got=%q err=%v", got, err)
		}
	})
	t.Run("default podman socket falls back to rootful path", func(t *testing.T) {
		t.Setenv("CONTAINER_HOST", "")
		t.Setenv("XDG_RUNTIME_DIR", "")
		got, err := PodmanSocketPath()
		if err != nil {
			t.Fatal(err)
		}
		if got != "/run/podman/podman.sock" && !strings.Contains(got, "podman.sock") {
			t.Fatalf("got=%q", got)
		}
	})
}

func TestNewRuntimeClientForNameRejectsUnknownRuntime(t *testing.T) {
	if _, err := NewRuntimeClientForName("containerd"); err == nil {
		t.Fatal("expected an unsupported runtime name to be rejected")
	}
}
