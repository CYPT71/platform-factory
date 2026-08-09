//go:build windows

package main

import (
	"fmt"
	"os"
	"strings"
)

func guestSignal(value string) (os.Signal, error) {
	value = strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(value)), "SIG")
	if value != "KILL" {
		return nil, fmt.Errorf("unsupported signal %q on windows", value)
	}
	return os.Kill, nil
}
