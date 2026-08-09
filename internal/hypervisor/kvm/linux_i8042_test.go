package kvm

import (
	"errors"
	"testing"
)

func TestHandleLinuxI8042IOReadsNeutralRegisters(t *testing.T) {
	for _, port := range []uint16{i8042DataPort, i8042CommandPort} {
		data := []byte{0xaa, 0xbb}
		handled, err := handleLinuxI8042IO(0, 1, port, data)
		if err != nil || !handled {
			t.Fatalf("port=%#x handled=%t err=%v", port, handled, err)
		}
		if data[0] != 0 || data[1] != 0 {
			t.Fatalf("port=%#x read=%x, want neutral bytes", port, data)
		}
	}
}

func TestHandleLinuxI8042IOWritesAndReset(t *testing.T) {
	tests := []struct {
		name      string
		port      uint16
		data      []byte
		wantReset bool
	}{
		{name: "ordinary command", port: i8042CommandPort, data: []byte{0xad}},
		{name: "ordinary data", port: i8042DataPort, data: []byte{0xed}},
		{name: "reset command", port: i8042CommandPort, data: []byte{0xad, i8042Reset}, wantReset: true},
		{name: "reset byte on data port", port: i8042DataPort, data: []byte{i8042Reset}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			handled, err := handleLinuxI8042IO(linuxIODirectionOut, 1, tc.port, tc.data)
			if !handled {
				t.Fatal("i8042 access was not handled")
			}
			if errors.Is(err, errLinuxGuestReset) != tc.wantReset {
				t.Fatalf("err=%v wantReset=%t", err, tc.wantReset)
			}
		})
	}
}

func TestHandleLinuxI8042IOLeavesForeignAndNonByteAccessesUnhandled(t *testing.T) {
	for _, tc := range []struct {
		name string
		size uint64
		port uint16
	}{
		{name: "foreign UART", size: 1, port: linuxSerialPortBase},
		{name: "foreign CMOS", size: 1, port: 0x70},
		{name: "word command", size: 2, port: i8042CommandPort},
		{name: "zero size", size: 0, port: i8042DataPort},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := []byte{0xaa, 0xbb}
			handled, err := handleLinuxI8042IO(0, tc.size, tc.port, data)
			if err != nil || handled {
				t.Fatalf("handled=%t err=%v", handled, err)
			}
			if data[0] != 0xaa || data[1] != 0xbb {
				t.Fatalf("unhandled access mutated data: %x", data)
			}
		})
	}
}
