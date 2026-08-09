//go:build linux

package main

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// prepareConsole makes PID 1 independent of bootloader-created device nodes.
// A directly loaded initramfs may contain no /dev/console at all; mounting
// devtmpfs and explicitly wiring descriptors 0..2 guarantees guest-agent and
// application diagnostics reach the VMM serial device.
func prepareConsole() error {
	if err := os.MkdirAll("/dev", 0o755); err != nil {
		return fmt.Errorf("create /dev: %w", err)
	}
	if err := syscall.Mount("devtmpfs", "/dev", "devtmpfs",
		syscall.MS_NOSUID|syscall.MS_NOEXEC, "mode=0755"); err != nil &&
		!errors.Is(err, syscall.EBUSY) {
		return fmt.Errorf("mount devtmpfs: %w", err)
	}

	var console *os.File
	var openErr error
	for _, path := range []string{"/dev/console", "/dev/ttyS0"} {
		console, openErr = os.OpenFile(path, os.O_RDWR, 0)
		if openErr == nil {
			break
		}
	}
	if openErr != nil {
		return fmt.Errorf("open guest console (/dev/console or /dev/ttyS0): %w", openErr)
	}
	defer console.Close()

	for fd := 0; fd <= 2; fd++ {
		if err := dup2(int(console.Fd()), fd); err != nil {
			return fmt.Errorf("attach console fd %d: %w", fd, err)
		}
	}
	return nil
}

// prepareMessageQueue supplies the kernel-owned POSIX message queue
// filesystem requested by the standard OCI mount set. It is deliberately
// created inside the guest rather than forwarding Podman's host mount.
func prepareMessageQueue() error {
	if err := os.MkdirAll("/dev/mqueue", 0o755); err != nil {
		return fmt.Errorf("create /dev/mqueue: %w", err)
	}
	if err := syscall.Mount("mqueue", "/dev/mqueue", "mqueue",
		syscall.MS_NOSUID|syscall.MS_NODEV|syscall.MS_NOEXEC, ""); err != nil &&
		!errors.Is(err, syscall.EBUSY) {
		return fmt.Errorf("mount /dev/mqueue: %w", err)
	}
	return nil
}

// prepareSharedMemory supplies the tmpfs POSIX shared-memory mount Podman
// requests at /dev/shm. Podman backs its own /dev/shm with a per-container
// bind mount on the host, but that path is deliberately not forwarded into
// the MicroVM; the guest gets an equivalent private tmpfs instead.
func prepareSharedMemory() error {
	if err := os.MkdirAll("/dev/shm", 0o1777); err != nil {
		return fmt.Errorf("create /dev/shm: %w", err)
	}
	if err := syscall.Mount("tmpfs", "/dev/shm", "tmpfs",
		syscall.MS_NOSUID|syscall.MS_NODEV, "mode=1777"); err != nil &&
		!errors.Is(err, syscall.EBUSY) {
		return fmt.Errorf("mount /dev/shm: %w", err)
	}
	return nil
}

// prepareContainerEnv recreates Podman's container marker inside the guest.
// The host-backed bind mount is deliberately not forwarded into the MicroVM.
func prepareContainerEnv() error {
	if err := os.MkdirAll("/run", 0o755); err != nil {
		return fmt.Errorf("create /run: %w", err)
	}
	if err := os.WriteFile("/run/.containerenv", []byte("engine=\"podman\"\n"), 0o644); err != nil {
		return fmt.Errorf("write /run/.containerenv: %w", err)
	}
	return nil
}

// dup2 mirrors POSIX dup2 using Dup3, since Dup2 is unavailable in the Go
// syscall package on linux/arm64 (the kernel itself dropped dup2 there in
// favor of dup3). Dup3 additionally rejects oldfd == newfd with EINVAL,
// where dup2 would simply succeed as a no-op, so that case is special-cased.
func dup2(oldfd, newfd int) error {
	if oldfd == newfd {
		return nil
	}
	return syscall.Dup3(oldfd, newfd, 0)
}
