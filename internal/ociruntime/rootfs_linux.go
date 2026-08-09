// BuildGuestInitramfs prepares the initramfs for supervisor_linux.go's
// KVM-backed boot, which is linux/amd64 only - see that file's build tag
// comment for why this one must match.
//go:build linux && amd64

package ociruntime

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/CYPT71/secure-oci-base/internal/rootfs"
)

const (
	annotationInitPath   = "platform-factory.dev/init-path"
	annotationInitDigest = "platform-factory.dev/init-digest"
	maxRootFSFiles       = 1_000_000
	maxRootFSBytes       = int64(64 << 30)
	maxRootFSFileBytes   = int64(8 << 30)
)

// BuildGuestInitramfs snapshots Podman's already-materialized rootfs through
// os.Root confinement, injects the project-owned PID 1 and the OCI entrypoint,
// then creates the deterministic archive consumed by the native VMM.
func BuildGuestInitramfs(bundle string, config Config) (string, func(), error) {
	return buildGuestInitramfs(bundle, config, nil)
}

func buildGuestInitramfs(bundle string, config Config, sessionKey []byte) (string, func(), error) {
	source := config.Root.Path
	var sourceRoot *os.Root
	var err error
	if filepath.IsAbs(source) {
		// Podman's overlay storage driver points root.path at a "merged"
		// directory it already owns, outside the bundle. Confine traversal
		// within it directly instead of requiring a bundle-relative path.
		sourceRoot, err = os.OpenRoot(source)
	} else {
		var bundleRoot *os.Root
		bundleRoot, err = os.OpenRoot(bundle)
		if err == nil {
			defer bundleRoot.Close()
			sourceRoot, err = bundleRoot.OpenRoot(source)
		}
	}
	if err != nil {
		return "", nil, fmt.Errorf("oci runtime: confine Podman rootfs: %w", err)
	}
	work, err := os.MkdirTemp("", "platform-factory-podman-rootfs-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(work) }
	staging := filepath.Join(work, "rootfs")
	if err := copyConfinedTree(sourceRoot, staging); err != nil {
		cleanup()
		return "", nil, err
	}
	initPath := config.Annotations[annotationInitPath]
	initData, err := readPinnedFile(initPath, config.Annotations[annotationInitDigest], "microvm-init")
	if err != nil {
		cleanup()
		return "", nil, err
	}
	trustedInit := filepath.Join(work, "microvm-init")
	if err := os.WriteFile(trustedInit, initData, 0o500); err != nil {
		cleanup()
		return "", nil, err
	}
	clear(initData)
	if err := rootfs.InstallInit(staging, trustedInit, config.Process.Args); err != nil {
		cleanup()
		return "", nil, err
	}
	if err := rootfs.InstallProcessConfig(staging, rootfs.ProcessConfig{
		Args: config.Process.Args, Env: config.Process.Env, Cwd: config.Process.Cwd,
		UID: config.Process.User.UID, GID: config.Process.User.GID,
		Groups: config.Process.User.AdditionalGids, Umask: config.Process.User.Umask,
		Rlimits: toRootfsRlimits(config.Process.Rlimits),
	}); err != nil {
		cleanup()
		return "", nil, err
	}
	if sessionKey != nil {
		if err := rootfs.InstallGuestTransportConfig(staging, sessionKey); err != nil {
			cleanup()
			return "", nil, err
		}
	}
	archivePath := filepath.Join(work, "initramfs.cpio.gz")
	archive, err := os.OpenFile(archivePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		cleanup()
		return "", nil, err
	}
	if err := rootfs.WriteInitramfs(staging, archive); err != nil {
		archive.Close()
		cleanup()
		return "", nil, err
	}
	if err := archive.Close(); err != nil {
		cleanup()
		return "", nil, err
	}
	return archivePath, cleanup, nil
}

func toRootfsRlimits(limits []Rlimit) []rootfs.ProcessRlimit {
	result := make([]rootfs.ProcessRlimit, len(limits))
	for index, limit := range limits {
		result[index] = rootfs.ProcessRlimit{Type: limit.Type, Hard: limit.Hard, Soft: limit.Soft}
	}
	return result
}

func copyConfinedTree(sourceRoot *os.Root, destination string) error {
	if err := os.Mkdir(destination, 0o700); err != nil {
		return err
	}
	destinationRoot, err := os.OpenRoot(destination)
	if err != nil {
		return err
	}
	defer destinationRoot.Close()
	var files int
	var bytes int64
	return fs.WalkDir(confinedFS{root: sourceRoot}, ".", func(relative string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if relative == "." {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		files++
		if files > maxRootFSFiles {
			return fmt.Errorf("oci runtime: rootfs exceeds %d entries", maxRootFSFiles)
		}
		if info.Mode().IsRegular() {
			if info.Size() < 0 || info.Size() > maxRootFSFileBytes ||
				bytes > maxRootFSBytes-info.Size() {
				return fmt.Errorf("oci runtime: rootfs exceeds file or total byte budget")
			}
			bytes += info.Size()
		}
		switch {
		case entry.IsDir():
			if err := destinationRoot.Mkdir(relative, info.Mode().Perm()); err != nil {
				return err
			}
			// Mkdir's mode argument, like OpenFile's below, is only a
			// request: the kernel applies the calling process's umask to
			// it before creating the entry. A permissive host umask (022)
			// leaves an all-read/execute source mode like 0555 alone, but
			// nothing here controls what umask the runtime actually runs
			// under, and a stricter one silently strips bits an entry
			// needed - most visibly the execute bit on a workload's own
			// entrypoint binary, which the guest then refuses to exec.
			// Chmod sets the exact bits regardless of umask.
			return destinationRoot.Chmod(relative, info.Mode().Perm())
		case entry.Type()&os.ModeSymlink != 0:
			target, err := sourceRoot.Readlink(relative)
			if err != nil {
				return err
			}
			return destinationRoot.Symlink(target, relative)
		case info.Mode().IsRegular():
			input, err := sourceRoot.Open(relative)
			if err != nil {
				return err
			}
			output, err := destinationRoot.OpenFile(relative, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
			if err != nil {
				input.Close()
				return err
			}
			_, copyErr := io.Copy(output, input)
			inputCloseErr := input.Close()
			closeErr := output.Close()
			if copyErr != nil {
				return copyErr
			}
			if inputCloseErr != nil {
				return inputCloseErr
			}
			if closeErr != nil {
				return closeErr
			}
			// See the Mkdir case above: OpenFile's mode is subject to the
			// same umask masking, and a copied file's exec bit is exactly
			// what a stripped bit would silently cost the guest.
			return destinationRoot.Chmod(relative, info.Mode().Perm())
		default:
			return fmt.Errorf("oci runtime: unsupported rootfs entry type %q", relative)
		}
	})
}

type confinedFS struct {
	root *os.Root
}

func (f confinedFS) Open(name string) (fs.File, error) {
	return f.root.Open(name)
}
