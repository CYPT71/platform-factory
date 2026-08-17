//go:build linux

package ociruntime

import "testing"

// TestNormalizeLegacyAnnotationsCopiesForwardWithoutOverwriting proves
// the platform-factory rebrand's compatibility bridge: a bundle still
// carrying pre-rebrand secure-oci.dev/* annotation keys decodes exactly
// as if it had been written with the current platform-factory.dev/*
// ones, but a bundle that already has the current key wins over a
// conflicting legacy one rather than being silently overwritten.
func TestNormalizeLegacyAnnotationsCopiesForwardWithoutOverwriting(t *testing.T) {
	annotations := map[string]string{
		"secure-oci.dev/kernel-path":      "/legacy/kernel",
		"secure-oci.dev/vcpus":            "1",
		"platform-factory.dev/vcpus":      "4", // already current: must win
		"unrelated.example.com/something": "kept as-is",
	}
	normalizeLegacyAnnotations(annotations)

	if got := annotations["platform-factory.dev/kernel-path"]; got != "/legacy/kernel" {
		t.Fatalf("kernel-path not copied forward: %q", got)
	}
	if got := annotations["platform-factory.dev/vcpus"]; got != "4" {
		t.Fatalf("existing current-key value was overwritten by the legacy one: %q", got)
	}
	if got := annotations["unrelated.example.com/something"]; got != "kept as-is" {
		t.Fatalf("unrelated annotation was disturbed: %q", got)
	}
	// The legacy keys themselves are left in place, not deleted - callers
	// only ever look up the current key, so this is inert either way, and
	// leaving it is simpler and less surprising than mutating input the
	// caller didn't ask to have keys removed from.
	if _, ok := annotations["secure-oci.dev/kernel-path"]; !ok {
		t.Fatal("legacy key was unexpectedly removed")
	}
}

func TestNormalizeLegacyAnnotationsHandlesNilMap(t *testing.T) {
	normalizeLegacyAnnotations(nil) // must not panic
}
