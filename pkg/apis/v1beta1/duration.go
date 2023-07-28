package v1beta1

import (
	"encoding/json"
	"time"
)

// DisableableDuration is a wrapper around time.Duration which supports correct
// marshaling to YAML and JSON. In particular, it marshals into strings, which
// can be used as map keys in json.
type DisableableDuration struct {
	Disabled      bool `json:"disabled" protobuf:"varint,1,opt,name=disabled,casttype=bool"`
	time.Duration `json:"duration" protobuf:"varint,1,opt,name=duration,casttype=time.Duration"`
}

// UnmarshalJSON implements the json.Unmarshaller interface.
func (d *DisableableDuration) UnmarshalJSON(b []byte) error {
	var str string
	err := json.Unmarshal(b, &str)
	if err != nil {
		return err
	}
	if str == "disabled" {
		d.Disabled = true
		return nil
	}
	pd, err := time.ParseDuration(str)
	if err != nil {
		return err
	}
	d.Duration = pd
	return nil
}

// MarshalJSON implements the json.Marshaler interface.
func (d DisableableDuration) MarshalJSON() ([]byte, error) {
	if d.Disabled {
		return []byte("disabled"), nil
	}
	return json.Marshal(d.Duration.String())
}

// ToUnstructured implements the value.UnstructuredConverter interface.
func (d DisableableDuration) ToUnstructured() interface{} {
	if d.Disabled {
		return "disabled"
	}
	return d.Duration.String()
}

// OpenAPISchemaType is used by the kube-openapi generator when constructing
// the OpenAPI spec of this type.
//
// See: https://github.com/kubernetes/kube-openapi/tree/master/pkg/generators
func (_ DisableableDuration) OpenAPISchemaType() []string { return []string{"string"} }

// OpenAPISchemaFormat is used by the kube-openapi generator when constructing
// the OpenAPI spec of this type.
func (_ DisableableDuration) OpenAPISchemaFormat() string { return "" }
