/*
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package terminator

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/util/workqueue"
	"knative.dev/pkg/logging"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	terminatorevents "github.com/aws/karpenter-core/pkg/controllers/node/termination/terminator/events"
	"github.com/aws/karpenter-core/pkg/operator/controller"

	"github.com/aws/karpenter-core/pkg/events"
)

const (
	evictionQueueBaseDelay = 100 * time.Millisecond
	evictionQueueMaxDelay  = 10 * time.Second
)

type NodeDrainError struct {
	error
}

func NewNodeDrainError(err error) *NodeDrainError {
	return &NodeDrainError{error: err}
}

func IsNodeDrainError(err error) bool {
	if err == nil {
		return false
	}
	var nodeDrainErr *NodeDrainError
	return errors.As(err, &nodeDrainErr)
}

type QueueKey struct {
	types.NamespacedName

	NodeName string
}

type Queue struct {
	workqueue.RateLimitingInterface

	// evictionMapping is a mapping from the node name to the pods that require eviction
	// This is a map from NodeName -> sets.Set[QueueKey] to enable cleanup on the eviction queue when the node cleans up
	mu              sync.Mutex
	evictionMapping map[string]sets.Set[QueueKey]

	kubeClient client.Client
	recorder   events.Recorder
}

func NewQueue(kubeClient client.Client, recorder events.Recorder) *Queue {
	queue := &Queue{
		RateLimitingInterface: workqueue.NewRateLimitingQueue(workqueue.NewItemExponentialFailureRateLimiter(evictionQueueBaseDelay, evictionQueueMaxDelay)),
		evictionMapping:       map[string]sets.Set[QueueKey]{},
		kubeClient:            kubeClient,
		recorder:              recorder,
	}
	return queue
}

func (q *Queue) Name() string {
	return "eviction-queue"
}

func (q *Queue) Builder(_ context.Context, m manager.Manager) controller.Builder {
	return controller.NewSingletonManagedBy(m)
}

func (q *Queue) Has(pod *v1.Pod) bool {
	q.mu.Lock()
	defer q.mu.Unlock()

	pods, ok := q.evictionMapping[pod.Spec.NodeName]
	if !ok {
		return false
	}
	return pods.Has(QueueKey{NodeName: pod.Spec.NodeName, NamespacedName: client.ObjectKeyFromObject(pod)})
}

// Add adds pods to the Queue
func (q *Queue) Add(pods ...*v1.Pod) {
	if len(pods) == 0 {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()

	if _, ok := q.evictionMapping[pods[0].Spec.NodeName]; !ok {
		q.evictionMapping[pods[0].Spec.NodeName] = sets.New[QueueKey]()
	}
	for _, pod := range pods {
		qk := QueueKey{NamespacedName: client.ObjectKeyFromObject(pod), NodeName: pod.Spec.NodeName}
		if !q.evictionMapping[pod.Spec.NodeName].Has(qk) {
			q.evictionMapping[pod.Spec.NodeName].Insert(qk)
			q.RateLimitingInterface.Add(qk)
		}
	}
}

// ClearForNode removes all pods that were sitting on the eviction queue that were associated with a given nodeName
func (q *Queue) ClearForNode(nodeName string) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if _, ok := q.evictionMapping[nodeName]; !ok {
		return
	}
	for qk := range q.evictionMapping[nodeName].UnsortedList() {
		q.RateLimitingInterface.Forget(qk)
		q.RateLimitingInterface.Done(qk)
	}
	delete(q.evictionMapping, nodeName)
}

func (q *Queue) Reconcile(ctx context.Context, _ reconcile.Request) (reconcile.Result, error) {
	// Check if the queue is empty. client-go recommends not using this function to gate the subsequent
	// get call, but since we're popping items off the queue synchronously, there should be no synchonization
	// issues.
	if q.Len() == 0 {
		return reconcile.Result{RequeueAfter: 1 * time.Second}, nil
	}
	// Get pod from queue. This waits until queue is non-empty.
	item, shutdown := q.RateLimitingInterface.Get()
	if shutdown {
		return reconcile.Result{}, fmt.Errorf("EvictionQueue is broken and has shutdown")
	}
	key := item.(QueueKey)
	defer q.RateLimitingInterface.Done(key)
	// Evict pod
	if q.Evict(ctx, key) {
		q.mu.Lock()
		q.evictionMapping[key.NodeName].Delete(key)
		q.mu.Unlock()

		q.RateLimitingInterface.Forget(key)
		return reconcile.Result{RequeueAfter: controller.Immediately}, nil
	}
	// Requeue pod if eviction failed
	q.RateLimitingInterface.AddRateLimited(key)
	return reconcile.Result{RequeueAfter: controller.Immediately}, nil
}

// Evict returns true if successful eviction call, and false if not an eviction-related error
func (q *Queue) Evict(ctx context.Context, key QueueKey) bool {
	ctx = logging.WithLogger(ctx, logging.FromContext(ctx).With("pod", key.NamespacedName))
	if err := q.kubeClient.SubResource("eviction").Create(ctx, &v1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name}}, &policyv1.Eviction{}); err != nil {
		// status codes for the eviction API are defined here:
		// https://kubernetes.io/docs/concepts/scheduling-eviction/api-eviction/#how-api-initiated-eviction-works
		if apierrors.IsNotFound(err) { // 404
			return true
		}
		if apierrors.IsTooManyRequests(err) { // 429 - PDB violation
			q.recorder.Publish(terminatorevents.NodeFailedToDrain(&v1.Node{ObjectMeta: metav1.ObjectMeta{
				Name:      key.Name,
				Namespace: key.Namespace,
			}}, fmt.Errorf("evicting pod %s/%s violates a PDB", key.Namespace, key.Name)))
			return false
		}
		logging.FromContext(ctx).Errorf("evicting pod, %s", err)
		return false
	}
	q.recorder.Publish(terminatorevents.EvictPod(&v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}}))
	return true
}

func (q *Queue) Reset() {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.RateLimitingInterface = workqueue.NewRateLimitingQueue(workqueue.NewItemExponentialFailureRateLimiter(evictionQueueBaseDelay, evictionQueueMaxDelay))
	q.evictionMapping = map[string]sets.Set[QueueKey]{}
}
