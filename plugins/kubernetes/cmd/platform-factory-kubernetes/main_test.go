package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	pfkubernetes "github.com/CYPT71/platform-factory/plugins/kubernetes"
	plugin "github.com/CYPT71/platform-factory/sdk/plugin"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/kubernetes/scheme"
)

func stubClient(objects ...runtime.Object) *pfkubernetes.Client {
	gvrToListKind := map[schema.GroupVersionResource]string{
		{Group: "apps", Version: "v1", Resource: "deployments"}: "DeploymentList",
	}
	return &pfkubernetes.Client{
		Dynamic:   dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme.Scheme, gvrToListKind),
		Clientset: fake.NewSimpleClientset(objects...),
	}
}

func withStubClient(t *testing.T, client *pfkubernetes.Client, clientErr error) {
	t.Helper()
	previous := newClient
	newClient = func() (*pfkubernetes.Client, error) { return client, clientErr }
	t.Cleanup(func() { newClient = previous })
}

func TestHandleApplyAppliesManifestAndRejectsBadInput(t *testing.T) {
	withStubClient(t, stubClient(), nil)
	raw, err := json.Marshal(plugin.DeploymentApplyParams{
		Manifest: json.RawMessage(`{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"api","namespace":"prod"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := handleApply(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	applyResult, ok := result.(plugin.DeploymentApplyResult)
	if !ok || !applyResult.Applied || len(applyResult.Resources) != 1 {
		t.Fatalf("result=%+v ok=%v", result, ok)
	}

	if _, err := handleApply(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("expected an empty manifest to be rejected")
	}
	if _, err := handleApply(context.Background(), json.RawMessage(`not json`)); err == nil {
		t.Fatal("expected malformed params to be rejected")
	}
}

func TestHandleApplySurfacesClientConstructionFailure(t *testing.T) {
	withStubClient(t, nil, errors.New("no kubeconfig"))
	raw, _ := json.Marshal(plugin.DeploymentApplyParams{Manifest: json.RawMessage(`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"x","namespace":"prod"}}`)})
	if _, err := handleApply(context.Background(), raw); err == nil || !strings.Contains(err.Error(), "no kubeconfig") {
		t.Fatalf("err=%v", err)
	}
}

func TestHandleObserveDispatchesEveryKind(t *testing.T) {
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "migrate", Namespace: "prod"},
		Status:     batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}},
	}
	cronJob := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: "nightly", Namespace: "prod"},
		Spec:       batchv1.CronJobSpec{Schedule: "0 2 * * *"},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "api-pod", Namespace: "prod", Labels: map[string]string{"app.kubernetes.io/name": "api"}},
	}
	withStubClient(t, stubClient(job, cronJob, pod), nil)

	t.Run("wait-job", func(t *testing.T) {
		raw, _ := json.Marshal(plugin.DeploymentObserveParams{Kind: "wait-job", Namespace: "prod", Name: "migrate", Timeout: "1s"})
		result, err := handleObserve(context.Background(), raw)
		if err != nil {
			t.Fatal(err)
		}
		observeResult := result.(plugin.DeploymentObserveResult)
		if !observeResult.Ready {
			t.Fatalf("result=%+v", observeResult)
		}
	})

	t.Run("get-cronjob", func(t *testing.T) {
		raw, _ := json.Marshal(plugin.DeploymentObserveParams{Kind: "get-cronjob", Namespace: "prod", Name: "nightly"})
		result, err := handleObserve(context.Background(), raw)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result.(plugin.DeploymentObserveResult).Output, "nightly") {
			t.Fatalf("result=%+v", result)
		}
	})

	t.Run("rollout-status requires resource_type", func(t *testing.T) {
		raw, _ := json.Marshal(plugin.DeploymentObserveParams{Kind: "rollout-status", Namespace: "prod", Name: "api"})
		if _, err := handleObserve(context.Background(), raw); err == nil || !strings.Contains(err.Error(), "resource_type") {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("logs", func(t *testing.T) {
		raw, _ := json.Marshal(plugin.DeploymentObserveParams{Kind: "logs", Namespace: "prod", Name: "api"})
		result, err := handleObserve(context.Background(), raw)
		if err != nil {
			t.Fatal(err)
		}
		if !result.(plugin.DeploymentObserveResult).Ready {
			t.Fatalf("result=%+v", result)
		}
	})

	t.Run("events", func(t *testing.T) {
		raw, _ := json.Marshal(plugin.DeploymentObserveParams{Kind: "events", Namespace: "prod", Name: "api"})
		result, err := handleObserve(context.Background(), raw)
		if err != nil {
			t.Fatal(err)
		}
		if !result.(plugin.DeploymentObserveResult).Ready {
			t.Fatalf("result=%+v", result)
		}
	})

	t.Run("unsupported kind", func(t *testing.T) {
		raw, _ := json.Marshal(plugin.DeploymentObserveParams{Kind: "bogus", Namespace: "prod", Name: "api"})
		if _, err := handleObserve(context.Background(), raw); err == nil || !strings.Contains(err.Error(), "unsupported observe kind") {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("missing namespace or name rejected", func(t *testing.T) {
		raw, _ := json.Marshal(plugin.DeploymentObserveParams{Kind: "events"})
		if _, err := handleObserve(context.Background(), raw); err == nil {
			t.Fatal("expected missing namespace/name to be rejected")
		}
	})
}

func TestHandleRollbackRequiresNamespaceAndName(t *testing.T) {
	withStubClient(t, stubClient(), nil)
	raw, _ := json.Marshal(plugin.DeploymentRollbackParams{})
	if _, err := handleRollback(context.Background(), raw); err == nil {
		t.Fatal("expected missing namespace/name to be rejected")
	}
}

func TestParseTimeoutFallsBackToDefault(t *testing.T) {
	for _, value := range []string{"", "not-a-duration", "-5s", "0s"} {
		if got := parseTimeout(value); got != defaultTimeout {
			t.Fatalf("value=%q got=%v want=%v", value, got, defaultTimeout)
		}
	}
	if got := parseTimeout("90s"); got != 90*time.Second {
		t.Fatalf("got=%v", got)
	}
}
