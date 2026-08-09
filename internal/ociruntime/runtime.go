//go:build linux

// Package ociruntime owns the persistent OCI-facing lifecycle used by Podman.
// It is deliberately independent from Podman: the engine invokes the standard
// runtime commands and remains the initiator of every lifecycle transition.
package ociruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
)

const (
	OCIVersion = "1.2.0"
	maxConfig  = 1 << 20
)

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.+-]{0,127}$`)

type Config struct {
	OCIVersion  string            `json:"ociVersion"`
	Root        Root              `json:"root"`
	Process     Process           `json:"process"`
	Hostname    string            `json:"hostname,omitempty"`
	Mounts      []Mount           `json:"mounts,omitempty"`
	Hooks       json.RawMessage   `json:"hooks,omitempty"`
	Linux       json.RawMessage   `json:"linux,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

type Root struct {
	Path     string `json:"path"`
	Readonly bool   `json:"readonly,omitempty"`
}

type Mount struct {
	Destination string   `json:"destination"`
	Type        string   `json:"type,omitempty"`
	Source      string   `json:"source,omitempty"`
	Options     []string `json:"options,omitempty"`
}

type Process struct {
	Terminal        bool          `json:"terminal,omitempty"`
	User            User          `json:"user"`
	Args            []string      `json:"args"`
	Env             []string      `json:"env,omitempty"`
	Cwd             string        `json:"cwd"`
	Capabilities    *Capabilities `json:"capabilities,omitempty"`
	Rlimits         []Rlimit      `json:"rlimits,omitempty"`
	NoNewPrivileges bool          `json:"noNewPrivileges,omitempty"`
	ApparmorProfile string        `json:"apparmorProfile,omitempty"`
	OOMScoreAdj     *int          `json:"oomScoreAdj,omitempty"`
	SelinuxLabel    string        `json:"selinuxLabel,omitempty"`
}

type Capabilities struct {
	Bounding    []string `json:"bounding,omitempty"`
	Effective   []string `json:"effective,omitempty"`
	Inheritable []string `json:"inheritable,omitempty"`
	Permitted   []string `json:"permitted,omitempty"`
	Ambient     []string `json:"ambient,omitempty"`
}

type Rlimit struct {
	Type string `json:"type"`
	Hard uint64 `json:"hard"`
	Soft uint64 `json:"soft"`
}

type User struct {
	UID            uint32   `json:"uid"`
	GID            uint32   `json:"gid"`
	AdditionalGids []uint32 `json:"additionalGids,omitempty"`
	Username       string   `json:"username,omitempty"`
	Umask          *uint32  `json:"umask,omitempty"`
}

type State struct {
	OCIVersion  string            `json:"ociVersion"`
	ID          string            `json:"id"`
	Status      string            `json:"status"`
	PID         int               `json:"pid"`
	Bundle      string            `json:"bundle"`
	Created     time.Time         `json:"created"`
	ExitStatus  *uint32           `json:"exitStatus,omitempty"`
	ExitedAt    *time.Time        `json:"exitedAt,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

type startResult struct {
	Command     string `json:"command,omitempty"`
	ID          string `json:"id"`
	Incarnation string `json:"incarnation"`
	PID         int    `json:"pid"`
	Signal      int    `json:"signal,omitempty"`
	Started     bool   `json:"started,omitempty"`
	Signaled    bool   `json:"signaled,omitempty"`
	Error       string `json:"error,omitempty"`
}

type Store struct {
	root      *os.Root
	dir       string
	dirHandle *os.File
}

func OpenStore(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("oci runtime: state root is required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("oci runtime: create state root: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("oci runtime: state root must be a real directory")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	dirHandle, err := os.Open(dir)
	if err != nil {
		root.Close()
		return nil, err
	}
	return &Store{root: root, dir: dir, dirHandle: dirHandle}, nil
}

func (s *Store) Close() error {
	rootErr := s.root.Close()
	handleErr := s.dirHandle.Close()
	if rootErr != nil {
		return rootErr
	}
	return handleErr
}
func (s *Store) Dir() string { return s.dir }

func (s *Store) Create(ctx context.Context, id, bundle string) (State, error) {
	if err := ctx.Err(); err != nil {
		return State{}, err
	}
	if !idPattern.MatchString(id) {
		return State{}, fmt.Errorf("oci runtime: invalid container id %q", id)
	}
	bundle, err := filepath.Abs(bundle)
	if err != nil {
		return State{}, err
	}
	config, err := LoadConfig(bundle)
	if err != nil {
		return State{}, err
	}
	var state State
	err = s.withLock(id, func() error {
		if _, found, err := s.getUnlocked(id); err != nil {
			return err
		} else if found {
			return fmt.Errorf("oci runtime: container %q already exists", id)
		}
		state = State{
			OCIVersion: OCIVersion, ID: id, Status: "created", Bundle: bundle,
			Created: time.Now().UTC(), Annotations: config.Annotations,
		}
		return s.put(state)
	})
	return state, err
}

// legacyAnnotationKeys maps each pre-rebrand secure-oci.dev/* OCI
// annotation key this package reads to its platform-factory.dev/*
// replacement, for the documented compatibility overlap window (see
// docs/api-compatibility.md) - a bundle produced by an older tool may
// still carry the old keys. Deliberately spelled out as literal strings
// rather than referencing supervisor_linux.go/rootfs_linux.go's own
// annotationX constants: those live in linux&&amd64-only files, while
// this one (and the linux/arm64 builds it must also serve) only
// requires plain "linux".
var legacyAnnotationKeys = map[string]string{
	"secure-oci.dev/kernel-path":           "platform-factory.dev/kernel-path",
	"secure-oci.dev/kernel-digest":         "platform-factory.dev/kernel-digest",
	"secure-oci.dev/initramfs-path":        "platform-factory.dev/initramfs-path",
	"secure-oci.dev/initramfs-digest":      "platform-factory.dev/initramfs-digest",
	"secure-oci.dev/memory-mib":            "platform-factory.dev/memory-mib",
	"secure-oci.dev/vcpus":                 "platform-factory.dev/vcpus",
	"secure-oci.dev/sandbox-cgroups":       "platform-factory.dev/sandbox-cgroups",
	"secure-oci.dev/sandbox-namespaces":    "platform-factory.dev/sandbox-namespaces",
	"secure-oci.dev/block-device-path":     "platform-factory.dev/block-device-path",
	"secure-oci.dev/block-device-readonly": "platform-factory.dev/block-device-readonly",
	"secure-oci.dev/network-tap":           "platform-factory.dev/network-tap",
	"secure-oci.dev/init-path":             "platform-factory.dev/init-path",
	"secure-oci.dev/init-digest":           "platform-factory.dev/init-digest",
}

// normalizeLegacyAnnotations copies each legacy annotation's value
// forward to its current key, in place, only when the current key isn't
// already set - so every read site elsewhere in this package only ever
// needs to look up the current key. Called once, here in LoadConfig, so
// both config.Annotations (read directly) and State.Annotations (copied
// from a LoadConfig result at Store.Create, see below) are covered by
// the same normalization.
func normalizeLegacyAnnotations(annotations map[string]string) {
	for legacy, current := range legacyAnnotationKeys {
		if _, ok := annotations[current]; ok {
			continue
		}
		if value, ok := annotations[legacy]; ok {
			annotations[current] = value
		}
	}
}

func LoadConfig(bundle string) (Config, error) {
	info, err := os.Lstat(bundle)
	if err != nil {
		return Config{}, fmt.Errorf("oci runtime: inspect bundle: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Config{}, errors.New("oci runtime: bundle must be a real directory")
	}
	file, err := os.Open(filepath.Join(bundle, "config.json"))
	if err != nil {
		return Config{}, fmt.Errorf("oci runtime: open config.json: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxConfig+1))
	if err != nil {
		return Config{}, fmt.Errorf("oci runtime: read config.json: %w", err)
	}
	if len(data) > maxConfig {
		return Config{}, errors.New("oci runtime: config.json exceeds 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var config Config
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("oci runtime: decode config.json: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Config{}, errors.New("oci runtime: config.json must contain exactly one document")
	}
	normalizeLegacyAnnotations(config.Annotations)
	if config.OCIVersion == "" || config.Root.Path == "" || len(config.Process.Args) == 0 ||
		!filepath.IsAbs(config.Process.Cwd) {
		return Config{}, errors.New("oci runtime: config.json lacks ociVersion, root.path, process.args, or absolute process.cwd")
	}
	if config.Process.Terminal || rawPresent(config.Hooks) {
		return Config{}, errors.New("oci runtime: terminal and hooks are not supported by the first MicroVM slice")
	}
	if config.Process.User.Umask != nil && *config.Process.User.Umask > 0o777 {
		return Config{}, errors.New("oci runtime: process umask exceeds 0777")
	}
	if config.Process.User.Username != "" {
		return Config{}, errors.New("oci runtime: process username is not supported")
	}
	if capabilitiesPresent(config.Process.Capabilities) {
		return Config{}, errors.New("oci runtime: non-empty process capabilities are not supported")
	}
	seenRlimits := map[string]bool{}
	for _, limit := range config.Process.Rlimits {
		if !supportedRlimit(limit.Type) {
			return Config{}, fmt.Errorf("oci runtime: unsupported process rlimit %q", limit.Type)
		}
		if seenRlimits[limit.Type] {
			return Config{}, fmt.Errorf("oci runtime: duplicate process rlimit %q", limit.Type)
		}
		if limit.Soft > limit.Hard {
			return Config{}, fmt.Errorf("oci runtime: process rlimit %q soft value exceeds hard value", limit.Type)
		}
		seenRlimits[limit.Type] = true
	}
	if config.Process.NoNewPrivileges {
		return Config{}, errors.New("oci runtime: no-new-privileges is not yet supported")
	}
	if config.Process.SelinuxLabel != "" {
		return Config{}, errors.New("oci runtime: SELinux labels are not yet supported")
	}
	if profile := config.Process.ApparmorProfile; profile != "" {
		if !validAppArmorProfileName(profile) {
			return Config{}, fmt.Errorf("oci runtime: invalid apparmor profile name %q", profile)
		}
		if !appArmorEnabled() {
			return Config{}, fmt.Errorf("oci runtime: apparmor profile %q requested but AppArmor is not enabled on this host", profile)
		}
		loaded, err := appArmorProfileLoaded(profile)
		if err != nil {
			return Config{}, err
		}
		if !loaded {
			return Config{}, fmt.Errorf("oci runtime: apparmor profile %q is not loaded on this host", profile)
		}
	}
	if config.Process.OOMScoreAdj != nil && *config.Process.OOMScoreAdj != 0 {
		return Config{}, errors.New("oci runtime: non-zero OOM adjustment is not yet supported")
	}
	if config.Process.OOMScoreAdj != nil && *config.Process.OOMScoreAdj == 0 {
		config.Process.OOMScoreAdj = nil
	}
	for _, mount := range config.Mounts {
		if guestOwnedPseudoMount(mount) || podmanContainerEnvMount(mount) {
			continue
		}

		return Config{}, fmt.Errorf(
			"oci runtime: host mount %q (%s) is unsupported; only guest-owned pseudo-filesystems are accepted",
			mount.Destination, mount.Type)
	}
	rootfs := config.Root.Path
	var rootHandle *os.Root
	if filepath.IsAbs(rootfs) {
		// Podman's overlay storage driver points root.path at a "merged"
		// directory it already owns, outside the bundle. Confine traversal
		// within it directly instead of requiring a bundle-relative path.
		rootHandle, err = os.OpenRoot(rootfs)
	} else {
		if rootfs == ".." ||
			len(rootfs) >= 3 && rootfs[:3] == ".."+string(filepath.Separator) {
			return Config{}, errors.New("oci runtime: root filesystem must remain inside the bundle")
		}
		var bundleRoot *os.Root
		bundleRoot, err = os.OpenRoot(bundle)
		if err == nil {
			defer bundleRoot.Close()
			rootHandle, err = bundleRoot.OpenRoot(rootfs)
		}
	}
	if err != nil {
		return Config{}, fmt.Errorf("oci runtime: confine root filesystem: %w", err)
	}
	defer rootHandle.Close()
	rootInfo, err := rootHandle.Stat(".")
	if err != nil || !rootInfo.IsDir() {
		return Config{}, errors.New("oci runtime: root filesystem must be a confined directory")
	}
	return config, nil
}

func rawPresent(value json.RawMessage) bool {
	trimmed := bytes.TrimSpace(value)
	return len(trimmed) != 0 && !bytes.Equal(trimmed, []byte("null"))
}

func capabilitiesPresent(value *Capabilities) bool {
	return value != nil && (len(value.Bounding) != 0 || len(value.Effective) != 0 ||
		len(value.Inheritable) != 0 || len(value.Permitted) != 0 || len(value.Ambient) != 0)
}

func supportedRlimit(value string) bool {
	switch value {
	case "RLIMIT_AS", "RLIMIT_CORE", "RLIMIT_CPU", "RLIMIT_DATA", "RLIMIT_FSIZE",
		"RLIMIT_LOCKS", "RLIMIT_MEMLOCK", "RLIMIT_MSGQUEUE", "RLIMIT_NICE",
		"RLIMIT_NOFILE", "RLIMIT_NPROC", "RLIMIT_RSS", "RLIMIT_RTPRIO",
		"RLIMIT_RTTIME", "RLIMIT_SIGPENDING", "RLIMIT_STACK":
		return true
	default:
		return false
	}
}

func guestOwnedPseudoMount(mount Mount) bool {
	allowed := map[string]string{
		"/proc":          "proc",
		"/sys":           "sysfs",
		"/dev":           "tmpfs",
		"/dev/pts":       "devpts",
		"/dev/mqueue":    "mqueue",
		"/dev/shm":       "tmpfs",
		"/sys/fs/cgroup": "cgroup",
	}
	expected, ok := allowed[filepath.Clean(mount.Destination)]
	if !ok {
		return false
	}
	return mount.Type == expected || expected == "cgroup" && mount.Type == "cgroup2"
}

// podmanContainerEnvMount recognizes Podman's standard bind mounts for
// container metadata and /dev/shm (bind-mounted, not tmpfs, so it doesn't
// match guestOwnedPseudoMount). The host path is never forwarded to the
// MicroVM; microvm-init recreates a guest-owned equivalent at the same
// destination.
func podmanContainerEnvMount(mount Mount) bool {
	if mount.Type != "bind" {
		return false
	}

	switch filepath.Clean(mount.Destination) {
	case "/run/.containerenv", "/etc/hosts", "/etc/hostname", "/etc/resolv.conf", "/dev/shm":
		return true
	default:
		return false
	}
}

func (s *Store) Get(ctx context.Context, id string) (State, bool, error) {
	if err := ctx.Err(); err != nil {
		return State{}, false, err
	}
	if !idPattern.MatchString(id) {
		return State{}, false, fmt.Errorf("oci runtime: invalid container id %q", id)
	}
	var state State
	var found bool
	err := s.withLock(id, func() error {
		var err error
		state, found, err = s.getUnlocked(id)
		return err
	})
	return state, found, err
}

// List returns the reconciled runtime states in stable ID order. State files
// are the only source of truth; lock files, control sockets and interrupted
// temporary writes are never surfaced as workloads.
func (s *Store) List(ctx context.Context) ([]State, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	directory, err := s.root.Open(".")
	if err != nil {
		return nil, fmt.Errorf("oci runtime: open state root for listing: %w", err)
	}
	defer directory.Close()
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return nil, fmt.Errorf("oci runtime: list state root: %w", err)
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if !idPattern.MatchString(id) {
			return nil, fmt.Errorf("oci runtime: invalid state filename %q", entry.Name())
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	states := make([]State, 0, len(ids))
	for _, id := range ids {
		state, found, err := s.Get(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("oci runtime: list container %q: %w", id, err)
		}
		if !found {
			continue
		}
		states = append(states, state)
	}
	return states, nil
}

func (s *Store) getUnlocked(id string) (State, bool, error) {
	data, err := s.root.ReadFile(id + ".json")
	if errors.Is(err, os.ErrNotExist) {
		return State{}, false, nil
	}
	if err != nil {
		return State{}, false, err
	}
	if len(data) > maxConfig {
		return State{}, false, errors.New("oci runtime: stored state exceeds 1 MiB")
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil || state.ID != id {
		return State{}, false, errors.New("oci runtime: corrupt stored state")
	}
	if state.PID > 0 && !processAlive(state.PID) {
		_ = os.Remove(s.controlSocketPath(state))
		state.PID = 0
		state.Status = "stopped"
		if state.ExitStatus == nil {
			unknown := uint32(255)
			exitedAt := time.Now().UTC()
			state.ExitStatus = &unknown
			state.ExitedAt = &exitedAt
		}
		if err := s.put(state); err != nil {
			return State{}, false, err
		}
	}
	return state, true, nil
}

func (s *Store) Delete(ctx context.Context, id string, force bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.withLock(id, func() error {
		state, found, err := s.getUnlocked(id)
		if err != nil || !found {
			return err
		}
		if !force && state.Status == "running" {
			return errors.New("oci runtime: cannot delete a running container")
		}
		if state.PID > 0 && processAlive(state.PID) {
			if !force {
				return errors.New("oci runtime: cannot delete a live supervisor")
			}
			_ = syscall.Kill(state.PID, syscall.SIGKILL)
		}
		_ = os.Remove(s.controlSocketPath(state))
		if err := s.root.Remove(id + ".json"); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	})
}

func (s *Store) SetSupervisor(ctx context.Context, id string, pid int) error {
	if pid <= 0 {
		return errors.New("oci runtime: supervisor PID must be positive")
	}
	return s.update(ctx, id, func(state *State) {
		state.PID = pid
	})
}

func (s *Store) SetStatus(ctx context.Context, id, status string) error {
	switch status {
	case "created", "running", "stopped":
	default:
		return fmt.Errorf("oci runtime: invalid state %q", status)
	}
	return s.withLock(id, func() error {
		state, found, err := s.getUnlocked(id)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("oci runtime: container %q does not exist", id)
		}
		state.Status = status
		if status == "stopped" {
			_ = os.Remove(s.controlSocketPath(state))
			state.PID = 0
		}
		return s.put(state)
	})
}

// SetExited atomically publishes the terminal guest process result.
func (s *Store) SetExited(ctx context.Context, id string, status uint32, exitedAt time.Time) error {
	if exitedAt.IsZero() {
		return errors.New("oci runtime: exit time is required")
	}
	exitedAt = exitedAt.UTC()
	return s.withLock(id, func() error {
		state, found, err := s.getUnlocked(id)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("oci runtime: container %q does not exist", id)
		}
		state.Status = "stopped"
		state.PID = 0
		state.ExitStatus = &status
		state.ExitedAt = &exitedAt
		_ = os.Remove(s.controlSocketPath(state))
		return s.put(state)
	})
}

func (s *Store) Start(ctx context.Context, id string) error {
	var state State
	if err := s.withLock(id, func() error {
		current, found, err := s.getUnlocked(id)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("oci runtime: container %q does not exist", id)
		}
		if current.Status != "created" || current.PID <= 0 || !processAlive(current.PID) {
			return errors.New("oci runtime: container has no live created supervisor")
		}
		state = current
		return nil
	}); err != nil {
		return err
	}
	socket := s.controlSocketPath(state)
	dialer := net.Dialer{Timeout: 5 * time.Second}
	connection, err := dialer.DialContext(ctx, "unix", socket)
	if err != nil {
		return fmt.Errorf("oci runtime: connect supervisor command socket: %w", err)
	}
	defer connection.Close()
	deadline := time.Now().Add(30 * time.Second)
	if value, ok := ctx.Deadline(); ok && value.Before(deadline) {
		deadline = value
	}
	if err := connection.SetDeadline(deadline); err != nil {
		return err
	}
	request := startResult{
		Command: "start", ID: id, Incarnation: stateIncarnation(state), PID: state.PID,
	}
	if err := json.NewEncoder(connection).Encode(request); err != nil {
		return fmt.Errorf("oci runtime: send supervisor start command: %w", err)
	}
	var response startResult
	if err := json.NewDecoder(io.LimitReader(connection, 4097)).Decode(&response); err != nil {
		return fmt.Errorf("oci runtime: read supervisor start response: %w", err)
	}
	if response.ID != request.ID || response.Incarnation != request.Incarnation ||
		response.PID != request.PID || response.Started == (response.Error != "") {
		return errors.New("oci runtime: invalid or stale supervisor start response")
	}
	if response.Started {
		return nil
	}
	return fmt.Errorf("oci runtime: supervisor failed to start MicroVM: %s", response.Error)
}

func (s *Store) Kill(ctx context.Context, id string, signal syscall.Signal) error {
	if !guestTerminationSignal(signal) {
		return fmt.Errorf("oci runtime: signal %d requires the guest-agent signal relay; only INT, QUIT, KILL, and TERM are currently supported", signal)
	}
	state, found, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("oci runtime: container %q does not exist", id)
	}
	if state.PID <= 0 || !processAlive(state.PID) {
		return errors.New("oci runtime: supervisor is not running")
	}
	connection, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", s.controlSocketPath(state))
	if err != nil {
		return fmt.Errorf("oci runtime: connect supervisor command socket: %w", err)
	}
	defer connection.Close()
	deadline := time.Now().Add(30 * time.Second)
	if value, ok := ctx.Deadline(); ok && value.Before(deadline) {
		deadline = value
	}
	if err := connection.SetDeadline(deadline); err != nil {
		return err
	}
	request := startResult{
		Command: "signal", ID: id, Incarnation: stateIncarnation(state),
		PID: state.PID, Signal: int(signal),
	}
	if err := json.NewEncoder(connection).Encode(request); err != nil {
		return fmt.Errorf("oci runtime: send supervisor signal command: %w", err)
	}
	var response startResult
	if err := json.NewDecoder(io.LimitReader(connection, 4097)).Decode(&response); err != nil {
		return fmt.Errorf("oci runtime: read supervisor signal response: %w", err)
	}
	if response.Command != "signal" || response.ID != request.ID ||
		response.Incarnation != request.Incarnation || response.PID != request.PID ||
		response.Signal != request.Signal || response.Signaled == (response.Error != "") {
		return errors.New("oci runtime: invalid or stale supervisor signal response")
	}
	if response.Signaled {
		return nil
	}
	return fmt.Errorf("oci runtime: supervisor rejected signal: %s", response.Error)
}

func guestTerminationSignal(signal syscall.Signal) bool {
	switch signal {
	case syscall.SIGINT, syscall.SIGQUIT, syscall.SIGKILL, syscall.SIGTERM:
		return true
	default:
		return false
	}
}

func stateIncarnation(state State) string {
	// Avoid an intermediate string allocation by formatting directly into
	// a byte slice.
	buf := make([]byte, 0)
	buf = fmt.Appendf(buf, "%s\x00%d\x00%s", state.ID, state.PID, state.Created.Format(time.RFC3339Nano))
	sum := sha256.Sum256(buf)
	return fmt.Sprintf("%x", sum[:16])
}

func (s *Store) controlSocketPath(state State) string {
	sum := sha256.Sum256([]byte(state.ID + "\x00" + stateIncarnation(state)))
	// sockaddr_un paths are limited to 108 bytes on Linux. Addressing the
	// already-open, confined state directory through procfs keeps the socket
	// pathname bounded even when the configured state root is deeply nested.
	return fmt.Sprintf("/proc/self/fd/%d/.ctl-%x.sock", s.dirHandle.Fd(), sum[:16])
}

func (s *Store) update(ctx context.Context, id string, mutate func(*State)) error {
	return s.withLock(id, func() error {
		state, found, err := s.getUnlocked(id)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("oci runtime: container %q does not exist", id)
		}
		mutate(&state)
		return s.put(state)
	})
}

func processAlive(pid int) bool {
	return pid > 0 && syscall.Kill(pid, 0) == nil
}

func (s *Store) withLock(id string, action func() error) error {
	if !idPattern.MatchString(id) {
		return fmt.Errorf("oci runtime: invalid container id %q", id)
	}
	lock, err := os.OpenFile(filepath.Join(s.dir, "."+id+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("oci runtime: lock container %q: %w", id, err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	return action()
}

func (s *Store) put(state State) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(s.dir, ".state-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(s.dir, state.ID+".json"))
}
