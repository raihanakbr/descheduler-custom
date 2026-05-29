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
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
)

// ValidateNetworkCostEvictorArgs validates the NetworkCostEvictor plugin args.
func ValidateNetworkCostEvictorArgs(obj runtime.Object) error {
	args := obj.(*NetworkCostEvictorArgs)

	if args.NetworkGroupLabelKey == "" {
		return fmt.Errorf("networkGroupLabelKey must not be empty")
	}

	if args.MinBetterCandidatesPercent < 1 || args.MinBetterCandidatesPercent > 100 {
		return fmt.Errorf("minBetterCandidatesPercent must be between 1 and 100, got %d",
			args.MinBetterCandidatesPercent)
	}

	if args.LatencyMetrics != nil {
		if args.LatencyMetrics.Prometheus == nil {
			return fmt.Errorf("latencyMetrics.prometheus must be specified when latencyMetrics is set")
		}
		if args.LatencyMetrics.Prometheus.Query == "" {
			return fmt.Errorf("latencyMetrics.prometheus.query must not be empty")
		}
	}

	return nil
}
