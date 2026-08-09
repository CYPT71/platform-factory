//go:build linux

package main

import (
	"fmt"
	"io"
	"os"
	"syscall"
	"unsafe"
)

func openGuestAgentDevice(path string, _ int, perm os.FileMode) (io.ReadWriteCloser, error) {
	file, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOCTTY, perm)
	if err != nil {
		return nil, err
	}
	termios := syscall.Termios{
		Iflag: syscall.IGNPAR,
		Cflag: syscall.B115200 | syscall.CS8 | syscall.CREAD | syscall.CLOCAL,
	}
	termios.Cc[syscall.VMIN] = 1
	termios.Cc[syscall.VTIME] = 0
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, file.Fd(), uintptr(syscall.TCSETS), uintptr(unsafe.Pointer(&termios)))
	if errno != 0 {
		_ = file.Close()
		return nil, fmt.Errorf("configure guest serial device raw mode: %w", errno)
	}
	return file, nil
}
