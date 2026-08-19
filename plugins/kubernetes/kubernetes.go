// Package kubernetes is the Kubernetes backend plugin for platform-factory
// deploy/rollback/observe: it applies a generated manifest, waits for
// workloads to become ready, reads status, and rolls a Deployment back to
// a prior revision - all through a real Kubernetes API client
// (k8s.io/client-go), never by shelling out to the kubectl binary. That
// CLI-shelling approach is exactly what broke platform-factory-mcp's
// distroless container image (it ships no kubectl), the same flaw this
// plugin exists to fix - see cmd/platform-factory/lifecycle.go and
// observe.go for the host side that dispatches to it.
package kubernetes

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"
)

// FieldManager identifies platform-factory's own writes for server-side
// apply's field-ownership tracking (and for the annotation left after a
// rollback), the same role `kubectl apply/rollout --field-manager` plays
// for the kubectl CLI.
const FieldManager = "platform-factory"

// pollInterval is how often WaitForJobComplete/RolloutStatus re-check
// cluster state - a real duration (not a mock clock) because a real wait
// is bounded by a real cluster's own condition-propagation latency, the
// same tradeoff `kubectl wait`/`kubectl rollout status`'s own polling
// makes. A var so tests can shrink it instead of running for real
// seconds against a fake clientset that reflects new state instantly.
var pollInterval = 2 * time.Second

// Client wraps the two client-go client shapes this plugin needs:
// Dynamic (generic apply of the map[string]any/unstructured JSON
// internal/publicationtarget.KubernetesManifest already produces, which
// spans multiple resource kinds without needing a typed clientset for
// each) and Clientset, the typed Kubernetes clientset - status polling
// and the Deployment rollback's ReplicaSet revision history are both far
// more naturally expressed against typed apps/v1 and batch/v1 types than
// unstructured.Unstructured, so those operations use Clientset directly
// rather than going back through Dynamic.
type Client struct {
	Dynamic   dynamic.Interface
	Clientset kubernetes.Interface
}

// NewClientFromKubeconfig builds a Client from the standard client-go
// discovery chain: $KUBECONFIG, then ~/.kube/config, then in-cluster
// config as a fallback. clientcmd.NewNonInteractiveDeferredLoadingClientConfig
// already implements exactly that precedence (its DeferredLoadingClientConfig
// falls back to rest.InClusterConfig when no kubeconfig file resolves a
// usable config), so this is a thin wrapper rather than reimplementing
// the chain.
func NewClientFromKubeconfig() (*Client, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("kubernetes plugin: load kubeconfig: %w", err)
	}
	return NewClientFromRESTConfig(config)
}

// NewClientFromRESTConfig builds a Client directly from an already
// resolved *rest.Config - the entry point NewClientFromKubeconfig itself
// uses, and the one tests would use against a real envtest/kind API
// server if one were available in this environment (it is not; see the
// accompanying report).
func NewClientFromRESTConfig(config *rest.Config) (*Client, error) {
	dyn, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("kubernetes plugin: build dynamic client: %w", err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("kubernetes plugin: build clientset: %w", err)
	}
	return &Client{Dynamic: dyn, Clientset: clientset}, nil
}

// knownGVRs maps the exact apiVersion/kind pairs
// internal/publicationtarget.KubernetesManifest can produce
// (Deployment/Service/Job/CronJob/StatefulSet/DaemonSet/ConfigMap/
// PersistentVolumeClaim/Ingress) to the GroupVersionResource Apply needs.
// A static table rather than discovery-based REST mapping: the manifest
// generator's own output is a closed, known set of kinds, so a discovery
// round trip (and the RESTMapper machinery it requires) buys nothing
// here that this table doesn't already give directly.
var knownGVRs = map[string]schema.GroupVersionResource{
	"apps/v1|Deployment":                  {Group: "apps", Version: "v1", Resource: "deployments"},
	"apps/v1|StatefulSet":                 {Group: "apps", Version: "v1", Resource: "statefulsets"},
	"apps/v1|DaemonSet":                   {Group: "apps", Version: "v1", Resource: "daemonsets"},
	"v1|Service":                          {Group: "", Version: "v1", Resource: "services"},
	"v1|ConfigMap":                        {Group: "", Version: "v1", Resource: "configmaps"},
	"v1|PersistentVolumeClaim":            {Group: "", Version: "v1", Resource: "persistentvolumeclaims"},
	"batch/v1|Job":                        {Group: "batch", Version: "v1", Resource: "jobs"},
	"batch/v1|CronJob":                    {Group: "batch", Version: "v1", Resource: "cronjobs"},
	"networking.k8s.io/v1|Ingress":        {Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"},
}

func gvrForKind(apiVersion, kind string) (schema.GroupVersionResource, error) {
	if gvr, ok := knownGVRs[apiVersion+"|"+kind]; ok {
		return gvr, nil
	}
	return schema.GroupVersionResource{}, fmt.Errorf("kubernetes plugin: no known resource mapping for apiVersion=%q kind=%q", apiVersion, kind)
}

// decodeManifest parses raw - a single Kubernetes object or a
// "kind":"List" wrapper, exactly what
// internal/publicationtarget.KubernetesManifest produces - into the
// unstructured objects Apply applies one at a time.
func decodeManifest(raw []byte) ([]unstructured.Unstructured, error) {
	var probe struct {
		Kind  string            `json:"kind"`
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("kubernetes plugin: decode manifest: %w", err)
	}
	if probe.Kind == "List" {
		objects := make([]unstructured.Unstructured, 0, len(probe.Items))
		for _, item := range probe.Items {
			var object unstructured.Unstructured
			if err := object.UnmarshalJSON(item); err != nil {
				return nil, fmt.Errorf("kubernetes plugin: decode list item: %w", err)
			}
			objects = append(objects, object)
		}
		return objects, nil
	}
	var object unstructured.Unstructured
	if err := object.UnmarshalJSON(raw); err != nil {
		return nil, fmt.Errorf("kubernetes plugin: decode manifest: %w", err)
	}
	return []unstructured.Unstructured{object}, nil
}

// Apply applies raw to the cluster via server-side apply
// (types.ApplyPatchType, force-owned by FieldManager - the same
// conflict-resolution kubectl apply --server-side --force-conflicts
// uses), through the dynamic client rather than a typed clientset per
// kind: the manifest already arrives as generic JSON spanning multiple
// resource kinds, and applying it as unstructured.Unstructured through
// one dynamic client lets every kind share the same code path instead of
// needing a typed client and a type switch per kind. Returns
// "kind/namespace/name" for every object actually applied, in manifest
// order; a failure partway through returns that partial list alongside
// the error so a caller can report exactly how far the apply got.
func (c *Client) Apply(ctx context.Context, raw []byte) ([]string, error) {
	objects, err := decodeManifest(raw)
	if err != nil {
		return nil, err
	}
	applied := make([]string, 0, len(objects))
	for _, object := range objects {
		gvr, err := gvrForKind(object.GetAPIVersion(), object.GetKind())
		if err != nil {
			return applied, err
		}
		data, err := json.Marshal(object.Object)
		if err != nil {
			return applied, fmt.Errorf("kubernetes plugin: encode %s/%s: %w", object.GetKind(), object.GetName(), err)
		}
		namespace := object.GetNamespace()
		resourceClient := c.Dynamic.Resource(gvr).Namespace(namespace)
		force := true
		_, applyErr := resourceClient.Patch(ctx, object.GetName(), types.ApplyPatchType, data,
			metav1.PatchOptions{FieldManager: FieldManager, Force: &force})
		if apierrors.IsNotFound(applyErr) {
			// Server-side apply against a real API server implicitly
			// creates the object the first time it's applied; a couple of
			// fake/older ObjectTracker-backed test doubles (this
			// package's own fake-clientset tests included - see
			// k8s.io/client-go's testing/fixture.go tracker.Apply, which
			// requires the object to already exist) don't extend that
			// courtesy, so fall back to a plain create for exactly that
			// case rather than failing an apply of a brand-new resource.
			_, applyErr = resourceClient.Create(ctx, &object, metav1.CreateOptions{FieldManager: FieldManager})
		}
		if applyErr != nil {
			return applied, fmt.Errorf("kubernetes plugin: apply %s %s/%s: %w", object.GetKind(), namespace, object.GetName(), applyErr)
		}
		applied = append(applied, fmt.Sprintf("%s/%s/%s", object.GetKind(), namespace, object.GetName()))
	}
	return applied, nil
}

// sleepOrDone waits for d or ctx's cancellation, whichever comes first -
// the polling loops' shared "don't ignore a caller-requested timeout/
// cancellation just because we're mid-poll" primitive.
func sleepOrDone(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// WaitForJobComplete polls Job namespace/name until its status reaches
// condition Complete, equivalent to `kubectl wait
// --for=condition=complete job/NAME --timeout T`. A Failed condition is
// reported immediately rather than waiting out the full timeout.
func (c *Client) WaitForJobComplete(ctx context.Context, namespace, name string, timeout time.Duration) (ready bool, output string, err error) {
	deadline := time.Now().Add(timeout)
	for {
		job, getErr := c.Clientset.BatchV1().Jobs(namespace).Get(ctx, name, metav1.GetOptions{})
		if getErr != nil {
			return false, "", fmt.Errorf("kubernetes plugin: get job %s/%s: %w", namespace, name, getErr)
		}
		for _, condition := range job.Status.Conditions {
			if condition.Type == batchv1.JobComplete && condition.Status == corev1.ConditionTrue {
				return true, fmt.Sprintf("job.batch/%s condition met", name), nil
			}
			if condition.Type == batchv1.JobFailed && condition.Status == corev1.ConditionTrue {
				return false, "", fmt.Errorf("kubernetes plugin: job %s/%s failed: %s", namespace, name, condition.Message)
			}
		}
		if time.Now().After(deadline) {
			return false, "", fmt.Errorf("kubernetes plugin: timed out waiting for job %s/%s to complete", namespace, name)
		}
		if err := sleepOrDone(ctx, pollInterval); err != nil {
			return false, "", fmt.Errorf("kubernetes plugin: wait for job %s/%s: %w", namespace, name, err)
		}
	}
}

// GetCronJobSummary renders a point-in-time summary of CronJob
// namespace/name equivalent to `kubectl get cronjob/NAME`'s own table
// output.
func (c *Client) GetCronJobSummary(ctx context.Context, namespace, name string) (string, error) {
	cronJob, err := c.Clientset.BatchV1().CronJobs(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("kubernetes plugin: get cronjob %s/%s: %w", namespace, name, err)
	}
	suspend := "False"
	if cronJob.Spec.Suspend != nil && *cronJob.Spec.Suspend {
		suspend = "True"
	}
	lastSchedule := "<none>"
	if cronJob.Status.LastScheduleTime != nil {
		lastSchedule = cronJob.Status.LastScheduleTime.Format(time.RFC3339)
	}
	return fmt.Sprintf("NAME\tSCHEDULE\tSUSPEND\tACTIVE\tLAST SCHEDULE\n%s\t%s\t%s\t%d\t%s\n",
		cronJob.Name, cronJob.Spec.Schedule, suspend, len(cronJob.Status.Active), lastSchedule), nil
}

// RolloutStatus polls resourceType ("deployment", "statefulset" or
// "daemonset") namespace/name until it is fully rolled out, equivalent
// to `kubectl rollout status RESOURCE/NAME --timeout T`. The "fully
// rolled out" condition is evaluated per kind by
// deploymentRolledOut/statefulSetRolledOut/daemonSetRolledOut, mirroring
// kubectl's own per-kind StatusViewer logic rather than a naive
// existence check.
func (c *Client) RolloutStatus(ctx context.Context, resourceType, namespace, name string, timeout time.Duration) (ready bool, output string, err error) {
	deadline := time.Now().Add(timeout)
	for {
		var done bool
		var message string
		switch resourceType {
		case "deployment":
			done, message, err = c.deploymentRolledOut(ctx, namespace, name)
		case "statefulset":
			done, message, err = c.statefulSetRolledOut(ctx, namespace, name)
		case "daemonset":
			done, message, err = c.daemonSetRolledOut(ctx, namespace, name)
		default:
			return false, "", fmt.Errorf("kubernetes plugin: unsupported rollout resource type %q", resourceType)
		}
		if err != nil {
			return false, "", err
		}
		if done {
			return true, message, nil
		}
		if time.Now().After(deadline) {
			return false, message, fmt.Errorf("kubernetes plugin: timed out waiting for %s %s/%s rollout: %s", resourceType, namespace, name, message)
		}
		if sleepErr := sleepOrDone(ctx, pollInterval); sleepErr != nil {
			return false, "", fmt.Errorf("kubernetes plugin: wait for %s %s/%s rollout: %w", resourceType, namespace, name, sleepErr)
		}
	}
}

// deploymentRolledOut mirrors kubectl's own DeploymentStatusViewer
// (k8s.io/kubectl/pkg/polymorphichelpers): observed generation caught
// up, no ProgressDeadlineExceeded, every replica updated, no old
// replicas left, every updated replica available.
func (c *Client) deploymentRolledOut(ctx context.Context, namespace, name string) (bool, string, error) {
	deployment, err := c.Clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return false, "", fmt.Errorf("kubernetes plugin: get deployment %s/%s: %w", namespace, name, err)
	}
	if deployment.Generation > deployment.Status.ObservedGeneration {
		return false, "Waiting for deployment spec update to be observed", nil
	}
	for _, condition := range deployment.Status.Conditions {
		if condition.Type == appsv1.DeploymentProgressing && condition.Reason == "ProgressDeadlineExceeded" {
			return false, "", fmt.Errorf("kubernetes plugin: deployment %s/%s exceeded its progress deadline", namespace, name)
		}
	}
	desired := int32(1)
	if deployment.Spec.Replicas != nil {
		desired = *deployment.Spec.Replicas
	}
	if deployment.Status.UpdatedReplicas < desired {
		return false, fmt.Sprintf("Waiting for rollout: %d out of %d new replicas have been updated", deployment.Status.UpdatedReplicas, desired), nil
	}
	if deployment.Status.Replicas > deployment.Status.UpdatedReplicas {
		return false, fmt.Sprintf("Waiting for rollout: %d old replicas are pending termination", deployment.Status.Replicas-deployment.Status.UpdatedReplicas), nil
	}
	if deployment.Status.AvailableReplicas < deployment.Status.UpdatedReplicas {
		return false, fmt.Sprintf("Waiting for rollout: %d of %d updated replicas are available", deployment.Status.AvailableReplicas, deployment.Status.UpdatedReplicas), nil
	}
	return true, fmt.Sprintf("deployment %q successfully rolled out", name), nil
}

// statefulSetRolledOut mirrors kubectl's StatefulSetStatusViewer for the
// common (non-OnDelete) case: observed generation caught up, every
// replica ready, every replica at or above the update partition
// updated, and current/update revision converged.
func (c *Client) statefulSetRolledOut(ctx context.Context, namespace, name string) (bool, string, error) {
	sts, err := c.Clientset.AppsV1().StatefulSets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return false, "", fmt.Errorf("kubernetes plugin: get statefulset %s/%s: %w", namespace, name, err)
	}
	if sts.Spec.UpdateStrategy.Type != "" && sts.Spec.UpdateStrategy.Type != appsv1.RollingUpdateStatefulSetStrategyType {
		return true, fmt.Sprintf("statefulset rolling update complete %d pods at revision %s", sts.Status.CurrentReplicas, sts.Status.CurrentRevision), nil
	}
	if sts.Status.ObservedGeneration == 0 || sts.Generation > sts.Status.ObservedGeneration {
		return false, "Waiting for statefulset spec update to be observed", nil
	}
	desired := int32(1)
	if sts.Spec.Replicas != nil {
		desired = *sts.Spec.Replicas
	}
	if sts.Status.ReadyReplicas < desired {
		return false, fmt.Sprintf("Waiting for %d pods to be ready", desired-sts.Status.ReadyReplicas), nil
	}
	partition := int32(0)
	if sts.Spec.UpdateStrategy.RollingUpdate != nil && sts.Spec.UpdateStrategy.RollingUpdate.Partition != nil {
		partition = *sts.Spec.UpdateStrategy.RollingUpdate.Partition
	}
	if sts.Status.UpdatedReplicas < desired-partition {
		return false, fmt.Sprintf("Waiting for partitioned roll out to finish: %d out of %d new pods have been updated", sts.Status.UpdatedReplicas, desired-partition), nil
	}
	if sts.Status.CurrentRevision != sts.Status.UpdateRevision {
		return false, fmt.Sprintf("waiting for statefulset rolling update to complete %d pods at revision %s", sts.Status.UpdatedReplicas, sts.Status.UpdateRevision), nil
	}
	return true, fmt.Sprintf("statefulset rolling update complete %d pods at revision %s", sts.Status.CurrentReplicas, sts.Status.CurrentRevision), nil
}

// daemonSetRolledOut mirrors kubectl's DaemonSetStatusViewer: observed
// generation caught up, every desired pod updated, every desired pod
// available.
func (c *Client) daemonSetRolledOut(ctx context.Context, namespace, name string) (bool, string, error) {
	ds, err := c.Clientset.AppsV1().DaemonSets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return false, "", fmt.Errorf("kubernetes plugin: get daemonset %s/%s: %w", namespace, name, err)
	}
	if ds.Spec.UpdateStrategy.Type != "" && ds.Spec.UpdateStrategy.Type != appsv1.RollingUpdateDaemonSetStrategyType {
		return true, fmt.Sprintf("daemon set %q successfully rolled out", name), nil
	}
	if ds.Generation > ds.Status.ObservedGeneration {
		return false, "Waiting for daemon set spec update to be observed", nil
	}
	if ds.Status.UpdatedNumberScheduled < ds.Status.DesiredNumberScheduled {
		return false, fmt.Sprintf("Waiting for daemon set %q rollout to finish: %d out of %d new pods have been updated",
			name, ds.Status.UpdatedNumberScheduled, ds.Status.DesiredNumberScheduled), nil
	}
	if ds.Status.NumberAvailable < ds.Status.DesiredNumberScheduled {
		return false, fmt.Sprintf("Waiting for daemon set %q rollout to finish: %d of %d updated pods are available",
			name, ds.Status.NumberAvailable, ds.Status.DesiredNumberScheduled), nil
	}
	return true, fmt.Sprintf("daemon set %q successfully rolled out", name), nil
}

// revisionAnnotation is the annotation the Deployment controller stamps
// on every ReplicaSet it owns with that ReplicaSet's revision number -
// the same annotation `kubectl rollout undo`/`kubectl rollout history`
// read.
const revisionAnnotation = "deployment.kubernetes.io/revision"

func revisionOf(rs *appsv1.ReplicaSet) int {
	value, err := strconv.Atoi(rs.Annotations[revisionAnnotation])
	if err != nil {
		return 0
	}
	return value
}

// ownedReplicaSets returns all of replicaSets that deployment's own
// OwnerReferences claims - the label selector alone is not sufficient
// (a selector can be shared or coincidentally match unrelated
// ReplicaSets), so an explicit owner-UID check is required for this to
// be genuinely "this Deployment's own revision history" rather than an
// approximation.
func ownedReplicaSets(deployment *appsv1.Deployment, replicaSets []appsv1.ReplicaSet) []*appsv1.ReplicaSet {
	owned := make([]*appsv1.ReplicaSet, 0, len(replicaSets))
	for i := range replicaSets {
		rs := &replicaSets[i]
		for _, ref := range rs.OwnerReferences {
			if ref.UID == deployment.UID && ref.Kind == "Deployment" {
				owned = append(owned, rs)
				break
			}
		}
	}
	return owned
}

// RolloutUndo rolls Deployment namespace/name back to toRevision (0
// selects the previous revision), equivalent to `kubectl rollout undo
// deployment/NAME [--to-revision=N]`. This is genuinely what kubectl
// itself does internally (see k8s.io/kubectl/pkg/polymorphichelpers/
// rollback.go's DeploymentRollbacker): list the ReplicaSets this
// Deployment owns, read each one's deployment.kubernetes.io/revision
// annotation, pick the target revision's ReplicaSet, and patch the
// Deployment's pod template spec to match that ReplicaSet's - not an
// approximation of rollback, the same mechanism.
func (c *Client) RolloutUndo(ctx context.Context, namespace, name string, toRevision int) (appliedRevision int, err error) {
	deployment, err := c.Clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return 0, fmt.Errorf("kubernetes plugin: get deployment %s/%s: %w", namespace, name, err)
	}
	selector, err := metav1.LabelSelectorAsSelector(deployment.Spec.Selector)
	if err != nil {
		return 0, fmt.Errorf("kubernetes plugin: deployment %s/%s selector: %w", namespace, name, err)
	}
	replicaSets, err := c.Clientset.AppsV1().ReplicaSets(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
	if err != nil {
		return 0, fmt.Errorf("kubernetes plugin: list replicasets for deployment %s/%s: %w", namespace, name, err)
	}
	owned := ownedReplicaSets(deployment, replicaSets.Items)
	if len(owned) == 0 {
		return 0, fmt.Errorf("kubernetes plugin: deployment %s/%s has no revision history to roll back to", namespace, name)
	}
	sort.Slice(owned, func(i, j int) bool { return revisionOf(owned[i]) < revisionOf(owned[j]) })
	currentRevision := revisionOf(owned[len(owned)-1])

	var target *appsv1.ReplicaSet
	if toRevision == 0 {
		for i := len(owned) - 1; i >= 0; i-- {
			if revisionOf(owned[i]) < currentRevision {
				target = owned[i]
				break
			}
		}
	} else {
		for _, rs := range owned {
			if revisionOf(rs) == toRevision {
				target = rs
				break
			}
		}
	}
	if target == nil {
		return 0, fmt.Errorf("kubernetes plugin: no revision history found for deployment %s/%s to roll back to", namespace, name)
	}
	targetRevision := revisionOf(target)
	if targetRevision == currentRevision {
		return 0, fmt.Errorf("kubernetes plugin: deployment %s/%s is already at revision %d", namespace, name, targetRevision)
	}

	err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		current, getErr := c.Clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if getErr != nil {
			return getErr
		}
		current.Spec.Template = target.Spec.Template
		if current.Annotations == nil {
			current.Annotations = map[string]string{}
		}
		current.Annotations["kubernetes.io/change-cause"] = fmt.Sprintf("platform-factory rollback to revision %d", targetRevision)
		_, updateErr := c.Clientset.AppsV1().Deployments(namespace).Update(ctx, current, metav1.UpdateOptions{FieldManager: FieldManager})
		return updateErr
	})
	if err != nil {
		return 0, fmt.Errorf("kubernetes plugin: roll back deployment %s/%s: %w", namespace, name, err)
	}
	return targetRevision, nil
}

// podLabelSelector is the label every pod internal/publicationtarget's
// manifest builders attach carries: "app.kubernetes.io/name": s.Name,
// on every workload kind (Deployment/Job/StatefulSet/DaemonSet/CronJob
// alike). Logs/Events use it directly rather than first fetching the
// owning workload to read its own selector, since every workload this
// plugin's caller ever generates already uses this exact convention.
func podLabelSelector(name string) string { return "app.kubernetes.io/name=" + name }

// Logs returns namespace/name's pod logs (every pod matching the
// standard app.kubernetes.io/name label), tailing the last tail lines
// when tail > 0. follow streams until the source closes or ctx is
// canceled/times out; because this returns one accumulated string
// rather than a live stream, following does not print incrementally to
// a terminal the way `kubectl logs --follow` does - see the
// accompanying report's honest accounting of that limitation.
func (c *Client) Logs(ctx context.Context, namespace, name string, tail int, follow bool) (string, error) {
	pods, err := c.Clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: podLabelSelector(name)})
	if err != nil {
		return "", fmt.Errorf("kubernetes plugin: list pods for %s/%s: %w", namespace, name, err)
	}
	if len(pods.Items) == 0 {
		return "", fmt.Errorf("kubernetes plugin: no pods found for %s/%s", namespace, name)
	}
	var combined strings.Builder
	for _, pod := range pods.Items {
		options := &corev1.PodLogOptions{Follow: follow}
		if tail > 0 {
			tailLines := int64(tail)
			options.TailLines = &tailLines
		}
		stream, streamErr := c.Clientset.CoreV1().Pods(namespace).GetLogs(pod.Name, options).Stream(ctx)
		if streamErr != nil {
			return combined.String(), fmt.Errorf("kubernetes plugin: stream logs for pod %s/%s: %w", namespace, pod.Name, streamErr)
		}
		fmt.Fprintf(&combined, "== %s ==\n", pod.Name)
		_, copyErr := io.Copy(&combined, stream)
		_ = stream.Close()
		if copyErr != nil {
			return combined.String(), fmt.Errorf("kubernetes plugin: read logs for pod %s/%s: %w", namespace, pod.Name, copyErr)
		}
	}
	return combined.String(), nil
}

// Events renders namespace's events involving name, oldest first (the
// same order kubectl get events --sort-by=.lastTimestamp uses).
func (c *Client) Events(ctx context.Context, namespace, name string) (string, error) {
	events, err := c.Clientset.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{FieldSelector: "involvedObject.name=" + name})
	if err != nil {
		return "", fmt.Errorf("kubernetes plugin: list events for %s/%s: %w", namespace, name, err)
	}
	sort.Slice(events.Items, func(i, j int) bool {
		return events.Items[i].LastTimestamp.Before(&events.Items[j].LastTimestamp)
	})
	var out strings.Builder
	fmt.Fprintln(&out, "LAST SEEN\tTYPE\tREASON\tOBJECT\tMESSAGE")
	for _, event := range events.Items {
		fmt.Fprintf(&out, "%s\t%s\t%s\t%s/%s\t%s\n",
			event.LastTimestamp.Format(time.RFC3339), event.Type, event.Reason,
			event.InvolvedObject.Kind, event.InvolvedObject.Name, event.Message)
	}
	return out.String(), nil
}
