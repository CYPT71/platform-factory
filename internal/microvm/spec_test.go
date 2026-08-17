package microvm

import (
	"reflect"
	"testing"
)

func TestValidateNative(t *testing.T) {
	native := Spec{Name: "demo", Layout: "/layout", Listen: "127.0.0.1", MemoryMiB: 128, VCPUs: 1, Port: 8080}
	if err := Validate(native, "native"); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejectsUnsafeValues(t *testing.T) {
	base := Spec{Name: "demo", Namespace: "default", Layout: "/layout", Listen: "127.0.0.1", MemoryMiB: 128, VCPUs: 1, Port: 8080}
	for _, test := range []struct {
		backend string
		spec    Spec
	}{
		{"other", base},
		{"kubevirt", base},
		{"native", withName(base, "../escape")},
		{"native", withMemory(base, 1)},
		{"native", withVCPUs(base, 0)},
		{"native", withPort(base, 0)},
		{"native", Spec{Name: "demo", Layout: "", Listen: "127.0.0.1", MemoryMiB: 128, VCPUs: 1, Port: 8080}},
		{"native", Spec{Name: "demo", Layout: "/layout", Listen: "::1", MemoryMiB: 128, VCPUs: 1, Port: 8080}},
	} {
		if err := Validate(test.spec, test.backend); err == nil {
			t.Fatalf("%s accepted %#v", test.backend, test.spec)
		}
	}
}

func TestResourceBoundsAreConsistent(t *testing.T) {
	base := Spec{Name: "demo", Layout: "/layout", Listen: "127.0.0.1", MemoryMiB: MinMemoryMiB, VCPUs: 1, Port: 8080}
	if err := Validate(base, "native"); err != nil {
		t.Fatal(err)
	}
	for _, memory := range []int{MinMemoryMiB - 1, MaxMemoryMiB + 1} {
		candidate := base
		candidate.MemoryMiB = memory
		if err := Validate(candidate, "native"); err == nil {
			t.Fatalf("memory %d accepted", memory)
		}
	}
	candidate := base
	candidate.VCPUs = MaxVCPUs + 1
	if err := Validate(candidate, "native"); err == nil {
		t.Fatalf("vcpus %d accepted", candidate.VCPUs)
	}
}

func TestNativeTargetAndEnvironment(t *testing.T) {
	spec := Spec{
		Name: "demo", Listen: "127.0.0.1", MemoryMiB: 128, VCPUs: 2, Port: 8080,
		Forwards: []Forward{{HostPort: 8443, GuestPort: 443, Protocol: "tcp"}},
	}
	if err := ValidateNativeTarget(spec); err != nil {
		t.Fatal(err)
	}
	if err := ValidateNativeTarget(Spec{Name: "../bad"}); err == nil {
		t.Fatal("invalid target accepted")
	}
	want := []string{
		"MICROVM_MEMORY=128M", "MICROVM_SMP=2", "MICROVM_HOST_ADDRESS=127.0.0.1",
		"MICROVM_FORWARDS=tcp|127.0.0.1|8443|443",
	}
	if got := NativeEnvironment(spec); !reflect.DeepEqual(got, want) {
		t.Fatalf("environment=%v want=%v", got, want)
	}
}

func withName(s Spec, value string) Spec { s.Name = value; return s }
func withMemory(s Spec, value int) Spec  { s.MemoryMiB = value; return s }
func withVCPUs(s Spec, value int) Spec   { s.VCPUs = value; return s }
func withPort(s Spec, value int) Spec    { s.Port = value; return s }
