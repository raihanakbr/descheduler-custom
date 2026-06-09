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

package resourcedefragmentation

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
)

func ValidateResourceDefragmentationArgs(obj runtime.Object) error {
	args := obj.(*ResourceDefragmentationArgs)
	var allErrs []error
	// At most one of include/exclude can be set
	if args.Namespaces != nil && len(args.Namespaces.Include) > 0 && len(args.Namespaces.Exclude) > 0 {
		allErrs = append(allErrs, fmt.Errorf("only one of Include/Exclude namespaces can be set"))
	}
	switch args.UsageMode {
	case "", UsageModeRequests, UsageModeActualRaw, UsageModeActualEWMA, UsageModeActualEWMAPersisted, UsageModePublishedEWMA:
	default:
		allErrs = append(allErrs, fmt.Errorf("unsupported usageMode %q, must be one of %q, %q, %q, %q, %q", args.UsageMode, UsageModeRequests, UsageModeActualRaw, UsageModeActualEWMA, UsageModeActualEWMAPersisted, UsageModePublishedEWMA))
	}
	if args.ConsolidationThreshold < 0 || args.ConsolidationThreshold > 1 {
		allErrs = append(allErrs, fmt.Errorf("consolidationThreshold must be in range [0, 1], got %v", args.ConsolidationThreshold))
	}
	if args.ConsolidationTarget < 0 || args.ConsolidationTarget > 1 {
		allErrs = append(allErrs, fmt.Errorf("consolidationTarget must be in range [0, 1], got %v", args.ConsolidationTarget))
	}
	if args.BalancePenaltyWeight < 0 || args.BalancePenaltyWeight > 1 {
		allErrs = append(allErrs, fmt.Errorf("balancePenaltyWeight must be in range [0, 1], got %v", args.BalancePenaltyWeight))
	}
	switch args.SelectionPolicy {
	case "", SelectionTOPSIS, SelectionJustC1, SelectionJustC2, SelectionJustC3, SelectionJustC4,
		SelectionNoC1, SelectionNoC2, SelectionNoC3, SelectionNoC4,
		SelectionRandom, SelectionLargest, SelectionLowestPriority:
	default:
		allErrs = append(allErrs, fmt.Errorf("unsupported selectionPolicy %q", args.SelectionPolicy))
	}
	return utilerrors.NewAggregate(allErrs)
}
