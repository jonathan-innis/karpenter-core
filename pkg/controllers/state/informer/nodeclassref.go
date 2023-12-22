/*
Copyright 2023 The Kubernetes Authors.

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

package informer

import (
	"context"
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"knative.dev/pkg/logging"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/karpenter/pkg/apis/v1beta1"
	"sigs.k8s.io/karpenter/pkg/controllers/state"
	operatorcontroller "sigs.k8s.io/karpenter/pkg/operator/controller"
)

var (
	informerStoreSize = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "karpenter",
			Subsystem: "state",
			Name:      "nodeclassref_informer_store_size",
			Help:      "Size of the NodeClassRef informer store.",
		},
	)
	trackedGVRSize = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "karpenter",
			Subsystem: "state",
			Name:      "nodeclassref_tracked_gvr_store_size",
			Help:      "Size of the NodeClassRef tracked GVR store.",
		},
	)
)

func init() {
	crmetrics.Registry.MustRegister(informerStoreSize, trackedGVRSize)
}

type informerData struct {
	Informer cache.SharedIndexInformer
	Cancel   context.CancelFunc
}

// NodeClassRefController is a controller informer that watches NodePools and informs
type NodeClassRefController struct {
	kubeClient      client.Client
	informerFactory dynamicinformer.DynamicSharedInformerFactory
	informerStore   map[schema.GroupVersionResource]informerData
	trackedGVRs     map[types.NamespacedName]schema.GroupVersionResource
}

func NewNodeClassRefController(config *rest.Config, kubeClient client.Client) *NodeClassRefController {
	return &NodeClassRefController{
		kubeClient:      kubeClient,
		informerFactory: dynamicinformer.NewFilteredDynamicSharedInformerFactory(dynamic.NewForConfigOrDie(config), time.Hour*12, corev1.NamespaceAll, nil),
		informerStore:   map[schema.GroupVersionResource]informerData{},
		trackedGVRs:     map[types.NamespacedName]schema.GroupVersionResource{},
	}
}

func (c *NodeClassRefController) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	// Update our metrics for our store sizes at the end of each reconcile
	defer func() {
		informerStoreSize.Set(float64(len(c.informerStore)))
		trackedGVRSize.Set(float64(len(c.trackedGVRs)))
	}()
	nodePool := &v1beta1.NodePool{}
	if err := c.kubeClient.Get(ctx, req.NamespacedName, nodePool); err != nil {
		if errors.IsNotFound(err) {
			if gvr, ok := c.trackedGVRs[req.NamespacedName]; ok {
				c.cleanupInformerOnGVR(gvr)
				delete(c.trackedGVRs, req.NamespacedName)
			}
		}
		return reconcile.Result{}, err
	}
	if nodePool.Spec.Template.Spec.NodeClassRef == nil {
		return reconcile.Result{}, nil
	}
	gv, err := schema.ParseGroupVersion(nodePool.Spec.Template.Spec.NodeClassRef.APIVersion)
	if err != nil {
		logging.FromContext(ctx).Errorf("parsing group version, %v", err)
		return reconcile.Result{}, nil
	}
	// Make sure that we have a valid Group and Version here
	if gv.Group == "" || gv.Version == "" {
		return reconcile.Result{}, nil
	}
	restMapping, err := c.kubeClient.RESTMapper().RESTMapping(schema.GroupKind{Group: gv.Group, Kind: nodePool.Spec.Template.Spec.NodeClassRef.Kind})
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("finding REST mapping, %w", err)
	}
	// If the rest mapping has changed for this NodePool, we need to cleanup the old tracking
	if c.trackedGVRs[req.NamespacedName] != restMapping.Resource {
		c.cleanupInformerOnGVR(c.trackedGVRs[req.NamespacedName])
	}
	c.trackedGVRs[req.NamespacedName] = restMapping.Resource
	if _, ok := c.informerStore[restMapping.Resource]; ok {
		return reconcile.Result{}, nil
	}
	// Create the informer for this GVR if this is the first time that we have seen this GVR for NodePools
	informer := c.informerFactory.ForResource(restMapping.Resource).Informer()
	if _, err = informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			state.NodeClassEventChannel <- event.GenericEvent{Object: obj.(*unstructured.Unstructured)}
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			state.NodeClassEventChannel <- event.GenericEvent{Object: newObj.(*unstructured.Unstructured)}
		},
		DeleteFunc: func(obj interface{}) {
			state.NodeClassEventChannel <- event.GenericEvent{Object: obj.(*unstructured.Unstructured)}
		},
	}); err != nil {
		return reconcile.Result{}, fmt.Errorf("adding event handler to informer, %w", err)
	}
	informerCtx, informerCancel := context.WithCancel(ctx)
	c.informerStore[restMapping.Resource] = informerData{Informer: informer, Cancel: informerCancel}
	// Initialize the informer
	// This goroutine won't leak since we are tracking it and cancelling it through our store mechanism
	// And the entire factory (including the informers spawned off of the factory) will cancel when the top-level reconcile
	// context cancels due to process shutdown
	go informer.Run(informerCtx.Done())
	return reconcile.Result{}, nil
}

// cleanupInformerOnGVR looks at all the keys that we are storing here and checks the ref-count
// for the number of keys that are referencing that GVR. If this element is the last one that is referencing this
// GVR, then we can dynamically cancel the informer
func (c *NodeClassRefController) cleanupInformerOnGVR(gvr schema.GroupVersionResource) {
	// Cleanup the informer watch if this is the last NodePool we've stored tracking this GVR
	refCount := 0
	for _, v := range c.trackedGVRs {
		if v == gvr {
			refCount++
		}
	}
	if refCount == 1 {
		c.informerStore[gvr].Cancel()
		delete(c.informerStore, gvr)
	}
}

func (c *NodeClassRefController) Name() string {
	return "state.nodeclassref"
}

func (c *NodeClassRefController) Builder(ctx context.Context, m manager.Manager) operatorcontroller.Builder {
	// Start the informer factory at the same time that we are building the controller
	c.informerFactory.Start(ctx.Done())
	return operatorcontroller.Adapt(controllerruntime.
		NewControllerManagedBy(m).
		For(&v1beta1.NodePool{}),
	)
}
