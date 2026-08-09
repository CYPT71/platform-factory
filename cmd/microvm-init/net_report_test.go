package main

import "testing"

func TestCmdlineRequestsDHCP(t *testing.T) {
	cases := map[string]bool{
		"":                                       false,
		"console=ttyS0,115200 rdinit=/sbin/init": false,
		"ip=169.254.100.2::169.254.100.1:255.255.255.252::eth0:off": false,
		"console=hvc0 earlycon=hvc0 ip=dhcp rdinit=/sbin/init":      true,
		"ip=dhcp":       true,
		"myip=dhcp":     false, // exact field match only, not a substring
		"ip=dhcpserver": false,
	}
	for cmdline, want := range cases {
		if got := cmdlineRequestsDHCP(cmdline); got != want {
			t.Errorf("cmdlineRequestsDHCP(%q) = %v, want %v", cmdline, got, want)
		}
	}
}
