package v1

import (
	"testing"
)

func TestDiscoveryStatusConstants(t *testing.T) {
	// Verify that the constants have the expected string values
	if DiscoveryStatusComplete != "complete" {
		t.Errorf("DiscoveryStatusComplete = %q, want %q", DiscoveryStatusComplete, "complete")
	}

	if DiscoveryStatusPartial != "partial" {
		t.Errorf("DiscoveryStatusPartial = %q, want %q", DiscoveryStatusPartial, "partial")
	}

	if DiscoveryStatusFailed != "failed" {
		t.Errorf("DiscoveryStatusFailed = %q, want %q", DiscoveryStatusFailed, "failed")
	}
}

func TestDiscoveryStatusString(t *testing.T) {
	// Verify that DiscoveryStatus implements string interface correctly
	status := DiscoveryStatusComplete
	if status != "complete" {
		t.Errorf("DiscoveryStatus string value = %q, want %q", status, "complete")
	}

	status = DiscoveryStatusPartial
	if status != "partial" {
		t.Errorf("DiscoveryStatus string value = %q, want %q", status, "partial")
	}

	status = DiscoveryStatusFailed
	if status != "failed" {
		t.Errorf("DiscoveryStatus string value = %q, want %q", status, "failed")
	}
}
