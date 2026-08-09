package kvm

import "testing"

func TestHandleLinuxPCIConfigIOReadsAllOnes(t *testing.T) {
	for _, port := range []uint16{pciConfigAddressPort, pciConfigDataPort} {
		data := []byte{0x00, 0x00, 0x00, 0x00}
		handled, err := handleLinuxPCIConfigIO(0, 4, port, data)
		if err != nil || !handled {
			t.Fatalf("port=%#x handled=%t err=%v", port, handled, err)
		}
		for _, b := range data {
			if b != 0xff {
				t.Fatalf("port=%#x read=%x, want all-ones", port, data)
			}
		}
	}
}

func TestHandleLinuxPCIConfigIONeverEchoesWrites(t *testing.T) {
	for _, port := range []uint16{pciConfigAddressPort, pciConfigDataPort} {
		data := []byte{0x00, 0x00, 0x00, 0x80}
		handled, err := handleLinuxPCIConfigIO(linuxIODirectionOut, 4, port, data)
		if err != nil || !handled {
			t.Fatalf("port=%#x handled=%t err=%v", port, handled, err)
		}
		if data[0] != 0x00 || data[3] != 0x80 {
			t.Fatalf("port=%#x write was mutated: %x", port, data)
		}
	}
}

func TestHandleLinuxPCIConfigIOLeavesForeignPortsUnhandled(t *testing.T) {
	data := []byte{0xaa, 0xbb}
	handled, err := handleLinuxPCIConfigIO(0, 2, linuxSerialPortBase, data)
	if err != nil || handled {
		t.Fatalf("handled=%t err=%v", handled, err)
	}
	if data[0] != 0xaa || data[1] != 0xbb {
		t.Fatalf("unhandled access mutated data: %x", data)
	}
}
