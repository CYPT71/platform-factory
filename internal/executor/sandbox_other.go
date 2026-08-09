//go:build !linux

package executor

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
)

// SandboxSupport reports which isolation facilities the current host
// offers. On non-Linux platforms nothing is available; the sandboxed
// executor fails closed instead of approximating isolation.
type SandboxSupport struct {
	UserNamespaces bool
	CgroupPIDs     bool
	CgroupCPU      bool
	cgroupDir      string
	Details        map[string]string
}

// ProbeSandbox reports that no sandbox facility exists on this
// platform. Namespaces and cgroups are Linux kernel features; the
// native macOS and Windows isolation paths are the v4 MicroVM work,
// not process sandboxing.
func ProbeSandbox() SandboxSupport {
	return SandboxSupport{Details: map[string]string{
		"user-namespaces": "not available on this platform (Linux kernel feature)",
		"cgroup-v2":       "not available on this platform (Linux kernel feature)",
	}}
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
	return nil, nil, errors.New("DNS relay socket pairs are not available on this platform")
}

func wrapWithSandboxHelper(*exec.Cmd, sandboxHelperPayload, bool, bool) error {
	return errors.New("sandboxed execution is not available on this platform")
}

type stageCgroup struct{}

func newStageCgroup(string, *exec.Cmd, int64, int64) (*stageCgroup, error) {
	return nil, errors.New("cgroup limits are not available on this platform")
}

func (g *stageCgroup) cleanup() {}

// MaybeApplySandboxHelper is a no-op on this platform. Safe to call
// unconditionally from main() on any platform.
func MaybeApplySandboxHelper(...DNSRelayServer) {}

// DNSRelayServer is unused where sandboxed execution is unavailable.
type DNSRelayServer func(context.Context, *net.UDPConn, net.Conn) error
