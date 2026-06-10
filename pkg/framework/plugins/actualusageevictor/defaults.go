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

package actualusageevictor

import "k8s.io/apimachinery/pkg/runtime"

const (
	defaultCPUUsageThreshold    = 0.80
	defaultMemoryUsageThreshold = 0.80
)

func addDefaultingFuncs(scheme *runtime.Scheme) error {
	return RegisterDefaults(scheme)
}

// SetDefaults_ActualUsageEvictorArgs sets default usage thresholds.
func SetDefaults_ActualUsageEvictorArgs(obj runtime.Object) {
	args := obj.(*ActualUsageEvictorArgs)
	if args.CPUUsageThreshold == 0 {
		args.CPUUsageThreshold = defaultCPUUsageThreshold
	}
	if args.MemoryUsageThreshold == 0 {
		args.MemoryUsageThreshold = defaultMemoryUsageThreshold
	}
}
