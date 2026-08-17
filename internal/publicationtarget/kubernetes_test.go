package publicationtarget

import (
	"strings"
	"testing"
)

func TestKubernetesManifestIsPinnedAndHardened(t *testing.T) {
	spec := KubernetesSpec{Workload: "service", Name: "demo", Namespace: "default", Image: "registry.example/app@sha256:" + strings.Repeat("a", 64), Replicas: 2, Port: 8080, CPURequest: "100m", MemoryRequest: "128Mi"}
	manifest, err := KubernetesManifest(spec)
	if err != nil {
		t.Fatal(err)
	}
	text := string(manifest)
	for _, want := range []string{`"kind": "Deployment"`, `"kind": "Service"`, `"runAsNonRoot": true`, `"type": "RuntimeDefault"`, `"allowPrivilegeEscalation": false`, `"readOnlyRootFilesystem": true`, `"ALL"`, `"readinessProbe"`, `"livenessProbe"`, `"cpu": "100m"`, `"memory": "128Mi"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("manifest missing %q:\n%s", want, text)
		}
	}
}

func TestKubernetesManifestRejectsHostileInputs(t *testing.T) {
	base := KubernetesSpec{Workload: "service", Name: "demo", Namespace: "default", Image: "registry.example/app@sha256:" + strings.Repeat("a", 64), Replicas: 1, Port: 8080, CPURequest: "100m", MemoryRequest: "128Mi"}
	cases := []KubernetesSpec{base, base, base, base}
	cases[0].Name = "../../escape"
	cases[1].Image = "registry.example/app:latest"
	cases[2].Port = 65536
	cases[3].CPURequest = "100m\nmalicious"
	for _, spec := range cases {
		if _, err := KubernetesManifest(spec); err == nil {
			t.Fatalf("accepted hostile spec: %+v", spec)
		}
	}
}

func TestKubernetesJobHasNoServiceOrProbes(t *testing.T) {
	spec := KubernetesSpec{Workload: "job", Name: "batch", Namespace: "jobs", Image: "registry.example/job@sha256:" + strings.Repeat("b", 64), Replicas: 1, Port: 8080, CPURequest: "250m", MemoryRequest: "256Mi"}
	manifest, err := KubernetesManifest(spec)
	if err != nil {
		t.Fatal(err)
	}
	text := string(manifest)
	if !strings.Contains(text, `"kind": "Job"`) || !strings.Contains(text, `"restartPolicy": "Never"`) || strings.Contains(text, "readinessProbe") || strings.Contains(text, `"kind": "Service"`) {
		t.Fatalf("invalid job manifest:\n%s", text)
	}
}

func TestKubernetesRuntimeClassSchedulesOnlyOnReadyNodes(t *testing.T) {
	spec := KubernetesSpec{Workload: "service", Name: "demo", Namespace: "default", Image: "registry.example/app@sha256:" + strings.Repeat("c", 64), Replicas: 1, Port: 8080, CPURequest: "100m", MemoryRequest: "128Mi", RuntimeClass: "platform-factory"}
	manifest, err := KubernetesManifest(spec)
	if err != nil {
		t.Fatal(err)
	}
	text := string(manifest)
	for _, want := range []string{`"runtimeClassName": "platform-factory"`, `"platform-factory.dev/runtime-platform-factory": "ready"`, `"effect": "NoSchedule"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("manifest missing %q:\n%s", want, text)
		}
	}
}

func TestKubernetesRuntimeClassRejectsHostileName(t *testing.T) {
	spec := KubernetesSpec{Workload: "job", Name: "demo", Namespace: "default", Image: "registry.example/app@sha256:" + strings.Repeat("d", 64), Replicas: 1, Port: 8080, CPURequest: "100m", MemoryRequest: "128Mi", RuntimeClass: "../escape"}
	if _, err := KubernetesManifest(spec); err == nil {
		t.Fatal("hostile runtime class accepted")
	}
}

func TestKubernetesAdditionalWorkloadsAreDeterministicAndHardened(t *testing.T) {
	for _, workload := range []string{"statefulset", "daemonset", "cronjob"} {
		t.Run(workload, func(t *testing.T) {
			spec := KubernetesSpec{Workload: workload, Name: "demo", Namespace: "default", Image: "registry.example/app@sha256:" + strings.Repeat("e", 64), Replicas: 2, Port: 8080, CPURequest: "100m", MemoryRequest: "128Mi"}
			if workload == "cronjob" {
				spec.Schedule = "*/5 * * * *"
			}
			first, err := KubernetesManifest(spec)
			if err != nil {
				t.Fatal(err)
			}
			second, err := KubernetesManifest(spec)
			if err != nil || string(first) != string(second) {
				t.Fatalf("non-deterministic manifest: err=%v", err)
			}
			for _, want := range []string{`"runAsNonRoot": true`, `"readOnlyRootFilesystem": true`, `"image": "` + spec.Image + `"`} {
				if !strings.Contains(string(first), want) {
					t.Fatalf("%s missing %s: %s", workload, want, first)
				}
			}
			if workload == "statefulset" && !strings.Contains(string(first), `"clusterIP": "None"`) {
				t.Fatalf("statefulset service is not headless: %s", first)
			}
		})
	}
}

func TestKubernetesCronJobRejectsMissingOrInjectedSchedule(t *testing.T) {
	base := KubernetesSpec{Workload: "cronjob", Name: "demo", Namespace: "default", Image: "registry.example/app@sha256:" + strings.Repeat("f", 64), Replicas: 1, Port: 8080, CPURequest: "100m", MemoryRequest: "128Mi"}
	for _, schedule := range []string{"", "* * * * *\nkind: Pod", "@daily", "* * * * * extra"} {
		base.Schedule = schedule
		if _, err := KubernetesManifest(base); err == nil {
			t.Fatalf("accepted schedule %q", schedule)
		}
	}
}

func TestKubernetesIngressConfigSecretReferencesAndPVC(t *testing.T) {
	spec := KubernetesSpec{
		Workload: "service", Name: "demo", Namespace: "default", Image: "registry.example/app@sha256:" + strings.Repeat("a", 64), Replicas: 1, Port: 8080, CPURequest: "100m", MemoryRequest: "128Mi",
		IngressHost: "demo.example.com", IngressPath: "/api",
		Config:    []KeyValue{{Key: "MODE", Value: "production"}},
		SecretEnv: []SecretEnvReference{{Env: "DATABASE_PASSWORD", Secret: "database", Key: "password"}},
		Volumes:   []PersistentVolume{{MountPath: "/var/lib/demo", Size: "10Gi"}},
	}
	manifest, err := KubernetesManifest(spec)
	if err != nil {
		t.Fatal(err)
	}
	text := string(manifest)
	for _, want := range []string{`"kind": "Ingress"`, `"host": "demo.example.com"`, `"kind": "ConfigMap"`, `"MODE": "production"`, `"secretKeyRef"`, `"name": "database"`, `"key": "password"`, `"kind": "PersistentVolumeClaim"`, `"storage": "10Gi"`, `"mountPath": "/var/lib/demo"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("manifest missing %s: %s", want, text)
		}
	}
	if strings.Contains(text, "secret-value") {
		t.Fatalf("manifest leaked a secret value: %s", text)
	}
}

func TestKubernetesExtensionsRejectHostileOrAmbiguousInputs(t *testing.T) {
	base := KubernetesSpec{Workload: "service", Name: "demo", Namespace: "default", Image: "registry.example/app@sha256:" + strings.Repeat("b", 64), Replicas: 1, Port: 8080, CPURequest: "100m", MemoryRequest: "128Mi"}
	cases := []KubernetesSpec{base, base, base, base, base}
	cases[0].IngressHost, cases[0].IngressPath = "Bad_Host", "/"
	cases[1].IngressHost, cases[1].IngressPath = "demo.example", "/\nkind: Pod"
	cases[2].Volumes = []PersistentVolume{{MountPath: "/var/../etc", Size: "1Gi"}}
	cases[3].Config = []KeyValue{{Key: "TOKEN", Value: "plain"}}
	cases[3].SecretEnv = []SecretEnvReference{{Env: "TOKEN", Secret: "secret", Key: "token"}}
	cases[4].SecretEnv = []SecretEnvReference{{Env: "TOKEN", Secret: "../../secret", Key: "token"}}
	for _, spec := range cases {
		if _, err := KubernetesManifest(spec); err == nil {
			t.Fatalf("accepted hostile spec: %+v", spec)
		}
	}
}
