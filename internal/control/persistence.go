package control

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const snapshotVersion = 3

type snapshot struct {
	Version int      `json:"version"`
	NextID  int      `json:"next_id"`
	Leases  []Lease  `json:"leases"`
	Pending []string `json:"pending"`
}

// Save atomically persists scheduler state. Worker registrations are
// intentionally not persisted: after a process restart every worker must
// authenticate and register again. Assigned leases are stored as assigned,
// then reclaimed by LoadControlPlane because their former process ownership
// cannot survive the control-plane incarnation.
func (c *ControlPlane) Save(path string) error {
	if path == "" {
		return errors.New("control: snapshot path is required")
	}
	c.mu.Lock()
	state := snapshot{Version: snapshotVersion, NextID: c.nextID, Pending: append([]string(nil), c.pending...)}
	for _, lease := range c.leases {
		state.Leases = append(state.Leases, *lease)
	}
	c.mu.Unlock()
	sort.Slice(state.Leases, func(i, j int) bool { return state.Leases[i].ID < state.Leases[j].ID })
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("control: create snapshot directory: %w", err)
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.New("control: snapshot path must not be a symlink")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := os.CreateTemp(parent, ".control-state-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// LoadControlPlane restores a snapshot and safely requeues every lease that
// was assigned when the previous control-plane process stopped.
func LoadControlPlane(heartbeatTimeout time.Duration, path string) (*ControlPlane, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("control: read snapshot: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (64<<20)+1))
	if err != nil {
		return nil, fmt.Errorf("control: read snapshot: %w", err)
	}
	if len(data) > 64<<20 {
		return nil, errors.New("control: snapshot exceeds 64 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var state snapshot
	if err := decoder.Decode(&state); err != nil {
		return nil, fmt.Errorf("control: decode snapshot: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("control: snapshot must contain exactly one document")
	}
	if (state.Version < 1 || state.Version > snapshotVersion) || state.NextID < 0 {
		return nil, errors.New("control: unsupported or invalid snapshot")
	}
	plane := NewControlPlane(heartbeatTimeout)
	plane.nextID = state.NextID
	pendingSeen := make(map[string]bool)
	for _, id := range state.Pending {
		if pendingSeen[id] {
			return nil, fmt.Errorf("control: duplicate pending lease %q", id)
		}
		pendingSeen[id] = true
		plane.pending = append(plane.pending, id)
	}
	var reclaimed []string
	for i := range state.Leases {
		lease := state.Leases[i]
		number, valid := leaseSequence(lease.ID)
		if !valid || number > state.NextID || plane.leases[lease.ID] != nil ||
			len(lease.Payload) == 0 || len(lease.Payload) > maxOpaqueFieldBytes ||
			len(lease.Result) > maxOpaqueFieldBytes {
			return nil, fmt.Errorf("control: invalid persisted lease %q", lease.ID)
		}
		if lease.RequiredPlatform != "" && !platformPattern.MatchString(lease.RequiredPlatform) {
			return nil, fmt.Errorf("control: invalid platform on lease %q", lease.ID)
		}
		capabilities, err := normalizedTokens(lease.RequiredCapabilities, capabilityPattern, 64, "capability")
		if err != nil || len(capabilities) != len(lease.RequiredCapabilities) {
			return nil, fmt.Errorf("control: invalid capabilities on lease %q", lease.ID)
		}
		for index := range capabilities {
			if capabilities[index] != lease.RequiredCapabilities[index] {
				return nil, fmt.Errorf("control: non-canonical capabilities on lease %q", lease.ID)
			}
		}
		if lease.PreferredContent != "" && !contentPattern.MatchString(lease.PreferredContent) {
			return nil, fmt.Errorf("control: invalid preferred content on lease %q", lease.ID)
		}
		switch lease.State {
		case LeasePending:
			if hasCompletionProvenance(lease) || hasCancellationProvenance(lease) {
				return nil, fmt.Errorf("control: pending lease %q has completion provenance", lease.ID)
			}
			if !pendingSeen[lease.ID] {
				return nil, fmt.Errorf("control: pending lease %q is absent from queue", lease.ID)
			}
		case LeaseAssigned:
			if hasCompletionProvenance(lease) || hasCancellationProvenance(lease) {
				return nil, fmt.Errorf("control: assigned lease %q has completion provenance", lease.ID)
			}
			lease.State, lease.Worker = LeasePending, ""
			reclaimed = append(reclaimed, lease.ID)
		case LeaseCompleted:
			if pendingSeen[lease.ID] {
				return nil, fmt.Errorf("control: completed lease %q is queued", lease.ID)
			}
			if state.Version == 1 && lease.CompletedBy == "" {
				// Version 1 kept the completing identity only in Worker.
				lease.CompletedBy = lease.Worker
			}
			if !workerIDPattern.MatchString(lease.CompletedBy) ||
				(state.Version >= 2 && lease.CompletedAt.IsZero()) || hasCancellationProvenance(lease) {
				return nil, fmt.Errorf("control: completed lease %q has invalid provenance", lease.ID)
			}
		case LeaseCanceled:
			if state.Version < 3 || pendingSeen[lease.ID] || hasCompletionProvenance(lease) ||
				!workerIDPattern.MatchString(lease.CanceledBy) || lease.CanceledAt.IsZero() {
				return nil, fmt.Errorf("control: canceled lease %q has invalid provenance", lease.ID)
			}
		default:
			return nil, fmt.Errorf("control: invalid state on lease %q", lease.ID)
		}
		copy := lease
		plane.leases[lease.ID] = &copy
	}
	for id := range pendingSeen {
		if plane.leases[id] == nil {
			return nil, fmt.Errorf("control: queue references unknown lease %q", id)
		}
	}
	sort.Strings(reclaimed)
	plane.pending = append(plane.pending, reclaimed...)
	return plane, nil
}

func hasCompletionProvenance(lease Lease) bool {
	return lease.CompletedBy != "" || !lease.CompletedAt.IsZero()
}

func hasCancellationProvenance(lease Lease) bool {
	return lease.CanceledBy != "" || !lease.CanceledAt.IsZero()
}

func leaseSequence(id string) (int, bool) {
	if !strings.HasPrefix(id, "lease-") {
		return 0, false
	}
	number, err := strconv.Atoi(strings.TrimPrefix(id, "lease-"))
	return number, err == nil && number > 0
}
