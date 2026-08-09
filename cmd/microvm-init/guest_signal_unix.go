//go:build !windows

package main

import (
	"fmt"
	"os"
	"strings"
	"syscall"
)

func guestSignal(value string) (os.Signal, error) {
	signals := map[string]syscall.Signal{
		"TERM": syscall.SIGTERM, "INT": syscall.SIGINT, "KILL": syscall.SIGKILL,
		"HUP": syscall.SIGHUP, "QUIT": syscall.SIGQUIT, "USR1": syscall.SIGUSR1, "USR2": syscall.SIGUSR2,
	}
	value = strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(value)), "SIG")
	signal, ok := signals[value]
	if !ok {
		return nil, fmt.Errorf("unsupported signal %q", value)
	}
	return signal, nil
}
