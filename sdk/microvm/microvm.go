// Package microvm defines the public contract used to configure secure-oci
// microVM backends. Native backends and out-of-module runtime integrations
// share these types so consumers never need to import an internal package.
package microvm

import (
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

var namePattern = regexp.MustCompile(`^[a-z0-9](?:[-a-z0-9]{0,61}[a-z0-9])?$`)

// NamePattern is the DNS-label rule every backend validates Spec.Name and
// Spec.Namespace against.
var NamePattern = namePattern

// Spec is the common, deliberately small configuration understood by every
// microVM backend. Backend-specific implementation details never belong here.
type Spec struct {
	Name      string
	Namespace string
	Image     string
	Layout    string
	Arch      string
	Listen    string
	MemoryMiB int
	VCPUs     int
	Port      int
	Forwards  []Forward
}

// ValidateCommon rejects ambiguous or unsafe values shared by every backend,
// before they reach a VMM, container runtime, kubectl, or the Kubernetes API.
// Backend-specific validation runs this first and then applies its own rules.
func (s Spec) ValidateCommon() error {
	if !namePattern.MatchString(s.Name) {
		return fmt.Errorf("name must be a DNS label of at most 63 characters")
	}
	if s.MemoryMiB < 64 || s.MemoryMiB > 1<<20 {
		return fmt.Errorf("memory must be between 64 and 1048576 MiB")
	}
	if s.VCPUs < 1 || s.VCPUs > 256 {
		return fmt.Errorf("vcpus must be between 1 and 256")
	}
	if s.Port < 1 || s.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	if s.Listen != "127.0.0.1" && s.Listen != "0.0.0.0" {
		return fmt.Errorf("listen address must be 127.0.0.1 or 0.0.0.0")
	}
	return nil
}

// Forward is one host-to-guest port forward.
type Forward struct {
	HostIP    string
	HostPort  int
	GuestPort int
	Protocol  string
}

// ParseForward parses a --publish value: CONTAINER, HOST:CONTAINER, or
// IP:HOST:CONTAINER, each with an optional /tcp or /udp suffix.
func ParseForward(value string) (Forward, error) {
	if value == "" || strings.ContainsAny(value, "\x00 \t\r\n") {
		return Forward{}, fmt.Errorf("invalid publish value %q", value)
	}
	protocol := "tcp"
	address := value
	if before, after, found := strings.Cut(value, "/"); found {
		address, protocol = before, after
	}
	if protocol != "tcp" && protocol != "udp" {
		return Forward{}, fmt.Errorf("publish protocol must be tcp or udp")
	}
	parts, err := splitAddress(address)
	if err != nil {
		return Forward{}, err
	}
	result := Forward{Protocol: protocol}
	switch len(parts) {
	case 1:
		result.GuestPort, err = parsePort(parts[0])
		result.HostPort = result.GuestPort
	case 2:
		result.HostPort, err = parsePort(parts[0])
		if err == nil {
			result.GuestPort, err = parsePort(parts[1])
		}
	case 3:
		result.HostIP = strings.Trim(parts[0], "[]")
		if _, parseErr := netip.ParseAddr(result.HostIP); parseErr != nil {
			return Forward{}, fmt.Errorf("invalid publish host IP %q", result.HostIP)
		}
		result.HostPort, err = parsePort(parts[1])
		if err == nil {
			result.GuestPort, err = parsePort(parts[2])
		}
	default:
		err = fmt.Errorf("publish must be CONTAINER, HOST:CONTAINER, or IP:HOST:CONTAINER")
	}
	if err != nil {
		return Forward{}, err
	}
	return result, nil
}

func splitAddress(value string) ([]string, error) {
	if strings.HasPrefix(value, "[") {
		end := strings.Index(value, "]")
		if end < 0 || end+1 >= len(value) || value[end+1] != ':' {
			return nil, fmt.Errorf("invalid bracketed publish address")
		}
		return append([]string{value[:end+1]}, strings.Split(value[end+2:], ":")...), nil
	}
	return strings.Split(value, ":"), nil
}

func parsePort(value string) (int, error) {
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("port must be between 1 and 65535")
	}
	return port, nil
}
