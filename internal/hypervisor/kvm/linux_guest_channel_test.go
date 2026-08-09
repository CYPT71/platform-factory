package kvm

import (
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestHandleLinuxAbsentGuestChannelIOReadsAllOnes(t *testing.T) {
	for port := linuxGuestSerialPortBase; port <= linuxGuestSerialPortBase+7; port++ {
		data := []byte{0x00, 0x00}
		handled, err := handleLinuxAbsentGuestChannelIO(0, 1, port, data)
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

func TestHandleLinuxAbsentGuestChannelIONeverEchoesWrites(t *testing.T) {
	data := []byte{0x41}
	handled, err := handleLinuxAbsentGuestChannelIO(linuxIODirectionOut, 1, linuxGuestSerialPortBase, data)
	if err != nil || !handled {
		t.Fatalf("handled=%t err=%v", handled, err)
	}
	if data[0] != 0x41 {
		t.Fatalf("write was mutated: %x", data)
	}
}

func TestHandleLinuxAbsentGuestChannelIOLeavesForeignPortsUnhandled(t *testing.T) {
	data := []byte{0xaa, 0xbb}
	handled, err := handleLinuxAbsentGuestChannelIO(0, 1, linuxSerialPortBase, data)
	if err != nil || handled {
		t.Fatalf("handled=%t err=%v", handled, err)
	}
	if data[0] != 0xaa || data[1] != 0xbb {
		t.Fatalf("unhandled access mutated data: %x", data)
	}
}

func TestLinuxGuestSerialDeviceBidirectionalABI(t *testing.T) {
	host, guest := net.Pipe()
	interrupts := make(chan struct{}, 4)
	device := newLinuxGuestSerialDevice(host, func() {
		select {
		case interrupts <- struct{}{}:
		default:
		}
	})
	defer device.Close()
	defer guest.Close()

	// Enable receive interrupts, then deliver host bytes to the guest UART.
	if handled, err := device.handle(
		linuxIODirectionOut, 1, linuxGuestSerialPortBase+1, []byte{0x01}, nil,
	); !handled || err != nil {
		t.Fatalf("enable RX interrupt: handled=%t err=%v", handled, err)
	}
	go func() {
		_, _ = guest.Write([]byte("host"))
	}()
	select {
	case <-interrupts:
	case <-time.After(time.Second):
		t.Fatal("host input did not raise IRQ3")
	}
	eventually(t, func() bool {
		status := []byte{0}
		_, _ = device.handle(0, 1, linuxGuestSerialPortBase+5, status, nil)
		return status[0]&0x01 != 0
	})
	received := make([]byte, 4)
	for index := range received {
		if _, err := device.handle(0, 1, linuxGuestSerialPortBase, received[index:index+1], nil); err != nil {
			t.Fatal(err)
		}
	}
	if string(received) != "host" {
		t.Fatalf("guest received %q", received)
	}

	hostOutput := make(chan []byte, 1)
	go func() {
		buffer := make([]byte, 5)
		_, _ = io.ReadFull(guest, buffer)
		hostOutput <- buffer
	}()
	if _, err := device.handle(
		linuxIODirectionOut, 1, linuxGuestSerialPortBase, []byte("guest"), nil,
	); err != nil {
		t.Fatal(err)
	}
	select {
	case output := <-hostOutput:
		if string(output) != "guest" {
			t.Fatalf("host received %q", output)
		}
	case <-time.After(time.Second):
		t.Fatal("guest output did not reach host stream")
	}
}

func TestLinuxGuestSerialDeviceRegistersAndBounds(t *testing.T) {
	host, guest := net.Pipe()
	device := newLinuxGuestSerialDevice(host, nil)
	defer device.Close()
	defer guest.Close()

	if handled, err := device.handle(0, 1, linuxGuestSerialPortBase-1, []byte{0}, nil); handled || err != nil {
		t.Fatalf("foreign port handled=%t err=%v", handled, err)
	}
	if _, err := device.handle(0, 2, linuxGuestSerialPortBase, []byte{0, 0}, nil); err == nil {
		t.Fatal("wide UART access accepted")
	}
	if _, err := device.handle(
		linuxIODirectionOut, 1, linuxGuestSerialPortBase+3, []byte{0x80}, nil,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := device.handle(
		linuxIODirectionOut, 1, linuxGuestSerialPortBase, []byte{0x34}, nil,
	); err != nil {
		t.Fatal(err)
	}
	divisor := []byte{0}
	if _, err := device.handle(0, 1, linuxGuestSerialPortBase, divisor, nil); err != nil {
		t.Fatal(err)
	}
	if divisor[0] != 0x34 {
		t.Fatalf("divisor low=%#x", divisor[0])
	}

	// A full bounded queue fails closed instead of blocking KVM_RUN.
	device.lineControl = 0
	device.tx <- 0 // The writer consumes this byte and blocks in net.Pipe.Write.
	eventually(t, func() bool { return len(device.tx) == 0 })
	for range cap(device.tx) {
		device.tx <- 0
	}
	if _, err := device.handle(
		linuxIODirectionOut, 1, linuxGuestSerialPortBase, []byte{'x'}, nil,
	); err == nil || !strings.Contains(err.Error(), "queue is full") {
		t.Fatalf("full queue error=%v", err)
	}
}

func TestLinuxGuestSerialDeviceReportsHostFailure(t *testing.T) {
	connection := &failingReadWriteCloser{}
	device := newLinuxGuestSerialDevice(connection, nil)
	defer device.Close()
	eventually(t, func() bool { return device.transportError() != nil })
	if err := device.transportError(); err == nil || !strings.Contains(err.Error(), "read failed") {
		t.Fatalf("transport error=%v", err)
	}
}

func TestLinuxGuestSerialDeviceCoversControlAndStatusRegisters(t *testing.T) {
	device := &linuxGuestSerialDevice{
		rx: make(chan byte, 4), tx: make(chan byte, 4), done: make(chan struct{}),
	}
	irq := 0
	raiseIRQ := func() { irq++ }
	write := func(register uint16, value byte) {
		t.Helper()
		if handled, err := device.handle(
			linuxIODirectionOut, 1, linuxGuestSerialPortBase+register, []byte{value}, raiseIRQ,
		); !handled || err != nil {
			t.Fatalf("write register %d: handled=%t err=%v", register, handled, err)
		}
	}
	read := func(register uint16) byte {
		t.Helper()
		data := []byte{0}
		if handled, err := device.handle(0, 1, linuxGuestSerialPortBase+register, data, nil); !handled || err != nil {
			t.Fatalf("read register %d: handled=%t err=%v", register, handled, err)
		}
		return data[0]
	}

	write(3, 0x80)
	write(0, 0x34)
	write(1, 0x12)
	if got := read(0); got != 0x34 {
		t.Fatalf("divisor low=%#x", got)
	}
	if got := read(1); got != 0x12 {
		t.Fatalf("divisor high=%#x", got)
	}
	write(3, 0x03)
	write(4, 0x0b)
	write(7, 0xa5)
	if read(3) != 0x03 || read(4) != 0x0b || read(7) != 0xa5 {
		t.Fatal("control register round trip failed")
	}

	device.rx <- 'r'
	write(1, 0x03)
	if irq != 1 {
		t.Fatalf("receive interrupt count=%d", irq)
	}
	if got := read(2); got != 0x04 {
		t.Fatalf("receive interrupt identification=%#x", got)
	}
	if got := read(5); got != 0x61 {
		t.Fatalf("line status with data=%#x", got)
	}
	if got := read(0); got != 'r' {
		t.Fatalf("received byte=%#x", got)
	}
	write(0, 't')
	if irq != 2 || !device.txPending {
		t.Fatalf("transmit interrupt count=%d pending=%t", irq, device.txPending)
	}
	if got := read(2); got != 0x02 {
		t.Fatalf("transmit interrupt identification=%#x", got)
	}
	if got := read(2); got != 0x01 {
		t.Fatalf("idle interrupt identification=%#x", got)
	}
	if read(5) != 0x60 || read(6) != 0xb0 {
		t.Fatal("fixed UART status registers changed")
	}
}

func eventually(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition was not met")
		}
		time.Sleep(time.Millisecond)
	}
}

type failingReadWriteCloser struct{}

func (*failingReadWriteCloser) Read([]byte) (int, error) {
	return 0, errors.New("read failed")
}

func (*failingReadWriteCloser) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}

func (*failingReadWriteCloser) Close() error { return nil }
