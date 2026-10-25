// Package ciguard verifies explicit, recursive CI inputs before and after builds.
package ciguard

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/CYPT71/platform-factory/internal/strictjson"
)

const Version = "platform-factory.dev/ci-inputs/v1"
const maxEntries = 20000
const maxBytes int64 = 1 << 30
const maxFile int64 = 256 << 20

type Entry struct {
	Path       string `json:"path"`
	Kind       string `json:"kind"`
	Executable bool   `json:"executable,omitempty"`
	Size       int64  `json:"size,omitempty"`
	Digest     string `json:"digest,omitempty"`
}
type Lock struct {
	APIVersion string   `json:"api_version"`
	Paths      []string `json:"paths"`
	Entries    []Entry  `json:"entries"`
}

func SafePath(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.HasPrefix(name, "/") && !strings.HasPrefix(name, "../") && path.Clean(name) == name && !strings.ContainsAny(name, "\\\x00\r\n:")
}

func Snapshot(directory string, paths []string) (Lock, error) {
	lock := Lock{APIVersion: Version, Paths: append([]string(nil), paths...), Entries: []Entry{}}
	if len(paths) == 0 || len(paths) > 128 {
		return Lock{}, errors.New("select 1..128 explicit input files or directories")
	}
	sort.Strings(lock.Paths)
	for i, name := range lock.Paths {
		if !SafePath(name) {
			return Lock{}, errors.New("input paths must be confined canonical relative paths")
		}
		for j := 0; j < i; j++ {
			if name == lock.Paths[j] || strings.HasPrefix(name, lock.Paths[j]+"/") {
				return Lock{}, errors.New("duplicate or overlapping input roots")
			}
		}
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return Lock{}, err
	}
	defer root.Close()
	var total int64
	for _, name := range lock.Paths {
		// Refuse symlink ancestors as well as leaf links, even if they stay inside root.
		components := strings.Split(name, "/")
		for i := range components {
			info, err := root.Lstat(strings.Join(components[:i+1], "/"))
			if err != nil {
				return Lock{}, err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return Lock{}, errors.New("symlink in input path")
			}
		}
		err = fs.WalkDir(root.FS(), name, func(relative string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if len(lock.Entries) >= maxEntries {
				return errors.New("input entry budget exceeded")
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			e := Entry{Path: relative, Kind: "directory"}
			if info.IsDir() {
				lock.Entries = append(lock.Entries, e)
				return nil
			}
			if !info.Mode().IsRegular() {
				return errors.New("inputs must contain only real directories and regular files")
			}
			if info.Size() > maxFile || info.Size() < 0 || total > maxBytes-info.Size() {
				return errors.New("input byte budget exceeded")
			}
			file, err := root.Open(relative)
			if err != nil {
				return err
			}
			opened, err := file.Stat()
			if err != nil {
				file.Close()
				return err
			}
			if !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
				file.Close()
				return errors.New("input changed while opening")
			}
			hash := sha256.New()
			n, readErr := io.Copy(hash, io.LimitReader(file, maxFile+1))
			after, statErr := file.Stat()
			closeErr := file.Close()
			if readErr != nil {
				return readErr
			}
			if statErr != nil {
				return statErr
			}
			if closeErr != nil {
				return closeErr
			}
			if n != info.Size() || n > maxFile || !after.ModTime().Equal(opened.ModTime()) || after.Size() != opened.Size() {
				return errors.New("input changed during hashing")
			}
			total += n
			e.Kind = "file"
			e.Size = n
			e.Executable = info.Mode()&0111 != 0
			e.Digest = "sha256:" + hex.EncodeToString(hash.Sum(nil))
			lock.Entries = append(lock.Entries, e)
			return nil
		})
		if err != nil {
			return Lock{}, fmt.Errorf("snapshot input %q: %w", name, err)
		}
	}
	sort.Slice(lock.Entries, func(i, j int) bool { return lock.Entries[i].Path < lock.Entries[j].Path })
	return lock, nil
}

func Read(filename string) (Lock, error) {
	info, err := os.Lstat(filename)
	if err != nil {
		return Lock{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > 8<<20 {
		return Lock{}, errors.New("inputs lock must be a real regular file below 8 MiB")
	}
	file, err := os.Open(filename)
	if err != nil {
		return Lock{}, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return Lock{}, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return Lock{}, errors.New("inputs lock changed during open")
	}
	data, err := io.ReadAll(io.LimitReader(file, 8<<20+1))
	if err != nil {
		return Lock{}, err
	}
	if len(data) > 8<<20 {
		return Lock{}, errors.New("inputs lock exceeds budget")
	}
	var lock Lock
	if err := strictjson.Decode(data, &lock); err != nil {
		return Lock{}, err
	}
	if lock.APIVersion != Version || len(lock.Entries) == 0 || len(lock.Entries) > maxEntries {
		return Lock{}, errors.New("invalid or empty inputs lock")
	}
	return lock, nil
}

func Verify(directory string, lock Lock) error {
	current, err := Snapshot(directory, lock.Paths)
	if err != nil {
		return err
	}
	expected, _ := json.Marshal(lock)
	actual, _ := json.Marshal(current)
	if string(expected) != string(actual) {
		return errors.New("CI inputs changed: content, executable mode, directory membership or file type differs from committed lock")
	}
	return nil
}
