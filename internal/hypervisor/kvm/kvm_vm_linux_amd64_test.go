//go:build linux && amd64

package kvm

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// helloWorldPayload is the canonical minimal 16-bit real-mode KVM guest:
//
//	mov al, 'A'    ; B0 41
//	mov dx, 0x3f8  ; BA F8 03
//	out dx, al     ; EE
//	hlt            ; F4
var helloWorldPayload = []byte{0xB0, 0x41, 0xBA, 0xF8, 0x03, 0xEE, 0xF4}

// TestRunFlatPayloadBootsAndHalts is real, not mocked: it opens /dev/kvm
// and drives the actual KVM ioctl sequence (KVM_CREATE_VM, KVM_CREATE_VCPU,
// KVM_SET_USER_MEMORY_REGION, KVM_SET_SREGS, KVM_SET_REGS, KVM_RUN). It
// skips cleanly - the correct, honest outcome, not a failure - on any host
// without /dev/kvm access (this repository's own sandboxed test
// environment included).
func TestRunFlatPayloadBootsAndHalts(t *testing.T) {
	requireKVMAccess(t)
	result, err := RunFlatPayload(context.Background(), 1<<20, helloWorldPayload, 0x1000, 1000)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !result.HaltedCleanly {
		t.Fatalf("guest did not halt cleanly: %+v", result)
	}
	if len(result.PortWrites) != 1 || result.PortWrites[0].Port != 0x3f8 || result.PortWrites[0].Byte != 'A' {
		t.Fatalf("port writes=%+v, want a single write of 'A' to port 0x3f8", result.PortWrites)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := RunFlatPayload(ctx, 1<<20, []byte{0xEB, 0xFE}, 0x1000, 1000); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("looping guest cancellation err=%v", err)
	}
}

func requireKVMAccess(t *testing.T) {
	t.Helper()
	device, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("/dev/kvm unavailable to this process: %v", err)
	}
	if err := device.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRunFlatPayloadRejectsOversizedPayload(t *testing.T) {
	for name, tc := range map[string]struct {
		memoryBytes uint64
		payload     []byte
		loadAddr    uint64
		maxSteps    int
	}{
		"zero memory":      {memoryBytes: 0, maxSteps: 1},
		"oversized memory": {memoryBytes: ^uint64(0), maxSteps: 1},
		"unaligned memory": {memoryBytes: 4097, maxSteps: 1},
		"oversized payload": {
			memoryBytes: 4096, payload: make([]byte, 8192), maxSteps: 10,
		},
		"offset overflow": {memoryBytes: 4096, payload: []byte{1}, loadAddr: 4097, maxSteps: 10},
		"real mode overflow": {
			memoryBytes: 1 << 20, payload: []byte{1}, loadAddr: 0x10000, maxSteps: 10,
		},
		"zero steps": {memoryBytes: 4096, maxSteps: 0},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := RunFlatPayload(context.Background(), tc.memoryBytes, tc.payload, tc.loadAddr, tc.maxSteps); err == nil {
				t.Fatal("invalid KVM execution limits accepted")
			}
		})
	}
}

func TestRunFlatPayloadHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := RunFlatPayload(ctx, 1<<20, helloWorldPayload, 0x1000, 1000); err == nil {
		t.Fatal("expected cancellation to be honored before touching /dev/kvm")
	}
}
