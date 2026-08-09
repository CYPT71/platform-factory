//go:build !linux

package main

import "os/exec"

// microvm-init is a Linux guest binary. Other targets are built by the
// bootstrap portability check only; they cannot apply Linux credentials.
func applyProcessIdentity(*exec.Cmd, processIdentity) {}

func applyProcessUmask(uint32) {}

func applyProcessRlimits([]processRlimit) error { return nil }
