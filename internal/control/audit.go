package control

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	auditVersion      = 1
	maxAuditFileBytes = 64 << 20
	maxAuditLineBytes = 64 << 10
)

// AuditEvent is one hash-chained control-plane lifecycle event. Hash covers
// every preceding field, including PreviousHash, so deletion, insertion,
// reordering, or modification is detectable by VerifyAuditJournal.
type AuditEvent struct {
	Version      int       `json:"version"`
	Sequence     uint64    `json:"sequence"`
	Time         time.Time `json:"time"`
	Actor        string    `json:"actor"`
	Action       string    `json:"action"`
	Subject      string    `json:"subject"`
	PreviousHash string    `json:"previous_hash,omitempty"`
	Hash         string    `json:"hash"`
}

type auditPayload struct {
	Version      int       `json:"version"`
	Sequence     uint64    `json:"sequence"`
	Time         time.Time `json:"time"`
	Actor        string    `json:"actor"`
	Action       string    `json:"action"`
	Subject      string    `json:"subject"`
	PreviousHash string    `json:"previous_hash,omitempty"`
}

// AuditJournal serializes append operations and remembers the verified tail.
type AuditJournal struct {
	mu       sync.Mutex
	path     string
	sequence uint64
	lastHash string
	now      func() time.Time
}

// OpenAuditJournal verifies an existing journal before accepting new events.
func OpenAuditJournal(path string) (*AuditJournal, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("control: audit journal path is required")
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("control: audit journal path must not be a symlink")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	journal := &AuditJournal{path: path, now: time.Now}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return journal, nil
	} else if err != nil {
		return nil, err
	}
	events, err := VerifyAuditJournal(path)
	if err != nil {
		return nil, err
	}
	if len(events) != 0 {
		journal.sequence = events[len(events)-1].Sequence
		journal.lastHash = events[len(events)-1].Hash
	}
	return journal, nil
}

// Append adds and durably syncs one event without ever seeking or rewriting.
func (j *AuditJournal) Append(actor, action, subject string) (AuditEvent, error) {
	if err := validateAuditText(actor, "actor"); err != nil {
		return AuditEvent{}, err
	}
	if err := validateAuditText(action, "action"); err != nil {
		return AuditEvent{}, err
	}
	if err := validateAuditText(subject, "subject"); err != nil {
		return AuditEvent{}, err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	event := AuditEvent{Version: auditVersion, Sequence: j.sequence + 1, Time: j.now().UTC(),
		Actor: actor, Action: action, Subject: subject, PreviousHash: j.lastHash}
	event.Hash = auditEventHash(event)
	data, err := json.Marshal(event)
	if err != nil {
		return AuditEvent{}, err
	}
	parent := filepath.Dir(j.path)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return AuditEvent{}, err
	}
	if info, err := os.Lstat(j.path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return AuditEvent{}, errors.New("control: audit journal path became a symlink")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return AuditEvent{}, err
	}
	file, err := os.OpenFile(j.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return AuditEvent{}, err
	}
	if _, err = file.Write(append(data, '\n')); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return AuditEvent{}, err
	}
	if closeErr != nil {
		return AuditEvent{}, closeErr
	}
	j.sequence, j.lastHash = event.Sequence, event.Hash
	return event, nil
}

func validateAuditText(value, field string) error {
	if value == "" || len(value) > 256 || strings.IndexFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return fmt.Errorf("control: invalid audit %s", field)
	}
	return nil
}

func auditEventHash(event AuditEvent) string {
	payload, _ := json.Marshal(auditPayload{Version: event.Version, Sequence: event.Sequence, Time: event.Time,
		Actor: event.Actor, Action: event.Action, Subject: event.Subject, PreviousHash: event.PreviousHash})
	digest := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(digest[:])
}

// VerifyAuditJournal validates JSON shape, sequence, and the complete hash
// chain, returning all events only after the whole file has passed.
func VerifyAuditJournal(path string) ([]AuditEvent, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("control: audit journal path must not be a symlink")
	}
	if info.Size() > maxAuditFileBytes {
		return nil, errors.New("control: audit journal exceeds 64 MiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader := bufio.NewReader(io.LimitReader(file, maxAuditFileBytes+1))
	var events []AuditEvent
	previous := ""
	for lineNumber := 1; ; lineNumber++ {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > maxAuditLineBytes {
			return nil, fmt.Errorf("control: audit line %d exceeds 64 KiB", lineNumber)
		}
		if len(line) != 0 {
			if line[len(line)-1] != '\n' {
				return nil, fmt.Errorf("control: audit line %d is not durably terminated", lineNumber)
			}
			decoder := json.NewDecoder(strings.NewReader(string(line)))
			decoder.DisallowUnknownFields()
			var event AuditEvent
			if err := decoder.Decode(&event); err != nil {
				return nil, fmt.Errorf("control: decode audit line %d: %w", lineNumber, err)
			}
			if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
				return nil, fmt.Errorf("control: audit line %d must contain exactly one event", lineNumber)
			}
			if event.Version != auditVersion || event.Sequence != uint64(len(events)+1) || event.PreviousHash != previous ||
				event.Time.IsZero() || validateAuditText(event.Actor, "actor") != nil ||
				validateAuditText(event.Action, "action") != nil || validateAuditText(event.Subject, "subject") != nil ||
				event.Hash != auditEventHash(event) {
				return nil, fmt.Errorf("control: invalid audit chain at line %d", lineNumber)
			}
			events = append(events, event)
			previous = event.Hash
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	return events, nil
}
