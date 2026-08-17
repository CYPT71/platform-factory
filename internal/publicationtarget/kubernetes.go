// Package publicationtarget defines deterministic, side-effect-free target
// contracts shared by the CLI and the public conformance suite.
package publicationtarget

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type KubernetesSpec struct {
	Workload      string               `json:"workload"`
	Name          string               `json:"name"`
	Namespace     string               `json:"namespace"`
	Image         string               `json:"image"`
	Replicas      int                  `json:"replicas"`
	Port          int                  `json:"port"`
	CPURequest    string               `json:"cpu_request"`
	MemoryRequest string               `json:"memory_request"`
	RuntimeClass  string               `json:"runtime_class,omitempty"`
	Schedule      string               `json:"schedule,omitempty"`
	IngressHost   string               `json:"ingress_host,omitempty"`
	IngressPath   string               `json:"ingress_path,omitempty"`
	Config        []KeyValue           `json:"config,omitempty"`
	SecretEnv     []SecretEnvReference `json:"secret_env,omitempty"`
	Volumes       []PersistentVolume   `json:"volumes,omitempty"`
}

type KeyValue struct{ Key, Value string }
type SecretEnvReference struct{ Env, Secret, Key string }
type PersistentVolume struct{ MountPath, Size string }

func (s KubernetesSpec) Validate() error {
	if s.Workload != "service" && s.Workload != "job" && s.Workload != "statefulset" && s.Workload != "daemonset" && s.Workload != "cronjob" {
		return errors.New("workload must be service, job, statefulset, daemonset, or cronjob")
	}
	if !ValidKubernetesName(s.Name) || !ValidKubernetesName(s.Namespace) {
		return errors.New("name and namespace must be valid Kubernetes names")
	}
	if !ValidDigestReference(s.Image) {
		return errors.New("image must be pinned by sha256 digest")
	}
	if s.Replicas < 1 || s.Port < 1 || s.Port > 65535 {
		return errors.New("replicas and port are out of range")
	}
	if !ValidResourceQuantity(s.CPURequest) || !ValidResourceQuantity(s.MemoryRequest) {
		return errors.New("resource requests must be non-empty Kubernetes quantities")
	}
	if s.RuntimeClass != "" && !ValidKubernetesName(s.RuntimeClass) {
		return errors.New("runtime class must be a valid Kubernetes name")
	}
	if s.Workload == "cronjob" {
		if !validCronSchedule(s.Schedule) {
			return errors.New("cronjob requires a bounded five-field schedule")
		}
	} else if s.Schedule != "" {
		return errors.New("schedule is valid only for cronjob")
	}
	if err := validateExtensions(s); err != nil {
		return err
	}
	return nil
}

func validateExtensions(s KubernetesSpec) error {
	if (s.IngressHost == "") != (s.IngressPath == "") {
		return errors.New("ingress host and path must be provided together")
	}
	if s.IngressHost != "" && (s.Workload == "job" || s.Workload == "cronjob") {
		return errors.New("ingress requires a service workload")
	}
	if s.IngressHost != "" && (!validDNSName(s.IngressHost) || !strings.HasPrefix(s.IngressPath, "/") || strings.ContainsAny(s.IngressPath, "\r\n\x00")) {
		return errors.New("invalid ingress host or path")
	}
	seen := map[string]bool{}
	for _, item := range s.Config {
		if !validConfigKey(item.Key) || seen["env:"+item.Key] {
			return errors.New("invalid or duplicate config key")
		}
		seen["env:"+item.Key] = true
	}
	for _, item := range s.SecretEnv {
		if !validConfigKey(item.Env) || !ValidKubernetesName(item.Secret) || !validConfigKey(item.Key) || seen["env:"+item.Env] {
			return errors.New("invalid or duplicate secret environment reference")
		}
		seen["env:"+item.Env] = true
	}
	for _, item := range s.Volumes {
		if item.MountPath == "" || !strings.HasPrefix(item.MountPath, "/") || strings.Contains(item.MountPath, "..") || strings.ContainsAny(item.MountPath, "\r\n\x00") || !ValidResourceQuantity(item.Size) || seen["mount:"+item.MountPath] {
			return errors.New("invalid or duplicate persistent volume")
		}
		seen["mount:"+item.MountPath] = true
	}
	return nil
}

func validConfigKey(value string) bool {
	if value == "" || len(value) > 253 {
		return false
	}
	for _, c := range value {
		if (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' && c != '-' && c != '.' {
			return false
		}
	}
	return true
}
func validDNSName(value string) bool {
	if value == "" || len(value) > 253 || strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if !ValidKubernetesName(label) {
			return false
		}
	}
	return true
}

func KubernetesManifest(s KubernetesSpec) ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	var items []any
	switch s.Workload {
	case "job":
		items = []any{job(s)}
	case "cronjob":
		items = []any{cronJob(s)}
	case "daemonset":
		items = []any{daemonSet(s), service(s)}
	case "statefulset":
		items = []any{statefulSet(s), service(s)}
	default:
		items = []any{deployment(s), service(s)}
	}
	if len(s.Config) > 0 {
		items = append(items, configMap(s))
	}
	for i := range s.Volumes {
		items = append(items, persistentVolumeClaim(s, i))
	}
	if s.IngressHost != "" {
		items = append(items, ingress(s))
	}
	if len(items) == 1 {
		return marshal(items[0]), nil
	}
	return marshal(map[string]any{"apiVersion": "v1", "kind": "List", "items": items}), nil
}

func validCronSchedule(value string) bool {
	if value == "" || len(value) > 128 || strings.ContainsAny(value, "\r\n\x00") {
		return false
	}
	fields := strings.Fields(value)
	if len(fields) != 5 {
		return false
	}
	for _, field := range fields {
		for _, c := range field {
			if !strings.ContainsRune("0123456789*/,-", c) {
				return false
			}
		}
	}
	return true
}

func ValidDigestReference(value string) bool {
	_, digest, found := strings.Cut(value, "@sha256:")
	if !found || len(digest) != 64 {
		return false
	}
	for _, c := range digest {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

func ValidKubernetesName(value string) bool {
	if value == "" || len(value) > 63 || value[0] == '-' || value[len(value)-1] == '-' {
		return false
	}
	for _, c := range value {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

func ValidResourceQuantity(value string) bool {
	return value != "" && !strings.ContainsAny(value, " \t\r\n\x00")
}
func resources(s KubernetesSpec) map[string]any {
	return map[string]any{"requests": map[string]string{"cpu": s.CPURequest, "memory": s.MemoryRequest}}
}
func security() map[string]any {
	return map[string]any{"allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": map[string]any{"drop": []string{"ALL"}}}
}
func podSecurity() map[string]any {
	return map[string]any{"runAsNonRoot": true, "seccompProfile": map[string]string{"type": "RuntimeDefault"}}
}

func podSpec(s KubernetesSpec, containers []any) map[string]any {
	spec := map[string]any{"securityContext": podSecurity(), "containers": containers}
	if s.RuntimeClass != "" {
		spec["runtimeClassName"] = s.RuntimeClass
		spec["nodeSelector"] = map[string]string{"platform-factory.dev/runtime-" + s.RuntimeClass: "ready"}
		spec["tolerations"] = []any{map[string]any{
			"key": "platform-factory.dev/runtime-" + s.RuntimeClass, "operator": "Equal",
			"value": "ready", "effect": "NoSchedule",
		}}
	}
	if len(s.Volumes) > 0 {
		volumes := make([]any, 0, len(s.Volumes))
		for i := range s.Volumes {
			volumes = append(volumes, map[string]any{"name": volumeName(i), "persistentVolumeClaim": map[string]string{"claimName": volumeName(i) + "-" + s.Name}})
		}
		spec["volumes"] = volumes
	}
	return spec
}
func probe(port, delay, period int) map[string]any {
	return map[string]any{"tcpSocket": map[string]any{"port": port}, "initialDelaySeconds": delay, "periodSeconds": period}
}

func deployment(s KubernetesSpec) map[string]any {
	return map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": s.Name, "namespace": s.Namespace},
		"spec": map[string]any{"replicas": s.Replicas, "selector": map[string]any{"matchLabels": map[string]string{"app.kubernetes.io/name": s.Name}}, "template": map[string]any{"metadata": map[string]any{"labels": map[string]string{"app.kubernetes.io/name": s.Name}}, "spec": podSpec(s, serviceContainers(s))}},
	}
}
func statefulSet(s KubernetesSpec) map[string]any {
	return map[string]any{
		"apiVersion": "apps/v1", "kind": "StatefulSet", "metadata": map[string]any{"name": s.Name, "namespace": s.Namespace},
		"spec": map[string]any{"serviceName": s.Name, "replicas": s.Replicas, "selector": map[string]any{"matchLabels": map[string]string{"app.kubernetes.io/name": s.Name}}, "template": map[string]any{"metadata": map[string]any{"labels": map[string]string{"app.kubernetes.io/name": s.Name}}, "spec": podSpec(s, serviceContainers(s))}},
	}
}
func daemonSet(s KubernetesSpec) map[string]any {
	return map[string]any{
		"apiVersion": "apps/v1", "kind": "DaemonSet", "metadata": map[string]any{"name": s.Name, "namespace": s.Namespace},
		"spec": map[string]any{"selector": map[string]any{"matchLabels": map[string]string{"app.kubernetes.io/name": s.Name}}, "template": map[string]any{"metadata": map[string]any{"labels": map[string]string{"app.kubernetes.io/name": s.Name}}, "spec": podSpec(s, serviceContainers(s))}},
	}
}
func serviceContainers(s KubernetesSpec) []any {
	return []any{container(s, true)}
}

func container(s KubernetesSpec, withPort bool) map[string]any {
	c := map[string]any{"name": s.Name, "image": s.Image, "securityContext": security(), "resources": resources(s)}
	if withPort {
		c["ports"] = []any{map[string]any{"containerPort": s.Port}}
		c["readinessProbe"] = probe(s.Port, 1, 5)
		c["livenessProbe"] = probe(s.Port, 5, 10)
	}
	var env []any
	if len(s.Config) > 0 {
		for _, item := range s.Config {
			env = append(env, map[string]any{"name": item.Key, "valueFrom": map[string]any{"configMapKeyRef": map[string]string{"name": s.Name + "-config", "key": item.Key}}})
		}
	}
	for _, item := range s.SecretEnv {
		env = append(env, map[string]any{"name": item.Env, "valueFrom": map[string]any{"secretKeyRef": map[string]string{"name": item.Secret, "key": item.Key}}})
	}
	if len(env) > 0 {
		c["env"] = env
	}
	if len(s.Volumes) > 0 {
		mounts := make([]any, 0, len(s.Volumes))
		for i, item := range s.Volumes {
			mounts = append(mounts, map[string]string{"name": volumeName(i), "mountPath": item.MountPath})
		}
		c["volumeMounts"] = mounts
	}
	return c
}
func service(s KubernetesSpec) map[string]any {
	spec := map[string]any{"selector": map[string]string{"app.kubernetes.io/name": s.Name}, "ports": []any{map[string]any{"port": s.Port, "targetPort": s.Port}}}
	if s.Workload == "statefulset" {
		spec["clusterIP"] = "None"
	}
	return map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": s.Name, "namespace": s.Namespace}, "spec": spec}
}
func job(s KubernetesSpec) map[string]any {
	pod := podSpec(s, []any{container(s, false)})
	pod["restartPolicy"] = "Never"
	return map[string]any{"apiVersion": "batch/v1", "kind": "Job", "metadata": map[string]any{"name": s.Name, "namespace": s.Namespace}, "spec": map[string]any{"backoffLimit": 3, "template": map[string]any{"metadata": map[string]any{"labels": map[string]string{"app.kubernetes.io/name": s.Name}}, "spec": pod}}}
}
func cronJob(s KubernetesSpec) map[string]any {
	pod := podSpec(s, []any{container(s, false)})
	pod["restartPolicy"] = "Never"
	return map[string]any{"apiVersion": "batch/v1", "kind": "CronJob", "metadata": map[string]any{"name": s.Name, "namespace": s.Namespace}, "spec": map[string]any{"schedule": s.Schedule, "concurrencyPolicy": "Forbid", "successfulJobsHistoryLimit": 1, "failedJobsHistoryLimit": 1, "jobTemplate": map[string]any{"spec": map[string]any{"backoffLimit": 3, "template": map[string]any{"metadata": map[string]any{"labels": map[string]string{"app.kubernetes.io/name": s.Name}}, "spec": pod}}}}}
}
func configMap(s KubernetesSpec) map[string]any {
	data := map[string]string{}
	for _, item := range s.Config {
		data[item.Key] = item.Value
	}
	return map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": s.Name + "-config", "namespace": s.Namespace}, "data": data}
}
func volumeName(i int) string { return fmt.Sprintf("data-%d", i) }
func persistentVolumeClaim(s KubernetesSpec, i int) map[string]any {
	return map[string]any{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": map[string]any{"name": volumeName(i) + "-" + s.Name, "namespace": s.Namespace}, "spec": map[string]any{"accessModes": []string{"ReadWriteOnce"}, "resources": map[string]any{"requests": map[string]string{"storage": s.Volumes[i].Size}}}}
}
func ingress(s KubernetesSpec) map[string]any {
	return map[string]any{"apiVersion": "networking.k8s.io/v1", "kind": "Ingress", "metadata": map[string]any{"name": s.Name, "namespace": s.Namespace}, "spec": map[string]any{"rules": []any{map[string]any{"host": s.IngressHost, "http": map[string]any{"paths": []any{map[string]any{"path": s.IngressPath, "pathType": "Prefix", "backend": map[string]any{"service": map[string]any{"name": s.Name, "port": map[string]int{"number": s.Port}}}}}}}}}}
}
func marshal(value any) []byte {
	data, _ := json.MarshalIndent(value, "", "  ")
	return append(data, '\n')
}
