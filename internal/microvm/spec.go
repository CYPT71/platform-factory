// Package microvm owns the contracts used by the in-process microVM domain.
// Public SDK values are translated at composition boundaries; internal code
// never depends on the SDK representation.
package microvm

import (
	"context"
	"fmt"
	"io"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const APIVersion = "platform-factory.dev/vmm/v1alpha1"

var NamePattern = regexp.MustCompile(`^[a-z0-9](?:[-a-z0-9]{0,61}[a-z0-9])?$`)

type Spec struct {
	Name      string
	Namespace string
	Image     string
	Layout    string
	Arch      string
	Listen    string
	MemoryMiB int
	VCPUs     int
	Port      int
	Forwards  []Forward
}

func (s Spec) ValidateCommon() error {
	if !NamePattern.MatchString(s.Name) {
		return fmt.Errorf("name must be a DNS label of at most 63 characters")
	}
	if s.MemoryMiB < 64 || s.MemoryMiB > 1<<20 {
		return fmt.Errorf("memory must be between 64 and 1048576 MiB")
	}
	if s.VCPUs < 1 || s.VCPUs > 256 {
		return fmt.Errorf("vcpus must be between 1 and 256")
	}
	if s.Port < 1 || s.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	if s.Listen != "127.0.0.1" && s.Listen != "0.0.0.0" {
		return fmt.Errorf("listen address must be 127.0.0.1 or 0.0.0.0")
	}
	return nil
}

type Forward struct {
	HostIP    string
	HostPort  int
	GuestPort int
	Protocol  string
}

func ParseForward(value string) (Forward, error) {
	if value == "" || strings.ContainsAny(value, "\x00 \t\r\n") {
		return Forward{}, fmt.Errorf("invalid publish value %q", value)
	}
	protocol := "tcp"
	address := value
	if before, after, found := strings.Cut(value, "/"); found {
		address, protocol = before, after
	}
	if protocol != "tcp" && protocol != "udp" {
		return Forward{}, fmt.Errorf("publish protocol must be tcp or udp")
	}
	parts, err := splitAddress(address)
	if err != nil {
		return Forward{}, err
	}
	result := Forward{Protocol: protocol}
	switch len(parts) {
	case 1:
		result.GuestPort, err = parsePort(parts[0])
		result.HostPort = result.GuestPort
	case 2:
		result.HostPort, err = parsePort(parts[0])
		if err == nil {
			result.GuestPort, err = parsePort(parts[1])
		}
	case 3:
		result.HostIP = strings.Trim(parts[0], "[]")
		if _, parseErr := netip.ParseAddr(result.HostIP); parseErr != nil {
			return Forward{}, fmt.Errorf("invalid publish host IP %q", result.HostIP)
		}
		result.HostPort, err = parsePort(parts[1])
		if err == nil {
			result.GuestPort, err = parsePort(parts[2])
		}
	default:
		err = fmt.Errorf("publish must be CONTAINER, HOST:CONTAINER, or IP:HOST:CONTAINER")
	}
	if err != nil {
		return Forward{}, err
	}
	return result, nil
}

func splitAddress(value string) ([]string, error) {
	if strings.HasPrefix(value, "[") {
		end := strings.Index(value, "]")
		if end < 0 || end+1 >= len(value) || value[end+1] != ':' {
			return nil, fmt.Errorf("invalid bracketed publish address")
		}
		return append([]string{value[:end+1]}, strings.Split(value[end+2:], ":")...), nil
	}
	return strings.Split(value, ":"), nil
}

func parsePort(value string) (int, error) {
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("port must be between 1 and 65535")
	}
	return port, nil
}

type MachineState string

const (
	StateCreated MachineState = "created"
	StateRunning MachineState = "running"
	StateStopped MachineState = "stopped"
	StateFailed  MachineState = "failed"
)

type Port struct {
	Protocol string `json:"protocol"`
	Host     uint16 `json:"host"`
	Guest    uint16 `json:"guest"`
	HostIP   string `json:"host_ip,omitempty"`
}

type Volume struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"read_only,omitempty"`
}

type Resources struct {
	VCPUs     uint32 `json:"vcpus"`
	MemoryMiB uint64 `json:"memory_mib"`
}

type BootBundle struct {
	APIVersion  string            `json:"api_version"`
	Digest      string            `json:"digest"`
	Kernel      string            `json:"kernel_digest"`
	Initrd      string            `json:"initrd_digest,omitempty"`
	RootFS      string            `json:"rootfs_digest"`
	CommandLine []string          `json:"command_line,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

type MachineSpec struct {
	ID        string            `json:"id"`
	Bundle    BootBundle        `json:"bundle"`
	Resources Resources         `json:"resources"`
	Ports     []Port            `json:"ports,omitempty"`
	Volumes   []Volume          `json:"volumes,omitempty"`
	DNS       []string          `json:"dns,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
}

type MachineStatus struct {
	ID        string       `json:"id"`
	State     MachineState `json:"state"`
	PID       int          `json:"pid,omitempty"`
	CreatedAt time.Time    `json:"created_at"`
	UpdatedAt time.Time    `json:"updated_at"`
	Error     string       `json:"error,omitempty"`
}

type Device interface {
	ID() string
	Attach(context.Context, Machine) error
	Detach(context.Context, Machine) error
}
type GuestAgent interface {
	Exec(context.Context, []string, io.Reader, io.Writer, io.Writer) (int, error)
	Signal(context.Context, string) error
	Shutdown(context.Context) error
}
type Machine interface {
	ID() string
	Start(context.Context) error
	Stop(context.Context) error
	Status(context.Context) (MachineStatus, error)
	Logs(context.Context, io.Writer) error
	Agent(context.Context) (GuestAgent, error)
}
type VMM interface {
	Name() string
	Probe(context.Context) (Capabilities, error)
	Create(context.Context, MachineSpec) (Machine, error)
	Load(context.Context, string) (Machine, error)
	Delete(context.Context, string) error
}

type Capabilities struct {
	Available    bool              `json:"available"`
	Architecture string            `json:"architecture"`
	Features     map[string]bool   `json:"features,omitempty"`
	Details      map[string]string `json:"details,omitempty"`
}

type StateStore interface {
	Put(context.Context, MachineStatus) error
	Get(context.Context, string) (MachineStatus, bool, error)
	Delete(context.Context, string) error
	List(context.Context) ([]MachineStatus, error)
}

func Validate(s Spec, backend string) error {
	if err := s.ValidateCommon(); err != nil {
		return err
	}
	if backend != "native" {
		return fmt.Errorf("backend must be native")
	}
	if strings.TrimSpace(s.Layout) == "" {
		return fmt.Errorf("native backend requires a layout")
	}
	return nil
}

func ValidateNativeTarget(s Spec) error {
	if !NamePattern.MatchString(s.Name) {
		return fmt.Errorf("name must be a DNS label of at most 63 characters")
	}
	return nil
}

func NativeEnvironment(s Spec) []string {
	forwards := make([]string, 0, len(s.Forwards))
	for _, forward := range s.Forwards {
		hostIP := forward.HostIP
		if hostIP == "" {
			hostIP = s.Listen
		}
		forwards = append(forwards, fmt.Sprintf("%s|%s|%d|%d", forward.Protocol, hostIP, forward.HostPort, forward.GuestPort))
	}
	return []string{"MICROVM_MEMORY=" + strconv.Itoa(s.MemoryMiB) + "M", "MICROVM_SMP=" + strconv.Itoa(s.VCPUs), "MICROVM_HOST_ADDRESS=" + s.Listen, "MICROVM_FORWARDS=" + strings.Join(forwards, ";")}
}
