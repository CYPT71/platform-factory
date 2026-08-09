// Package networking validates runtime network configuration without
// executing a container engine, QEMU, or a Kubernetes client.
package networking

import (
	"fmt"
	"net/netip"
	"regexp"
	"strings"

	"github.com/CYPT71/secure-oci-base/internal/microvm"
)

var (
	networkName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	hostName    = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)
)

// Forward and ParseForward expose the consumer-owned microVM network contract
// without coupling this internal domain package to a public SDK.
type Forward = microvm.Forward

var ParseForward = microvm.ParseForward

func ValidateNetwork(value string, allowHost bool) error {
	if value == "none" || value == "bridge" {
		return nil
	}
	if value == "host" {
		if !allowHost {
			return fmt.Errorf("host network requires --allow-host-network")
		}
		return nil
	}
	if !networkName.MatchString(value) {
		return fmt.Errorf("network must be none, bridge, host, or a valid runtime network name")
	}
	return nil
}

func ValidateDNS(value string) error {
	if _, err := netip.ParseAddr(value); err != nil {
		return fmt.Errorf("invalid DNS server %q", value)
	}
	return nil
}

func ValidateHostname(value string) error {
	if value != "" && !hostName.MatchString(value) {
		return fmt.Errorf("invalid hostname %q", value)
	}
	return nil
}

func ValidateAddHost(value string) error {
	name, address, found := strings.Cut(value, ":")
	if !found || !hostName.MatchString(name) {
		return fmt.Errorf("add-host must be NAME:IP")
	}
	address = strings.Trim(address, "[]")
	if address != "host-gateway" {
		if _, err := netip.ParseAddr(address); err != nil {
			return fmt.Errorf("add-host must be NAME:IP")
		}
	}
	return nil
}
