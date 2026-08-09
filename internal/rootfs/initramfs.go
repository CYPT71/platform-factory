package rootfs

import (
	"bufio"
	"compress/gzip"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	cpioModeDir     = 0o040000
	cpioModeReg     = 0o100000
	cpioModeSymlink = 0o120000
)

type ProcessConfig struct {
	Args    []string        `json:"args"`
	Env     []string        `json:"env,omitempty"`
	Cwd     string          `json:"cwd,omitempty"`
	UID     uint32          `json:"uid,omitempty"`
	GID     uint32          `json:"gid,omitempty"`
	Groups  []uint32        `json:"additional_gids,omitempty"`
	Umask   *uint32         `json:"umask,omitempty"`
	Rlimits []ProcessRlimit `json:"rlimits,omitempty"`
}

type ProcessRlimit struct {
	Type string `json:"type"`
	Hard uint64 `json:"hard"`
	Soft uint64 `json:"soft"`
}

const (
	GuestTransportConfigPath = "/etc/platform-factory/guest-transport.json"
	GuestSessionKeyPath      = "/etc/platform-factory/guest-session.key"
)

type GuestTransportConfig struct {
	Device         string `json:"device"`
	SessionKeyPath string `json:"session_key_path"`
}

// InstallGuestTransportConfig provisions one per-boot authenticated COM2
// endpoint. The key is deliberately written mode 0600 and is never included
// in the non-secret JSON metadata.
func InstallGuestTransportConfig(dir string, sessionKey []byte) error {
	if len(sessionKey) < 32 || len(sessionKey) > 4096 {
		return errors.New("rootfs: guest session key must contain 32..4096 bytes")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("rootfs: open converted rootfs: %w", err)
	}
	defer root.Close()
	if err := root.MkdirAll("etc/platform-factory", 0o755); err != nil {
		return err
	}
	config := GuestTransportConfig{
		Device: "/dev/ttyS1", SessionKeyPath: GuestSessionKeyPath,
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return err
	}
	if err := root.RemoveAll("etc/platform-factory/guest-session.key"); err != nil {
		return err
	}
	encodedKey := []byte(hex.EncodeToString(sessionKey) + "\n")
	if err := root.WriteFile("etc/platform-factory/guest-session.key", encodedKey, 0o600); err != nil {
		return err
	}
	if err := root.Chmod("etc/platform-factory/guest-session.key", 0o600); err != nil {
		return err
	}
	if err := root.RemoveAll("etc/platform-factory/guest-transport.json"); err != nil {
		return err
	}
	if err := root.WriteFile("etc/platform-factory/guest-transport.json", encoded, 0o444); err != nil {
		return err
	}
	if err := root.Chmod("etc/platform-factory/guest-transport.json", 0o444); err != nil {
		return err
	}
	if err := root.Chtimes("etc/platform-factory/guest-session.key", epoch, epoch); err != nil {
		return err
	}
	return root.Chtimes("etc/platform-factory/guest-transport.json", epoch, epoch)
}

// WriteInitramfs packs dir, a directory tree already produced by Convert
// (plus, typically, InstallInit), into a deterministic gzip-compressed cpio
// "newc" archive suitable as a Linux kernel initramfs. It does not
// re-validate paths or link targets: it trusts dir to already be the output
// of Convert/InstallInit, exactly as digestTree does.
//
// Inode numbers are synthesized sequentially in sorted-path order rather
// than read from the filesystem, and every entry's mtime is pinned to the
// Unix epoch, so byte-identical input directories always produce
// byte-identical archives regardless of host inode numbers or wall-clock
// time. Hardlink relationships from the source OCI layers are not
// preserved in the packed archive - each linked path is written as an
// independent regular file with its own full content - because only path
// and content (not inode identity) are observable to a kernel booting from
// this initramfs.
func WriteInitramfs(dir string, w io.Writer) error {
	root, err := realDirectory(dir)
	if err != nil {
		return fmt.Errorf("rootfs: initramfs source: %w", err)
	}
	entries, err := sortedRelativePaths(root)
	if err != nil {
		return err
	}
	// gzip.BestCompression is a valid constant compression level, so
	// NewWriterLevel cannot return an error here.
	gz, _ := gzip.NewWriterLevel(w, gzip.BestCompression)
	archive := &cpioWriter{w: bufio.NewWriter(gz)}
	for index, name := range entries {
		if err := archive.writeEntry(root, name, uint32(index+1)); err != nil {
			return fmt.Errorf("rootfs: pack %q: %w", name, err)
		}
	}
	if err := archive.writeTrailer(); err != nil {
		return err
	}
	if err := archive.w.Flush(); err != nil {
		return err
	}
	return gz.Close()
}

// InstallInit writes a project-owned PID 1 binary, and optionally a fixed
// entrypoint argv, into dir, a directory previously produced by Convert. It
// is deliberately separate from Convert: the init binary and entrypoint
// override are trusted, project-controlled inputs, never part of the
// untrusted OCI image content Convert extracts and verifies.
//
// Unlike Convert, InstallInit is not atomic across its two writes: if the
// entrypoint argv fails validation, the init binary has already been
// written into dir. Callers that need all-or-nothing semantics must stage
// dir themselves (as the microvm-initramfs command does, by running
// Convert and InstallInit against a temporary directory before packing and
// installing the result).
func InstallInit(dir, initBinary string, entrypoint []string) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("rootfs: open converted rootfs: %w", err)
	}
	defer root.Close()

	data, err := os.ReadFile(initBinary)
	if err != nil {
		return fmt.Errorf("rootfs: read init binary: %w", err)
	}
	if err := root.MkdirAll("sbin", 0o755); err != nil {
		return fmt.Errorf("rootfs: create sbin: %w", err)
	}
	if err := root.RemoveAll("sbin/init"); err != nil {
		return fmt.Errorf("rootfs: remove existing init: %w", err)
	}
	if err := root.WriteFile("sbin/init", data, 0o555); err != nil {
		return fmt.Errorf("rootfs: write init: %w", err)
	}
	if err := root.Chmod("sbin/init", 0o555); err != nil {
		return err
	}
	if err := root.Chtimes("sbin", epoch, epoch); err != nil {
		return err
	}
	if err := root.Chtimes("sbin/init", epoch, epoch); err != nil {
		return err
	}

	if len(entrypoint) == 0 {
		return nil
	}
	if len(entrypoint) > 128 {
		return errors.New("rootfs: entrypoint must contain at most 128 arguments")
	}
	if !filepath.IsAbs(entrypoint[0]) {
		return errors.New("rootfs: entrypoint[0] must be an absolute path")
	}
	for _, value := range entrypoint {
		if strings.ContainsRune(value, 0) {
			return errors.New("rootfs: entrypoint contains NUL")
		}
	}
	encoded, err := json.Marshal(entrypoint)
	if err != nil {
		return err
	}
	if err := root.MkdirAll("etc/platform-factory", 0o755); err != nil {
		return fmt.Errorf("rootfs: create entrypoint directory: %w", err)
	}
	if err := root.RemoveAll("etc/platform-factory/entrypoint.json"); err != nil {
		return fmt.Errorf("rootfs: remove existing entrypoint config: %w", err)
	}
	if err := root.WriteFile("etc/platform-factory/entrypoint.json", encoded, 0o444); err != nil {
		return fmt.Errorf("rootfs: write entrypoint config: %w", err)
	}
	if err := root.Chmod("etc/platform-factory/entrypoint.json", 0o444); err != nil {
		return err
	}
	if err := root.Chtimes("etc/platform-factory", epoch, epoch); err != nil {
		return err
	}
	return root.Chtimes("etc/platform-factory/entrypoint.json", epoch, epoch)
}

// InstallProcessConfig writes the fuller OCI process contract used by the
// runtime facade. InstallInit's entrypoint.json remains supported for bundles
// that only need argv.
func InstallProcessConfig(dir string, config ProcessConfig) error {
	if len(config.Args) == 0 || len(config.Args) > 128 || !filepath.IsAbs(config.Args[0]) {
		return errors.New("rootfs: process args must contain 1..128 values and start with an absolute path")
	}
	if config.Cwd == "" {
		config.Cwd = "/"
	}
	if !filepath.IsAbs(config.Cwd) {
		return errors.New("rootfs: process cwd must be absolute")
	}
	for _, value := range append(append([]string(nil), config.Args...), config.Env...) {
		if strings.ContainsRune(value, 0) {
			return errors.New("rootfs: process config contains NUL")
		}
	}
	for _, value := range config.Env {
		if strings.IndexByte(value, '=') <= 0 {
			return errors.New("rootfs: process environment entries must use KEY=value")
		}
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("rootfs: open converted rootfs: %w", err)
	}
	defer root.Close()
	if err := root.MkdirAll("etc/platform-factory", 0o755); err != nil {
		return err
	}
	if err := root.RemoveAll("etc/platform-factory/process.json"); err != nil {
		return err
	}
	if err := root.WriteFile("etc/platform-factory/process.json", encoded, 0o444); err != nil {
		return err
	}
	return root.Chtimes("etc/platform-factory/process.json", epoch, epoch)
}

var epoch = time.Unix(0, 0)

func sortedRelativePaths(root string) ([]string, error) {
	var names []string
	err := filepath.WalkDir(root, func(name string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name == root {
			return nil
		}
		relative, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		names = append(names, relative)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}

type cpioWriter struct {
	w      *bufio.Writer
	offset int64
}

func (c *cpioWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.offset += int64(n)
	return n, err
}

func (c *cpioWriter) pad() error {
	if remainder := c.offset % 4; remainder != 0 {
		_, err := c.Write(make([]byte, 4-remainder))
		return err
	}
	return nil
}

func (c *cpioWriter) writeHeader(name string, mode, filesize, ino uint32) error {
	nameBytes := append([]byte(name), 0)
	header := fmt.Sprintf("070701%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x",
		ino, mode, 0, 0, 1, 0, filesize, 0, 0, 0, 0, len(nameBytes), 0)
	if _, err := c.Write([]byte(header)); err != nil {
		return err
	}
	if _, err := c.Write(nameBytes); err != nil {
		return err
	}
	return c.pad()
}

func (c *cpioWriter) writeEntry(root, relative string, ino uint32) error {
	full := filepath.Join(root, relative)
	info, err := os.Lstat(full)
	if err != nil {
		return err
	}
	name := filepath.ToSlash(relative)
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(full)
		if err != nil {
			return err
		}
		if err := c.writeHeader(name, cpioModeSymlink|0o777, uint32(len(target)), ino); err != nil {
			return err
		}
		if _, err := c.Write([]byte(target)); err != nil {
			return err
		}
		return c.pad()
	case info.IsDir():
		return c.writeHeader(name, cpioModeDir|0o755, 0, ino)
	case info.Mode().IsRegular():
		size := info.Size()
		if size < 0 || size > 1<<32-1 {
			return fmt.Errorf("size %d out of range", size)
		}
		if err := c.writeHeader(name, cpioModeReg|uint32(info.Mode().Perm()), uint32(size), ino); err != nil {
			return err
		}
		file, err := os.Open(full)
		if err != nil {
			return err
		}
		defer file.Close()
		written, err := io.Copy(c, io.LimitReader(file, size))
		if err != nil {
			return err
		}
		if written != size {
			return errors.New("file changed size while packing")
		}
		return c.pad()
	default:
		return fmt.Errorf("unsupported entry type for %q", name)
	}
}

func (c *cpioWriter) writeTrailer() error {
	return c.writeHeader("TRAILER!!!", 0, 0, 0)
}
