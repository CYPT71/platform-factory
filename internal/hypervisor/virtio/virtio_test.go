package virtio

import (
	"bytes"
	"net"
	"testing"
)

func TestDeviceType(t *testing.T) {
	if DevNet != 1 {
		t.Errorf("DevNet = %d, want 1", DevNet)
	}
	if DevBlock != 2 {
		t.Errorf("DevBlock = %d, want 2", DevBlock)
	}
	if DevConsole != 3 {
		t.Errorf("DevConsole = %d, want 3", DevConsole)
	}
}

func TestBlockDevice(t *testing.T) {
	dev := NewBlockDevice(10000)
	if dev.Config.Capacity != 10000 {
		t.Errorf("Capacity = %d, want 10000", dev.Config.Capacity)
	}
	if dev.Config.BlkSize != 512 {
		t.Errorf("BlkSize = %d, want 512", dev.Config.BlkSize)
	}
}

func TestNetworkDevice(t *testing.T) {
	mac := net.HardwareAddr{0x00, 0x11, 0x22, 0x33, 0x44, 0x55}
	dev := NewNetworkDevice(mac, 1500)
	if dev.Config.MTU != 1500 {
		t.Errorf("MTU = %d, want 1500", dev.Config.MTU)
	}
	if !bytes.Equal(dev.MAC(), mac) {
		t.Errorf("MAC = %v, want %v", dev.MAC(), mac)
	}
}

func TestConsoleDevice(t *testing.T) {
	var buf bytes.Buffer
	dev := NewConsoleDevice(nil, &buf)

	n, err := dev.Write([]byte("test"))
	if err != nil {
		t.Errorf("Write error = %v", err)
	}
	if n != 4 {
		t.Errorf("Write returned %d, want 4", n)
	}
	if buf.String() != "test" {
		t.Errorf("buffer = %q, want %q", buf.String(), "test")
	}
}

func TestConsoleDeviceRead(t *testing.T) {
	input := bytes.NewBufferString("hello")
	dev := NewConsoleDevice(input, nil)

	data := make([]byte, 5)
	n, err := dev.Read(data)
	if err != nil {
		t.Errorf("Read error = %v", err)
	}
	if n != 5 {
		t.Errorf("Read returned %d, want 5", n)
	}
	if string(data) != "hello" {
		t.Errorf("data = %q, want %q", string(data), "hello")
	}
}

func TestConsoleDeviceReadEOF(t *testing.T) {
	dev := NewConsoleDevice(nil, nil)

	data := make([]byte, 10)
	n, err := dev.Read(data)
	if err == nil {
		t.Error("Read expected EOF error, got nil")
	}
	if n != 0 {
		t.Errorf("Read returned %d, want 0", n)
	}
}

func TestConsoleDeviceWriteNoOutput(t *testing.T) {
	dev := NewConsoleDevice(nil, nil)

	n, err := dev.Write([]byte("test"))
	if err != nil {
		t.Errorf("Write error = %v", err)
	}
	if n != 4 {
		t.Errorf("Write returned %d, want 4", n)
	}
}
