package kvm

import (
	"errors"
	"fmt"
	"io"
	"sync"
)

const (
	linuxGuestSerialPortBase = uint16(0x2f8)
	linuxGuestSerialQueue    = 4096
)

// handleLinuxAbsentGuestChannelIO answers the COM2 port range (0x2F8-0x2FF)
// with a clean "no device" signal when no guest-transport channel is
// configured. Left unhandled, these ports would fall through untouched,
// leaking whatever stale bytes were last left in the shared kvm_run mapping
// by an unrelated I/O exit - the same class of bug fixed for the PCI
// configuration ports in linux_pci.go, this time letting the 8250 driver's
// COM2 autoconfig() probe see coincidentally "valid-looking" garbage instead
// of a deterministic absence.
func handleLinuxAbsentGuestChannelIO(direction byte, size uint64, port uint16, data []byte) (bool, error) {
	if port < linuxGuestSerialPortBase || port > linuxGuestSerialPortBase+7 {
		return false, nil
	}
	if direction != linuxIODirectionOut {
		for index := range data {
			data[index] = 0xff
		}
	}
	return true, nil
}

// linuxGuestSerialDevice is a bounded, bidirectional 8250 UART data plane.
// Only its pumps touch the host stream; the KVM thread communicates with them
// through bounded channels and therefore never blocks in a host Read or Write.
type linuxGuestSerialDevice struct {
	conn io.ReadWriteCloser
	rx   chan byte
	tx   chan byte
	done chan struct{}

	closeOnce sync.Once
	wg        sync.WaitGroup
	errMu     sync.Mutex
	err       error

	divisorLow      byte
	divisorHigh     byte
	interruptEnable byte
	lineControl     byte
	modemControl    byte
	scratch         byte
	txPending       bool
}

func newLinuxGuestSerialDevice(conn io.ReadWriteCloser, raiseIRQ func()) *linuxGuestSerialDevice {
	device := &linuxGuestSerialDevice{
		conn: conn,
		rx:   make(chan byte, linuxGuestSerialQueue),
		tx:   make(chan byte, linuxGuestSerialQueue),
		done: make(chan struct{}),
	}
	device.wg.Add(2)
	go device.readPump(raiseIRQ)
	go device.writePump()
	return device
}

func (d *linuxGuestSerialDevice) readPump(raiseIRQ func()) {
	defer d.wg.Done()
	buffer := make([]byte, 4096)
	for {
		count, err := d.conn.Read(buffer)
		for _, value := range buffer[:count] {
			select {
			case d.rx <- value:
			case <-d.done:
				return
			}
		}
		if count != 0 && raiseIRQ != nil {
			raiseIRQ()
		}
		if err != nil {
			d.recordError(err)
			return
		}
		if count == 0 {
			d.recordError(io.ErrNoProgress)
			return
		}
	}
}

func (d *linuxGuestSerialDevice) writePump() {
	defer d.wg.Done()
	for {
		select {
		case value := <-d.tx:
			buffer := []byte{value}
			for len(buffer) != 0 {
				count, err := d.conn.Write(buffer)
				if err != nil {
					d.recordError(err)
					return
				}
				if count <= 0 || count > len(buffer) {
					d.recordError(io.ErrNoProgress)
					return
				}
				buffer = buffer[count:]
			}
		case <-d.done:
			return
		}
	}
}

func (d *linuxGuestSerialDevice) recordError(err error) {
	select {
	case <-d.done:
		return
	default:
	}
	d.errMu.Lock()
	if d.err == nil {
		d.err = err
	}
	d.errMu.Unlock()
}

func (d *linuxGuestSerialDevice) transportError() error {
	d.errMu.Lock()
	defer d.errMu.Unlock()
	if d.err == nil {
		return nil
	}
	return fmt.Errorf("vmm: guest serial transport: %w", d.err)
}

func (d *linuxGuestSerialDevice) Close() error {
	var closeErr error
	d.closeOnce.Do(func() {
		close(d.done)
		closeErr = d.conn.Close()
		d.wg.Wait()
	})
	return closeErr
}

func (d *linuxGuestSerialDevice) handle(direction byte, size uint64, port uint16, data []byte, raiseIRQ func()) (bool, error) {
	if port < linuxGuestSerialPortBase || port > linuxGuestSerialPortBase+7 {
		return false, nil
	}
	if size != 1 {
		return true, errors.New("vmm: guest serial transport requires byte I/O")
	}
	if err := d.transportError(); err != nil {
		return true, err
	}
	register := port - linuxGuestSerialPortBase
	if direction == linuxIODirectionOut {
		for _, value := range data {
			switch register {
			case 0:
				if d.lineControl&0x80 != 0 {
					d.divisorLow = value
					continue
				}
				select {
				case d.tx <- value:
				case <-d.done:
					return true, errors.New("vmm: guest serial transport is closed")
				default:
					return true, errors.New("vmm: guest serial transmit queue is full")
				}
				if d.interruptEnable&0x02 != 0 {
					d.txPending = true
					if raiseIRQ != nil {
						raiseIRQ()
					}
				}
			case 1:
				if d.lineControl&0x80 != 0 {
					d.divisorHigh = value
				} else {
					d.interruptEnable = value
					if value&0x01 != 0 && len(d.rx) != 0 && raiseIRQ != nil {
						raiseIRQ()
					}
				}
			case 3:
				d.lineControl = value
			case 4:
				d.modemControl = value
			case 7:
				d.scratch = value
			}
		}
		return true, nil
	}

	for index := range data {
		switch register {
		case 0:
			if d.lineControl&0x80 != 0 {
				data[index] = d.divisorLow
			} else {
				select {
				case data[index] = <-d.rx:
				default:
					data[index] = 0
				}
			}
		case 1:
			if d.lineControl&0x80 != 0 {
				data[index] = d.divisorHigh
			} else {
				data[index] = d.interruptEnable
			}
		case 2:
			if len(d.rx) != 0 && d.interruptEnable&0x01 != 0 {
				data[index] = 0x04
			} else if d.txPending {
				data[index] = 0x02
				d.txPending = false
			} else {
				data[index] = 0x01
			}
		case 3:
			data[index] = d.lineControl
		case 4:
			data[index] = d.modemControl
		case 5:
			data[index] = 0x60
			if len(d.rx) != 0 {
				data[index] |= 0x01
			}
		case 6:
			data[index] = 0xb0
		case 7:
			data[index] = d.scratch
		}
	}
	return true, nil
}
