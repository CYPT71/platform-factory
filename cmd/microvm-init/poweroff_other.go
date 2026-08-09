//go:build !linux

package main

import "errors"

// poweroff is unsupported outside Linux; this binary only ever runs as a
// Linux kernel's PID 1, but the package must still build and test on the
// host platforms used for local development.
func poweroff() error {
	return errors.New("poweroff is only supported on linux")
}
