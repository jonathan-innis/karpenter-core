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

package settings_test

import (
	"context"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	v1 "k8s.io/api/core/v1"
	. "knative.dev/pkg/logging/testing"

	. "github.com/aws/karpenter-core/pkg/test/expectations"

	"github.com/aws/karpenter-core/pkg/apis/config/settings"
)

var ctx context.Context

func TestAPIs(t *testing.T) {
	ctx = TestContextWithLogger(t)
	RegisterFailHandler(Fail)
	RunSpecs(t, "Settings")
}

var _ = Describe("Validation", func() {
	It("should succeed to set defaults", func() {
		cm := &v1.ConfigMap{
			Data: map[string]string{
				"clusterEndpoint": "https://00000000000000000000000.gr7.us-west-2.eks.amazonaws.com",
				"clusterName":     "my-cluster",
			},
		}
		s, _ := settings.NewSettingsFromConfigMap(cm)
		Expect(s.BatchMaxDuration.Duration).To(Equal(time.Second * 10))
		Expect(s.BatchIdleDuration.Duration).To(Equal(time.Second))
	})
	It("should succeed to set custom values", func() {
		cm := &v1.ConfigMap{
			Data: map[string]string{
				"batchMaxDuration":  "30s",
				"batchIdleDuration": "5s",
				"clusterEndpoint":   "https://00000000000000000000000.gr7.us-west-2.eks.amazonaws.com",
				"clusterName":       "my-cluster",
			},
		}
		s, _ := settings.NewSettingsFromConfigMap(cm)
		Expect(s.BatchMaxDuration.Duration).To(Equal(time.Second * 30))
		Expect(s.BatchIdleDuration.Duration).To(Equal(time.Second * 5))
	})
	It("should fail validation with panic when clusterName not included", func() {
		defer ExpectPanic()
		cm := &v1.ConfigMap{
			Data: map[string]string{
				"batchMaxDuration":  "15s",
				"batchIdleDuration": "5s",
				"clusterEndpoint":   "https://00000000000000000000000.gr7.us-west-2.eks.amazonaws.com",
			},
		}
		_, _ = settings.NewSettingsFromConfigMap(cm)
	})
	It("should fail validation with panic when clusterEndpoint not included", func() {
		defer ExpectPanic()
		cm := &v1.ConfigMap{
			Data: map[string]string{
				"batchMaxDuration":  "15s",
				"batchIdleDuration": "5s",
				"clusterName":       "my-name",
			},
		}
		_, _ = settings.NewSettingsFromConfigMap(cm)
	})
	It("should fail validation with panic when clusterEndpoint is invalid (not absolute)", func() {
		defer ExpectPanic()
		cm := &v1.ConfigMap{
			Data: map[string]string{
				"batchMaxDuration":  "15s",
				"batchIdleDuration": "5s",
				"clusterName":       "my-name",
				"clusterEndpoint":   "00000000000000000000000.gr7.us-west-2.eks.amazonaws.com",
			},
		}
		_, _ = settings.NewSettingsFromConfigMap(cm)
	})
})

var _ = Describe("Unmarshalling", func() {
	It("should succeed to unmarshal default data", func() {
		data := lo.Assign(settings.Registration.DefaultData, map[string]string{
			"clusterName":     "my-name",
			"clusterEndpoint": "https://00000000000000000000000.gr7.us-west-2.eks.amazonaws.com",
		})
		cm := &v1.ConfigMap{
			Data: data,
		}
		s, _ := settings.NewSettingsFromConfigMap(cm)
		Expect(s.BatchMaxDuration.Duration).To(Equal(time.Second * 10))
		Expect(s.BatchIdleDuration.Duration).To(Equal(time.Second))
	})
})
