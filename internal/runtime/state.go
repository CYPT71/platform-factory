package runtime

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"

	api "github.com/CYPT71/secure-oci-base/internal/microvm"
)

var machineIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// ValidMachineID reports whether id is safe for persistent runtime state.
func ValidMachineID(id string) bool { return machineIDPattern.MatchString(id) }

type FileStateStore struct {
	root *os.Root
}

func NewFileStateStore(dir string) (*FileStateStore, error) {
	if dir == "" {
		return nil, errors.New("vmm: state directory is required")
	}
	info, err := os.Lstat(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("vmm: create state directory: %w", err)
		}
	case err != nil:
		return nil, fmt.Errorf("vmm: inspect state directory: %w", err)
	case info.Mode()&os.ModeSymlink != 0:
		return nil, errors.New("vmm: state directory must not be a symbolic link")
	case !info.IsDir():
		return nil, errors.New("vmm: state path is not a directory")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("vmm: secure state directory permissions: %w", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("vmm: open state directory: %w", err)
	}
	return &FileStateStore{root: root}, nil
}

// Close releases the directory handle used to keep every state operation
// rooted in the directory originally validated by NewFileStateStore.
func (s *FileStateStore) Close() error {
	return s.root.Close()
}

func (s *FileStateStore) Put(ctx context.Context, status api.MachineStatus) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !ValidMachineID(status.ID) {
		return fmt.Errorf("vmm: invalid machine id %q", status.ID)
	}
	data, err := json.Marshal(status)
	if err != nil {
		return err
	}
	if len(data) > 1<<20 {
		return errors.New("vmm: machine state exceeds 1 MiB")
	}
	temporaryName, temporary, err := s.createTemporary()
	if err != nil {
		return err
	}
	defer s.root.Remove(temporaryName)
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
	if err := s.root.Rename(temporaryName, s.name(status.ID)); err != nil {
		return err
	}
	return s.syncDirectory()
}

func (s *FileStateStore) Get(ctx context.Context, id string) (api.MachineStatus, bool, error) {
	if err := ctx.Err(); err != nil {
		return api.MachineStatus{}, false, err
	}
	if !ValidMachineID(id) {
		return api.MachineStatus{}, false, fmt.Errorf("vmm: invalid machine id %q", id)
	}
	file, err := s.root.Open(s.name(id))
	if errors.Is(err, os.ErrNotExist) {
		return api.MachineStatus{}, false, nil
	}
	if err != nil {
		return api.MachineStatus{}, false, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil {
		return api.MachineStatus{}, false, err
	}
	if len(data) > 1<<20 {
		return api.MachineStatus{}, false, fmt.Errorf("vmm: state for %q exceeds 1 MiB", id)
	}
	var status api.MachineStatus
	if err := json.Unmarshal(data, &status); err != nil {
		return api.MachineStatus{}, false, fmt.Errorf("vmm: corrupt state for %q: %w", id, err)
	}
	if status.ID != id {
		return api.MachineStatus{}, false, fmt.Errorf("vmm: corrupt state for %q: document id is %q", id, status.ID)
	}
	return status, true, nil
}

func (s *FileStateStore) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !ValidMachineID(id) {
		return fmt.Errorf("vmm: invalid machine id %q", id)
	}
	err := s.root.Remove(s.name(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.syncDirectory()
}

func (s *FileStateStore) List(ctx context.Context) ([]api.MachineStatus, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	directory, err := s.root.Open(".")
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	var result []api.MachineStatus
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := entry.Name()[:len(entry.Name())-len(".json")]
		status, found, err := s.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if found {
			result = append(result, status)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func (s *FileStateStore) name(id string) string {
	return id + ".json"
}

func (s *FileStateStore) createTemporary() (string, *os.File, error) {
	for attempts := 0; attempts < 10; attempts++ {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", nil, fmt.Errorf("vmm: generate temporary state name: %w", err)
		}
		name := fmt.Sprintf(".state-%x", random[:])
		file, err := s.root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", nil, fmt.Errorf("vmm: create temporary state: %w", err)
		}
		return name, file, nil
	}
	return "", nil, errors.New("vmm: could not allocate a unique temporary state file")
}

func (s *FileStateStore) syncDirectory() error {
	// Windows does not expose a directory handle that File.Sync can flush.
	// Rename is still atomic there, but directory-entry durability across a
	// sudden power loss is governed by the filesystem. Unix hosts can and
	// must flush the containing directory explicitly.
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := s.root.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("vmm: sync state directory: %w", err)
	}
	return nil
}

var _ api.StateStore = (*FileStateStore)(nil)
