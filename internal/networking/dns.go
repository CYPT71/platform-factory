package networking

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync"
	"time"
)

const (
	maxDNSPacket = 4096
	dnsHeaderLen = 12
)

// DNSForwarder is the project-owned, dependency-free DNS data plane used by
// resolve-only sandboxes. It accepts DNS datagrams on a caller-provided local
// socket and relays them to exactly one pinned upstream resolver.
//
// The forwarder deliberately does not discover an upstream from the ambient
// host configuration: the caller must make that trust decision explicitly.
// Packets that are not DNS queries and responses that do not match the query
// transaction are dropped fail-closed.
type DNSForwarder struct {
	Upstream    netip.AddrPort
	Timeout     time.Duration
	MaxInflight int

	mu      sync.Mutex
	conn    *net.UDPConn
	workers sync.WaitGroup
}

// Serve handles DNS packets until ctx is cancelled. conn must be bound to an
// address reachable only by the intended sandbox (normally loopback in its
// network namespace). Serve owns conn and closes it before returning.
func (f *DNSForwarder) Serve(ctx context.Context, conn *net.UDPConn) error {
	if conn == nil {
		return errors.New("dns forwarder: nil listener")
	}
	if !f.Upstream.IsValid() || f.Upstream.Port() == 0 {
		_ = conn.Close()
		return errors.New("dns forwarder: an explicit upstream address and port are required")
	}
	if f.MaxInflight < 0 {
		_ = conn.Close()
		return errors.New("dns forwarder: max inflight must not be negative")
	}
	maxInflight := f.MaxInflight
	if maxInflight == 0 {
		maxInflight = 64
	}
	slots := make(chan struct{}, maxInflight)
	f.mu.Lock()
	if f.conn != nil {
		f.mu.Unlock()
		_ = conn.Close()
		return errors.New("dns forwarder: already serving")
	}
	f.conn = conn
	f.mu.Unlock()
	defer func() {
		_ = conn.Close()
		f.workers.Wait()
		f.mu.Lock()
		f.conn = nil
		f.mu.Unlock()
	}()

	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-stop:
		}
	}()
	defer close(stop)

	for {
		buffer := make([]byte, maxDNSPacket)
		n, peer, err := conn.ReadFromUDP(buffer)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("dns forwarder: read query: %w", err)
		}
		query := append([]byte(nil), buffer[:n]...)
		if !validDNSQuery(query) {
			continue
		}
		select {
		case slots <- struct{}{}:
		default:
			// Overload is denied rather than creating unbounded goroutines.
			continue
		}
		f.workers.Add(1)
		go func() {
			defer f.workers.Done()
			defer func() { <-slots }()
			response, err := f.exchange(ctx, query)
			if err != nil {
				return
			}
			_, _ = conn.WriteToUDP(response, peer)
		}()
	}
}

func (f *DNSForwarder) exchange(ctx context.Context, query []byte) ([]byte, error) {
	timeout := f.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	dialer := net.Dialer{Timeout: timeout}
	upstream, err := dialer.DialContext(ctx, "udp", f.Upstream.String())
	if err != nil {
		return nil, err
	}
	defer upstream.Close()
	deadline := time.Now().Add(timeout)
	_ = upstream.SetDeadline(deadline)
	if _, err := upstream.Write(query); err != nil {
		return nil, err
	}
	response := make([]byte, maxDNSPacket)
	n, err := upstream.Read(response)
	if err != nil {
		return nil, err
	}
	response = response[:n]
	if !validDNSResponse(query, response) {
		return nil, errors.New("invalid or mismatched DNS response")
	}
	return response, nil
}

// ServeRelay exchanges length-prefixed DNS datagrams over a connected,
// message-oriented transport such as a Unix SOCK_SEQPACKET socket. It is the
// host side of the resolve-only sandbox data plane: the transport can cross a
// network-namespace boundary as an inherited file descriptor without exposing
// an IP interface to the sandbox.
func (f *DNSForwarder) ServeRelay(ctx context.Context, conn net.Conn) error {
	if conn == nil {
		return errors.New("dns forwarder: nil relay")
	}
	defer conn.Close()
	if !f.Upstream.IsValid() || f.Upstream.Port() == 0 {
		return errors.New("dns forwarder: an explicit upstream address and port are required")
	}
	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-stop:
		}
	}()
	defer close(stop)
	packet := make([]byte, maxDNSPacket+2)
	for {
		n, err := conn.Read(packet)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) || errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("dns forwarder: read relay: %w", err)
		}
		if n < 2 {
			continue
		}
		size := int(binary.BigEndian.Uint16(packet[:2]))
		if size != n-2 || !validDNSQuery(packet[2:n]) {
			continue
		}
		response, err := f.exchange(ctx, append([]byte(nil), packet[2:n]...))
		if err != nil {
			// A zero-length frame releases the sandbox relay immediately. A
			// silent drop here would leave its single transport exchange
			// blocked forever after an upstream timeout.
			_, _ = conn.Write([]byte{0, 0})
			continue
		}
		out := make([]byte, len(response)+2)
		binary.BigEndian.PutUint16(out[:2], uint16(len(response)))
		copy(out[2:], response)
		if _, err := conn.Write(out); err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("dns forwarder: write relay: %w", err)
		}
	}
}

// ServeDNSRelay is the sandbox side of the project-owned DNS transport. It
// listens only on loopback and forwards one query at a time through relay.
func ServeDNSRelay(ctx context.Context, listener *net.UDPConn, relay net.Conn) error {
	if listener == nil || relay == nil {
		return errors.New("dns relay: listener and transport are required")
	}
	defer listener.Close()
	defer relay.Close()
	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = listener.Close()
			_ = relay.Close()
		case <-stop:
		}
	}()
	defer close(stop)
	buffer := make([]byte, maxDNSPacket)
	framed := make([]byte, maxDNSPacket+2)
	for {
		n, peer, err := listener.ReadFromUDP(buffer)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("dns relay: read query: %w", err)
		}
		if !validDNSQuery(buffer[:n]) {
			continue
		}
		binary.BigEndian.PutUint16(framed[:2], uint16(n))
		copy(framed[2:], buffer[:n])
		if _, err := relay.Write(framed[:n+2]); err != nil {
			return fmt.Errorf("dns relay: write transport: %w", err)
		}
		responseSize, err := relay.Read(framed)
		if err != nil {
			return fmt.Errorf("dns relay: read transport: %w", err)
		}
		if responseSize < 2 {
			continue
		}
		size := int(binary.BigEndian.Uint16(framed[:2]))
		if size == 0 {
			continue
		}
		if size != responseSize-2 || !validDNSResponse(buffer[:n], framed[2:responseSize]) {
			continue
		}
		if _, err := listener.WriteToUDP(framed[2:responseSize], peer); err != nil {
			return fmt.Errorf("dns relay: write response: %w", err)
		}
	}
}

func validDNSQuery(packet []byte) bool {
	if len(packet) < dnsHeaderLen || len(packet) > maxDNSPacket {
		return false
	}
	flags := binary.BigEndian.Uint16(packet[2:4])
	questionCount := binary.BigEndian.Uint16(packet[4:6])
	// Only ordinary resolver queries are accepted: QR=0, OPCODE=QUERY and a
	// small non-empty question set. The bound prevents one datagram from
	// forcing disproportionate parser work.
	if flags&0xf800 != 0 || questionCount == 0 || questionCount > 16 {
		return false
	}
	offset := dnsHeaderLen
	for range questionCount {
		next, ok := skipDNSName(packet, offset)
		if !ok || next+4 > len(packet) {
			return false
		}
		offset = next + 4 // QTYPE + QCLASS
	}
	return true
}

func skipDNSName(packet []byte, offset int) (int, bool) {
	for labels := 0; labels < 128 && offset < len(packet); labels++ {
		size := int(packet[offset])
		offset++
		switch {
		case size == 0:
			return offset, true
		case size&0xc0 == 0xc0:
			// A compressed name terminates at the two-byte pointer. Validate
			// that its target is inside the packet; following it is unnecessary
			// for establishing the question boundary.
			if offset >= len(packet) {
				return 0, false
			}
			target := (size&0x3f)<<8 | int(packet[offset])
			return offset + 1, target < len(packet)
		case size > 63 || offset+size > len(packet):
			return 0, false
		default:
			offset += size
		}
	}
	return 0, false
}

func validDNSResponse(query, response []byte) bool {
	if len(response) < dnsHeaderLen || len(response) > maxDNSPacket {
		return false
	}
	flags := binary.BigEndian.Uint16(response[2:4])
	return response[0] == query[0] && response[1] == query[1] && flags&0x8000 != 0
}

// GetUpstream returns the configured upstream resolver address.
// Implements core.NetworkRelay interface.
func (f *DNSForwarder) GetUpstream() netip.AddrPort {
	return f.Upstream
}

// GetTimeout returns the configured timeout for DNS operations in nanoseconds.
// Implements core.NetworkRelay interface.
func (f *DNSForwarder) GetTimeout() int64 {
	return int64(f.Timeout)
}

// GetMaxInflight returns the maximum number of concurrent DNS requests.
// Implements core.NetworkRelay interface.
func (f *DNSForwarder) GetMaxInflight() int {
	return f.MaxInflight
}

// ServeRelay already exists and implements the core.NetworkRelay interface.
// See line 149 for the implementation.
