package plugin

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// NetworkRule is one validated destination entry from Manifest.Permissions.Network.
type NetworkRule struct {
	Host     string
	Port     string // empty means any port; otherwise decimal port number
	Wildcard bool   // true for "*.example.com"
	CIDR     *net.IPNet
	Broad    bool // true when the rule permits broad access (e.g. "*" or "/0")
}

// NetworkPolicy enforces per-destination egress allowlists declared in a
// plugin's signed manifest.
type NetworkPolicy struct {
	Rules []NetworkRule
}

// ParseNetworkPolicy compiles Manifest.Permissions.Network entries into a
// fail-closed allowlist policy.
func ParseNetworkPolicy(entries []string) (NetworkPolicy, error) {
	var policy NetworkPolicy
	for _, raw := range entries {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		rule, err := parseNetworkRule(entry)
		if err != nil {
			return NetworkPolicy{}, fmt.Errorf("plugin network policy %q: %w", entry, err)
		}
		policy.Rules = append(policy.Rules, rule)
	}
	return policy, nil
}

func parseNetworkRule(entry string) (NetworkRule, error) {
	if entry == "*" || entry == "all" {
		return NetworkRule{Host: "*", Broad: true}, nil
	}
	if strings.Contains(entry, "://") {
		u, err := url.Parse(entry)
		if err != nil || u.Hostname() == "" {
			return NetworkRule{}, errors.New("invalid destination URL")
		}
		port := u.Port()
		if port == "" {
			switch strings.ToLower(u.Scheme) {
			case "https":
				port = "443"
			case "http":
				port = "80"
			}
		}
		return makeHostRule(strings.ToLower(u.Hostname()), port)
	}
	if _, ipnet, err := net.ParseCIDR(entry); err == nil {
		ones, _ := ipnet.Mask.Size()
		return NetworkRule{CIDR: ipnet, Broad: ones == 0}, nil
	}
	if host, port, err := net.SplitHostPort(entry); err == nil {
		if _, pErr := strconv.Atoi(port); pErr != nil {
			return NetworkRule{}, fmt.Errorf("invalid port %q", port)
		}
		return makeHostRule(strings.ToLower(host), port)
	}
	return makeHostRule(strings.ToLower(entry), "")
}

func makeHostRule(host, port string) (NetworkRule, error) {
	if host == "" {
		return NetworkRule{}, errors.New("empty host")
	}
	if strings.HasPrefix(host, "*.") {
		suffix := strings.TrimPrefix(host, "*.")
		if suffix == "" || strings.Contains(suffix, "*") {
			return NetworkRule{}, errors.New("invalid wildcard host")
		}
		return NetworkRule{Host: suffix, Port: port, Wildcard: true}, nil
	}
	if strings.Contains(host, "*") {
		return NetworkRule{}, errors.New("unsupported wildcard pattern")
	}
	return NetworkRule{Host: host, Port: port}, nil
}

// HasBroadPermission reports whether any rule grants unrestricted egress.
func (p NetworkPolicy) HasBroadPermission() bool {
	for _, r := range p.Rules {
		if r.Broad {
			return true
		}
	}
	return false
}

// Allows reports whether connecting to host:port is permitted by the allowlist.
// Cloud metadata endpoints (169.254.169.254, fd00:ec2::254) are always denied
// unless explicitly named by exact IP.
func (p NetworkPolicy) Allows(host, port string) bool {
	if len(p.Rules) == 0 {
		return false
	}
	h := strings.ToLower(strings.TrimSpace(host))
	ip := net.ParseIP(h)
	isMetadata := h == "169.254.169.254" || h == "fd00:ec2::254" || h == "metadata.google.internal"
	for _, r := range p.Rules {
		if r.Port != "" && r.Port != port {
			continue
		}
		if r.Broad {
			if isMetadata {
				continue
			}
			return true
		}
		if r.CIDR != nil {
			if ip != nil && r.CIDR.Contains(ip) && !isMetadata {
				return true
			}
			continue
		}
		if r.Wildcard {
			if !isMetadata && strings.HasSuffix(h, "."+r.Host) {
				return true
			}
			continue
		}
		if h == r.Host {
			return true
		}
	}
	return false
}

// SafeDialContext returns a DialContext function enforcing p on every outbound
// connection attempt.
func (p NetworkPolicy) SafeDialContext(base *net.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	if base == nil {
		base = &net.Dialer{}
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("plugin network policy: invalid address %q: %w", addr, err)
		}
		if !p.Allows(host, port) {
			return nil, fmt.Errorf("plugin network policy: destination %s:%s is not in manifest allowlist", host, port)
		}
		return base.DialContext(ctx, network, addr)
	}
}
