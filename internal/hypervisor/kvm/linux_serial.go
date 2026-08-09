package kvm

import "io"

const (
	linuxSerialPortBase = uint16(0x3f8)
	maxLinuxSerialBytes = 1 << 20
	linuxIODirectionOut = byte(1)
)

type LinuxRunResult struct {
	ExitReason uint32
	Shutdown   bool
	Halted     bool
	Exits      int
	Serial     []byte
}

type linuxSerialState struct {
	divisorLow      byte
	divisorHigh     byte
	interruptEnable byte
	fifoControl     byte
	lineControl     byte
	modemControl    byte
	scratch         byte

	cmosIndex byte

	txInterruptPending bool
	output             io.Writer
	outputErr          error
}

// handleLinuxSerialIO emulates the minimal 8250 UART and CMOS/RTC ports the
// Linux boot protocol touches. Port 0x61 (the PIT channel-2 gate Linux polls
// during TSC calibration) is deliberately absent here: KVM_CREATE_PIT2's
// kvmPITSpeakerDummy flag makes KVM itself the in-kernel owner of that port,
// backed by a real elapsed-time-accurate countdown - see its doc comment in
// kvm_run_linux_amd64.go. A guest access to 0x61 never reaches this
// function.
// raiseIRQ, if non-nil, is called to pulse IRQ4 (COM1) on the guest's
// in-kernel PIC whenever a transmit-holding-register write leaves a
// THRE-interrupt armed: the 8250 driver Linux uses once it takes over from
// earlycon is interrupt-driven, and without a real interrupt telling it the
// (already-empty, in this emulation) transmit register is free again, it
// blocks forever after the first byte waiting for one that never comes.
func handleLinuxSerialIO(
	direction byte,
	size uint64,
	port uint16,
	count uint64,
	data []byte,
	state *linuxSerialState,
	result *LinuxRunResult,
	raiseIRQ func(),
) {
	if size != 1 {
		return
	}

	// Minimal MC146818 CMOS/RTC emulation. Reporting a floating bus (0xff)
	// here, like every other unimplemented port, leaves status register A's
	// UIP bit permanently set; callers that poll for it to clear (the RTC
	// driver's own read-time retry loop and, transitively, whatever probes
	// it at driver init) spin on this pair of ports indefinitely instead of
	// observing a bounded failure. Reporting UIP always clear, and 0x00 for
	// every other register, keeps every such read a single bounded op.
	if port == 0x70 || port == 0x71 {
		if port == 0x70 {
			if direction == linuxIODirectionOut && len(data) != 0 {
				state.cmosIndex = data[len(data)-1] &^ 0x80
			}
			return
		}
		if direction != linuxIODirectionOut {
			value := byte(0x00)
			if state.cmosIndex == 0x0b {
				value = 0x06 // Status B: 24-hour format, binary (not BCD) data.
			}
			for index := range data {
				data[index] = value
			}
		}
		return
	}

	if port < linuxSerialPortBase || port > linuxSerialPortBase+7 {
		if direction != linuxIODirectionOut {
			for index := range data {
				data[index] = 0xff
			}
		}
		return
	}

	register := port - linuxSerialPortBase

	if direction == linuxIODirectionOut {
		for _, value := range data {
			switch register {
			case 0:
				if state.lineControl&0x80 != 0 {
					state.divisorLow = value
				} else {
					if len(result.Serial) < maxLinuxSerialBytes {
						result.Serial = append(result.Serial, value)
					}
					if state.output != nil && state.outputErr == nil {
						written, err := state.output.Write([]byte{value})
						if err != nil {
							state.outputErr = err
						} else if written != 1 {
							state.outputErr = io.ErrShortWrite
						}
					}
					// THRE (bit 1 of IER): the transmit register is always
					// immediately empty in this emulation, so a byte written
					// while that interrupt is armed can be reported as
					// "ready for more" right away.
					if state.interruptEnable&0x02 != 0 {
						state.txInterruptPending = true
						if raiseIRQ != nil {
							raiseIRQ()
						}
					}
				}

			case 1:
				if state.lineControl&0x80 != 0 {
					state.divisorHigh = value
				} else {
					state.interruptEnable = value
				}

			case 2:
				state.fifoControl = value

			case 3:
				state.lineControl = value

			case 4:
				state.modemControl = value

			case 7:
				state.scratch = value
			}
		}
		return
	}

	var value byte

	switch register {
	case 0:
		if state.lineControl&0x80 != 0 {
			value = state.divisorLow
		} else {
			value = 0x00 // RBR: aucune donnée reçue
		}

	case 1:
		if state.lineControl&0x80 != 0 {
			value = state.divisorHigh
		} else {
			value = state.interruptEnable
		}

	case 2:
		// Do not advertise FIFO state here: Linux's early 8250 probing only
		// needs the pending bit and may repeatedly probe the transmitter
		// when synthetic FIFO capability bits are reported. Reading IIR
		// acknowledges a THRE interrupt on real hardware, so clear it here
		// too; otherwise the driver's interrupt handler would see it stay
		// asserted forever after the first byte.
		if state.txInterruptPending {
			value = 0x02
			state.txInterruptPending = false
		} else {
			value = 0x01
		}

	case 3:
		value = state.lineControl

	case 4:
		value = state.modemControl

	case 5:
		value = 0x60 // LSR: THR vide et transmetteur vide

	case 6:
		value = 0xb0 // MSR: CTS, DSR et DCD actifs

	case 7:
		value = state.scratch

	default:
		value = 0x00
	}

	for index := range data {
		data[index] = value
	}
}
