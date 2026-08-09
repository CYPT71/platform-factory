//go:build linux && amd64

package kvm

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"
	"unsafe"
)

const (
	testCPUIDHeaderSize = 8
	testCPUIDEntrySize  = 40
)

// testCPUIDBuffer builds a KVM_GET_SUPPORTED_CPUID-shaped buffer (a uint32
// entry count followed by kvm_cpuid_entry2 structs) listing the given
// leaf/function numbers, in order, each with every other field zeroed.
func testCPUIDBuffer(capacity uint32, functions ...uint32) []byte {
	cpuid := make([]byte, testCPUIDHeaderSize+int(capacity)*testCPUIDEntrySize)
	*(*uint32)(unsafe.Pointer(&cpuid[0])) = uint32(len(functions))
	for i, function := range functions {
		base := testCPUIDHeaderSize + i*testCPUIDEntrySize
		*(*uint32)(unsafe.Pointer(&cpuid[base])) = function
	}
	return cpuid
}

func testCPUIDEntry(cpuid []byte, index int) (function, eax, ebx, ecx, edx uint32) {
	base := testCPUIDHeaderSize + index*testCPUIDEntrySize
	return *(*uint32)(unsafe.Pointer(&cpuid[base])),
		*(*uint32)(unsafe.Pointer(&cpuid[base+12])),
		*(*uint32)(unsafe.Pointer(&cpuid[base+16])),
		*(*uint32)(unsafe.Pointer(&cpuid[base+20])),
		*(*uint32)(unsafe.Pointer(&cpuid[base+24]))
}

func TestInjectTSCFrequencyLeafEditsExistingEntry(t *testing.T) {
	cpuid := testCPUIDBuffer(8, 0, 1, 0x15, 0x16)
	entries := injectTSCFrequencyLeaf(cpuid, 4, 2500000)
	if entries != 4 {
		t.Fatalf("entries=%d, want unchanged 4", entries)
	}
	function, eax, ebx, ecx, edx := testCPUIDEntry(cpuid, 2)
	if function != 0x15 || eax != 1 || ebx != 1 || ecx != 2500000000 || edx != 0 {
		t.Fatalf("leaf 0x15 entry = {function=%#x eax=%d ebx=%d ecx=%d edx=%d}", function, eax, ebx, ecx, edx)
	}
	// The other entries must be untouched.
	if function, _, _, _, _ := testCPUIDEntry(cpuid, 3); function != 0x16 {
		t.Fatalf("leaf 0x16 entry disturbed: function=%#x", function)
	}
}

func TestInjectTSCFrequencyLeafAppendsMissingEntry(t *testing.T) {
	// The host's KVM_GET_SUPPORTED_CPUID response never mentions leaf
	// 0x15 at all - the scenario this function exists to handle.
	cpuid := testCPUIDBuffer(8, 0, 1, 0x16)
	entries := injectTSCFrequencyLeaf(cpuid, 3, 3000000)
	if entries != 4 {
		t.Fatalf("entries=%d, want 4 after append", entries)
	}
	if header := *(*uint32)(unsafe.Pointer(&cpuid[0])); header != 4 {
		t.Fatalf("header entry count=%d, want 4", header)
	}
	function, eax, ebx, ecx, _ := testCPUIDEntry(cpuid, 3)
	if function != 0x15 || eax != 1 || ebx != 1 || ecx != 3000000000 {
		t.Fatalf("appended leaf 0x15 entry = {function=%#x eax=%d ebx=%d ecx=%d}", function, eax, ebx, ecx)
	}
	// The pre-existing entries must be untouched.
	if function, _, _, _, _ := testCPUIDEntry(cpuid, 1); function != 1 {
		t.Fatalf("leaf 1 entry disturbed: function=%#x", function)
	}
	if function, _, _, _, _ := testCPUIDEntry(cpuid, 2); function != 0x16 {
		t.Fatalf("leaf 0x16 entry disturbed: function=%#x", function)
	}
}

func TestLoadLinuxBootWithEntropyReaderFailsClosed(t *testing.T) {
	memory := make([]byte, 8<<20)
	kernel := testBZImage(4, []byte{0xf4})
	if _, err := loadLinuxBootWithEntropyReader(memory, kernel, nil, "", bytes.NewReader(make([]byte, 63))); err == nil ||
		!strings.Contains(err.Error(), "guest entropy seed") {
		t.Fatalf("short entropy source err=%v", err)
	}
	seed := bytes.Repeat([]byte{0x42}, 64)
	layout, err := loadLinuxBootWithEntropyReader(memory, kernel, nil, "", bytes.NewReader(seed))
	if err != nil {
		t.Fatal(err)
	}
	record := memory[layout.SetupDataAddress : layout.SetupDataAddress+layout.SetupDataSize]
	if !bytes.Equal(record[linuxSetupDataHeaderSize:], seed) {
		t.Fatal("entropy reader data did not reach setup_data")
	}
}

func TestRunLinuxRejectsInvalidInputsBeforeKVM(t *testing.T) {
	if _, err := RunLinux(context.Background(), 0, nil, nil, "", 1); err == nil ||
		!strings.Contains(err.Error(), "memory size") {
		t.Fatalf("memory validation err=%v", err)
	}
	if _, err := RunLinux(context.Background(), 16<<20, nil, nil, "", 1); err == nil ||
		!strings.Contains(err.Error(), "bzImage") {
		t.Fatalf("kernel validation err=%v", err)
	}
	if _, err := RunLinux(context.Background(), 16<<20, testBZImage(4, []byte{0xf4}), nil, "", 0); err == nil ||
		!strings.Contains(err.Error(), "max exits") {
		t.Fatalf("exit budget validation err=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := RunLinux(ctx, 16<<20, nil, nil, "", 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel err=%v", err)
	}
}

func TestLinuxRunStartNotifierCallsCallbackExactlyOnce(t *testing.T) {
	calls := 0
	notifier := linuxRunStartNotifier{callback: func() error {
		calls++
		return nil
	}}
	if err := notifier.notify(); err != nil {
		t.Fatal(err)
	}
	if err := notifier.notify(); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("callback calls=%d, want 1", calls)
	}
}

func TestLinuxRunStartNotifierPropagatesCallbackErrorOnce(t *testing.T) {
	sentinel := errors.New("ack failed")
	calls := 0
	notifier := linuxRunStartNotifier{callback: func() error {
		calls++
		return sentinel
	}}
	if err := notifier.notify(); !errors.Is(err, sentinel) {
		t.Fatalf("first notification err=%v, want %v", err, sentinel)
	}
	if err := notifier.notify(); err != nil {
		t.Fatalf("second notification err=%v, want nil", err)
	}
	if calls != 1 {
		t.Fatalf("callback calls=%d, want 1", calls)
	}
}

func TestLinuxRunStartNotifierAllowsNilCallback(t *testing.T) {
	notifier := linuxRunStartNotifier{}
	if err := notifier.notify(); err != nil {
		t.Fatal(err)
	}
	if err := notifier.notify(); err != nil {
		t.Fatal(err)
	}
	if !notifier.notified {
		t.Fatal("notifier did not record notification")
	}
}

func TestRunLinuxWithOptionsDoesNotSignalBeforeKVM(t *testing.T) {
	calls := 0
	if _, err := RunLinuxWithOptions(
		context.Background(),
		0,
		nil,
		nil,
		"",
		1,
		LinuxRunOptions{OnStarted: func() error {
			calls++
			return nil
		}},
	); err == nil {
		t.Fatal("RunLinuxWithOptions accepted invalid memory")
	}
	if calls != 0 {
		t.Fatalf("callback calls=%d before KVM_RUN, want 0", calls)
	}
}

func TestRunLinuxWithOptionsClosesGuestChannelOnSetupFailure(t *testing.T) {
	host, guest := net.Pipe()
	if _, err := RunLinuxWithOptions(
		context.Background(), 0, nil, nil, "", 1,
		LinuxRunOptions{GuestChannel: host},
	); err == nil {
		t.Fatal("RunLinuxWithOptions accepted invalid memory")
	}
	if _, err := guest.Write([]byte{1}); err == nil {
		t.Fatal("guest channel remained open after setup failure")
	}
	guest.Close()
}

// This is an opt-in real KVM smoke test for the dedicated Linux boot runner.
// CI can provide a project-built bzImage through PLATFORM_FACTORY_TEST_BZIMAGE; the
// ordinary unit suite remains hermetic and skips without that explicit input.
func TestRunLinuxWithRealKVM(t *testing.T) {
	path := os.Getenv("PLATFORM_FACTORY_TEST_BZIMAGE")
	if path == "" {
		t.Skip("PLATFORM_FACTORY_TEST_BZIMAGE is not set")
	}
	requireKVMAccess(t)
	kernel, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var initrd []byte
	if initrdPath := os.Getenv("PLATFORM_FACTORY_TEST_INITRD"); initrdPath != "" {
		initrd, err = os.ReadFile(initrdPath)
		if err != nil {
			t.Fatal(err)
		}
	}
	// Nested KVM runners can take substantially longer than local KVM to
	// decompress a hardened kernel, initialize PID 1, and start the OCI
	// entrypoint. Keep the test bounded, but leave enough headroom for slow
	// or contended CI hosts.
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	result, err := RunLinux(ctx, 128<<20, kernel, initrd,
		"console=ttyS0,115200 earlycon=uart,io,0x3f8,115200 ignore_loglevel panic=0 rdinit=/sbin/init pci=off", 1<<20)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("run Linux: %v; serial=%q", err, result.Serial)
	}
	if len(result.Serial) == 0 {
		t.Fatalf("kernel produced no serial boot diagnostics (exit_reason=%d exits=%d halted=%t shutdown=%t)",
			result.ExitReason, result.Exits, result.Halted, result.Shutdown)
	}
	if len(initrd) != 0 && !strings.Contains(string(result.Serial), `"component":"example-service"`) {
		t.Fatalf("OCI application did not start in the native guest; serial=%q", result.Serial)
	}
}
