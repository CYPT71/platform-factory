package dockersave

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RuntimeClient is the socket-level operations PrepareContainerImage needs
// from a local docker- or podman-compatible Engine API: load an archive
// into the runtime, and confirm an image reference is present afterward.
// Satisfied by *SocketClient talking to a real Unix domain socket, and by
// any test double talking to a fake one (see socketclient_test.go).
type RuntimeClient interface {
	// LoadArchive POSTs body (a tar stream - Docker Save format for
	// docker, an OCI Image Layout archive for podman; see
	// streamLayoutToRuntime) to the runtime's own image-load endpoint.
	LoadArchive(ctx context.Context, body io.Reader) error
	// ImageExists reports whether reference is present in the runtime's
	// local image store.
	ImageExists(ctx context.Context, reference string) (bool, error)
}

// SocketClient talks to a docker- or podman-compatible Engine API over
// that runtime's own Unix domain socket. It replaces the previous
// approach of shelling out to the docker/podman CLI binaries (see
// PrepareContainerImage's doc comment), which does not work inside a
// distroless/scratch container image that ships neither binary - only
// this process's own compiled Go code, which can still open a Unix
// socket and speak HTTP over it without any external executable.
//
// Endpoint choice: POST /images/load and GET /images/{name}/json are
// docker's own Engine API (https://docs.docker.com/engine/api/) and are
// also implemented by podman's Docker-compatibility API, mounted on the
// same socket family podman's own CLI already talks to (confirmed by
// this repo's own field-test report, which used $CONTAINER_HOST to reach
// it) - podman's compat handler for /images/load delegates to the same
// image-loading code its own `podman load` CLI command uses, which is
// exactly why the existing WriteDockerArchive/writeLayoutArchive split
// (Docker Save format for docker, raw OCI Image Layout for podman) was
// already runtime-specific before this change: that split is preserved
// here, only the transport (HTTP-over-socket instead of stdin-to-a-CLI-
// process) changes. This could not be verified against a live podman
// daemon in this environment (see PART A of the accompanying report);
// it is the documented, spec-consistent choice, not an empirically
// confirmed one.
type SocketClient struct {
	httpClient *http.Client
}

// NewSocketClient returns a SocketClient that dials socketPath (a
// filesystem path to a Unix domain socket) for every request.
func NewSocketClient(socketPath string) *SocketClient {
	return &SocketClient{
		httpClient: &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var dialer net.Dialer
					return dialer.DialContext(ctx, "unix", socketPath)
				},
			},
			// No overall timeout: a large image load can legitimately take
			// a long time, and the caller controls cancellation through ctx.
		},
	}
}

// NewRuntimeClientForName resolves and connects to the local socket for
// runtimeName ("docker" or "podman"), honoring $DOCKER_HOST/$CONTAINER_HOST
// the same way the docker/podman CLIs themselves do. When that socket is
// not reachable - the common case for podman, which does not run a
// background API service unless something has explicitly started one -
// it falls back to shelling the runtimeName CLI binary directly (see
// cliclient.go), so environments that have the CLI installed but no
// socket service running keep working exactly as they did before the
// socket-based client was introduced.
func NewRuntimeClientForName(runtimeName string) (RuntimeClient, error) {
	var (
		socketPath string
		err        error
	)
	switch runtimeName {
	case "docker":
		socketPath, err = DockerSocketPath()
	case "podman":
		socketPath, err = PodmanSocketPath()
	default:
		return nil, fmt.Errorf("unsupported container runtime %q", runtimeName)
	}
	if err != nil {
		return nil, err
	}
	if !socketReachable(socketPath) {
		return &cliRuntimeClient{runtimeName: runtimeName}, nil
	}
	return NewSocketClient(socketPath), nil
}

// socketReachable reports whether a Unix domain socket at path accepts a
// connection right now. A short-lived, otherwise-unused connection - the
// same "is anyone listening" check `docker version`/`podman info` make
// implicitly on every invocation - not a load-bearing part of any actual
// request.
func socketReachable(path string) bool {
	conn, err := net.DialTimeout("unix", path, 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// DockerSocketPath resolves the Unix domain socket docker's Engine API
// listens on: $DOCKER_HOST if set (a unix:// URL, or a bare path the way
// the docker CLI itself also accepts), else the conventional
// /var/run/docker.sock.
func DockerSocketPath() (string, error) {
	if host := strings.TrimSpace(os.Getenv("DOCKER_HOST")); host != "" {
		return parseUnixSocketURL(host)
	}
	return "/var/run/docker.sock", nil
}

// PodmanSocketPath resolves the Unix domain socket podman's Docker-
// compatible REST API listens on: $CONTAINER_HOST if set (the variable
// this repo's own field test used to point at a podman socket), else the
// conventional rootless per-user socket
// ($XDG_RUNTIME_DIR/podman/podman.sock, falling back to
// /run/user/<uid>/podman/podman.sock when $XDG_RUNTIME_DIR is unset or
// the socket is not there), falling back further to the rootful default
// /run/podman/podman.sock.
func PodmanSocketPath() (string, error) {
	if host := strings.TrimSpace(os.Getenv("CONTAINER_HOST")); host != "" {
		return parseUnixSocketURL(host)
	}
	if xdg := strings.TrimSpace(os.Getenv("XDG_RUNTIME_DIR")); xdg != "" {
		candidate := filepath.Join(xdg, "podman", "podman.sock")
		if _, statErr := os.Stat(candidate); statErr == nil {
			return candidate, nil
		}
	}
	byUID := fmt.Sprintf("/run/user/%d/podman/podman.sock", os.Getuid())
	if _, statErr := os.Stat(byUID); statErr == nil {
		return byUID, nil
	}
	// podman on macOS/Windows runs its engine inside a "podman machine" VM;
	// `podman info`'s own RemoteSocket.Path (.../run/user/<uid>/podman/podman.sock,
	// the byUID candidate above) is a path *inside* that VM, not reachable
	// directly from the host. podman forwards it to a real host-side Unix
	// socket at this fixed, machine-name-independent path instead
	// (confirmed empirically against a real `podman machine` VM: both the
	// libpod and Docker-compatible APIs answer on it).
	if home, homeErr := os.UserHomeDir(); homeErr == nil {
		machineSocket := filepath.Join(home, ".local", "share", "containers", "podman", "machine", "podman.sock")
		if _, statErr := os.Stat(machineSocket); statErr == nil {
			return machineSocket, nil
		}
	}
	return "/run/podman/podman.sock", nil
}

func parseUnixSocketURL(raw string) (string, error) {
	if !strings.Contains(raw, "://") {
		// DOCKER_HOST/CONTAINER_HOST bare filesystem path, the same
		// shorthand the docker/podman CLIs themselves accept.
		return raw, nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse socket url %q: %w", raw, err)
	}
	if parsed.Scheme != "unix" {
		return "", fmt.Errorf("socket url %q: only unix:// is supported for a local runtime socket", raw)
	}
	path := parsed.Path
	if path == "" {
		path = parsed.Opaque
	}
	if path == "" {
		return "", fmt.Errorf("socket url %q has no path", raw)
	}
	return path, nil
}

// LoadArchive implements RuntimeClient.
func (c *SocketClient) LoadArchive(ctx context.Context, body io.Reader) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/images/load", body)
	if err != nil {
		return fmt.Errorf("build load request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-tar")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("POST /images/load: %w", err)
	}
	defer resp.Body.Close()
	return decodeLoadResponse(resp)
}

// ImageExists implements RuntimeClient.
func (c *SocketClient) ImageExists(ctx context.Context, reference string) (bool, error) {
	if reference == "" {
		return false, errors.New("image reference is empty")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix/images/"+reference+"/json", nil)
	if err != nil {
		return false, fmt.Errorf("build inspect request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("GET /images/%s/json: %w", reference, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return false, fmt.Errorf("GET /images/%s/json: unexpected status %d: %s", reference, resp.StatusCode, strings.TrimSpace(string(data)))
	}
}

// loadStreamMessage is one line of the newline/concatenated-JSON-object
// stream docker's own /images/load response body is (mirrored by
// podman's compat endpoint): normally a sequence of {"stream":"..."}
// progress lines, but an error partway through the load - the load
// endpoint returns HTTP 200 even for a load that fails partway - surfaces
// as {"error":"...","errorDetail":{"message":"..."}} instead.
type loadStreamMessage struct {
	Stream      string `json:"stream,omitempty"`
	Error       string `json:"error,omitempty"`
	ErrorDetail *struct {
		Message string `json:"message,omitempty"`
	} `json:"errorDetail,omitempty"`
}

// apiErrorMessage is the JSON body shape docker's Engine API (and
// podman's compat API) return for a non-2xx response: {"message":"..."}.
type apiErrorMessage struct {
	Message string `json:"message"`
}

func decodeLoadResponse(resp *http.Response) error {
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		var apiErr apiErrorMessage
		if json.Unmarshal(data, &apiErr) == nil && apiErr.Message != "" {
			return fmt.Errorf("load: %s (status %d)", apiErr.Message, resp.StatusCode)
		}
		trimmed := strings.TrimSpace(string(data))
		if trimmed == "" {
			trimmed = resp.Status
		}
		return fmt.Errorf("load: unexpected status %d: %s", resp.StatusCode, trimmed)
	}
	decoder := json.NewDecoder(resp.Body)
	for {
		var msg loadStreamMessage
		if err := decoder.Decode(&msg); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("decode load response: %w", err)
		}
		if msg.Error != "" {
			return fmt.Errorf("load: %s", msg.Error)
		}
		if msg.ErrorDetail != nil && msg.ErrorDetail.Message != "" {
			return fmt.Errorf("load: %s", msg.ErrorDetail.Message)
		}
	}
}
