// Package forward provides bounded host-port -> guest-port TCP and UDP
// relays. It exists as the native-KVM
// microVM path's stand-in for QEMU SLIRP's `hostfwd=` NAT rules, which
// this project's TAP-based virtio-net device has no equivalent for (see
// internal/hypervisor/kvm's NetworkDeviceOptions doc comment). It is not
// NAT and not a general-purpose proxy: one fixed guest address per relay,
// with no notion of the guest's own outbound connectivity.
package forward

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

const (
	maxUDPSessions      = 1024
	udpSessionIdle      = 2 * time.Minute
	udpReadPollInterval = 250 * time.Millisecond
	maxUDPDatagram      = 65535
)

// Relay listens on listenAddr and, for every accepted connection, dials
// dialAddr and copies bytes in both directions until either side closes or
// ctx is cancelled. Cancellation force-closes the listener and every
// in-flight connection outright - matching how a microVM's own shutdown
// already tears down its KVM_RUN loop on context cancellation - rather
// than waiting for each relayed connection to end on its own, which a
// long-lived client (a kept-alive HTTP connection, for instance) might
// never do by itself. Relay returns once that teardown is complete.
//
// A dial failure (the guest not reachable yet, or no longer reachable) is
// per-connection: the accepted connection is simply closed, and the
// listener keeps accepting - the same behavior a client sees against
// QEMU's own hostfwd before the guest's server is listening, so callers
// do not need a separate readiness handshake before starting Relay.
func Relay(ctx context.Context, listenAddr, dialAddr string) error {
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(ctx, "tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("microvm: forward: listen %s: %w", listenAddr, err)
	}

	var (
		mu    sync.Mutex
		conns = make(map[net.Conn]struct{})
	)
	track := func(c net.Conn) { mu.Lock(); conns[c] = struct{}{}; mu.Unlock() }
	untrack := func(c net.Conn) { mu.Lock(); delete(conns, c); mu.Unlock() }
	closeAll := func() {
		mu.Lock()
		defer mu.Unlock()
		for c := range conns {
			_ = c.Close()
		}
	}

	var wg sync.WaitGroup
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
		case <-done:
		}
		_ = listener.Close()
		closeAll()
	}()
	defer close(done)

	var dialer net.Dialer
	for {
		conn, err := listener.Accept()
		if err != nil {
			wg.Wait()
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("microvm: forward: accept on %s: %w", listenAddr, err)
		}
		track(conn)
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer untrack(conn)
			relayOne(ctx, &dialer, conn, dialAddr, track, untrack)
		}()
	}
}

type udpSession struct {
	conn     *net.UDPConn
	client   *net.UDPAddr
	lastSeen time.Time
}

// RelayUDP forwards datagrams while preserving a bounded per-client UDP
// session. Responses from the fixed guest endpoint return to the exact host
// source address. Idle sessions expire and the oldest entry is evicted at the
// fixed limit, preventing source-address floods from growing descriptors
// without bound. Cancellation closes every socket before returning.
func RelayUDP(ctx context.Context, listenAddr, dialAddr string) error {
	listenUDP, err := net.ResolveUDPAddr("udp", listenAddr)
	if err != nil {
		return fmt.Errorf("microvm: forward: resolve UDP listen %s: %w", listenAddr, err)
	}
	guestUDP, err := net.ResolveUDPAddr("udp", dialAddr)
	if err != nil {
		return fmt.Errorf("microvm: forward: resolve UDP guest %s: %w", dialAddr, err)
	}
	listener, err := net.ListenUDP("udp", listenUDP)
	if err != nil {
		return fmt.Errorf("microvm: forward: listen UDP %s: %w", listenAddr, err)
	}
	defer listener.Close()

	var (
		mu       sync.Mutex
		sessions = make(map[string]*udpSession)
		wg       sync.WaitGroup
	)
	remove := func(key string, expected *udpSession) {
		mu.Lock()
		if sessions[key] == expected {
			delete(sessions, key)
			_ = expected.conn.Close()
		}
		mu.Unlock()
	}
	closeAll := func() {
		mu.Lock()
		for key, session := range sessions {
			_ = session.conn.Close()
			delete(sessions, key)
		}
		mu.Unlock()
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
		case <-done:
		}
		_ = listener.Close()
		closeAll()
	}()
	defer close(done)
	defer func() {
		closeAll()
		wg.Wait()
	}()

	buffer := make([]byte, maxUDPDatagram)
	for {
		n, client, readErr := listener.ReadFromUDP(buffer)
		if readErr != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("microvm: forward: receive UDP on %s: %w", listenAddr, readErr)
		}
		key := client.String()
		now := time.Now()
		mu.Lock()
		session := sessions[key]
		if session == nil {
			if len(sessions) >= maxUDPSessions {
				var oldestKey string
				var oldest *udpSession
				for candidateKey, candidate := range sessions {
					if oldest == nil || candidate.lastSeen.Before(oldest.lastSeen) {
						oldestKey, oldest = candidateKey, candidate
					}
				}
				delete(sessions, oldestKey)
				_ = oldest.conn.Close()
			}
			guestConn, dialErr := net.DialUDP("udp", nil, guestUDP)
			if dialErr != nil {
				mu.Unlock()
				continue
			}
			session = &udpSession{conn: guestConn, client: client, lastSeen: now}
			sessions[key] = session
			wg.Add(1)
			go func(key string, session *udpSession) {
				defer wg.Done()
				response := make([]byte, maxUDPDatagram)
				for {
					_ = session.conn.SetReadDeadline(time.Now().Add(udpReadPollInterval))
					n, err := session.conn.Read(response)
					if n > 0 {
						if _, writeErr := listener.WriteToUDP(response[:n], session.client); writeErr != nil {
							remove(key, session)
							return
						}
						mu.Lock()
						session.lastSeen = time.Now()
						mu.Unlock()
					}
					if err == nil {
						continue
					}
					if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
						mu.Lock()
						idle := time.Since(session.lastSeen) >= udpSessionIdle
						mu.Unlock()
						if !idle && ctx.Err() == nil {
							continue
						}
					}
					remove(key, session)
					return
				}
			}(key, session)
		}
		session.lastSeen = now
		guestConn := session.conn
		mu.Unlock()
		if _, writeErr := guestConn.Write(buffer[:n]); writeErr != nil {
			remove(key, session)
		}
	}
}

// relayOne handles one accepted host-side connection: dial the guest,
// splice both directions, close both ends once either direction is done.
// The dialed guest connection is tracked/untracked the same way the
// caller already tracks hostConn, so Relay's cancellation teardown closes
// both ends of a connection currently mid-copy, not just the host side.
func relayOne(ctx context.Context, dialer *net.Dialer, hostConn net.Conn, dialAddr string, track, untrack func(net.Conn)) {
	defer hostConn.Close()
	guestConn, err := dialer.DialContext(ctx, "tcp", dialAddr)
	if err != nil {
		return
	}
	track(guestConn)
	defer untrack(guestConn)
	defer guestConn.Close()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(guestConn, hostConn)
		_ = closeWrite(guestConn)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(hostConn, guestConn)
		_ = closeWrite(hostConn)
	}()
	wg.Wait()
}

// closeWrite half-closes conn's write side so the peer sees EOF as soon as
// one direction finishes, instead of only once the whole connection is
// torn down by the deferred Close in relayOne - required for request-
// response protocols (HTTP/1.x included) where the client signals "no
// more data" by closing its write side while still reading the response.
func closeWrite(conn net.Conn) error {
	type writeCloser interface {
		CloseWrite() error
	}
	if wc, ok := conn.(writeCloser); ok {
		return wc.CloseWrite()
	}
	return errors.New("microvm: forward: connection does not support half-close")
}
