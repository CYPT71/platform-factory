//go:build linux

package kvm

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestProbeNativeHonorsCancellationBeforeOpeningKVM(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := ProbeNative(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ProbeNative error=%v", err)
	}
	if result.Architecture == "" || result.Details["backend"] != "linux-kvm-native" {
		t.Fatalf("partial capabilities=%+v", result)
	}
}

func TestKVMExtensionCheckerReportsIOCTLError(t *testing.T) {
	file, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := kvmExtensionChecker(file)(kvmCapUserMemory); err == nil {
		t.Fatal("KVM_CHECK_EXTENSION unexpectedly succeeded on /dev/null")
	}
}

func TestNegotiateRequiredKVMExtensions(t *testing.T) {
	features := map[string]bool{}
	details := map[string]string{}
	err := negotiateRequiredKVMExtensions(func(extension uintptr) (uintptr, error) {
		return extension + 1, nil
	}, features, details)
	if err != nil {
		t.Fatal(err)
	}
	for _, extension := range requiredKVMExtensions {
		key := "kvm." + extension.name
		if !features[key] || details[key] == "" {
			t.Fatalf("extension %q was not exposed: features=%v details=%v", key, features, details)
		}
	}
}

func TestNegotiateRequiredKVMExtensionsRejectsMissingCapability(t *testing.T) {
	err := negotiateRequiredKVMExtensions(func(uintptr) (uintptr, error) {
		return 0, nil
	}, map[string]bool{}, map[string]string{})
	if err == nil || !strings.Contains(err.Error(), "is unavailable") {
		t.Fatalf("err=%v", err)
	}
}

func TestNegotiateRequiredKVMExtensionsReportsIOCTLFailure(t *testing.T) {
	sentinel := errors.New("ioctl failed")
	err := negotiateRequiredKVMExtensions(func(uintptr) (uintptr, error) {
		return 0, sentinel
	}, map[string]bool{}, map[string]string{})
	if !errors.Is(err, sentinel) || !strings.Contains(err.Error(), "KVM_CHECK_EXTENSION") {
		t.Fatalf("err=%v", err)
	}
}
