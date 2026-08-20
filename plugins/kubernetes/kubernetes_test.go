package kubernetes

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/kubernetes/scheme"
)

func newTestClient(objects ...runtime.Object) (*Client, *fake.Clientset) {
	clientset := fake.NewSimpleClientset(objects...)
	gvrToListKind := map[schema.GroupVersionResource]string{
		{Group: "apps", Version: "v1", Resource: "deployments"}:  "DeploymentList",
		{Group: "apps", Version: "v1", Resource: "statefulsets"}: "StatefulSetList",
		{Group: "apps", Version: "v1", Resource: "daemonsets"}:   "DaemonSetList",
		{Group: "", Version: "v1", Resource: "services"}:         "ServiceList",
		{Group: "", Version: "v1", Resource: "configmaps"}:       "ConfigMapList",
		{Group: "batch", Version: "v1", Resource: "jobs"}:        "JobList",
		{Group: "batch", Version: "v1", Resource: "cronjobs"}:    "CronJobList",
	}
	dynClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme.Scheme, gvrToListKind)
	return &Client{Dynamic: dynClient, Clientset: clientset}, clientset
}

func TestApplySingleObjectAndList(t *testing.T) {
	client, _ := newTestClient()
	ctx := context.Background()

	single := []byte(`{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"api","namespace":"prod"},"spec":{"replicas":1}}`)
	applied, err := client.Apply(ctx, single)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 || applied[0] != "Deployment/prod/api" {
		t.Fatalf("applied=%v", applied)
	}
	if _, err := client.Dynamic.Resource(schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}).
		Namespace("prod").Get(ctx, "api", metav1.GetOptions{}); err != nil {
		t.Fatalf("deployment not found after apply: %v", err)
	}

	list := []byte(`{
		"apiVersion":"v1","kind":"List","items":[
			{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"web","namespace":"prod"},"spec":{"replicas":2}},
			{"apiVersion":"v1","kind":"Service","metadata":{"name":"web","namespace":"prod"},"spec":{}}
		]}`)
	applied, err = client.Apply(ctx, list)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 2 || applied[0] != "Deployment/prod/web" || applied[1] != "Service/prod/web" {
		t.Fatalf("applied=%v", applied)
	}
}

func TestApplyRejectsUnknownKind(t *testing.T) {
	client, _ := newTestClient()
	raw := []byte(`{"apiVersion":"example.com/v1","kind":"Widget","metadata":{"name":"x","namespace":"prod"}}`)
	if _, err := client.Apply(context.Background(), raw); err == nil || !strings.Contains(err.Error(), "no known resource mapping") {
		t.Fatalf("err=%v", err)
	}
}

func TestApplyRejectsMalformedManifest(t *testing.T) {
	client, _ := newTestClient()
	if _, err := client.Apply(context.Background(), []byte("not json")); err == nil {
		t.Fatal("expected malformed manifest to be rejected")
	}
}

func jobWithConditions(name, namespace string, conditions ...batchv1.JobCondition) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Status:     batchv1.JobStatus{Conditions: conditions},
	}
}

func TestWaitForJobCompleteSucceeds(t *testing.T) {
	job := jobWithConditions("migrate", "prod", batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue})
	client, _ := newTestClient(job)
	ready, output, err := client.WaitForJobComplete(context.Background(), "prod", "migrate", time.Second)
	if err != nil || !ready || output == "" {
		t.Fatalf("ready=%v output=%q err=%v", ready, output, err)
	}
}

func TestWaitForJobCompleteFailsOnJobFailedCondition(t *testing.T) {
	job := jobWithConditions("migrate", "prod", batchv1.JobCondition{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Message: "backoff limit exceeded"})
	client, _ := newTestClient(job)
	_, _, err := client.WaitForJobComplete(context.Background(), "prod", "migrate", time.Second)
	if err == nil || !strings.Contains(err.Error(), "backoff limit exceeded") {
		t.Fatalf("err=%v", err)
	}
}

func TestWaitForJobCompleteTimesOut(t *testing.T) {
	restore := setPollInterval(time.Millisecond)
	defer restore()
	job := jobWithConditions("migrate", "prod") // never completes
	client, _ := newTestClient(job)
	_, _, err := client.WaitForJobComplete(context.Background(), "prod", "migrate", 20*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err=%v", err)
	}
}

func setPollInterval(d time.Duration) func() {
	previous := pollInterval
	pollInterval = d
	return func() { pollInterval = previous }
}

func TestGetCronJobSummary(t *testing.T) {
	suspend := true
	scheduled := metav1.NewTime(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	cronJob := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: "nightly", Namespace: "prod"},
		Spec:       batchv1.CronJobSpec{Schedule: "0 2 * * *", Suspend: &suspend},
		Status:     batchv1.CronJobStatus{LastScheduleTime: &scheduled},
	}
	client, _ := newTestClient(cronJob)
	output, err := client.GetCronJobSummary(context.Background(), "prod", "nightly")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"nightly", "0 2 * * *", "True", "2026-01-02T03:04:05Z"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q: %s", want, output)
		}
	}
}

func TestGetCronJobSummarySurfacesNotFound(t *testing.T) {
	client, _ := newTestClient()
	if _, err := client.GetCronJobSummary(context.Background(), "prod", "missing"); err == nil {
		t.Fatal("expected an error for a missing cronjob")
	}
}

func int32ptr(v int32) *int32 { return &v }

func TestRolloutStatusDeploymentSucceeds(t *testing.T) {
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod", Generation: 1},
		Spec:       appsv1.DeploymentSpec{Replicas: int32ptr(3)},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: 1, UpdatedReplicas: 3, Replicas: 3, AvailableReplicas: 3,
		},
	}
	client, _ := newTestClient(deployment)
	ready, output, err := client.RolloutStatus(context.Background(), "deployment", "prod", "api", time.Second)
	if err != nil || !ready || !strings.Contains(output, "successfully rolled out") {
		t.Fatalf("ready=%v output=%q err=%v", ready, output, err)
	}
}

func TestRolloutStatusDeploymentTimesOutWhileNotReady(t *testing.T) {
	restore := setPollInterval(time.Millisecond)
	defer restore()
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod", Generation: 1},
		Spec:       appsv1.DeploymentSpec{Replicas: int32ptr(3)},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: 1, UpdatedReplicas: 1, Replicas: 3, AvailableReplicas: 1,
		},
	}
	client, _ := newTestClient(deployment)
	_, _, err := client.RolloutStatus(context.Background(), "deployment", "prod", "api", 20*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err=%v", err)
	}
}

func TestRolloutStatusStatefulSetAndDaemonSet(t *testing.T) {
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "prod", Generation: 1},
		Spec: appsv1.StatefulSetSpec{
			Replicas:       int32ptr(2),
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{Type: appsv1.RollingUpdateStatefulSetStrategyType},
		},
		Status: appsv1.StatefulSetStatus{
			ObservedGeneration: 1, ReadyReplicas: 2, UpdatedReplicas: 2,
			CurrentRevision: "db-1", UpdateRevision: "db-1", CurrentReplicas: 2,
		},
	}
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "prod", Generation: 1},
		Spec:       appsv1.DaemonSetSpec{UpdateStrategy: appsv1.DaemonSetUpdateStrategy{Type: appsv1.RollingUpdateDaemonSetStrategyType}},
		Status: appsv1.DaemonSetStatus{
			ObservedGeneration: 1, DesiredNumberScheduled: 3, UpdatedNumberScheduled: 3, NumberAvailable: 3,
		},
	}
	client, _ := newTestClient(sts, ds)

	if ready, _, err := client.RolloutStatus(context.Background(), "statefulset", "prod", "db", time.Second); err != nil || !ready {
		t.Fatalf("statefulset ready=%v err=%v", ready, err)
	}
	if ready, _, err := client.RolloutStatus(context.Background(), "daemonset", "prod", "agent", time.Second); err != nil || !ready {
		t.Fatalf("daemonset ready=%v err=%v", ready, err)
	}
}

func TestRolloutStatusRejectsUnsupportedResourceType(t *testing.T) {
	client, _ := newTestClient()
	if _, _, err := client.RolloutStatus(context.Background(), "cronjob", "prod", "x", time.Second); err == nil {
		t.Fatal("expected an unsupported resource type to be rejected")
	}
}

// replicaSetFixture builds a Deployment plus a chain of owned ReplicaSets
// at revisions 1..n, container image "app:vN" for revision N, with
// revision n marked as the Deployment's current template (matching what
// a real Deployment controller leaves behind after n rollouts).
func replicaSetFixture(namespace, name string, revisions int) (*appsv1.Deployment, []runtime.Object) {
	deploymentUID := "deploy-uid-1"
	labels := map[string]string{"app.kubernetes.io/name": name}
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, UID: "deploy-uid-1"},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: name, Image: "app:v" + itoa(revisions)}}},
			},
		},
	}
	objects := make([]runtime.Object, 0, revisions)
	for revision := 1; revision <= revisions; revision++ {
		rs := &appsv1.ReplicaSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: name + "-rs" + itoa(revision), Namespace: namespace, Labels: labels,
				Annotations:     map[string]string{revisionAnnotation: itoa(revision)},
				OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: name, UID: types.UID(deploymentUID)}},
			},
			Spec: appsv1.ReplicaSetSpec{
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: labels},
					Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: name, Image: "app:v" + itoa(revision)}}},
				},
			},
		}
		objects = append(objects, rs)
	}
	return deployment, objects
}

func itoa(v int) string {
	return strconv.Itoa(v)
}

func TestRolloutUndoSelectsPreviousRevisionByDefault(t *testing.T) {
	deployment, replicaSets := replicaSetFixture("prod", "api", 3)
	objects := append([]runtime.Object{deployment}, replicaSets...)
	client, clientset := newTestClient(objects...)

	revision, err := client.RolloutUndo(context.Background(), "prod", "api", 0)
	if err != nil {
		t.Fatal(err)
	}
	if revision != 2 {
		t.Fatalf("revision=%d, want 2 (the previous revision before 3)", revision)
	}
	updated, err := clientset.AppsV1().Deployments("prod").Get(context.Background(), "api", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := updated.Spec.Template.Spec.Containers[0].Image; got != "app:v2" {
		t.Fatalf("image=%s, want app:v2 (revision 2's template)", got)
	}
}

func TestRolloutUndoSelectsExplicitRevision(t *testing.T) {
	deployment, replicaSets := replicaSetFixture("prod", "api", 4)
	objects := append([]runtime.Object{deployment}, replicaSets...)
	client, clientset := newTestClient(objects...)

	revision, err := client.RolloutUndo(context.Background(), "prod", "api", 1)
	if err != nil {
		t.Fatal(err)
	}
	if revision != 1 {
		t.Fatalf("revision=%d, want 1", revision)
	}
	updated, err := clientset.AppsV1().Deployments("prod").Get(context.Background(), "api", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := updated.Spec.Template.Spec.Containers[0].Image; got != "app:v1" {
		t.Fatalf("image=%s, want app:v1 (revision 1's template)", got)
	}
}

func TestRolloutUndoRejectsCurrentRevisionAndMissingHistory(t *testing.T) {
	deployment, replicaSets := replicaSetFixture("prod", "api", 2)
	objects := append([]runtime.Object{deployment}, replicaSets...)
	client, _ := newTestClient(objects...)

	if _, err := client.RolloutUndo(context.Background(), "prod", "api", 2); err == nil ||
		!strings.Contains(err.Error(), "already at revision") {
		t.Fatalf("err=%v", err)
	}
	if _, err := client.RolloutUndo(context.Background(), "prod", "api", 99); err == nil ||
		!strings.Contains(err.Error(), "no revision history") {
		t.Fatalf("err=%v", err)
	}

	noHistoryClient, _ := newTestClient(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "solo", Namespace: "prod", UID: "solo-uid"},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "solo"}},
		},
	})
	if _, err := noHistoryClient.RolloutUndo(context.Background(), "prod", "solo", 0); err == nil ||
		!strings.Contains(err.Error(), "no revision history") {
		t.Fatalf("err=%v", err)
	}
}

func TestEventsSortedOldestFirst(t *testing.T) {
	older := metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	newer := metav1.NewTime(time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC))
	events := []runtime.Object{
		&corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Name: "evt2", Namespace: "prod"},
			InvolvedObject: corev1.ObjectReference{Name: "api", Kind: "Deployment"},
			LastTimestamp:  newer, Type: "Normal", Reason: "ScalingReplicaSet", Message: "second",
		},
		&corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Name: "evt1", Namespace: "prod"},
			InvolvedObject: corev1.ObjectReference{Name: "api", Kind: "Deployment"},
			LastTimestamp:  older, Type: "Normal", Reason: "Scheduled", Message: "first",
		},
	}
	client, _ := newTestClient(events...)
	output, err := client.Events(context.Background(), "prod", "api")
	if err != nil {
		t.Fatal(err)
	}
	firstIndex := strings.Index(output, "first")
	secondIndex := strings.Index(output, "second")
	if firstIndex < 0 || secondIndex < 0 || firstIndex > secondIndex {
		t.Fatalf("events not sorted oldest-first: %s", output)
	}
}

func TestLogsSurfacesNoPodsFound(t *testing.T) {
	client, _ := newTestClient()
	if _, err := client.Logs(context.Background(), "prod", "api", 10, false); err == nil ||
		!strings.Contains(err.Error(), "no pods found") {
		t.Fatalf("err=%v", err)
	}
}
