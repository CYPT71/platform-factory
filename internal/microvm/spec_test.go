package microvm

import (
	"reflect"
	"strings"
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

func TestValidateNativeTargetNameEdgeCases(t *testing.T) {
	for _, test := range []struct {
		name string
		ok   bool
	}{
		{"a", true},
		{"demo-1", true},
		{strings.Repeat("a", 63), true},
		{strings.Repeat("a", 64), false},
		{"", false},
		{"UPPER", false},
		{"-leading-dash", false},
		{"trailing-dash-", false},
		{"../escape", false},
		{"has space", false},
	} {
		err := ValidateNativeTarget(Spec{Name: test.name})
		if test.ok && err != nil {
			t.Fatalf("name %q: unexpected error: %v", test.name, err)
		}
		if !test.ok && err == nil {
			t.Fatalf("name %q: expected error, got nil", test.name)
		}
	}
}

func TestNativeEnvironmentMultipleForwardsAndHostIPOverride(t *testing.T) {
	spec := Spec{
		Name: "demo", Listen: "0.0.0.0", MemoryMiB: 256, VCPUs: 4, Port: 9090,
		Forwards: []Forward{
			{HostPort: 80, GuestPort: 8080, Protocol: "tcp"},
			{HostIP: "10.0.0.5", HostPort: 53, GuestPort: 53, Protocol: "udp"},
		},
	}
	want := []string{
		"MICROVM_MEMORY=256M", "MICROVM_SMP=4", "MICROVM_HOST_ADDRESS=0.0.0.0",
		"MICROVM_FORWARDS=tcp|0.0.0.0|80|8080;udp|10.0.0.5|53|53",
	}
	if got := NativeEnvironment(spec); !reflect.DeepEqual(got, want) {
		t.Fatalf("environment=%v want=%v", got, want)
	}
}

func TestNativeEnvironmentNoForwards(t *testing.T) {
	spec := Spec{Name: "demo", Listen: "127.0.0.1", MemoryMiB: 64, VCPUs: 1}
	want := []string{
		"MICROVM_MEMORY=64M", "MICROVM_SMP=1", "MICROVM_HOST_ADDRESS=127.0.0.1",
		"MICROVM_FORWARDS=",
	}
	if got := NativeEnvironment(spec); !reflect.DeepEqual(got, want) {
		t.Fatalf("environment=%v want=%v", got, want)
	}
}

func withName(s Spec, value string) Spec { s.Name = value; return s }
func withMemory(s Spec, value int) Spec  { s.MemoryMiB = value; return s }
func withVCPUs(s Spec, value int) Spec   { s.VCPUs = value; return s }
func withPort(s Spec, value int) Spec    { s.Port = value; return s }
