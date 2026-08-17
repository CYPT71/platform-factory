// Package core defines the abstract interfaces for Platform Factory's domain.
package core

import (
	"context"
	"net"
	"net/netip"
)

// NetworkRelay is the interface for network forwarding capabilities.
// It abstracts the concrete DNSForwarder implementation in internal/networking,
// allowing internal/executor to depend only on this interface.
type NetworkRelay interface {
	// ServeRelay exchanges length-prefixed DNS datagrams over a connected,
	// message-oriented transport. It is the host side of the resolve-only
	// sandbox data plane.
	ServeRelay(ctx context.Context, conn net.Conn) error

	// GetUpstream returns the configured upstream resolver address.
	GetUpstream() netip.AddrPort
	// GetTimeout returns the configured timeout for DNS operations.
	GetTimeout() int64
	// GetMaxInflight returns the maximum number of concurrent DNS requests.
	GetMaxInflight() int
}
