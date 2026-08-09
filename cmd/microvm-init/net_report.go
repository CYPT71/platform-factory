package main

import (
	"slices"
	"strings"
)

// guestIPReportMarker is what the host looks for on the serial console to
// learn this guest's DHCP-negotiated address. Kept in one place so the
// host side (cmd/platform-factory's darwin native run path) and this
// writer never drift apart.
const guestIPReportMarker = "PLATFORM-FACTORY-GUEST-IP="

// dhcpCmdlineToken is the exact `ip=` value the native HVF boot path
// (cmd/platform-factory/microvm_native_darwin.go) uses to ask the
// kernel's own built-in DHCP client (CONFIG_IP_PNP_DHCP, see
// scripts/microvm/kernel-common.config) to negotiate an address, instead
// of KVM's static `ip=<addr>::...`.
const dhcpCmdlineToken = "ip=dhcp"

// cmdlineRequestsDHCP reports whether the kernel command line asked for
// DHCP addressing - the trigger for whether reportGuestIPIfDHCP attempts
// anything at all. Pure and platform-independent so it's testable
// without a real /proc/cmdline: a KVM guest's address is already known
// to the host statically and never reaches this.
func cmdlineRequestsDHCP(cmdline string) bool {
	return slices.Contains(strings.Fields(cmdline), dhcpCmdlineToken)
}
