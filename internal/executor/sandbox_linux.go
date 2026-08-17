//go:build linux

package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

const (
	sandboxHelperEnv = "PLATFORM_FACTORY_EXECUTOR_SANDBOX_HELPER"
	sandboxProbeEnv  = "PLATFORM_FACTORY_EXECUTOR_SANDBOX_PROBE"
	dnsRelayEnv      = "PLATFORM_FACTORY_EXECUTOR_DNS_RELAY"
)

// SandboxSupport reports which isolation facilities the current host
// offers, with an actionable reason for everything unavailable.
type SandboxSupport struct {
	UserNamespaces bool
	CgroupPIDs     bool
	CgroupCPU      bool
	// cgroupDir is this process's own writable cgroup v2 directory,
	// empty when cgroup limits are unavailable.
	cgroupDir string
	// Details maps a facility name to the reason it is unavailable.
	Details map[string]string
}

// ProbeSandbox checks, by construction rather than by version sniffing,
// whether this host can create the namespaces and cgroup limits the
// sandboxed executor needs.
func ProbeSandbox() SandboxSupport {
	support := SandboxSupport{Details: map[string]string{}}

	self, err := os.Executable()
	if err != nil {
		support.Details["user-namespaces"] = "resolve current executable: " + err.Error()
	} else {
		// The probe child does not just create the namespaces; it also
		// performs the mount operations the real sandbox depends on
		// (making mounts private, mounting a tmpfs). On hosts that create
		// the user namespace but restrict operations inside it — notably
		// Ubuntu's AppArmor unprivileged-userns restriction — the mount
		// fails, so the probe reports the sandbox as unavailable rather
		// than letting a stage fail mid-run.
		probe := exec.Command(self)
		probe.Env = []string{sandboxProbeEnv + "=1"}
		probe.SysProcAttr = sandboxSysProcAttr(os.Getuid(), os.Getgid(), 0, 0)
		if output, err := probe.CombinedOutput(); err != nil {
			support.Details["user-namespaces"] = strings.TrimSpace(
				fmt.Sprintf("cannot create or operate inside a user+mount+pid+net namespace: %v: %s", err, output))
		} else {
			support.UserNamespaces = true
		}
	}

	cgroupDir, err := ownCgroupV2Dir()
	if err != nil {
		support.Details["cgroup-v2"] = err.Error()
		return support
	}
	controllers, err := os.ReadFile(filepath.Join(cgroupDir, "cgroup.controllers"))
	if err != nil {
		support.Details["cgroup-v2"] = "read cgroup.controllers: " + err.Error()
		return support
	}
	available := map[string]bool{}
	for _, name := range strings.Fields(string(controllers)) {
		available[name] = true
	}
	// Measure the real capability by construction: create a throwaway
	// child cgroup and try to write each limit's interface file. A child
	// exposes pids.max / cpu.max only when the controller is present in
	// this cgroup's subtree_control, not merely in cgroup.controllers.
	// systemd's Delegate=yes delegates pids that way but commonly leaves
	// cpu out, so a check that trusted cgroup.controllers alone would
	// over-report and the stage would fail mid-run. The probe deliberately
	// does not enable the controller itself: doing so in a cgroup that
	// still holds member processes makes it reject the clone-into-cgroup
	// child the executor relies on (EOPNOTSUPP).
	probeDir := filepath.Join(cgroupDir, "platform-factory-probe")
	if err := os.Mkdir(probeDir, 0o755); err != nil {
		support.Details["cgroup-delegation"] = "cannot create a child cgroup (run under a delegated cgroup or as root): " + err.Error()
		return support
	}
	defer func() { _ = os.Remove(probeDir) }()
	support.cgroupDir = cgroupDir
	if cgroupLimitWritable(probeDir, "pids.max", "max") {
		support.CgroupPIDs = true
	} else {
		support.Details["cgroup-pids"] = cgroupControllerReason("pids", available["pids"])
	}
	if cgroupLimitWritable(probeDir, "cpu.max", "max 100000") {
		support.CgroupCPU = true
	} else {
		support.Details["cgroup-cpu"] = cgroupControllerReason("cpu", available["cpu"])
	}
	return support
}

// cgroupLimitWritable reports whether a limit interface file can actually
// be written in a freshly created child cgroup, which is the real test of
// whether the controller is delegated here. The value written is the
// controller's own unlimited default, so a success leaves no lingering
// restriction on the throwaway cgroup.
func cgroupLimitWritable(childDir, file, unlimited string) bool {
	return os.WriteFile(filepath.Join(childDir, file), []byte(unlimited), 0o644) == nil
}

func cgroupControllerReason(name string, present bool) string {
	if !present {
		return fmt.Sprintf("%s controller is not available to this cgroup", name)
	}
	return fmt.Sprintf(
		"%s controller is present but not delegated to child cgroups; enable it in cgroup.subtree_control (its parent must have no direct member processes) or run under a scope that delegates it",
		name)
}

func ownCgroupV2Dir() (string, error) {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) == 3 && parts[0] == "0" && parts[1] == "" {
			dir := filepath.Join("/sys/fs/cgroup", parts[2])
			if _, err := os.Stat(filepath.Join(dir, "cgroup.controllers")); err == nil {
				return dir, nil
			}
			return "", fmt.Errorf("cgroup v2 path %s has no cgroup.controllers (v1 hierarchy or hybrid host)", dir)
		}
	}
	return "", fmt.Errorf("no cgroup v2 entry in /proc/self/cgroup (host uses cgroup v1)")
}

func sandboxSysProcAttr(hostUID, hostGID, mapUID, mapGID int, isolateNetwork ...bool) *syscall.SysProcAttr {
	cloneFlags := uintptr(syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS | syscall.CLONE_NEWPID |
		syscall.CLONE_NEWNET | syscall.CLONE_NEWIPC | syscall.CLONE_NEWUTS)
	if len(isolateNetwork) > 0 && !isolateNetwork[0] {
		cloneFlags &^= syscall.CLONE_NEWNET
	}
	return &syscall.SysProcAttr{
		Cloneflags:                 cloneFlags,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: mapUID, HostID: hostUID, Size: 1}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: mapGID, HostID: hostGID, Size: 1}},
		GidMappingsEnableSetgroups: false,
	}
}

type sandboxBindMount struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"read_only"`
}

type sandboxSecretFile struct {
	Target string `json:"target"`
	Value  []byte `json:"value"`
}

type sandboxHelperPayload struct {
	Root            string              `json:"root"`
	WorkingDir      string              `json:"working_dir"`
	Executable      string              `json:"executable"`
	Args            []string            `json:"args"`
	MemoryBytes     int64               `json:"memory_bytes,omitempty"`
	ReadOnlyRoot    bool                `json:"read_only_root,omitempty"`
	NoNewPrivileges bool                `json:"no_new_privileges,omitempty"`
	Mounts          []sandboxBindMount  `json:"mounts,omitempty"`
	Secrets         []sandboxSecretFile `json:"secrets,omitempty"`
	DNSRelayFD      int                 `json:"dns_relay_fd,omitempty"`
}

func dnsSocketPair() (*os.File, *os.File, error) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_SEQPACKET|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	return os.NewFile(uintptr(fds[0]), "dns-host"), os.NewFile(uintptr(fds[1]), "dns-sandbox"), nil
}

// wrapWithSandboxHelper rewrites cmd to re-exec the current binary
// inside fresh user, mount, PID, network, IPC and UTS namespaces.
// MaybeApplySandboxHelper — which the consuming binary must call at the
// very start of main() — finishes the sandbox from inside: it builds
// the mount tree, pivots into the stage root and execs the target.
func wrapWithSandboxHelper(cmd *exec.Cmd, payload sandboxHelperPayload, nonRoot, isolateNetwork bool) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve current executable: %w", err)
	}
	payload.Executable = cmd.Path
	payload.Args = cmd.Args[1:]
	payload.NoNewPrivileges = nonRoot
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	// The namespace-initial process must map to uid 0 in its own user
	// namespace: after the self-exec it needs CAP_SYS_ADMIN (kept across
	// execve only for uid 0) to mount, pivot_root and sethostname.
	// Namespace uid 0 maps to the caller's unprivileged host uid, so it
	// holds no privilege on the host. An unprivileged single-uid
	// namespace cannot also expose a second in-namespace uid, so NonRoot
	// is honored with no_new_privs rather than a uid switch.
	cmd.Path = self
	cmd.Args = []string{self}
	cmd.Env = append(cmd.Env, sandboxHelperEnv+"="+string(encoded))
	cmd.SysProcAttr = sandboxSysProcAttr(os.Getuid(), os.Getgid(), 0, 0, isolateNetwork)
	return nil
}

// MaybeApplySandboxHelper must be called at the very start of main() by
// any binary that uses the sandboxed executor. Inside the freshly
// created namespaces it assembles the stage filesystem, brings up the
// loopback interface, applies the memory ceiling and execs the real
// target; it never returns on the helper path. It exits with a message
// naming the missing kernel facility when a step fails.
// DNSRelayServer serves the sandbox-side DNS transport. The composition root
// supplies the concrete networking adapter so executor does not depend on an
// infrastructure implementation.
type DNSRelayServer func(context.Context, *net.UDPConn, net.Conn) error

func MaybeApplySandboxHelper(relayServers ...DNSRelayServer) {
	if os.Getenv(dnsRelayEnv) != "" {
		var relayServer DNSRelayServer
		if len(relayServers) > 0 {
			relayServer = relayServers[0]
		}
		os.Exit(runDNSRelay(relayServer))
	}
	if os.Getenv(sandboxProbeEnv) != "" {
		os.Exit(probeSandboxOperations())
	}
	raw := os.Getenv(sandboxHelperEnv)
	if raw == "" {
		return
	}
	os.Unsetenv(sandboxHelperEnv)
	var payload sandboxHelperPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		sandboxFatal("invalid sandbox helper payload", err)
	}
	if err := enterSandbox(payload); err != nil {
		sandboxFatal("enter sandbox", err)
	}
	resolved, err := exec.LookPath(payload.Executable)
	if err != nil {
		sandboxFatal("resolve executable", err)
	}
	argv := append([]string{payload.Executable}, payload.Args...)
	if err := syscall.Exec(resolved, argv, os.Environ()); err != nil {
		sandboxFatal("exec", err)
	}
}

func sandboxFatal(what string, err error) {
	fmt.Fprintf(os.Stderr, "executor sandbox: %s: %v\n", what, err)
	os.Exit(125)
}

// probeSandboxOperations runs inside the probe child (already in fresh
// user/mount/pid/net namespaces via SysProcAttr). It performs the same
// mount operations enterSandbox depends on and returns 0 only when they
// all succeed, so ProbeSandbox reflects whether the sandbox can actually
// function on this host rather than merely whether the namespaces can be
// created.
func probeSandboxOperations() int {
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		fmt.Fprintf(os.Stderr, "make mounts private: %v\n", err)
		return 1
	}
	dir, err := os.MkdirTemp("", "platform-factory-probe-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create probe dir: %v\n", err)
		return 1
	}
	defer os.RemoveAll(dir)
	if err := syscall.Mount("tmpfs", dir, "tmpfs", syscall.MS_NOSUID|syscall.MS_NODEV, "size=1m"); err != nil {
		fmt.Fprintf(os.Stderr, "mount tmpfs: %v\n", err)
		return 1
	}
	_ = syscall.Unmount(dir, syscall.MNT_DETACH)
	return 0
}

func enterSandbox(payload sandboxHelperPayload) error {
	// Setting the hostname is cosmetic and gated by CAP_SYS_ADMIN in the
	// UTS namespace, which some hosts (Ubuntu's AppArmor unprivileged
	// userns restriction) deny even inside a fresh namespace. It is not a
	// security boundary, so a failure here is ignored rather than fatal;
	// ProbeSandbox already gates on the mount operations that matter.
	_ = syscall.Sethostname([]byte("platform-factory-stage"))
	if err := bringUpLoopback(); err != nil {
		return fmt.Errorf("bring up loopback in the new network namespace: %w", err)
	}
	// Stop mount events from propagating back to the host.
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("make mounts private (CLONE_NEWNS): %w", err)
	}
	root := payload.Root
	if err := syscall.Mount(root, root, "", syscall.MS_BIND|syscall.MS_REC, ""); err != nil {
		return fmt.Errorf("bind stage root: %w", err)
	}
	for _, mount := range payload.Mounts {
		target := filepath.Join(root, strings.TrimPrefix(mount.Target, "/"))
		if err := os.MkdirAll(target, 0o755); err != nil {
			return fmt.Errorf("create mount target %s: %w", mount.Target, err)
		}
		if err := syscall.Mount(mount.Source, target, "", syscall.MS_BIND|syscall.MS_REC, ""); err != nil {
			return fmt.Errorf("bind mount %s: %w", mount.Target, err)
		}
		if mount.ReadOnly {
			// The bind source may itself sit on a filesystem mounted
			// nosuid/nodev/noexec by a more privileged namespace (e.g. the
			// host's own bind mount of this workspace into a dev
			// container). The kernel locks those flags onto the bind
			// mount and rejects a remount that doesn't re-assert every
			// one of them - including MS_REC, since the bind above was
			// recursive - with EPERM, so query the flags it actually
			// carries rather than assuming none apply.
			if err := syscall.Mount("", target, "",
				syscall.MS_REMOUNT|syscall.MS_BIND|syscall.MS_REC|syscall.MS_RDONLY|lockedMountFlags(target), ""); err != nil {
				return fmt.Errorf("remount %s read-only: %w", mount.Target, err)
			}
		}
	}
	for _, dir := range []string{"proc", "tmp", "dev"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			return fmt.Errorf("create /%s: %w", dir, err)
		}
	}
	if err := syscall.Mount("proc", filepath.Join(root, "proc"), "proc", 0, ""); err != nil {
		return fmt.Errorf("mount /proc in the new PID namespace: %w", err)
	}
	if err := syscall.Mount("tmpfs", filepath.Join(root, "tmp"), "tmpfs", syscall.MS_NOSUID|syscall.MS_NODEV, "size=64m"); err != nil {
		return fmt.Errorf("mount tmpfs on /tmp: %w", err)
	}
	if payload.DNSRelayFD > 0 {
		if err := installSandboxResolver(root); err != nil {
			return err
		}
	}
	if len(payload.Secrets) > 0 {
		if err := materializeSandboxSecrets(root, payload.Secrets); err != nil {
			return err
		}
	}
	oldRoot := filepath.Join(root, ".platform-factory-oldroot")
	if err := os.MkdirAll(oldRoot, 0o700); err != nil {
		return fmt.Errorf("create pivot directory: %w", err)
	}
	if err := syscall.PivotRoot(root, oldRoot); err != nil {
		return fmt.Errorf("pivot_root into the stage root: %w", err)
	}
	if err := os.Chdir("/"); err != nil {
		return fmt.Errorf("chdir into the new root: %w", err)
	}
	if err := syscall.Unmount("/.platform-factory-oldroot", syscall.MNT_DETACH); err != nil {
		return fmt.Errorf("detach the old root: %w", err)
	}
	_ = os.Remove("/.platform-factory-oldroot")
	if payload.ReadOnlyRoot {
		// Same locked-flags requirement as the input-mount remount above.
		if err := syscall.Mount("", "/", "",
			syscall.MS_REMOUNT|syscall.MS_BIND|syscall.MS_REC|syscall.MS_RDONLY|lockedMountFlags("/"), ""); err != nil {
			return fmt.Errorf("remount the root read-only: %w", err)
		}
	}
	workingDir := payload.WorkingDir
	if workingDir == "" {
		workingDir = "/"
	}
	if err := os.MkdirAll(workingDir, 0o755); err != nil && !payload.ReadOnlyRoot {
		return fmt.Errorf("create working directory %s: %w", workingDir, err)
	}
	if err := os.Chdir(workingDir); err != nil {
		return fmt.Errorf("enter working directory %s: %w", workingDir, err)
	}
	if payload.DNSRelayFD > 0 {
		if err := startSandboxDNSRelay(payload.DNSRelayFD); err != nil {
			return err
		}
	}
	if payload.MemoryBytes > 0 {
		limit := syscall.Rlimit{Cur: uint64(payload.MemoryBytes), Max: uint64(payload.MemoryBytes)}
		if err := syscall.Setrlimit(syscall.RLIMIT_AS, &limit); err != nil {
			return fmt.Errorf("set RLIMIT_AS: %w", err)
		}
	}
	if payload.NoNewPrivileges {
		if _, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, prSetNoNewPrivs, 1, 0, 0, 0, 0); errno != 0 {
			return fmt.Errorf("set no_new_privs: %w", errno)
		}
	}
	return nil
}

// lockedMountFlags reports which of MS_NOSUID, MS_NODEV and MS_NOEXEC are
// currently set on the mount at path. The kernel locks these flags onto a
// mount inherited (directly or via a recursive bind) from a more privileged
// namespace, and an unprivileged remount that omits any locked flag is
// rejected with EPERM rather than treated as "leave it as is" - so callers
// remounting such a mount (e.g. to add MS_RDONLY) must OR this in. Statfs's
// ST_NOSUID/ST_NODEV/ST_NOEXEC bits share the same numeric values as the
// corresponding MS_* mount flags, so the field can be masked directly. A
// failed lookup yields no extra flags, leaving the remount's error, if any,
// to surface on its own.
func lockedMountFlags(path string) uintptr {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0
	}
	return uintptr(st.Flags) & (syscall.MS_NOSUID | syscall.MS_NODEV | syscall.MS_NOEXEC)
}

func installSandboxResolver(root string) error {
	target := filepath.Join(root, "etc", "resolv.conf")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create resolver directory: %w", err)
	}
	if _, err := os.Stat(target); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(target, nil, 0o644); err != nil {
			return fmt.Errorf("create resolver target: %w", err)
		}
	}
	source, err := os.CreateTemp(root, ".platform-factory-resolv-*")
	if err != nil {
		return fmt.Errorf("create resolver configuration: %w", err)
	}
	sourceName := source.Name()
	defer os.Remove(sourceName)
	if _, err := source.WriteString("nameserver 127.0.0.1\noptions attempts:1 timeout:1\n"); err != nil {
		source.Close()
		return fmt.Errorf("write resolver configuration: %w", err)
	}
	if err := source.Close(); err != nil {
		return fmt.Errorf("close resolver configuration: %w", err)
	}
	if err := syscall.Mount(sourceName, target, "", syscall.MS_BIND, ""); err != nil {
		return fmt.Errorf("bind resolver configuration: %w", err)
	}
	if err := syscall.Mount("", target, "", syscall.MS_REMOUNT|syscall.MS_BIND|syscall.MS_RDONLY, ""); err != nil {
		return fmt.Errorf("make resolver configuration read-only: %w", err)
	}
	return nil
}

func startSandboxDNSRelay(fd int) error {
	relayFile := os.NewFile(uintptr(fd), "dns-relay")
	if relayFile == nil {
		return errors.New("open inherited DNS relay descriptor")
	}
	readyReader, readyWriter, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("create DNS relay readiness pipe: %w", err)
	}
	defer readyReader.Close()
	cmd := exec.Command("/proc/self/exe")
	cmd.Env = append(filteredEnvironment(sandboxHelperEnv, sandboxProbeEnv), dnsRelayEnv+"=1")
	cmd.ExtraFiles = []*os.File{relayFile, readyWriter}
	if err := cmd.Start(); err != nil {
		readyWriter.Close()
		return fmt.Errorf("start DNS relay: %w", err)
	}
	_ = relayFile.Close()
	_ = readyWriter.Close()
	ready := []byte{0}
	if _, err := io.ReadFull(readyReader, ready); err != nil || ready[0] != 1 {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if err == nil {
			err = errors.New("relay rejected initialization")
		}
		return fmt.Errorf("wait for DNS relay: %w", err)
	}
	// Reap the relay process when the inherited transport closes with the
	// stage. The stage itself must not block on relay shutdown.
	go func() { _ = cmd.Wait() }()
	return nil
}

func runDNSRelay(relayServer DNSRelayServer) int {
	if relayServer == nil {
		return 125
	}
	relayFile := os.NewFile(3, "dns-relay")
	ready := os.NewFile(4, "dns-ready")
	relay, err := net.FileConn(relayFile)
	_ = relayFile.Close()
	if err != nil {
		_ = ready.Close()
		return 125
	}
	listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 53})
	if err != nil {
		_ = relay.Close()
		_ = ready.Close()
		return 125
	}
	_, _ = ready.Write([]byte{1})
	_ = ready.Close()
	if err := relayServer(context.Background(), listener, relay); err != nil {
		return 125
	}
	return 0
}

func filteredEnvironment(names ...string) []string {
	blocked := make(map[string]bool, len(names))
	for _, name := range names {
		blocked[name] = true
	}
	var env []string
	for _, item := range os.Environ() {
		name, _, _ := strings.Cut(item, "=")
		if !blocked[name] && name != dnsRelayEnv {
			env = append(env, item)
		}
	}
	return env
}

const prSetNoNewPrivs = 38

// materializeSandboxSecrets writes secret values into a dedicated
// tmpfs, so they live only in memory inside this mount namespace and
// vanish with it. Each secret target is bind-mounted read-only from
// the tmpfs file onto its declared path.
func materializeSandboxSecrets(root string, secrets []sandboxSecretFile) error {
	secretsDir := filepath.Join(root, ".platform-factory-secrets")
	if err := os.MkdirAll(secretsDir, 0o700); err != nil {
		return fmt.Errorf("create secrets directory: %w", err)
	}
	if err := syscall.Mount("tmpfs", secretsDir, "tmpfs", syscall.MS_NOSUID|syscall.MS_NODEV, "size=1m,mode=0700"); err != nil {
		return fmt.Errorf("mount secrets tmpfs: %w", err)
	}
	for index, secret := range secrets {
		source := filepath.Join(secretsDir, fmt.Sprintf("secret-%d", index))
		if err := os.WriteFile(source, secret.Value, 0o400); err != nil {
			return fmt.Errorf("write secret %s: %w", secret.Target, err)
		}
		target := filepath.Join(root, strings.TrimPrefix(secret.Target, "/"))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("create secret parent for %s: %w", secret.Target, err)
		}
		if err := os.WriteFile(target, nil, 0o400); err != nil && !os.IsExist(err) {
			return fmt.Errorf("create secret mount point %s: %w", secret.Target, err)
		}
		if err := syscall.Mount(source, target, "", syscall.MS_BIND, ""); err != nil {
			return fmt.Errorf("bind secret %s: %w", secret.Target, err)
		}
		if err := syscall.Mount("", target, "",
			syscall.MS_REMOUNT|syscall.MS_BIND|syscall.MS_RDONLY, ""); err != nil {
			return fmt.Errorf("remount secret %s read-only: %w", secret.Target, err)
		}
	}
	return nil
}

// bringUpLoopback sets IFF_UP on lo with raw ioctls, so stages can use
// localhost tooling while CLONE_NEWNET guarantees there is no route
// out of the namespace.
func bringUpLoopback() error {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	type ifreq struct {
		name  [16]byte
		flags uint16
		pad   [22]byte
	}
	request := ifreq{}
	copy(request.name[:], "lo")
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd),
		syscall.SIOCGIFFLAGS, uintptr(unsafe.Pointer(&request))); errno != 0 {
		return errno
	}
	const iffUp = 0x1
	request.flags |= iffUp
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd),
		syscall.SIOCSIFFLAGS, uintptr(unsafe.Pointer(&request))); errno != 0 {
		return errno
	}
	return nil
}
