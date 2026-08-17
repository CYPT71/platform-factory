// Package kubevirt is the KubeVirt backend plugin for cmd/platform-factory's
// microvm command: it turns a microvm.Spec into a KubeVirt VirtualMachine
// manifest and validates the KubeVirt-specific parts of that Spec. The
// runtime-independent contract itself (Spec, its common validation) lives
// in the public github.com/CYPT71/platform-factory/sdk/microvm package -
// this plugin, like every other out-of-module runtime-engine integration,
// never imports an internal/ package from the main module.
package kubevirt

import (
	"encoding/json"
	"fmt"
	"strings"

	microvm "github.com/CYPT71/platform-factory/sdk/microvm"
)

// Validate validates a Spec for a new KubeVirt VirtualMachine, in addition
// to the common validation every backend applies via Spec.ValidateCommon.
func Validate(s microvm.Spec) error {
	if err := s.ValidateCommon(); err != nil {
		return err
	}
	if err := ValidateTarget(s); err != nil {
		return err
	}
	if err := validateDigestReference(s.Image); err != nil {
		return err
	}
	if s.Arch != "amd64" && s.Arch != "arm64" {
		return fmt.Errorf("architecture must be amd64 or arm64")
	}
	return nil
}

// ValidateTarget validates lifecycle operations that address an existing VM
// and therefore do not need its original boot image or sizing.
func ValidateTarget(s microvm.Spec) error {
	if !microvm.NamePattern.MatchString(s.Name) {
		return fmt.Errorf("name must be a DNS label of at most 63 characters")
	}
	if !microvm.NamePattern.MatchString(s.Namespace) {
		return fmt.Errorf("namespace must be a DNS label of at most 63 characters")
	}
	return nil
}

func validateDigestReference(image string) error {
	const marker = "@sha256:"
	parts := strings.Split(image, marker)
	if len(parts) != 2 || parts[0] == "" || len(parts[1]) != 64 {
		return fmt.Errorf("kubevirt boot image must be pinned by sha256 digest")
	}
	for _, r := range parts[1] {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return fmt.Errorf("kubevirt boot image has an invalid sha256 digest")
		}
	}
	return nil
}

// VirtualMachine produces a stable VirtualMachine resource. Image is an
// external-kernel-boot image containing /boot/kernel and
// /boot/initramfs.cpio.gz, never an arbitrary application image.
func VirtualMachine(s microvm.Spec) ([]byte, error) {
	if err := Validate(s); err != nil {
		return nil, err
	}
	console := "ttyS0"
	if s.Arch == "arm64" {
		console = "ttyAMA0"
	}
	ports := make([]any, 0, len(s.Forwards))
	for index, forward := range s.Forwards {
		ports = append(ports, map[string]any{
			"name": fmt.Sprintf("port-%d", index+1), "port": forward.GuestPort,
			"protocol": strings.ToUpper(forward.Protocol),
		})
	}
	vm := map[string]any{
		"apiVersion": "kubevirt.io/v1",
		"kind":       "VirtualMachine",
		"metadata": map[string]any{
			"name":      s.Name,
			"namespace": s.Namespace,
			"labels": map[string]string{
				"app.kubernetes.io/managed-by": "platform-factory",
				"platform-factory.dev/backend": "kubevirt",
			},
			"annotations": map[string]string{
				"platform-factory.dev/boot-image": s.Image,
			},
		},
		"spec": map[string]any{
			"runStrategy": "Halted",
			"template": map[string]any{
				"metadata": map[string]any{
					"labels": map[string]string{
						"platform-factory.dev/microvm": s.Name,
					},
				},
				"spec": map[string]any{
					"architecture": s.Arch,
					"domain": map[string]any{
						"cpu":       map[string]any{"cores": s.VCPUs},
						"resources": map[string]any{"requests": map[string]string{"memory": fmt.Sprintf("%dMi", s.MemoryMiB)}},
						"firmware": map[string]any{
							"kernelBoot": map[string]any{
								"container": map[string]any{
									"image":           s.Image,
									"imagePullPolicy": "IfNotPresent",
									"kernelPath":      "/boot/kernel",
									"initrdPath":      "/boot/initramfs.cpio.gz",
								},
								"kernelArgs": fmt.Sprintf("console=%s rdinit=/sbin/init ip=dhcp panic=-1", console),
							},
						},
						"devices": map[string]any{
							"autoattachGraphicsDevice": false,
							"interfaces": []any{
								map[string]any{"name": "default", "masquerade": map[string]any{}, "ports": ports},
							},
						},
					},
					"networks": []any{
						map[string]any{"name": "default", "pod": map[string]any{}},
					},
					"terminationGracePeriodSeconds": 30,
				},
			},
		},
	}
	return json.MarshalIndent(vm, "", "  ")
}

// rbacResourceName is the ServiceAccount/Role/RoleBinding name every RBAC
// object for a given VM shares, so the three-object List RBAC produces is
// trivially attributable back to the VM it was generated for.
func rbacResourceName(s microvm.Spec) string {
	return "platform-factory-microvm-" + s.Name
}

// RBAC renders the minimal ServiceAccount, Role and RoleBinding a KubeVirt
// microVM needs, scoped to exactly s.Namespace (never a ClusterRole: RBAC
// bound to a Role, not a ClusterRole, cannot reach any other namespace no
// matter what verbs or resources it lists) and to exactly the KubeVirt
// resources this package's own actions touch - never "*" for verbs,
// resources or apiGroups, so this can never become a cluster-admin-
// equivalent grant regardless of what a future caller passes in. This is
// deliberately static (no caller-supplied verb/resource list): the whole
// point is that the RBAC this plugin asks for is exactly and only what
// VirtualMachine/ValidateTarget's own actions (create/start/stop/restart/
// status/logs/delete) require, not a general-purpose policy generator.
func RBAC(s microvm.Spec) ([]byte, error) {
	if err := ValidateTarget(s); err != nil {
		return nil, err
	}
	name := rbacResourceName(s)
	labels := map[string]string{
		"app.kubernetes.io/managed-by": "platform-factory",
		"platform-factory.dev/backend": "kubevirt",
	}
	serviceAccount := map[string]any{
		"apiVersion": "v1", "kind": "ServiceAccount",
		"metadata": map[string]any{"name": name, "namespace": s.Namespace, "labels": labels},
	}
	role := map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "Role",
		"metadata": map[string]any{"name": name, "namespace": s.Namespace, "labels": labels},
		"rules": []any{
			map[string]any{
				"apiGroups": []string{"kubevirt.io"},
				"resources": []string{"virtualmachines", "virtualmachineinstances"},
				"verbs":     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			},
			map[string]any{
				"apiGroups": []string{"subresources.kubevirt.io"},
				"resources": []string{"virtualmachines/start", "virtualmachines/stop", "virtualmachines/restart", "virtualmachineinstances/console"},
				"verbs":     []string{"update", "get"},
			},
		},
	}
	roleBinding := map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "RoleBinding",
		"metadata": map[string]any{"name": name, "namespace": s.Namespace, "labels": labels},
		"subjects": []any{
			map[string]any{"kind": "ServiceAccount", "name": name, "namespace": s.Namespace},
		},
		"roleRef": map[string]any{
			"apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": name,
		},
	}
	list := map[string]any{
		"apiVersion": "v1", "kind": "List",
		"items": []any{serviceAccount, role, roleBinding},
	}
	return json.MarshalIndent(list, "", "  ")
}
