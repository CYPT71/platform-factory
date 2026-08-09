//go:build linux

package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"time"
)

// reportGuestIPIfDHCP is a best-effort, fire-and-forget step: on a guest
// booted with `ip=dhcp` (the native HVF path - see
// cmd/platform-factory/microvm_native_darwin.go), poll for the address
// the kernel's own DHCP client assigns eth0 and write it to stdout (the
// serial console the host already reads) once found, so the host can
// start relaying --publish ports to it. Never blocks child-process
// startup: always run as its own goroutine. A guest booted with a static
// `ip=` (every other path) returns immediately without polling at all -
// this function's entire cost for that, by far the common, established
// case, is one file read.
//
// UNVERIFIED ON REAL HARDWARE as of the commit that added this - see
// docs/legacy-vm-disk-boot.md's HVF networking section.
func reportGuestIPIfDHCP(stdout io.Writer) {
	cmdline, err := os.ReadFile("/proc/cmdline")
	if err != nil || !cmdlineRequestsDHCP(string(cmdline)) {
		return
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		if addr := eth0IPv4(); addr != "" {
			fmt.Fprintf(stdout, "%s%s\n", guestIPReportMarker, addr)
			return
		}
		if time.Now().After(deadline) {
			fmt.Fprintln(stdout, "level=WARN component=microvm-init operation=network phase=dhcp message=\"no IPv4 address on eth0 within 15s\"")
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// eth0IPv4 returns eth0's first IPv4 address, or "" if the interface
// doesn't exist yet or has none - both expected while DHCP is still in
// progress, not errors worth logging on every poll attempt.
func eth0IPv4() string {
	iface, err := net.InterfaceByName("eth0")
	if err != nil {
		return ""
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		ipNet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		if ip4 := ipNet.IP.To4(); ip4 != nil {
			return ip4.String()
		}
	}
	return ""
}
