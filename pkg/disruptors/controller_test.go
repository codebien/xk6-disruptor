package disruptors

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/google/go-cmp/cmp"
	"github.com/grafana/xk6-disruptor/pkg/kubernetes/helpers"
	"github.com/grafana/xk6-disruptor/pkg/testutils/kubernetes/builders"
)

func Test_InjectAgent(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		title     string
		namespace string
		pods      []corev1.Pod
		// Set timeout to -1 to prevent waiting the ephemeral container to be ready,
		// as the fake client will not update its status
		timeout     time.Duration
		expectError bool
	}{
		{
			title:     "Inject ephemeral container",
			namespace: "test-ns",
			pods: []corev1.Pod{
				builders.NewPodBuilder("pod1").
					WithNamespace("test-ns").
					WithIP("192.0.2.6").
					Build(),
				builders.NewPodBuilder("pod2").
					WithNamespace("test-ns").
					WithIP("192.0.2.6").
					Build(),
			},
			timeout:     -1,
			expectError: false,
		},
		{
			title:     "ephemeral container not ready",
			namespace: "test-ns",
			pods: []corev1.Pod{
				builders.NewPodBuilder("pod1").
					WithNamespace("test-ns").
					Build(),
				builders.NewPodBuilder("pod2").
					WithNamespace("test-ns").
					Build(),
			},
			timeout:     1,
			expectError: true, // should fail because fake client will not update status
		},
	}

	for _, tc := range testCases {
		tc := tc

		t.Run(tc.title, func(t *testing.T) {
			t.Parallel()

			objs := []runtime.Object{}
			for p := range tc.pods {
				objs = append(objs, &tc.pods[p])
			}

			client := fake.NewSimpleClientset(objs...)
			executor := helpers.NewFakePodCommandExecutor()
			helper := helpers.NewPodHelper(client, executor, tc.namespace)
			controller := NewAgentController(
				context.TODO(),
				helper,
				tc.namespace,
				tc.pods,
				tc.timeout,
			)

			err := controller.InjectDisruptorAgent(context.TODO())
			if tc.expectError && err == nil {
				t.Errorf("should had failed")
				return
			}

			if !tc.expectError && err != nil {
				t.Errorf("failed: %v", err)
				return
			}

			if tc.expectError && err != nil {
				return
			}

			for _, p := range tc.pods {
				pod, err := client.CoreV1().
					Pods(tc.namespace).
					Get(context.TODO(), p.Name, metav1.GetOptions{})
				if err != nil {
					t.Errorf("failed: %v", err)
					return
				}

				if len(pod.Spec.EphemeralContainers) == 0 {
					t.Errorf("agent container is not attached")
					return
				}
			}
		})
	}
}

type fakeVisitor struct {
	cmds VisitCommands
	err  error
}

func (v fakeVisitor) Visit(_ corev1.Pod) (VisitCommands, error) {
	return v.cmds, v.err
}

func Test_VisitPod(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		title       string
		namespace   string
		pods        []corev1.Pod
		visitCmds   VisitCommands
		err         error
		stdout      []byte
		stderr      []byte
		timeout     time.Duration
		expectError bool
		expected    []helpers.Command
	}{
		{
			title:     "successful execution",
			namespace: "test-ns",
			pods: []corev1.Pod{
				builders.NewPodBuilder("pod1").
					WithNamespace("test-ns").
					Build(),
				builders.NewPodBuilder("pod2").
					WithNamespace("test-ns").
					Build(),
			},
			visitCmds: VisitCommands{
				Exec:    []string{"command"},
				Cleanup: []string{"cleanup"},
			},
			err:         nil,
			expectError: false,
			expected: []helpers.Command{
				{Pod: "pod1", Container: "xk6-agent", Namespace: "test-ns", Command: []string{"command"}, Stdin: []byte{}},
				{Pod: "pod2", Container: "xk6-agent", Namespace: "test-ns", Command: []string{"command"}, Stdin: []byte{}},
			},
		},
		{
			title:     "failed execution",
			namespace: "test-ns",
			pods: []corev1.Pod{
				builders.NewPodBuilder("pod1").
					WithNamespace("test-ns").
					Build(),
				builders.NewPodBuilder("pod2").
					WithNamespace("test-ns").
					Build(),
			},
			visitCmds: VisitCommands{
				Exec:    []string{"command"},
				Cleanup: []string{"cleanup"},
			},
			err:         fmt.Errorf("fake error"),
			stderr:      []byte("error output"),
			expectError: true,
			expected: []helpers.Command{
				{Pod: "pod1", Container: "xk6-agent", Namespace: "test-ns", Command: []string{"command"}, Stdin: []byte{}},
				{Pod: "pod1", Container: "xk6-agent", Namespace: "test-ns", Command: []string{"cleanup"}, Stdin: []byte{}},
				{Pod: "pod2", Container: "xk6-agent", Namespace: "test-ns", Command: []string{"command"}, Stdin: []byte{}},
				{Pod: "pod2", Container: "xk6-agent", Namespace: "test-ns", Command: []string{"cleanup"}, Stdin: []byte{}},
			},
		},
	}

	for _, tc := range testCases {
		tc := tc

		t.Run(tc.title, func(t *testing.T) {
			t.Parallel()

			objs := []runtime.Object{}

			targets := []corev1.Pod{}
			for p := range tc.pods {
				objs = append(objs, &tc.pods[p])
				targets = append(targets, tc.pods[p])
			}
			client := fake.NewSimpleClientset(objs...)
			executor := helpers.NewFakePodCommandExecutor()
			helper := helpers.NewPodHelper(client, executor, tc.namespace)
			controller := NewAgentController(
				context.TODO(),
				helper,
				tc.namespace,
				targets,
				tc.timeout,
			)

			executor.SetResult(tc.stdout, tc.stderr, tc.err)
			visitor := fakeVisitor{
				cmds: tc.visitCmds,
			}
			err := controller.Visit(context.TODO(), visitor)
			if tc.expectError && err == nil {
				t.Fatalf("should had failed")
			}

			if !tc.expectError && err != nil {
				t.Fatalf("failed unexpectedly: %v", err)
			}

			if tc.expectError && err != nil {
				if !strings.Contains(err.Error(), string(tc.stderr)) {
					t.Fatalf("returned error message should contain stderr (%q)", string(tc.stderr))
				}
			}

			// At this point, we either expected no error and got no error, or we got the error we expected.
			// In either case, we check the expected commands have been executed.

			sort.Slice(tc.expected, func(i, j int) bool {
				return tc.expected[i].Pod < tc.expected[j].Pod
			})

			history := executor.GetHistory()

			sort.Slice(history, func(i, j int) bool {
				return history[i].Pod < history[j].Pod
			})

			if diff := cmp.Diff(tc.expected, history); diff != "" {
				t.Errorf("Expected command did not match returned:\n%s", diff)
			}
		})
	}
}
