//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

func main() {
	if err := os.MkdirAll("/proc", 0o555); err != nil {
		fail("create proc mountpoint", err)
	}
	if err := syscall.Mount("proc", "/proc", "proc", 0, ""); err != nil {
		fail("mount proc", err)
	}
	if err := os.MkdirAll("/rosetta", 0o755); err != nil {
		fail("create mountpoint", err)
	}
	if err := syscall.Mount("rosetta", "/rosetta", "virtiofs", syscall.MS_RDONLY, ""); err != nil {
		fail("mount Rosetta VirtioFS", err)
	}
	cmd := exec.Command("/rosetta/rosetta", "/amd64-probe")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fail("execute Linux amd64 probe", err)
	}
	waitForever()
}

func fail(stage string, err error) {
	fmt.Fprintf(os.Stderr, "PLATFORM_FACTORY_ROSETTA_ERROR stage=%q error=%q\n", stage, err)
	waitForever()
}

func waitForever() {
	for {
		_ = syscall.Pause()
	}
}
