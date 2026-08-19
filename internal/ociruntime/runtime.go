//go:build linux

// Package ociruntime implements the persistent OCI lifecycle used by container
// engines to run MicroVM workloads.
package ociruntime

import (
	"bytes"
	"context"
	"crypto/rand"
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
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
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
	OCIVersion string `json:"ociVersion"`
	ID         string `json:"id"`
	Status     string `json:"status"`
	PID        int    `json:"pid"`
	// PIDStartTicks distinguishes this process from a later reuse of PID.
	// Zero preserves compatibility with state created before this field existed.
	PIDStartTicks int64             `json:"pidStartTicks,omitempty"`
	Bundle        string            `json:"bundle"`
	Created       time.Time         `json:"created"`
	ExitStatus    *uint32           `json:"exitStatus,omitempty"`
	ExitedAt      *time.Time        `json:"exitedAt,omitempty"`
	Annotations   map[string]string `json:"annotations,omitempty"`
	Metrics       *VMMMetrics       `json:"metrics,omitempty"`
}

// VMMMetrics is terminal, durable telemetry embedded in the canonical runtime
// state rather than maintained in a second sidecar store.
type VMMMetrics struct {
	APIVersion string `json:"api_version"`
	RuntimeMS  int64  `json:"runtime_ms"`
	MemoryMiB  int    `json:"memory_mib"`
	VCPUs      int    `json:"vcpus"`
	ExitStatus uint32 `json:"exit_status"`
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

// validSupervisorRequest binds a command to the host-visible supervisor
// identity even when the VMM is PID 1 in its namespace.
func validSupervisorRequest(request startResult, current, launched State, incarnation, command string) bool {
	return request.Command == command && request.ID == launched.ID &&
		launched.PID > 0 && request.PID == launched.PID && current.PID == launched.PID &&
		request.Incarnation == incarnation && stateIncarnation(current) == incarnation
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

// supervisorLogPath is the on-disk destination for a supervisor's stdout
// and stderr - its own diagnostics plus the guest's relayed serial console
// - kept independent of whatever pipe the short-lived CLI invocation that
// launched it happened to inherit. Callers pass an id already validated
// against idPattern, so it is safe as a filename.
func supervisorLogPath(store *Store, id string) string {
	return filepath.Join(store.Dir(), id+".supervisor.log")
}

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

// legacyAnnotationKeys preserves the documented secure-oci.dev -> platform-factory.dev
// rebrand compatibility window: a bundle still carrying pre-rebrand keys decodes
// exactly as if it had been written with the current ones. Literals keep this
// linux package independent of amd64-only constants.
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

// normalizeLegacyAnnotations fills current keys without overriding them.
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
		// Overlay root paths may live outside the bundle but remain confined.
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

// podmanContainerEnvMount accepts metadata mounts recreated inside the guest.
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
	state, found, err := s.readUnlocked(id)
	if err != nil || !found {
		return state, found, err
	}
	// Only the host PID namespace can reconcile the persisted supervisor PID.
	if state.PID > 0 && !processAlive(state.PID, state.PIDStartTicks) {
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

// readUnlocked reads persisted identity without attempting host-PID liveness
// reconciliation. It is the only safe read primitive inside a PID namespace.
func (s *Store) readUnlocked(id string) (State, bool, error) {
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
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil || state.ID != id {
		return State{}, false, errors.New("oci runtime: corrupt stored state")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return State{}, false, errors.New("oci runtime: corrupt stored state")
	}
	return state, true, nil
}

// readPersisted returns state without host-PID reconciliation. Supervisor code
// must use this method after entering its PID namespace.
func (s *Store) readPersisted(ctx context.Context, id string) (State, bool, error) {
	if err := ctx.Err(); err != nil {
		return State{}, false, err
	}
	var state State
	var found bool
	err := s.withLock(id, func() error {
		var err error
		state, found, err = s.readUnlocked(id)
		return err
	})
	return state, found, err
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
		if state.PID > 0 {
			// Signal the verified pidfd to avoid PID-reuse races.
			if fd, ok := openVerifiedPidfd(state.PID, state.PIDStartTicks); ok {
				if !force {
					_ = unix.Close(fd)
					return errors.New("oci runtime: cannot delete a live supervisor")
				}
				_ = unix.PidfdSendSignal(fd, syscall.SIGKILL, nil, 0)
				_ = unix.Close(fd)
			}
		}
		_ = os.Remove(s.controlSocketPath(state))
		_ = os.Remove(supervisorLogPath(s, id))
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
	// Missing start ticks retain the legacy existence-only liveness check.
	ticks, _ := processStartTicks(pid)
	return s.update(ctx, id, func(state *State) {
		state.PID = pid
		state.PIDStartTicks = ticks
	})
}

func (s *Store) SetStatus(ctx context.Context, id, status string) error {
	switch status {
	case "created", "running", "stopped":
	default:
		return fmt.Errorf("oci runtime: invalid state %q", status)
	}
	return s.update(ctx, id, func(state *State) {
		state.Status = status
		if status == "stopped" {
			_ = os.Remove(s.controlSocketPath(*state))
			state.PID = 0
		}
	})
}

// SetExited atomically publishes the terminal guest process result.
func (s *Store) SetExited(ctx context.Context, id string, status uint32, exitedAt time.Time) error {
	if exitedAt.IsZero() {
		return errors.New("oci runtime: exit time is required")
	}
	exitedAt = exitedAt.UTC()
	return s.update(ctx, id, func(state *State) {
		state.Status = "stopped"
		state.PID = 0
		state.ExitStatus = &status
		state.ExitedAt = &exitedAt
		memoryMiB, _ := strconv.Atoi(state.Annotations["platform-factory.dev/memory-mib"])
		vcpus, _ := strconv.Atoi(state.Annotations["platform-factory.dev/vcpus"])
		runtimeMS := exitedAt.Sub(state.Created).Milliseconds()
		if runtimeMS < 0 {
			runtimeMS = 0
		}
		state.Metrics = &VMMMetrics{
			APIVersion: "platform-factory.dev/vmm-metrics/v1", RuntimeMS: runtimeMS,
			MemoryMiB: memoryMiB, VCPUs: vcpus, ExitStatus: status,
		}
		_ = os.Remove(s.controlSocketPath(*state))
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
		if current.Status != "created" || current.PID <= 0 || !processAlive(current.PID, current.PIDStartTicks) {
			return errors.New("oci runtime: container has no live created supervisor")
		}
		state = current
		return nil
	}); err != nil {
		return err
	}
	request := startResult{
		Command: "start", ID: id, Incarnation: stateIncarnation(state), PID: state.PID,
	}
	response, err := s.supervisorCommand(ctx, state, request)
	if err != nil {
		if errors.Is(err, io.EOF) {
			// The supervisor accepted our connection and then closed it
			// without answering - it died before it could write a
			// response, rather than reporting a normal failure (which
			// would come back as a decoded error response instead of a
			// dropped connection). Point at its log instead of surfacing
			// a bare "EOF": see supervisorLogPath's own doc comment for
			// why writes to inherited stdio can kill it exactly this way.
			liveness := "supervisor process is no longer running"
			if processAlive(state.PID, state.PIDStartTicks) {
				liveness = "supervisor process is still running but stopped answering"
			}
			return fmt.Errorf("oci runtime: supervisor died before responding to start (%s); see %s: %w",
				liveness, supervisorLogPath(s, id), err)
		}
		return err
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
	if state.PID <= 0 || !processAlive(state.PID, state.PIDStartTicks) {
		return errors.New("oci runtime: supervisor is not running")
	}
	request := startResult{
		Command: "signal", ID: id, Incarnation: stateIncarnation(state),
		PID: state.PID, Signal: int(signal),
	}
	response, err := s.supervisorCommand(ctx, state, request)
	if err != nil {
		return err
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

func (s *Store) supervisorCommand(ctx context.Context, state State, request startResult) (startResult, error) {
	connection, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", s.controlSocketPath(state))
	if err != nil {
		return startResult{}, fmt.Errorf("oci runtime: connect supervisor command socket: %w", err)
	}
	defer connection.Close()
	deadline := time.Now().Add(30 * time.Second)
	if value, ok := ctx.Deadline(); ok && value.Before(deadline) {
		deadline = value
	}
	if err := connection.SetDeadline(deadline); err != nil {
		return startResult{}, err
	}
	if err := json.NewEncoder(connection).Encode(request); err != nil {
		return startResult{}, fmt.Errorf("oci runtime: send supervisor %s command: %w", request.Command, err)
	}
	var response startResult
	if err := json.NewDecoder(io.LimitReader(connection, 4097)).Decode(&response); err != nil {
		return startResult{}, fmt.Errorf("oci runtime: read supervisor %s response: %w", request.Command, err)
	}
	return response, nil
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
		state, found, err := s.readUnlocked(id)
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

// processAlive reports whether pid still identifies the recorded process.
func processAlive(pid int, startTicks int64) bool {
	fd, ok := openVerifiedPidfd(pid, startTicks)
	if ok {
		_ = unix.Close(fd)
	}
	return ok
}

// openVerifiedPidfd rejects PID reuse by matching the recorded start time.
// A zero startTicks accepts an existing pidfd for backward compatibility.
func openVerifiedPidfd(pid int, startTicks int64) (fd int, ok bool) {
	if pid <= 0 {
		return -1, false
	}
	pidfd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return -1, false
	}
	if startTicks == 0 {
		return pidfd, true
	}
	ticks, err := processStartTicks(pid)
	if err != nil || ticks != startTicks {
		_ = unix.Close(pidfd)
		return -1, false
	}
	return pidfd, true
}

// processStartTicks reads field 22 of /proc/<pid>/stat. It locates the final
// ')' because the preceding command field may contain spaces or parentheses.
func processStartTicks(pid int) (int64, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	line := string(data)
	closeParen := strings.LastIndexByte(line, ')')
	if closeParen < 0 || closeParen+2 > len(line) {
		return 0, fmt.Errorf("oci runtime: malformed /proc/%d/stat", pid)
	}
	const starttimeFieldAfterComm = 20
	fields := strings.Fields(line[closeParen+2:])
	if len(fields) < starttimeFieldAfterComm {
		return 0, fmt.Errorf("oci runtime: /proc/%d/stat has too few fields", pid)
	}
	ticks, err := strconv.ParseInt(fields[starttimeFieldAfterComm-1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("oci runtime: parse start ticks for pid %d: %w", pid, err)
	}
	return ticks, nil
}

// withLock and put resolve every path through s.root (an *os.Root opened
// once, at Store-creation time, while the caller still held every DAC
// capability - see OpenStore) rather than a fresh absolute os.OpenFile off
// s.dir. A container's supervisor process calls both again well into its
// own lifetime, from inside OnStarted, after applyVMMSandbox
// (supervisor_linux.go) has already dropped CAP_DAC_OVERRIDE/
// CAP_DAC_READ_SEARCH; a fresh absolute-path open at that point has to
// re-walk every ancestor directory down from "/", including a
// caller-supplied, non-root-owned state root parent (e.g.
// tests/microvm/test-containerd-kvm.sh's own work directory) that root can
// no longer traverse without those capabilities. Reusing s.root's
// already-open directory descriptor (openat relative to it) needs no such
// traversal - only permission on the state directory itself, which root
// still owns.
func (s *Store) withLock(id string, action func() error) error {
	if !idPattern.MatchString(id) {
		return fmt.Errorf("oci runtime: invalid container id %q", id)
	}
	lock, err := s.root.OpenFile("."+id+".lock", os.O_CREATE|os.O_RDWR, 0o600)
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
	var suffix [16]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return err
	}
	name := fmt.Sprintf(".state-%x", suffix)
	temporary, err := s.root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = s.root.Remove(name) }()
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
	if err := s.root.Rename(name, state.ID+".json"); err != nil {
		return err
	}
	if err := s.dirHandle.Sync(); err != nil {
		return fmt.Errorf("oci runtime: sync state directory: %w", err)
	}
	return nil
}
