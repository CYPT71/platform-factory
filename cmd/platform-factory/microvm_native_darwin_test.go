//go:build darwin

package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Exercise the real fail-closed preparation path without requiring the
// Virtualization.framework entitlement or starting a guest.
func TestRunNativeKVMRejectsMissingLayoutBeforeBoot(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := runNativeKVM(context.Background(), filepath.Join(t.TempDir(), "missing-layout"), 128, nil, &stdout, &stderr)
	if err == nil {
		t.Fatal("missing OCI layout reached native boot")
	}
	if !strings.Contains(err.Error(), "convert rootfs") {
		t.Fatalf("err=%v", err)
	}
	if !strings.Contains(stderr.String(), "phase=build-init") || !strings.Contains(stderr.String(), "phase=rootfs") {
		t.Fatalf("missing preparation evidence: %s", stderr.String())
	}
}

func TestIPWatchingWriterTriggersOnceOnMarkerLine(t *testing.T) {
	var passthrough bytes.Buffer
	var got []string
	w := &ipWatchingWriter{
		passthrough: &passthrough,
		onGuestIP:   func(ip string) { got = append(got, ip) },
	}
	if _, err := w.Write([]byte("boot messages\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(guestIPReportMarker + "10.20.30.40\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(guestIPReportMarker + "99.99.99.99\n")); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "10.20.30.40" {
		t.Fatalf("onGuestIP calls=%v, want exactly one call with the first IP", got)
	}
	if !strings.Contains(passthrough.String(), "boot messages") ||
		!strings.Contains(passthrough.String(), "10.20.30.40") ||
		!strings.Contains(passthrough.String(), "99.99.99.99") {
		t.Fatalf("passthrough=%q, want every byte forwarded regardless of trigger state", passthrough.String())
	}
}

func TestIPWatchingWriterHandlesMarkerSplitAcrossWrites(t *testing.T) {
	var passthrough bytes.Buffer
	var got string
	w := &ipWatchingWriter{
		passthrough: &passthrough,
		onGuestIP:   func(ip string) { got = ip },
	}
	marker := guestIPReportMarker + "192.168.1.1\n"
	mid := len(marker) / 2
	if _, err := w.Write([]byte(marker[:mid])); err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("onGuestIP fired early on a partial marker: %q", got)
	}
	if _, err := w.Write([]byte(marker[mid:])); err != nil {
		t.Fatal(err)
	}
	if got != "192.168.1.1" {
		t.Fatalf("got=%q, want 192.168.1.1 once the marker line completes", got)
	}
}

func TestIPWatchingWriterIgnoresMalformedAddress(t *testing.T) {
	var passthrough bytes.Buffer
	called := false
	w := &ipWatchingWriter{
		passthrough: &passthrough,
		onGuestIP:   func(string) { called = true },
	}
	if _, err := w.Write([]byte(guestIPReportMarker + "not-an-ip\n")); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("onGuestIP should not fire for an unparseable address")
	}
}

func TestRandomLocallyAdministeredMACIsWellFormed(t *testing.T) {
	mac, err := randomLocallyAdministeredMAC()
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(mac, ":")
	if len(parts) != 6 {
		t.Fatalf("mac=%q, want 6 colon-separated octets", mac)
	}
	value, err := strconv.ParseUint(parts[0], 16, 8)
	if err != nil {
		t.Fatalf("mac=%q: %v", mac, err)
	}
	first := byte(value)
	if first&0x01 != 0 {
		t.Fatalf("mac=%q: multicast bit set, want unicast", mac)
	}
	if first&0x02 == 0 {
		t.Fatalf("mac=%q: locally-administered bit not set", mac)
	}
}
