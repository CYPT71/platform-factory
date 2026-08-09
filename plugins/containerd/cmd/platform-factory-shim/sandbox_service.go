//go:build linux

package main

import (
	"context"
	"os"
	"sync"
	"time"

	sandboxapi "github.com/containerd/containerd/api/runtime/sandbox/v1"
	apitypes "github.com/containerd/containerd/api/types"
	"github.com/containerd/errdefs"
	"github.com/containerd/ttrpc"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// sandboxService implements containerd's runtime v2 Sandbox TTRPC service.
// It intentionally never boots a VM or invokes platform-factory-runtime at all:
// containerd's default "podsandbox" sandbox model unconditionally assigns
// the sandbox process a fixed, non-empty Linux capability set that
// platform-factory-runtime deliberately refuses (see
// docs/containerd-kubernetes.md). This shim exists specifically to give
// containerd's CRI plugin a pod-scoped lifecycle to drive that never
// presents anything for that capability policy to reject in the first
// place: the sandbox here is pure bookkeeping (an ID, its bundle and netns
// paths, a running/stopped flag) plus this shim process's own PID, which is
// the only "process" a pod sandbox represents in this architecture. Actual
// isolation is provided per-container, by each container's own MicroVM,
// once the Task service (see task_service.go) creates it.
type sandboxService struct {
	mu       sync.Mutex
	sandbox  *sandboxRecord
	exitedCh chan struct{}
}

type sandboxRecord struct {
	id        string
	bundle    string
	netns     string
	createdAt time.Time
	exitedAt  time.Time
	exited    bool
}

func newSandboxService() *sandboxService {
	return &sandboxService{exitedCh: make(chan struct{})}
}

func (s *sandboxService) RegisterTTRPC(server *ttrpc.Server) error {
	sandboxapi.RegisterTTRPCSandboxService(server, s)
	return nil
}

func (s *sandboxService) CreateSandbox(ctx context.Context, r *sandboxapi.CreateSandboxRequest) (*sandboxapi.CreateSandboxResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sandbox != nil {
		return nil, errdefs.ErrAlreadyExists
	}
	s.sandbox = &sandboxRecord{
		id:        r.GetSandboxID(),
		bundle:    r.GetBundlePath(),
		netns:     r.GetNetnsPath(),
		createdAt: time.Now(),
	}
	return &sandboxapi.CreateSandboxResponse{}, nil
}

func (s *sandboxService) StartSandbox(ctx context.Context, r *sandboxapi.StartSandboxRequest) (*sandboxapi.StartSandboxResponse, error) {
	s.mu.Lock()
	record := s.sandbox
	s.mu.Unlock()
	if record == nil {
		return nil, errdefs.ErrNotFound
	}
	// This shim process is the only thing representing the sandbox; there
	// is nothing further to start.
	return &sandboxapi.StartSandboxResponse{
		Pid:       uint32(os.Getpid()),
		CreatedAt: timestamppb.New(record.createdAt),
	}, nil
}

func (s *sandboxService) Platform(ctx context.Context, r *sandboxapi.PlatformRequest) (*sandboxapi.PlatformResponse, error) {
	return &sandboxapi.PlatformResponse{
		Platform: &apitypes.Platform{OS: "linux", Architecture: hostArch},
	}, nil
}

func (s *sandboxService) StopSandbox(ctx context.Context, r *sandboxapi.StopSandboxRequest) (*sandboxapi.StopSandboxResponse, error) {
	s.mu.Lock()
	if s.sandbox != nil && !s.sandbox.exited {
		s.sandbox.exited = true
		s.sandbox.exitedAt = time.Now()
		close(s.exitedCh)
	}
	s.mu.Unlock()
	return &sandboxapi.StopSandboxResponse{}, nil
}

func (s *sandboxService) WaitSandbox(ctx context.Context, r *sandboxapi.WaitSandboxRequest) (*sandboxapi.WaitSandboxResponse, error) {
	select {
	case <-s.exitedCh:
	case <-ctx.Done():
	}
	s.mu.Lock()
	exitedAt := time.Now()
	if s.sandbox != nil {
		exitedAt = s.sandbox.exitedAt
	}
	s.mu.Unlock()
	return &sandboxapi.WaitSandboxResponse{ExitStatus: 0, ExitedAt: timestamppb.New(exitedAt)}, nil
}

func (s *sandboxService) SandboxStatus(ctx context.Context, r *sandboxapi.SandboxStatusRequest) (*sandboxapi.SandboxStatusResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sandbox == nil {
		return nil, errdefs.ErrNotFound
	}
	state := "SANDBOX_READY"
	var exitedAt *timestamppb.Timestamp
	if s.sandbox.exited {
		state = "SANDBOX_EXITED"
		exitedAt = timestamppb.New(s.sandbox.exitedAt)
	}
	return &sandboxapi.SandboxStatusResponse{
		SandboxID: s.sandbox.id,
		Pid:       uint32(os.Getpid()),
		State:     state,
		CreatedAt: timestamppb.New(s.sandbox.createdAt),
		ExitedAt:  exitedAt,
	}, nil
}

func (s *sandboxService) PingSandbox(ctx context.Context, r *sandboxapi.PingRequest) (*sandboxapi.PingResponse, error) {
	return &sandboxapi.PingResponse{}, nil
}

func (s *sandboxService) ShutdownSandbox(ctx context.Context, r *sandboxapi.ShutdownSandboxRequest) (*sandboxapi.ShutdownSandboxResponse, error) {
	defer os.Exit(0)
	return &sandboxapi.ShutdownSandboxResponse{}, nil
}

func (s *sandboxService) SandboxMetrics(ctx context.Context, r *sandboxapi.SandboxMetricsRequest) (*sandboxapi.SandboxMetricsResponse, error) {
	return &sandboxapi.SandboxMetricsResponse{}, nil
}
