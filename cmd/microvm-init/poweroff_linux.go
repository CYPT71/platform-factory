//go:build linux

package main

import "syscall"

// poweroff shuts the machine down. It is only meaningful when this binary
// is actually running as a Linux kernel's PID 1.
func poweroff() error {
	return syscall.Reboot(syscall.LINUX_REBOOT_CMD_POWER_OFF)
}
