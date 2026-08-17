package kubevirt

import (
	"encoding/json"
	"strings"
	"testing"

	microvm "github.com/CYPT71/platform-factory/sdk/microvm"
)

func TestValidate(t *testing.T) {
	spec := microvm.Spec{
		Name: "demo", Namespace: "default", MemoryMiB: 256, VCPUs: 2, Port: 8080,
		Image: "registry.example/boot@sha256:" + strings.Repeat("a", 64), Arch: "amd64", Listen: "127.0.0.1",
	}
	if err := Validate(spec); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejectsUnsafeValues(t *testing.T) {
	base := microvm.Spec{Name: "demo", Namespace: "default", Listen: "127.0.0.1", MemoryMiB: 128, VCPUs: 1, Port: 8080}
	for _, spec := range []microvm.Spec{
		base,
		{Name: "demo", Namespace: "Bad", Image: "registry/image@sha256:" + strings.Repeat("a", 64), Arch: "amd64", Listen: "127.0.0.1", MemoryMiB: 128, VCPUs: 1, Port: 8080},
		{Name: "demo", Namespace: "default", Image: "registry/image:latest", Arch: "amd64", Listen: "127.0.0.1", MemoryMiB: 128, VCPUs: 1, Port: 8080},
		{Name: "demo", Namespace: "default", Image: "registry/image@sha256:" + strings.Repeat("g", 64), Arch: "amd64", Listen: "127.0.0.1", MemoryMiB: 128, VCPUs: 1, Port: 8080},
		{Name: "demo", Namespace: "default", Image: "registry/image@sha256:" + strings.Repeat("a", 64), Arch: "riscv64", Listen: "127.0.0.1", MemoryMiB: 128, VCPUs: 1, Port: 8080},
	} {
		if err := Validate(spec); err == nil {
			t.Fatalf("accepted %#v", spec)
		}
	}
}

func TestVirtualMachine(t *testing.T) {
	spec := microvm.Spec{
		Name: "demo", Namespace: "production", MemoryMiB: 256, VCPUs: 2, Port: 8080,
		Image: "registry.example/boot@sha256:" + strings.Repeat("b", 64), Arch: "amd64", Listen: "127.0.0.1",
	}
	output, err := VirtualMachine(spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"kind": "VirtualMachine"`, `"runStrategy": "Halted"`, spec.Image,
		`"app.kubernetes.io/managed-by": "platform-factory"`,
		`"kernelPath": "/boot/kernel"`, `"initrdPath": "/boot/initramfs.cpio.gz"`,
		`"kernelArgs": "console=ttyS0 rdinit=/sbin/init ip=dhcp panic=-1"`,
	} {
		if !strings.Contains(string(output), want) {
			t.Fatalf("manifest missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(string(output), `"containerDisk"`) {
		t.Fatalf("external kernel boot manifest unexpectedly attaches a containerDisk:\n%s", output)
	}
}

func TestARM64AndNetworkPorts(t *testing.T) {
	spec := microvm.Spec{
		Name: "demo", Namespace: "production", MemoryMiB: 256, VCPUs: 2, Port: 8080,
		Image: "registry.example/boot@sha256:" + strings.Repeat("c", 64),
		Arch:  "arm64", Listen: "127.0.0.1",
		Forwards: []microvm.Forward{{HostPort: 8443, GuestPort: 443, Protocol: "tcp"}},
	}
	output, err := VirtualMachine(spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"architecture": "arm64"`, `"console=ttyAMA0`, `"port": 443`, `"protocol": "TCP"`} {
		if !strings.Contains(string(output), want) {
			t.Fatalf("manifest missing %q:\n%s", want, output)
		}
	}
}

func TestRBACIsNamespaceBoundedWithNoWildcards(t *testing.T) {
	spec := microvm.Spec{Name: "demo", Namespace: "production"}
	output, err := RBAC(spec)
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Kind  string `json:"kind"`
		Items []struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Rules []struct {
				APIGroups []string `json:"apiGroups"`
				Resources []string `json:"resources"`
				Verbs     []string `json:"verbs"`
			} `json:"rules"`
			RoleRef struct {
				Kind string `json:"kind"`
			} `json:"roleRef"`
		} `json:"items"`
	}
	if err := json.Unmarshal(output, &list); err != nil {
		t.Fatalf("RBAC output is not valid JSON: %v\n%s", err, output)
	}
	if list.Kind != "List" || len(list.Items) != 3 {
		t.Fatalf("expected a List of 3 items (ServiceAccount, Role, RoleBinding), got %+v", list)
	}
	var sawServiceAccount, sawRole, sawRoleBinding bool
	for _, item := range list.Items {
		if item.Metadata.Namespace != spec.Namespace {
			t.Fatalf("item %+v is not scoped to namespace %q", item, spec.Namespace)
		}
		switch item.Kind {
		case "ServiceAccount":
			sawServiceAccount = true
		case "ClusterRole", "ClusterRoleBinding":
			t.Fatalf("RBAC() produced a cluster-scoped kind %q; only namespaced Role/RoleBinding are allowed", item.Kind)
		case "Role":
			sawRole = true
			if len(item.Rules) == 0 {
				t.Fatal("Role has no rules")
			}
			for _, rule := range item.Rules {
				for _, group := range rule.APIGroups {
					if group == "*" {
						t.Fatalf("Role rule declares apiGroups: [\"*\"], a cluster-admin-equivalent grant: %+v", rule)
					}
				}
				for _, resource := range rule.Resources {
					if resource == "*" {
						t.Fatalf("Role rule declares resources: [\"*\"]: %+v", rule)
					}
				}
				for _, verb := range rule.Verbs {
					if verb == "*" {
						t.Fatalf("Role rule declares verbs: [\"*\"]: %+v", rule)
					}
				}
			}
		case "RoleBinding":
			sawRoleBinding = true
			if item.RoleRef.Kind != "Role" {
				t.Fatalf("RoleBinding references a %q, not a namespaced Role", item.RoleRef.Kind)
			}
		}
	}
	if !sawServiceAccount || !sawRole || !sawRoleBinding {
		t.Fatalf("RBAC() did not produce all three of ServiceAccount, Role, RoleBinding: %+v", list)
	}
}

func TestRBACRejectsInvalidTarget(t *testing.T) {
	if _, err := RBAC(microvm.Spec{Name: "demo", Namespace: "Not Valid"}); err == nil {
		t.Fatal("accepted an invalid namespace")
	}
}
