package networking

import (
	"context"
	"encoding/binary"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestDNSForwarderRelaysMatchingQuery(t *testing.T) {
	upstream := listenUDP(t)
	defer upstream.Close()
	go func() {
		packet := make([]byte, maxDNSPacket)
		n, peer, err := upstream.ReadFromUDP(packet)
		if err != nil {
			return
		}
		packet[2] |= 0x80
		_, _ = upstream.WriteToUDP(packet[:n], peer)
	}()

	listener := listenUDP(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	forwarder := &DNSForwarder{
		Upstream: netip.MustParseAddrPort(upstream.LocalAddr().String()),
		Timeout:  time.Second,
	}
	done := make(chan error, 1)
	go func() { done <- forwarder.Serve(ctx, listener) }()

	client := listenUDP(t)
	defer client.Close()
	query := dnsQuery(0x1234)
	if _, err := client.WriteToUDP(query, listener.LocalAddr().(*net.UDPAddr)); err != nil {
		t.Fatal(err)
	}
	if err := client.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, maxDNSPacket)
	n, _, err := client.ReadFromUDP(response)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(query) || binary.BigEndian.Uint16(response[:2]) != 0x1234 || response[2]&0x80 == 0 {
		t.Fatalf("unexpected response: %x", response[:n])
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestDNSForwarderDropsMalformedAndMismatchedPackets(t *testing.T) {
	upstream := listenUDP(t)
	defer upstream.Close()
	received := make(chan []byte, 1)
	go func() {
		packet := make([]byte, maxDNSPacket)
		n, peer, err := upstream.ReadFromUDP(packet)
		if err != nil {
			return
		}
		received <- append([]byte(nil), packet[:n]...)
		packet[0], packet[1], packet[2] = 0xff, 0xff, 0x80
		_, _ = upstream.WriteToUDP(packet[:n], peer)
	}()

	listener := listenUDP(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- (&DNSForwarder{
			Upstream: netip.MustParseAddrPort(upstream.LocalAddr().String()),
			Timeout:  100 * time.Millisecond,
		}).Serve(ctx, listener)
	}()
	client := listenUDP(t)
	defer client.Close()
	peer := listener.LocalAddr().(*net.UDPAddr)
	_, _ = client.WriteToUDP([]byte{1, 2, 3}, peer)
	select {
	case packet := <-received:
		t.Fatalf("malformed packet reached upstream: %x", packet)
	case <-time.After(30 * time.Millisecond):
	}
	_, _ = client.WriteToUDP(dnsQuery(7), peer)
	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("valid query did not reach upstream")
	}
	_ = client.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	if _, _, err := client.ReadFromUDP(make([]byte, maxDNSPacket)); err == nil {
		t.Fatal("mismatched response was forwarded")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestDNSForwarderRequiresPinnedUpstream(t *testing.T) {
	listener := listenUDP(t)
	err := (&DNSForwarder{}).Serve(context.Background(), listener)
	if err == nil {
		t.Fatal("missing upstream accepted")
	}
}

func TestDNSRelayTransportRoundTrip(t *testing.T) {
	upstream := listenUDP(t)
	defer upstream.Close()
	go func() {
		packet := make([]byte, maxDNSPacket)
		n, peer, err := upstream.ReadFromUDP(packet)
		if err == nil {
			packet[2] |= 0x80
			_, _ = upstream.WriteToUDP(packet[:n], peer)
		}
	}()

	listener := listenUDP(t)
	host, sandbox := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	hostDone := make(chan error, 1)
	sandboxDone := make(chan error, 1)
	forwarder := &DNSForwarder{
		Upstream: netip.MustParseAddrPort(upstream.LocalAddr().String()),
		Timeout:  time.Second,
	}
	go func() { hostDone <- forwarder.ServeRelay(ctx, host) }()
	go func() { sandboxDone <- ServeDNSRelay(ctx, listener, sandbox) }()

	client := listenUDP(t)
	query := dnsQuery(0x4242)
	if _, err := client.WriteToUDP(query, listener.LocalAddr().(*net.UDPAddr)); err != nil {
		t.Fatal(err)
	}
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	response := make([]byte, maxDNSPacket)
	n, _, err := client.ReadFromUDP(response)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(query) || binary.BigEndian.Uint16(response[:2]) != 0x4242 || response[2]&0x80 == 0 {
		t.Fatalf("unexpected relayed response: %x", response[:n])
	}
	_ = client.Close()
	cancel()
	if err := <-hostDone; err != nil {
		t.Fatal(err)
	}
	if err := <-sandboxDone; err != nil {
		t.Fatal(err)
	}
}

func TestDNSRelayFailsClosed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (&DNSForwarder{}).ServeRelay(ctx, nil); err == nil {
		t.Fatal("nil host relay accepted")
	}
	host, sandbox := net.Pipe()
	if err := (&DNSForwarder{}).ServeRelay(ctx, host); err == nil {
		t.Fatal("relay without pinned upstream accepted")
	}
	_ = sandbox.Close()
	if err := ServeDNSRelay(ctx, nil, nil); err == nil {
		t.Fatal("nil sandbox relay accepted")
	}
}

func TestHostDNSRelayDropsMalformedFramesAndSignalsUpstreamFailure(t *testing.T) {
	host, sandbox := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- (&DNSForwarder{
			Upstream: netip.MustParseAddrPort("127.0.0.1:1"),
			Timeout:  20 * time.Millisecond,
		}).ServeRelay(ctx, host)
	}()
	for _, malformed := range [][]byte{
		{0},
		{0, 5, 1, 2},
		append([]byte{0, 3}, []byte{1, 2, 3}...),
	} {
		if _, err := sandbox.Write(malformed); err != nil {
			t.Fatal(err)
		}
	}
	query := dnsQuery(0x5151)
	frame := make([]byte, len(query)+2)
	binary.BigEndian.PutUint16(frame[:2], uint16(len(query)))
	copy(frame[2:], query)
	if _, err := sandbox.Write(frame); err != nil {
		t.Fatal(err)
	}
	_ = sandbox.SetReadDeadline(time.Now().Add(time.Second))
	response := make([]byte, 2)
	if n, err := sandbox.Read(response); err != nil || n != 2 ||
		binary.BigEndian.Uint16(response) != 0 {
		t.Fatalf("failure frame n=%d response=%x err=%v", n, response, err)
	}
	cancel()
	_ = sandbox.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSandboxDNSRelayReportsClosedTransport(t *testing.T) {
	listener := listenUDP(t)
	host, sandbox := net.Pipe()
	_ = host.Close()
	done := make(chan error, 1)
	go func() { done <- ServeDNSRelay(context.Background(), listener, sandbox) }()
	client := listenUDP(t)
	defer client.Close()
	if _, err := client.WriteToUDP(dnsQuery(9), listener.LocalAddr().(*net.UDPAddr)); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "transport") {
			t.Fatalf("err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("relay did not report closed transport")
	}
}

func TestDNSQueryValidationRejectsTruncatedQuestion(t *testing.T) {
	query := dnsQuery(1)
	if !validDNSQuery(query) {
		t.Fatal("valid query rejected")
	}
	for _, malformed := range [][]byte{
		query[:dnsHeaderLen],
		append(append([]byte(nil), query[:dnsHeaderLen]...), 64),
		append(append([]byte(nil), query[:dnsHeaderLen]...), 0xc0),
	} {
		if validDNSQuery(malformed) {
			t.Fatalf("malformed question accepted: %x", malformed)
		}
	}
}

func listenUDP(t *testing.T) *net.UDPConn {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func dnsQuery(id uint16) []byte {
	packet := make([]byte, dnsHeaderLen+5)
	binary.BigEndian.PutUint16(packet[0:2], id)
	binary.BigEndian.PutUint16(packet[4:6], 1)
	// Root name, type A, class IN.
	packet[12] = 0
	binary.BigEndian.PutUint16(packet[13:15], 1)
	binary.BigEndian.PutUint16(packet[15:17], 1)
	return packet
}
