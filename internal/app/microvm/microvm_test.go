package microvm

import "testing"

func TestCapability(t *testing.T) {
	cases := []struct {
		action          string
		capability      string
		mutating        bool
		wantErrRejected bool
	}{
		{"create", "runtime.create", true, false},
		{"start", "runtime.start", true, false},
		{"stop", "runtime.stop", true, false},
		{"restart", "runtime.restart", true, false},
		{"delete", "runtime.delete", true, false},
		{"status", "runtime.status", false, false},
		{"logs", "runtime.logs", false, false},
		{"rbac", "runtime.rbac", true, false},
		{"bogus", "", false, true},
	}
	for _, c := range cases {
		capability, mutating, err := Capability(c.action)
		if c.wantErrRejected {
			if err == nil {
				t.Errorf("Capability(%q): expected an error", c.action)
			}
			continue
		}
		if err != nil {
			t.Errorf("Capability(%q): unexpected error %v", c.action, err)
			continue
		}
		if capability != c.capability || mutating != c.mutating {
			t.Errorf("Capability(%q) = (%q, %v), want (%q, %v)", c.action, capability, mutating, c.capability, c.mutating)
		}
	}
}
