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

package injection

import (
	"context"
	"fmt"
	"reflect"

	"k8s.io/apimachinery/pkg/types"
)

// contextKey returns a key to use in context.WithValue() to store a singleton instance
// of a struct type within the context
func contextKey[T any]() interface{} {
	key := *new(T)
	if reflect.ValueOf(key).Kind() != reflect.Struct || (reflect.ValueOf(key).Kind() == reflect.Pointer && reflect.ValueOf(key).Elem().Kind() != reflect.Struct) {
		panic("ContextKey can only be used on struct types")
	}
	return key
}

// Into stores the element of type T into the context where type T is a singleton
// of its type in the context
func Into[T any](ctx context.Context, elem T) context.Context {
	return context.WithValue(ctx, contextKey[T](), elem)
}

// From returns the element of type T stored in the context where type T is a singleton
// of its type in the context
func From[T any](ctx context.Context) T {
	data := ctx.Value(contextKey[T]())
	if data == nil {
		// This is developer error if this happens, so we should panic
		panic(fmt.Sprintf("type %s doesn't exist in context", reflect.TypeOf(*new(T)).Name()))
	}
	return data.(T)
}

type resourceKey struct{}

func WithNamespacedName(ctx context.Context, namespacedname types.NamespacedName) context.Context {
	return context.WithValue(ctx, resourceKey{}, namespacedname)
}

func GetNamespacedName(ctx context.Context) types.NamespacedName {
	retval := ctx.Value(resourceKey{})
	if retval == nil {
		return types.NamespacedName{}
	}
	return retval.(types.NamespacedName)
}

type controllerNameKey struct{}

func WithControllerName(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, controllerNameKey{}, name)
}

func GetControllerName(ctx context.Context) string {
	name := ctx.Value(controllerNameKey{})
	if name == nil {
		return ""
	}
	return name.(string)
}
