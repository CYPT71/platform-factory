//go:build linux

package main

import (
	"errors"
	"fmt"
	"io"
	"syscall"
)

func reapExitedChildren(stderr io.Writer) int {
	return reapExitedChildrenWith(func() (int, syscall.WaitStatus, error) {
		var status syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &status, syscall.WNOHANG, nil)
		return pid, status, err
	}, stderr)
}

func reapExitedChildrenWith(wait func() (int, syscall.WaitStatus, error), stderr io.Writer) int {
	reaped := 0
	for {
		pid, status, err := wait()
		switch {
		case errors.Is(err, syscall.ECHILD), pid == 0:
			return reaped
		case err != nil:
			fmt.Fprintf(stderr, "level=WARN component=microvm-init operation=supervise phase=reap error=%q\n", err)
			return reaped
		default:
			reaped++
			fmt.Fprintf(stderr, "level=INFO component=microvm-init operation=supervise phase=reap pid=%d status=%d\n", pid, status)
		}
	}
}
