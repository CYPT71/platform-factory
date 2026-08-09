package kvm

import (
	"bytes"
	"errors"
	"testing"
)

func TestHandleLinuxSerialIO(t *testing.T) {
	result := LinuxRunResult{}
	state := linuxSerialState{}
	handleLinuxSerialIO(linuxIODirectionOut, 1, linuxSerialPortBase, 3, []byte("abc"), &state, &result, nil)
	if string(result.Serial) != "abc" {
		t.Fatalf("serial=%q", result.Serial)
	}
	status := []byte{0}
	handleLinuxSerialIO(0, 1, linuxSerialPortBase+5, 1, status, &state, &result, nil)
	if status[0] != 0x60 {
		t.Fatalf("LSR=%#x", status[0])
	}
	unknown := []byte{0, 0}
	handleLinuxSerialIO(0, 1, 0x1234, 2, unknown, &state, &result, nil)
	if !bytes.Equal(unknown, []byte{0xff, 0xff}) {
		t.Fatalf("floating bus=%x", unknown)
	}
}

func TestHandleLinuxSerialIOBoundsCapture(t *testing.T) {
	result := LinuxRunResult{Serial: make([]byte, maxLinuxSerialBytes-1)}
	state := linuxSerialState{}
	handleLinuxSerialIO(linuxIODirectionOut, 1, linuxSerialPortBase, 2, []byte("xy"), &state, &result, nil)
	if len(result.Serial) != maxLinuxSerialBytes || result.Serial[len(result.Serial)-1] != 'x' {
		t.Fatal("serial capture was not bounded")
	}
}

func TestHandleLinuxSerialIOStreamsBeyondCaptureLimit(t *testing.T) {
	var streamed bytes.Buffer
	result := LinuxRunResult{Serial: make([]byte, maxLinuxSerialBytes)}
	state := linuxSerialState{output: &streamed}
	handleLinuxSerialIO(linuxIODirectionOut, 1, linuxSerialPortBase, 2, []byte("xy"), &state, &result, nil)
	if got := streamed.String(); got != "xy" {
		t.Fatalf("streamed serial=%q", got)
	}
	if len(result.Serial) != maxLinuxSerialBytes {
		t.Fatalf("bounded capture grew to %d bytes", len(result.Serial))
	}
}

func TestHandleLinuxSerialIORemembersStreamFailure(t *testing.T) {
	want := errors.New("closed log")
	result := LinuxRunResult{}
	state := linuxSerialState{output: errorWriter{err: want}}
	handleLinuxSerialIO(linuxIODirectionOut, 1, linuxSerialPortBase, 1, []byte("x"), &state, &result, nil)
	if !errors.Is(state.outputErr, want) {
		t.Fatalf("stream error=%v, want %v", state.outputErr, want)
	}
}

type errorWriter struct {
	err error
}

func (w errorWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func TestHandleLinuxSerialIODoesNotLogDivisorProgramming(t *testing.T) {
	result := LinuxRunResult{}
	state := linuxSerialState{}
	handleLinuxSerialIO(linuxIODirectionOut, 1, linuxSerialPortBase+3, 1, []byte{0x80}, &state, &result, nil)
	handleLinuxSerialIO(linuxIODirectionOut, 1, linuxSerialPortBase, 1, []byte{0x01}, &state, &result, nil)
	handleLinuxSerialIO(linuxIODirectionOut, 1, linuxSerialPortBase+1, 1, []byte{0x00}, &state, &result, nil)
	handleLinuxSerialIO(linuxIODirectionOut, 1, linuxSerialPortBase+3, 1, []byte{0x03}, &state, &result, nil)
	handleLinuxSerialIO(linuxIODirectionOut, 1, linuxSerialPortBase, 1, []byte{'x'}, &state, &result, nil)
	if string(result.Serial) != "x" {
		t.Fatalf("serial=%x", result.Serial)
	}
}

func TestHandleLinuxSerialIOCMOS(t *testing.T) {
	result := LinuxRunResult{}
	state := linuxSerialState{}

	// Status register A (any index other than 0x0b): UIP always clear.
	handleLinuxSerialIO(linuxIODirectionOut, 1, 0x70, 1, []byte{0x0a}, &state, &result, nil)
	statusA := []byte{0xff}
	handleLinuxSerialIO(0, 1, 0x71, 1, statusA, &state, &result, nil)
	if statusA[0] != 0x00 {
		t.Fatalf("status A=%#x, want 0x00 (UIP clear)", statusA[0])
	}

	// Status register B: 24-hour, binary mode.
	handleLinuxSerialIO(linuxIODirectionOut, 1, 0x70, 1, []byte{0x0b}, &state, &result, nil)
	statusB := []byte{0xff}
	handleLinuxSerialIO(0, 1, 0x71, 1, statusB, &state, &result, nil)
	if statusB[0] != 0x06 {
		t.Fatalf("status B=%#x, want 0x06", statusB[0])
	}

	// The top (NMI-disable) bit of the latched index is masked off.
	handleLinuxSerialIO(linuxIODirectionOut, 1, 0x70, 1, []byte{0x8b}, &state, &result, nil)
	nmiMasked := []byte{0xff}
	handleLinuxSerialIO(0, 1, 0x71, 1, nmiMasked, &state, &result, nil)
	if nmiMasked[0] != 0x06 {
		t.Fatalf("status B with NMI-disable bit set=%#x, want 0x06", nmiMasked[0])
	}
}

func TestHandleLinuxSerialIORaisesTXInterruptWhenArmed(t *testing.T) {
	result := LinuxRunResult{}
	state := linuxSerialState{}

	// Arm the THRE interrupt (IER bit 1).
	handleLinuxSerialIO(linuxIODirectionOut, 1, linuxSerialPortBase+1, 1, []byte{0x02}, &state, &result, nil)

	raised := 0
	handleLinuxSerialIO(linuxIODirectionOut, 1, linuxSerialPortBase, 1, []byte{'y'}, &state, &result, func() { raised++ })
	if raised != 1 {
		t.Fatalf("raiseIRQ called %d times, want 1", raised)
	}
	if !state.txInterruptPending {
		t.Fatal("txInterruptPending was not set after an armed THR write")
	}

	iir := []byte{0}
	handleLinuxSerialIO(0, 1, linuxSerialPortBase+2, 1, iir, &state, &result, nil)
	if iir[0] != 0x02 {
		t.Fatalf("IIR=%#x, want 0x02 (THRE interrupt pending)", iir[0])
	}
	if state.txInterruptPending {
		t.Fatal("txInterruptPending was not cleared by the IIR read")
	}

	// A second IIR read, with nothing new pending, reports no interrupt.
	iir2 := []byte{0}
	handleLinuxSerialIO(0, 1, linuxSerialPortBase+2, 1, iir2, &state, &result, nil)
	if iir2[0] != 0x01 {
		t.Fatalf("IIR=%#x, want 0x01 (no interrupt pending)", iir2[0])
	}
}

func TestHandleLinuxSerialIODoesNotRaiseTXInterruptWhenDisarmed(t *testing.T) {
	result := LinuxRunResult{}
	state := linuxSerialState{}

	raised := 0
	handleLinuxSerialIO(linuxIODirectionOut, 1, linuxSerialPortBase, 1, []byte{'z'}, &state, &result, func() { raised++ })
	if raised != 0 {
		t.Fatalf("raiseIRQ called %d times, want 0 (THRE interrupt not armed)", raised)
	}
	if state.txInterruptPending {
		t.Fatal("txInterruptPending was set despite the THRE interrupt not being armed")
	}
}

func TestHandleLinuxSerialIOIgnoresWiderAccesses(t *testing.T) {
	result := LinuxRunResult{}
	state := linuxSerialState{}
	data := []byte{0xaa, 0xbb}
	handleLinuxSerialIO(linuxIODirectionOut, 2, linuxSerialPortBase, 1, data, &state, &result, nil)
	if len(result.Serial) != 0 {
		t.Fatalf("serial=%q, want no capture for a non-byte-sized access", result.Serial)
	}
}

func TestHandleLinuxSerialIORegisterRoundTrip(t *testing.T) {
	result := LinuxRunResult{}
	state := linuxSerialState{}

	// DLAB set: registers 0 and 1 become the baud-rate divisor latches
	// instead of THR/IER, and are not captured as boot diagnostics.
	handleLinuxSerialIO(linuxIODirectionOut, 1, linuxSerialPortBase+3, 1, []byte{0x80}, &state, &result, nil)
	handleLinuxSerialIO(linuxIODirectionOut, 1, linuxSerialPortBase, 1, []byte{0x12}, &state, &result, nil)
	handleLinuxSerialIO(linuxIODirectionOut, 1, linuxSerialPortBase+1, 1, []byte{0x34}, &state, &result, nil)
	if len(result.Serial) != 0 {
		t.Fatalf("serial=%q, want no capture while DLAB is set", result.Serial)
	}
	divisorLow := []byte{0}
	handleLinuxSerialIO(0, 1, linuxSerialPortBase, 1, divisorLow, &state, &result, nil)
	divisorHigh := []byte{0}
	handleLinuxSerialIO(0, 1, linuxSerialPortBase+1, 1, divisorHigh, &state, &result, nil)
	if divisorLow[0] != 0x12 || divisorHigh[0] != 0x34 {
		t.Fatalf("divisor=%#x/%#x, want 0x12/0x34", divisorLow[0], divisorHigh[0])
	}

	// Clear DLAB and round-trip every remaining writable/readable register.
	handleLinuxSerialIO(linuxIODirectionOut, 1, linuxSerialPortBase+3, 1, []byte{0x03}, &state, &result, nil)
	handleLinuxSerialIO(linuxIODirectionOut, 1, linuxSerialPortBase+2, 1, []byte{0xc7}, &state, &result, nil)
	handleLinuxSerialIO(linuxIODirectionOut, 1, linuxSerialPortBase+4, 1, []byte{0x0b}, &state, &result, nil)
	handleLinuxSerialIO(linuxIODirectionOut, 1, linuxSerialPortBase+7, 1, []byte{0x42}, &state, &result, nil)

	lineControl := []byte{0}
	handleLinuxSerialIO(0, 1, linuxSerialPortBase+3, 1, lineControl, &state, &result, nil)
	modemControl := []byte{0}
	handleLinuxSerialIO(0, 1, linuxSerialPortBase+4, 1, modemControl, &state, &result, nil)
	modemStatus := []byte{0}
	handleLinuxSerialIO(0, 1, linuxSerialPortBase+6, 1, modemStatus, &state, &result, nil)
	scratch := []byte{0}
	handleLinuxSerialIO(0, 1, linuxSerialPortBase+7, 1, scratch, &state, &result, nil)

	if lineControl[0] != 0x03 {
		t.Fatalf("LCR=%#x, want 0x03", lineControl[0])
	}
	if modemControl[0] != 0x0b {
		t.Fatalf("MCR=%#x, want 0x0b", modemControl[0])
	}
	if modemStatus[0] != 0xb0 {
		t.Fatalf("MSR=%#x, want 0xb0", modemStatus[0])
	}
	if scratch[0] != 0x42 {
		t.Fatalf("scratch=%#x, want 0x42", scratch[0])
	}
}

func TestHandleLinuxSerialIOReportsNoPendingInterrupt(t *testing.T) {
	result := LinuxRunResult{}
	state := linuxSerialState{}
	interruptIdentification := []byte{0}

	handleLinuxSerialIO(
		0,
		1,
		linuxSerialPortBase+2,
		1,
		interruptIdentification,
		&state,
		&result,
		nil,
	)

	if interruptIdentification[0] != 0x01 {
		t.Fatalf(
			"IIR=%#x, want %#x (no interrupt pending)",
			interruptIdentification[0],
			0x01,
		)
	}
}
