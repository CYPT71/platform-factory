//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	taskapi "github.com/containerd/containerd/api/runtime/task/v3"
	tasktypes "github.com/containerd/containerd/api/types/task"
	"github.com/containerd/containerd/v2/core/mount"
	"github.com/containerd/containerd/v2/pkg/sys/reaper"
	"github.com/containerd/errdefs"
	"github.com/containerd/ttrpc"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// secureOCIRuntimeBinary is resolved once at startup: the shim shells out
// to the same OCI-runtime-spec CLI facade Podman already drives in
// production (see cmd/platform-factory-runtime), rather than reimplementing any
// part of MicroVM lifecycle management itself.
const secureOCIRuntimeBinary = "platform-factory-runtime"

// ociState mirrors the JSON internal/ociruntime.State encodes; it is the
// only contract between this shim and platform-factory-runtime's "state" verb.
type ociState struct {
	ID         string     `json:"id"`
	Status     string     `json:"status"`
	PID        int        `json:"pid"`
	Bundle     string     `json:"bundle"`
	Created    time.Time  `json:"created"`
	ExitStatus *uint32    `json:"exitStatus,omitempty"`
	ExitedAt   *time.Time `json:"exitedAt,omitempty"`
}

// taskService implements containerd's runtime v2 Task TTRPC service by
// translating each call into an invocation of platform-factory-runtime. Every
// container this shim creates is its own independently isolated MicroVM;
// this type holds only enough bookkeeping (bundle paths, group membership)
// to route calls, none of it VM lifecycle state.
type taskService struct {
	mu    sync.Mutex
	tasks map[string]*taskState
}

type taskState struct {
	bundle string
}

func newTaskService() *taskService {
	return &taskService{tasks: make(map[string]*taskState)}
}

func (s *taskService) RegisterTTRPC(server *ttrpc.Server) error {
	taskapi.RegisterTTRPCTaskService(server, s)
	return nil
}

// runtimeCommand deliberately does not tie the child process to any TTRPC
// request's ctx: a request's deadline reflects how long that caller is
// willing to wait for a *response*, not how long platform-factory-runtime is
// allowed to run. Booting a MicroVM guest can legitimately take longer than
// a short client timeout (crictl defaults to 2s); tying the child to ctx
// would let a caller giving up on the RPC SIGKILL an in-flight, otherwise
// healthy boot out from under the guest instead of just abandoning the wait.
//
// It spawns and waits on the child through reaper.Default, never a plain
// cmd.Run()/cmd.Wait(): shim.Run (main.go) already made this process a
// child subreaper and installed its own SIGCHLD handler
// (pkg/shim/shim_unix.go's reaper.Reap loop) that reaps every exited
// descendant via wait4(-1, ...) - including ones a plain os/exec call
// elsewhere in this same process spawned. That handler and a direct
// cmd.Wait() race for the same child's exit status; whichever loses gets
// ECHILD ("waitid: no child processes"), which is exactly what surfaced
// here intermittently. reaper.Default.Start subscribes to the shared
// reaper's exit-event channel before starting the process, so this
// command's own Wait always observes the exit this same reaper already
// claimed instead of trying to claim it a second time.
func runtimeCommand(args ...string) ([]byte, error) {
	cmd := exec.Command(secureOCIRuntimeBinary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	exits, err := reaper.Default.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("%s %v: %w", secureOCIRuntimeBinary, args, err)
	}
	status, err := reaper.Default.Wait(cmd, exits)
	if err != nil {
		return nil, fmt.Errorf("%s %v: %w", secureOCIRuntimeBinary, args, err)
	}
	if status != 0 {
		return nil, fmt.Errorf("%s %v: exit status %d (%s)", secureOCIRuntimeBinary, args, status, bytes.TrimSpace(stderr.Bytes()))
	}
	return stdout.Bytes(), nil
}

// runtimeCreateCommand runs "platform-factory-runtime create" with its stdout and
// stderr wired directly to the container's own I/O streams instead of
// captured into a buffer: platform-factory-runtime's supervisor process inherits
// these same fds when create spawns it (internal/ociruntime sets
// command.Stdout/Stderr = os.Stdout/os.Stderr for the supervisor it launches)
// and keeps writing the guest's console to them for the container's entire
// lifetime - this is the exact mechanism Podman's own "podman logs" already
// depends on for the same runtime, so this shim only needs to point
// create's own stdio at what containerd gave it in the CreateTaskRequest.
//
// stdout/stderr must be real *os.File values, not merely io.Writer: when
// exec.Cmd.Stdout/Stderr is a plain io.Writer, os/exec allocates its own
// internal pipe and a goroutine that copies from it, and cmd.Wait() blocks
// until that pipe's write end sees EOF. The detached supervisor this
// command spawns inherits that same pipe and holds it open for the
// container's entire lifetime, so that EOF - and this call - would never
// arrive. A real *os.File is dup'd onto the child's fd directly with no
// such goroutine, so cmd.Wait() only waits for this one process to exit.
// This waits only for the short-lived "create" CLI process itself, not
// the long-lived, Setsid-detached supervisor it spawns internally (see
// LaunchSupervisor) - once "create" exits, that supervisor reparents to
// this shim process (the nearest subreaper in its ancestry; see
// runtimeCommand's own doc comment on why this process is one at all)
// and is reaped independently, on its own schedule, by the same shared
// reaper this function already goes through below.
func runtimeCreateCommand(stdout, stderr *os.File, args ...string) error {
	cmd := exec.Command(secureOCIRuntimeBinary, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	exits, err := reaper.Default.Start(cmd)
	if err != nil {
		return fmt.Errorf("%s %v: %w", secureOCIRuntimeBinary, args, err)
	}
	status, err := reaper.Default.Wait(cmd, exits)
	if err != nil {
		return fmt.Errorf("%s %v: %w", secureOCIRuntimeBinary, args, err)
	}
	if status != 0 {
		return fmt.Errorf("%s %v: exit status %d", secureOCIRuntimeBinary, args, status)
	}
	return nil
}

// openContainerIO opens a stdio path from a CreateTaskRequest for writing,
// as a real *os.File (see runtimeCreateCommand). containerd's own client has
// already created the named pipe at this path and is blocked opening it for
// reading before it will even attempt the subsequent Start call - never
// opening it here is what left Start permanently undelivered.
func openContainerIO(path string) (*os.File, func(), error) {
	if path == "" {
		devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		if err != nil {
			return nil, nil, fmt.Errorf("platform-factory-shim: open %s: %w", os.DevNull, err)
		}
		return devNull, func() { devNull.Close() }, nil
	}
	w, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("platform-factory-shim: open %s for writing: %w", path, err)
	}
	return w, func() { w.Close() }, nil
}

func (s *taskService) state(id string) (ociState, error) {
	out, err := runtimeCommand("state", id)
	if err != nil {
		return ociState{}, err
	}
	var state ociState
	if err := json.Unmarshal(out, &state); err != nil {
		return ociState{}, fmt.Errorf("platform-factory-shim: decode state: %w", err)
	}
	return state, nil
}

func (s *taskService) Create(ctx context.Context, r *taskapi.CreateTaskRequest) (*taskapi.CreateTaskResponse, error) {
	if len(r.GetRootfs()) != 0 {
		if err := os.MkdirAll(filepath.Join(r.GetBundle(), "rootfs"), 0o711); err != nil {
			return nil, fmt.Errorf("platform-factory-shim: create rootfs mountpoint: %w", err)
		}
		if err := mount.All(mount.FromProto(r.GetRootfs()), filepath.Join(r.GetBundle(), "rootfs")); err != nil {
			return nil, fmt.Errorf("platform-factory-shim: mount rootfs: %w", err)
		}
	}
	stdout, closeStdout, err := openContainerIO(r.GetStdout())
	if err != nil {
		return nil, err
	}
	defer closeStdout()
	stderr, closeStderr, err := openContainerIO(r.GetStderr())
	if err != nil {
		return nil, err
	}
	defer closeStderr()

	pidFile := filepath.Join(r.GetBundle(), "init.pid")
	if err := runtimeCreateCommand(stdout, stderr, "create", "--bundle", r.GetBundle(), "--pid-file", pidFile, r.GetID()); err != nil {
		return nil, err
	}
	state, err := s.state(r.GetID())
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.tasks[r.GetID()] = &taskState{bundle: r.GetBundle()}
	s.mu.Unlock()
	return &taskapi.CreateTaskResponse{Pid: uint32(state.PID)}, nil
}

func (s *taskService) Start(ctx context.Context, r *taskapi.StartRequest) (*taskapi.StartResponse, error) {
	if r.GetExecID() != "" {
		return nil, errdefs.ErrNotImplemented
	}
	if _, err := runtimeCommand("start", r.GetID()); err != nil {
		return nil, err
	}
	state, err := s.state(r.GetID())
	if err != nil {
		return nil, err
	}
	return &taskapi.StartResponse{Pid: uint32(state.PID)}, nil
}

func (s *taskService) Delete(ctx context.Context, r *taskapi.DeleteRequest) (*taskapi.DeleteResponse, error) {
	if r.GetExecID() != "" {
		return nil, errdefs.ErrNotImplemented
	}
	exitStatus, exitedAt := s.waitExit(ctx, r.GetID())
	if _, err := runtimeCommand("delete", r.GetID()); err != nil {
		return nil, err
	}
	s.mu.Lock()
	bundle := s.tasks[r.GetID()]
	delete(s.tasks, r.GetID())
	s.mu.Unlock()
	if bundle != nil {
		_ = mount.UnmountRecursive(filepath.Join(bundle.bundle, "rootfs"), 0)
	}
	return &taskapi.DeleteResponse{
		ExitStatus: exitStatus,
		ExitedAt:   timestamppb.New(exitedAt),
	}, nil
}

func (s *taskService) Kill(ctx context.Context, r *taskapi.KillRequest) (*emptypb.Empty, error) {
	if r.GetExecID() != "" {
		return nil, errdefs.ErrNotImplemented
	}
	signal := syscall.Signal(r.GetSignal())
	if _, err := runtimeCommand("kill", r.GetID(), signalName(signal)); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

func (s *taskService) State(ctx context.Context, r *taskapi.StateRequest) (*taskapi.StateResponse, error) {
	if r.GetExecID() != "" {
		return nil, errdefs.ErrNotImplemented
	}
	state, err := s.state(r.GetID())
	if err != nil {
		return nil, err
	}
	return &taskapi.StateResponse{
		ID:     state.ID,
		Bundle: state.Bundle,
		Pid:    uint32(state.PID),
		Status: ociStatusToTask(state.Status),
	}, nil
}

// Wait blocks until the task's process is gone, polling platform-factory-runtime's
// state verb. There is no push notification for this in the OCI CLI
// contract this shim delegates to.
func (s *taskService) Wait(ctx context.Context, r *taskapi.WaitRequest) (*taskapi.WaitResponse, error) {
	if r.GetExecID() != "" {
		return nil, errdefs.ErrNotImplemented
	}
	exitStatus, exitedAt := s.waitExit(ctx, r.GetID())
	return &taskapi.WaitResponse{ExitStatus: exitStatus, ExitedAt: timestamppb.New(exitedAt)}, nil
}

func (s *taskService) waitExit(ctx context.Context, id string) (uint32, time.Time) {
	const pollInterval = 500 * time.Millisecond
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		state, err := s.state(id)
		if err != nil || state.Status == "stopped" {
			if err == nil && state.ExitStatus != nil && state.ExitedAt != nil {
				return *state.ExitStatus, *state.ExitedAt
			}
			return 255, time.Now()
		}
		select {
		case <-ctx.Done():
			return 0, time.Now()
		case <-ticker.C:
		}
	}
}

func (s *taskService) Pids(ctx context.Context, r *taskapi.PidsRequest) (*taskapi.PidsResponse, error) {
	state, err := s.state(r.GetID())
	if err != nil {
		return nil, err
	}
	if state.PID == 0 {
		return &taskapi.PidsResponse{}, nil
	}
	return &taskapi.PidsResponse{
		Processes: []*tasktypes.ProcessInfo{{Pid: uint32(state.PID)}},
	}, nil
}

func (s *taskService) Connect(ctx context.Context, r *taskapi.ConnectRequest) (*taskapi.ConnectResponse, error) {
	state, err := s.state(r.GetID())
	if err != nil {
		return nil, err
	}
	return &taskapi.ConnectResponse{ShimPid: uint32(os.Getpid()), TaskPid: uint32(state.PID)}, nil
}

func (s *taskService) Shutdown(ctx context.Context, r *taskapi.ShutdownRequest) (*emptypb.Empty, error) {
	s.mu.Lock()
	remaining := len(s.tasks)
	s.mu.Unlock()
	if remaining == 0 {
		os.Exit(0)
	}
	return &emptypb.Empty{}, nil
}

// Pause, Resume, Checkpoint, Exec, ResizePty, CloseIO, Update, and Stats have
// no equivalent in platform-factory-runtime's OCI CLI contract - a MicroVM-backed
// container is not a process tree this shim can pause, checkpoint, or attach
// a second exec'd process to. They fail closed rather than silently no-op.
func (s *taskService) Pause(ctx context.Context, r *taskapi.PauseRequest) (*emptypb.Empty, error) {
	return nil, errdefs.ErrNotImplemented
}

func (s *taskService) Resume(ctx context.Context, r *taskapi.ResumeRequest) (*emptypb.Empty, error) {
	return nil, errdefs.ErrNotImplemented
}

func (s *taskService) Checkpoint(ctx context.Context, r *taskapi.CheckpointTaskRequest) (*emptypb.Empty, error) {
	return nil, errdefs.ErrNotImplemented
}

func (s *taskService) Exec(ctx context.Context, r *taskapi.ExecProcessRequest) (*emptypb.Empty, error) {
	return nil, errdefs.ErrNotImplemented
}

func (s *taskService) ResizePty(ctx context.Context, r *taskapi.ResizePtyRequest) (*emptypb.Empty, error) {
	return nil, errdefs.ErrNotImplemented
}

func (s *taskService) CloseIO(ctx context.Context, r *taskapi.CloseIORequest) (*emptypb.Empty, error) {
	return nil, errdefs.ErrNotImplemented
}

func (s *taskService) Update(ctx context.Context, r *taskapi.UpdateTaskRequest) (*emptypb.Empty, error) {
	return nil, errdefs.ErrNotImplemented
}

func (s *taskService) Stats(ctx context.Context, r *taskapi.StatsRequest) (*taskapi.StatsResponse, error) {
	return nil, errdefs.ErrNotImplemented
}

func ociStatusToTask(status string) tasktypes.Status {
	switch status {
	case "creating":
		return tasktypes.Status_CREATED
	case "created":
		return tasktypes.Status_CREATED
	case "running":
		return tasktypes.Status_RUNNING
	case "stopped":
		return tasktypes.Status_STOPPED
	default:
		return tasktypes.Status_UNKNOWN
	}
}

func signalName(signal syscall.Signal) string {
	if name := unix_SignalName(signal); name != "" {
		return name
	}
	return strconv.Itoa(int(signal))
}

// unix_SignalName covers exactly the termination signals
// internal/ociruntime.guestTerminationSignal accepts; anything else is
// passed through numerically and will be rejected the same way the CLI
// already rejects it for any other caller.
func unix_SignalName(signal syscall.Signal) string {
	switch signal {
	case syscall.SIGTERM:
		return "TERM"
	case syscall.SIGKILL:
		return "KILL"
	case syscall.SIGINT:
		return "INT"
	case syscall.SIGQUIT:
		return "QUIT"
	default:
		return ""
	}
}
