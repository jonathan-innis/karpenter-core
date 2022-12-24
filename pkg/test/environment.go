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

package test

import (
	"context"
	"os"
	"strings"

	"github.com/samber/lo"
	v1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/version"
	"k8s.io/client-go/kubernetes"
	"knative.dev/pkg/system"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/aws/karpenter-core/pkg/utils/env"
)

type Environment struct {
	envtest.Environment
	Client              client.Client
	KubernetesInterface kubernetes.Interface
	Version             *version.Version
	Done                chan struct{}
	Cancel              context.CancelFunc
}

func NewEnvironment(scheme *runtime.Scheme, crds ...*v1.CustomResourceDefinition) *Environment {
	ctx, cancel := context.WithCancel(context.Background())

	os.Setenv(system.NamespaceEnvKey, "default")
	version := version.MustParseSemantic(strings.Replace(env.WithDefaultString("K8S_VERSION", "1.21.x"), ".x", ".0", -1))
	environment := envtest.Environment{Scheme: scheme, CRDs: crds}
	if version.Minor() >= 21 {
		// PodAffinityNamespaceSelector is used for label selectors in pod affinities.  If the feature-gate is turned off,
		// the api-server just clears out the label selector so we never see it.  If we turn it on, the label selectors
		// are passed to us and we handle them. This feature is alpha in v1.21, beta in v1.22 and will be GA in 1.24. See
		// https://github.com/kubernetes/enhancements/issues/2249 for more info.
		environment.ControlPlane.GetAPIServer().Configure().Set("feature-gates", "PodAffinityNamespaceSelector=true")
	}

	_ = lo.Must(environment.Start())

	cache := lo.Must(cache.New(environment.Config, cache.Options{Scheme: scheme}))
	//lo.Must0(cache.IndexField(ctx, &corev1.Pod{}, "spec.nodeName", func(o client.Object) []string {
	//	return []string{o.(*corev1.Pod).Spec.NodeName}
	//}), "failed to setup nodeName pod indexer")
	//lo.Must0(cache.IndexField(ctx, &corev1.Node{}, "spec.providerID", func(o client.Object) []string {
	//	return []string{o.(*corev1.Node).Spec.ProviderID}
	//}), "failed to setup providerID node indexer")
	//lo.Must0(cache.IndexField(ctx, &v1alpha1.Machine{}, "status.providerID", func(o client.Object) []string {
	//	return []string{o.(*v1alpha1.Machine).Status.ProviderID}
	//}), "failed to setup providerID machine indexer")

	client := lo.Must(client.New(environment.Config, client.Options{Scheme: environment.Scheme}))

	go func() {
		lo.Must0(cache.Start(ctx))
	}()
	return &Environment{
		Environment:         environment,
		Client:              ,
		KubernetesInterface: kubernetes.NewForConfigOrDie(environment.Config),
		Version:             version,
		Done:                make(chan struct{}),
		Cancel:              cancel,
	}
}

func (e *Environment) Stop() error {
	close(e.Done)
	e.Cancel()
	return e.Environment.Stop()
}
