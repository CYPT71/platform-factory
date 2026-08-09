package microvm

import "testing"

func TestParseForward(t *testing.T) {
	for _, value := range []string{"8080", "127.0.0.1:8080:80/tcp", "[::1]:5353:53/udp"} {
		if _, err := ParseForward(value); err != nil {
			t.Fatalf("%s: %v", value, err)
		}
	}
	for _, value := range []string{"", "0", "host:80:80", "80:70000", "80/sctp", "[::1"} {
		if _, err := ParseForward(value); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
}

func TestValidateCommon(t *testing.T) {
	base := Spec{Name: "demo", Listen: "127.0.0.1", MemoryMiB: 128, VCPUs: 1, Port: 8080}
	if err := base.ValidateCommon(); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		spec Spec
	}{
		{"bad name", withName(base, "../escape")},
		{"low memory", withMemory(base, 1)},
		{"high memory", withMemory(base, 1<<21)},
		{"no vcpus", withVCPUs(base, 0)},
		{"too many vcpus", withVCPUs(base, 257)},
		{"bad port", withPort(base, 0)},
		{"unroutable listen", Spec{Name: "demo", Listen: "::1", MemoryMiB: 128, VCPUs: 1, Port: 8080}},
	} {
		if err := test.spec.ValidateCommon(); err == nil {
			t.Fatalf("%s: accepted %#v", test.name, test.spec)
		}
	}
}

func withName(s Spec, value string) Spec { s.Name = value; return s }
func withMemory(s Spec, value int) Spec  { s.MemoryMiB = value; return s }
func withVCPUs(s Spec, value int) Spec   { s.VCPUs = value; return s }
func withPort(s Spec, value int) Spec    { s.Port = value; return s }
