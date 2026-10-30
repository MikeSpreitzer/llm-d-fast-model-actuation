package launcher_populator

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	k8sapiextclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	kubeinformers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	"k8s.io/kubernetes/cmd/kube-apiserver/app/options"
	"k8s.io/kubernetes/test/integration/framework"
	"k8s.io/kubernetes/test/utils/ktesting"

	fmav1alpha1 "github.com/llm-d-incubation/llm-d-fast-model-actuation/api/fma/v1alpha1"
	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/controller/common"
	launcherpopulator "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/controller/launcher-populator"
	lpc "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/controller/launcher-populator"
	"github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/controller/utils"
	fmaclient "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/generated/clientset/versioned"
	fmainformers "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/generated/informers/externalversions"
)

const (
	stuckTestSchedThreshold = 2 * time.Minute
	stuckTestStartThreshold = 5 * time.Minute
	stuckTestNamespace      = "default"
	stuckTestNode           = "node-a"
	stuckTestLauncherConfig = "lc-a"
	stuckTestLauncherPopPol = "lpp-a"
)

var errNotQuiet = errors.New("controller did not quiesce in time")

func setup(t testing.TB) (context.Context, *rest.Config, framework.TearDownFunc) {
	tCtx := ktesting.Init(t)

	_, kubeConfig, tearDownFn := framework.StartTestServer(tCtx, t,
		framework.TestServerSetup{
			ModifyServerRunOptions: func(opts *options.ServerRunOptions) {
				// Ensure all clients are allowed to send requests.
				opts.Authorization.Modes = []string{"AlwaysAllow"}
				opts.GenericServerRunOptions.MaxRequestsInFlight = 100
				opts.GenericServerRunOptions.MaxMutatingRequestsInFlight = 100
			},
		})

	newTeardown := func() {
		tCtx.Cancel("tearing down apiserver")
		tearDownFn()
	}
	return tCtx, kubeConfig, newTeardown
}

func TestReconcileNodeAvailability(t *testing.T) {
	ctx, restConfig, closeFn := setup(t)
	defer closeFn()

	logger := klog.FromContext(ctx)
	kubeClient := kubernetes.NewForConfigOrDie(restConfig)
	kubePreInformers := kubeinformers.NewSharedInformerFactoryWithOptions(kubeClient, 0, kubeinformers.WithNamespace(stuckTestNamespace))
	fmaClient := fmaclient.NewForConfigOrDie(restConfig)
	fmaPreInformers := fmainformers.NewSharedInformerFactoryWithOptions(fmaClient, 0, fmainformers.WithNamespace(stuckTestNamespace))
	k8sapiextClient := k8sapiextclient.NewForConfigOrDie(restConfig)
	err := Prep(ctx, kubeClient, k8sapiextClient)
	if err != nil {
		t.Fatalf("Failed to prepare: %v", err)
		return
	}
	for _, nodeState := range []string{"missing", "deleting"} {
		for _, templateState := range []string{"current", "stale", "no-pods"} {
			for _, desiredCount := range []int{0, 2} {
				t.Run(fmt.Sprintf("%s/%s/desired-%d", nodeState, templateState, desiredCount), func(t *testing.T) {

					kubeClient.CoreV1().Nodes().DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{})
					fmaClient.FmaV1alpha1().LauncherConfigs(stuckTestNamespace).DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{})
					fmaClient.FmaV1alpha1().LauncherPopulationPolicies(stuckTestNamespace).DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{})

					t.Cleanup(func() {
						err := kubeClient.CoreV1().Pods(stuckTestNamespace).DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{})
						if err != nil {
							t.Logf("Failed to request deletion of all Pods: %v", err)
						}
					})
					lc, lpp, node, err := createStuckTestSetupObjects(ctx, kubeClient, fmaClient)
					if err != nil {

					}
					//var orphanPod, boundPod *corev1.Pod
					if templateState != "no-pods" {
						_, _, err = createStuckTestPods(ctx, kubeClient, true, lc, lpp, node)
						if err != nil {
							t.Fatalf("Failed to create test Pods: %v", err)
							return
						}
					}
					ctl, err := launcherpopulator.NewController(
						logger,
						kubeClient.CoreV1(),
						fmaClient.FmaV1alpha1(),
						stuckTestNamespace,
						kubePreInformers.Core().V1(),
						fmaPreInformers,
						launcherpopulator.DefaultExpectationTimeout,
						launcherpopulator.DefaultStuckSchedulingThreshold,
						launcherpopulator.DefaultStuckStartingThreshold,
					)
					if err != nil {
						t.Fatalf("Failed to create controller: %v", err)
						return
					}
					// Start informers
					kubePreInformers.Start(ctx.Done())
					fmaPreInformers.Start(ctx.Done())

					if err = ctl.Start(ctx); err != nil {
						t.Fatalf("Failed to start controller: %v", err)
						return
					}

					if !runUntilQuiet(t, ctl, time.Second*5, time.Minute) {
						return
					}

					ctx := t.Context()

					if nodeState == "deleting" {
						node = node.DeepCopy()
						deleting := metav1.NewTime(time.Now().Add(time.Second * 30))
						node.DeletionTimestamp = &deleting
						kubeClient.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
					} else {
						kubeClient.CoreV1().Nodes().Delete(ctx, node.Name, *metav1.NewDeleteOptions(0))
					}

					if !runUntilQuiet(t, ctl, time.Second*5, time.Minute) {
						return
					}

					if podExists(t, kubeClient, "orphan") {
						t.Error("unbound launcher remains on unavailable Node")
					}
					if !podExists(t, kubeClient, "bound") {
						t.Error("bound launcher must be left for the dual-pods controller")
					}
				})
			}
		}
	}
}

func createStuckTestSetupObjects(ctx context.Context, kubeClient kubernetes.Interface, fmaClient fmaclient.Interface) (lc *fmav1alpha1.LauncherConfig, lpp *fmav1alpha1.LauncherPopulationPolicy, node *corev1.Node, err error) {
	lc = &fmav1alpha1.LauncherConfig{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: stuckTestNamespace,
			Name:      stuckTestLauncherConfig,
		},
		Spec: fmav1alpha1.LauncherConfigSpec{
			MaxInstances: 2,
		},
	}
	lc, err = fmaClient.FmaV1alpha1().LauncherConfigs(stuckTestNamespace).Create(ctx, lc, metav1.CreateOptions{})
	if err != nil {
		return
	}
	lpp = &fmav1alpha1.LauncherPopulationPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: stuckTestNamespace,
			Name:      stuckTestLauncherPopPol,
		},
		Spec: fmav1alpha1.LauncherPopulationPolicySpec{
			EnhancedNodeSelector: fmav1alpha1.EnhancedNodeSelector{
				LabelSelector: metav1.LabelSelector{
					MatchLabels: map[string]string{
						"kubernetes.io/hostname": stuckTestNode,
					},
				},
			},
			CountForLauncher: []fmav1alpha1.CountForLauncher{{
				LauncherConfigName: stuckTestLauncherConfig,
				LauncherCount:      1,
			}},
		},
	}
	lpp, err = fmaClient.FmaV1alpha1().LauncherPopulationPolicies(stuckTestNamespace).Create(ctx, lpp, metav1.CreateOptions{})
	if err != nil {
		return
	}
	node = &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Labels: map[string]string{
			"kubernetes.io/hostname": stuckTestNode},
		Name: stuckTestNode}}
	node, err = kubeClient.CoreV1().Nodes().Create(ctx, node, metav1.CreateOptions{})
	return
}

func createStuckTestPods(ctx context.Context, kubeClient kubernetes.Interface, podsRunning bool, lc *fmav1alpha1.LauncherConfig, lpp *fmav1alpha1.LauncherPopulationPolicy, node *corev1.Node) (orphanPod, boundPod *corev1.Pod, err error) {
	var templateHash string
	_, templateHash, err = utils.BuildNodeIndependentLauncherTemplate(lc)
	if err != nil {
		return
	}
	orphanPod = orphanLifecyclePod("orphan", templateHash, podsRunning)
	boundPod = orphanLifecyclePod("bound", templateHash, podsRunning)
	orphanPod, err = kubeClient.CoreV1().Pods(stuckTestNamespace).Create(ctx, orphanPod, metav1.CreateOptions{})
	if err != nil {
		return
	}
	boundPod, err = kubeClient.CoreV1().Pods(stuckTestNamespace).Create(ctx, boundPod, metav1.CreateOptions{})
	if err != nil {
		return
	}
	return
}

func orphanLifecyclePod(name, templateHash string, running bool) *corev1.Pod {
	pod := stuckLauncherPod(name, templateHash)
	pod.Labels[common.NodeNameLabelKey] = stuckTestNode
	pod.Labels[common.ComponentLabelKey] = common.LauncherComponentLabelValue
	pod.Annotations[common.LauncherTemplateHashAnnotationKey] = templateHash
	pod.Spec.NodeSelector = map[string]string{"kubernetes.io/hostname": stuckTestNode}
	if running {
		ready(pod)
		pod.Status.Phase = corev1.PodRunning
	}
	if name == "bound" {
		pod.Annotations[common.RequesterAnnotationKey] = "requester-uid requester"
	}
	return pod
}

// stuckLauncherPod builds a launcher Pod in the current namespace with the
// current template hash.
func stuckLauncherPod(name, templateHash string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   stuckTestNamespace,
			UID:         types.UID(name + "-uid"),
			Labels:      map[string]string{common.LauncherConfigNameLabelKey: stuckTestLauncherConfig},
			Annotations: map[string]string{common.LauncherTemplateHashAnnotationKey: templateHash},
		},
	}
}

// ready marks the Pod Ready.
func ready(p *corev1.Pod) *corev1.Pod {
	p.Status.Conditions = append(p.Status.Conditions, corev1.PodCondition{
		Type:   corev1.PodReady,
		Status: corev1.ConditionTrue,
	})
	return p
}

func runUntilQuiet(t *testing.T, ctl lpc.Controller, idleTime, timeout time.Duration) bool {
	ctxTimed, cancel := context.WithTimeoutCause(t.Context(), timeout, errNotQuiet)
	defer cancel()
	lastDigesterStatus, lastKeyerStatus := ctl.GetStatus()
	err := wait.PollUntilContextCancel(ctxTimed, idleTime, false, func(ctx context.Context) (done bool, err error) {
		nextDigesterStatus, nextKeyerStatus := ctl.GetStatus()
		if lastDigesterStatus == nextDigesterStatus && lastKeyerStatus == nextKeyerStatus &&
			time.Since(lastDigesterStatus.ApproxLastChangeTime) >= idleTime &&
			time.Since(lastKeyerStatus.ApproxLastChangeTime) >= idleTime {
			return true, nil
		}
		lastDigesterStatus = nextDigesterStatus
		lastKeyerStatus = nextKeyerStatus
		return false, nil
	})
	if err != nil {
		t.Fatalf("controller did not quiesce in %v", timeout)
		return false
	}
	return true
}

func podExists(t *testing.T, cs kubernetes.Interface, name string) bool {
	t.Helper()
	_, err := cs.CoreV1().Pods(stuckTestNamespace).Get(t.Context(), name, metav1.GetOptions{})
	if err == nil {
		return true
	}
	if apierrors.IsNotFound(err) {
		return false
	}
	t.Fatalf("get pod %s: %v", name, err)
	return false
}
