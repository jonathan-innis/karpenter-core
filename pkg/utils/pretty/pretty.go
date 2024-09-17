/*
Copyright The Kubernetes Authors.

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

package pretty

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"golang.org/x/exp/constraints"
	"golang.org/x/exp/slices"
	v1 "k8s.io/api/core/v1"
)

func Concise(o interface{}) string {
	bytes, err := json.Marshal(o)
	if err != nil {
		return err.Error()
	}
	return string(bytes)
}

// Slice truncates a slice after a certain number of max items to ensure
// that the Slice isn't too long
func Slice[T any](s []T, maxItems int) string {
	var sb strings.Builder
	for i, elem := range s {
		if i > maxItems-1 {
			fmt.Fprintf(&sb, " and %d other(s)", len(s)-i)
			break
		} else if i > 0 {
			fmt.Fprint(&sb, ", ")
		}
		fmt.Fprint(&sb, elem)
	}
	return sb.String()
}

// Map truncates a map after a certain number of max items to ensure that the
// description in a log doesn't get too long
func Map[K constraints.Ordered, V any](values map[K]V, maxItems int) string {
	var buf bytes.Buffer
	count := 0
	var keys []K
	for k := range values {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		v := values[k]
		count++
		if buf.Len() > 0 {
			fmt.Fprintf(&buf, ", ")
		}
		fmt.Fprintf(&buf, "%v: %v", k, v)
		if count >= maxItems {
			break
		}

	}
	if count < len(values) {
		fmt.Fprintf(&buf, " and %d other(s)", len(values)-count)
	}
	return buf.String()
}

func Taint(t v1.Taint) string {
	if t.Value == "" {
		return fmt.Sprintf("%s:%s", t.Key, t.Effect)
	}
	return fmt.Sprintf("%s=%s:%s", t.Key, t.Value, t.Effect)
}
