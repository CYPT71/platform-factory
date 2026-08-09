//go:build !linux

package main

import (
	"errors"
	"io"
	"os"
)

func openGuestAgentDevice(string, int, os.FileMode) (io.ReadWriteCloser, error) {
	return nil, errors.New("guest serial device is only supported on Linux")
}
