package hypervisor

import (
	"context"
	"testing"
)

func TestProbeNativeIsActionable(t *testing.T) {
	result, err := ProbeNative(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Architecture == "" {
		t.Fatalf("probe does not report architecture: %+v", result)
	}
	if result.Available && len(result.Features) == 0 {
		t.Fatalf("available backend does not report features: %+v", result)
	}
	if !result.Available && result.Details["unavailable"] == "" {
		t.Fatalf("unavailable backend has no reason: %+v", result)
	}
}
