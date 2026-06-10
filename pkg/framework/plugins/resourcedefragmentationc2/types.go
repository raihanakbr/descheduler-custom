/*
Copyright 2026 The Kubernetes Authors.

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

package resourcedefragmentationc2

import (
	"bytes"
	"encoding/json"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/descheduler/pkg/api"
)

// +k8s:deepcopy-gen=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

type ResourceDefragmentationC2Args struct {
	metav1.TypeMeta `json:",inline"`

	Namespaces *api.Namespaces `json:"namespaces,omitempty"`

	MaxEvictions int `json:"maxEvictions,omitempty"`

	// ConsolidationThreshold: a node whose avg utilisation OR bin score is below
	// this is a drain candidate. Range [0,1]. Default 0.40.
	ConsolidationThreshold float64 `json:"consolidationThreshold,omitempty"`

	// ConsolidationTarget: packing ceiling for a destination node. Range [0,1].
	// Default 0.90.
	ConsolidationTarget float64 `json:"consolidationTarget,omitempty"`
}

// UnmarshalJSON rejects removed and misspelled fields instead of silently
// accepting a policy with ineffective configuration.
func (a *ResourceDefragmentationC2Args) UnmarshalJSON(data []byte) error {
	type argsAlias ResourceDefragmentationC2Args

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return decoder.Decode((*argsAlias)(a))
}
