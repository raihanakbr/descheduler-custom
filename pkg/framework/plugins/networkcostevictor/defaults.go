/*
Copyright 2025 The Kubernetes Authors.

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

package networkcostevictor

import (
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/descheduler/pkg/descheduler/networkcost"
)

func addDefaultingFuncs(scheme *runtime.Scheme) error {
	return RegisterDefaults(scheme)
}

// RegisterDefaults is a no-op for this plugin, but required by the scheme builder pattern.
func RegisterDefaults(_ *runtime.Scheme) error {
	return nil
}

// SetDefaults_NetworkCostEvictorArgs sets the default values for NetworkCostEvictorArgs.
func SetDefaults_NetworkCostEvictorArgs(obj runtime.Object) {
	args := obj.(*NetworkCostEvictorArgs)
	if args.NetworkGroupLabelKey == "" {
		args.NetworkGroupLabelKey = networkcost.DefaultNetworkGroupLabelKey
	}
}
