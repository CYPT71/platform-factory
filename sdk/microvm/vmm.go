package microvm

import (
	"context"
	"io"
	"time"
)

// APIVersion is the version of the native microVM lifecycle contract.
const APIVersion = "platform-factory.dev/vmm/v1alpha1"

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

// BootBundle pins every byte used to boot a guest.
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
