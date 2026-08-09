//go:build !linux

package main

import "io"

// reportGuestIPIfDHCP is a no-op off Linux: this binary only ever runs as
// PID 1 inside a Linux guest, so this stub exists solely to keep the
// package portable to build/vet on every host OS, matching
// console_other.go's own reasoning.
func reportGuestIPIfDHCP(_ io.Writer) {}
