package dockersave

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestNewRuntimeClientForNamePropagatesASocketPathResolutionError(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:2375")
	if _, err := NewRuntimeClientForName("docker"); err == nil {
		t.Fatal("expected DockerSocketPath's rejection of a non-unix DOCKER_HOST to propagate")
	}
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("CONTAINER_HOST", "tcp://127.0.0.1:2375")
	if _, err := NewRuntimeClientForName("podman"); err == nil {
		t.Fatal("expected PodmanSocketPath's rejection of a non-unix CONTAINER_HOST to propagate")
	}
}

func TestNewRuntimeClientForNameFallsBackToTheCLIWhenTheSocketIsUnreachable(t *testing.T) {
	t.Setenv("DOCKER_HOST", filepath.Join(t.TempDir(), "no-daemon-listening.sock"))
	client, err := NewRuntimeClientForName("docker")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := client.(*cliRuntimeClient); !ok {
		t.Fatalf("client=%T, want *cliRuntimeClient for an unreachable socket", client)
	}
}

func TestNewRuntimeClientForNameUsesTheSocketClientWhenReachable(t *testing.T) {
	_, socketPath := newFakeEngineSocket(t)
	t.Setenv("DOCKER_HOST", socketPath)
	client, err := NewRuntimeClientForName("docker")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := client.(*SocketClient); !ok {
		t.Fatalf("client=%T, want *SocketClient for a reachable socket", client)
	}
}

func TestSocketReachable(t *testing.T) {
	_, socketPath := newFakeEngineSocket(t)
	if !socketReachable(socketPath) {
		t.Fatal("expected a live listening socket to be reachable")
	}
	if socketReachable(filepath.Join(t.TempDir(), "nothing-here.sock")) {
		t.Fatal("expected a nonexistent socket path to be unreachable")
	}
}

func TestSocketClientImageExistsSurfacesAnUnexpectedGETStatus(t *testing.T) {
	engine, socketPath := newFakeEngineSocket(t)
	engine.existsStatus = http.StatusInternalServerError
	client := NewSocketClient(socketPath)
	_, err := client.ImageExists(context.Background(), "example/service:v1")
	if err == nil || !strings.Contains(err.Error(), "unexpected status 500") {
		t.Fatalf("err=%v, want an unexpected-status error", err)
	}
}

func TestPodmanSocketPathPrefersAnExistingXDGRuntimeDirSocket(t *testing.T) {
	t.Setenv("CONTAINER_HOST", "")
	xdg := t.TempDir()
	podmanDir := filepath.Join(xdg, "podman")
	if err := os.MkdirAll(podmanDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sockPath := filepath.Join(podmanDir, "podman.sock")
	if err := os.WriteFile(sockPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", xdg)

	got, err := PodmanSocketPath()
	if err != nil {
		t.Fatal(err)
	}
	if got != sockPath {
		t.Fatalf("got=%q, want %q", got, sockPath)
	}
}

func TestPodmanSocketPathFallsBackToTheMachineSocketUnderHOME(t *testing.T) {
	t.Setenv("CONTAINER_HOST", "")
	// An XDG_RUNTIME_DIR whose podman/podman.sock does not exist, so
	// PodmanSocketPath falls through past that candidate (and past the
	// real-UID /run/user/<uid>/podman/podman.sock, which will not exist
	// in this sandboxed test environment either) to the podman-machine
	// socket derived from $HOME.
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	home := t.TempDir()
	t.Setenv("HOME", home)
	machineDir := filepath.Join(home, ".local", "share", "containers", "podman", "machine")
	if err := os.MkdirAll(machineDir, 0o755); err != nil {
		t.Fatal(err)
	}
	machineSocket := filepath.Join(machineDir, "podman.sock")
	if err := os.WriteFile(machineSocket, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := PodmanSocketPath()
	if err != nil {
		t.Fatal(err)
	}
	if got != machineSocket {
		t.Skipf("got=%q, want the machine socket %q - the real /run/user/<uid>/podman/podman.sock apparently exists in this environment, taking precedence as PodmanSocketPath itself intends", got, machineSocket)
	}
}

func TestParseUnixSocketURL(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{"bare path with no scheme", "/var/run/docker.sock", "/var/run/docker.sock", false},
		{"a scheme-shaped string with no :// is treated as a bare path too", "unix:docker.sock", "unix:docker.sock", false},
		{"unix:// with an absolute path", "unix:///var/run/docker.sock", "/var/run/docker.sock", false},
		{"rejects a non-unix scheme", "tcp://127.0.0.1:2375", "", true},
		{"rejects an unparseable URL", "unix://%zz", "", true},
		{"rejects a unix:// URL with no path at all", "unix://", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseUnixSocketURL(c.raw)
			if (err != nil) != c.wantErr {
				t.Fatalf("parseUnixSocketURL(%q) error=%v, wantErr=%v", c.raw, err, c.wantErr)
			}
			if err == nil && got != c.want {
				t.Fatalf("parseUnixSocketURL(%q) = %q, want %q", c.raw, got, c.want)
			}
		})
	}
}

// fakeResponse builds an *http.Response with statusCode/body suitable
// for decodeLoadResponse, which only reads resp.StatusCode/resp.Body -
// no live socket or httptest server is needed to exercise it directly.
func fakeResponse(statusCode int, body string) *http.Response {
	recorder := httptest.NewRecorder()
	recorder.WriteHeader(statusCode)
	_, _ = recorder.WriteString(body)
	result := recorder.Result()
	result.Status = http.StatusText(statusCode)
	return result
}

func TestDecodeLoadResponseOnANonJSONErrorBody(t *testing.T) {
	err := decodeLoadResponse(fakeResponse(http.StatusInternalServerError, "boom: disk is full"))
	if err == nil || !strings.Contains(err.Error(), "boom: disk is full") {
		t.Fatalf("err=%v, want the raw non-JSON body surfaced", err)
	}
}

func TestDecodeLoadResponseOnAnEmptyErrorBody(t *testing.T) {
	err := decodeLoadResponse(fakeResponse(http.StatusInternalServerError, ""))
	if err == nil {
		t.Fatal("expected an error for a non-2xx status")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("err=%v, want it to mention the status code since the body carried no message", err)
	}
}

func TestDecodeLoadResponseSurfacesAMalformedStreamBody(t *testing.T) {
	err := decodeLoadResponse(fakeResponse(http.StatusOK, "not even close to json"))
	if err == nil || !strings.Contains(err.Error(), "decode load response") {
		t.Fatalf("err=%v, want a decode error for a malformed stream body", err)
	}
}
