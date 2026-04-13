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

	if args.TopologyCosts != nil {
		if args.TopologyCosts.SameZone < 0 {
			return fmt.Errorf("topologyCosts.sameZone must be non-negative, got %d", args.TopologyCosts.SameZone)
		}
		if args.TopologyCosts.SameRegion < 0 {
			return fmt.Errorf("topologyCosts.sameRegion must be non-negative, got %d", args.TopologyCosts.SameRegion)
		}
		if args.TopologyCosts.CrossRegion < 0 {
			return fmt.Errorf("topologyCosts.crossRegion must be non-negative, got %d", args.TopologyCosts.CrossRegion)
		}
		if args.TopologyCosts.SameZone > args.TopologyCosts.SameRegion {
			return fmt.Errorf("topologyCosts.sameZone (%d) must not be greater than sameRegion (%d)",
				args.TopologyCosts.SameZone, args.TopologyCosts.SameRegion)
		}
		if args.TopologyCosts.SameRegion > args.TopologyCosts.CrossRegion {
			return fmt.Errorf("topologyCosts.sameRegion (%d) must not be greater than crossRegion (%d)",
				args.TopologyCosts.SameRegion, args.TopologyCosts.CrossRegion)
		}
	}

	return nil
}
