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
	"reflect"
	"time"

	"github.com/samber/lo"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/aws/karpenter-core/pkg/apis/config/settings"
	"github.com/aws/karpenter-core/pkg/utils/injection"
)

// SettingsStore is a map from ContextKey to settings/config data
type SettingsStore struct {
	settings map[string]interface{}
}

func NewSettingsStore(settings ...interface{}) *SettingsStore {
	return &SettingsStore{
		settings: lo.SliceToMap(settings, func(setting interface{}) (string, interface{}) {
			if reflect.ValueOf(setting).Kind() == reflect.Pointer {
				panic("test settings store can't store pointer values")
			}
			return reflect.TypeOf(setting).Name(), setting
		}),
	}
}

func (ss *SettingsStore) Add(setting ...interface{}) {
	if reflect.ValueOf(setting).Kind() == reflect.Pointer {
		panic("test settings store can't store pointer values")
	}
	ss.settings[reflect.TypeOf(setting).Name()] = setting
}

func (ss *SettingsStore) InjectSettings(ctx context.Context) context.Context {
	for _, v := range ss.settings {
		ctx = injection.Into(ctx, v)
	}
	return ctx
}

func Settings() settings.Settings {
	return settings.Settings{
		BatchMaxDuration:  metav1.Duration{Duration: time.Second * 10},
		BatchIdleDuration: metav1.Duration{Duration: time.Second},
	}
}
