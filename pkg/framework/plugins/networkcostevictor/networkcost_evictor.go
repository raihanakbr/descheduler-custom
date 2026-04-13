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

// Package networkcostevictor implements a PreEvictionFilter plugin that prevents
// pod evictions that would increase network communication cost between pods in
// the same network-group. It uses topology labels (zone/region) to estimate
// communication cost and only allows eviction if at least one candidate node
// offers lower cost than the pod's current placement.
package networkcostevictor

import (
	"context"
	"fmt"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"

	"sigs.k8s.io/descheduler/pkg/descheduler/networkcost"
	nodeutil "sigs.k8s.io/descheduler/pkg/descheduler/node"
	frameworktypes "sigs.k8s.io/descheduler/pkg/framework/types"
)

const PluginName = "NetworkCostEvictor"

// NetworkCostEvictor implements the EvictorPlugin interface to provide
// network-cost-aware pre-eviction filtering. It acts as a global safety
// net across all strategies, checking whether evicting a pod would worsen
// its network communication cost with dependency pods.
type NetworkCostEvictor struct {
	logger klog.Logger
	handle frameworktypes.Handle
	args   *NetworkCostEvictorArgs
}

var _ frameworktypes.EvictorPlugin = &NetworkCostEvictor{}

// New builds the NetworkCostEvictor plugin from its arguments while passing a handle.
func New(ctx context.Context, args runtime.Object, handle frameworktypes.Handle) (frameworktypes.Plugin, error) {
	networkCostArgs, ok := args.(*NetworkCostEvictorArgs)
	if !ok {
		return nil, fmt.Errorf("want args to be of type NetworkCostEvictorArgs, got %T", args)
	}
	logger := klog.FromContext(ctx).WithValues("plugin", PluginName)

	return &NetworkCostEvictor{
		logger: logger,
		handle: handle,
		args:   networkCostArgs,
	}, nil
}

// Name retrieves the plugin name.
func (n *NetworkCostEvictor) Name() string {
	return PluginName
}

// Filter always returns true. This plugin only gates eviction at the
// PreEvictionFilter stage, not at the Filter stage.
func (n *NetworkCostEvictor) Filter(pod *v1.Pod) bool {
	return true
}

// PreEvictionFilter checks whether evicting this pod would worsen its
// network communication cost with dependency pods. It:
//  1. Checks if the pod has a network-group label — if not, allows eviction (opt-in).
//  2. Lists all ready nodes as potential reschedule candidates.
//  3. Finds all dependency pods with the same network-group label value.
//  4. Computes the current communication cost and compares it against
//     each candidate node's cost.
//  5. Allows eviction only if at least one candidate offers lower cost.
func (n *NetworkCostEvictor) PreEvictionFilter(pod *v1.Pod) bool {
	// pods without the network-group label are always allowed (opt-in)
	groupValue, exists := pod.Labels[n.args.NetworkGroupLabelKey]
	if !exists || groupValue == "" {
		return true
	}

	// resolve topology cost config
	costConfig := networkcost.DefaultTopologyCostConfig()
	if n.args.TopologyCosts != nil {
		costConfig = *n.args.TopologyCosts
	}

	// list all ready nodes as candidates
	nodes, err := nodeutil.ReadyNodes(
		context.TODO(),
		n.handle.ClientSet(),
		n.handle.SharedInformerFactory().Core().V1().Nodes().Lister(),
		"", // no node selector — consider all nodes
	)
	if err != nil {
		n.logger.Error(err, "unable to list ready nodes", "pod", klog.KObj(pod))
		return false
	}

	// build nodes map for quick lookup
	nodesMap := make(map[string]*v1.Node, len(nodes))
	for _, node := range nodes {
		nodesMap[node.Name] = node
	}

	return networkcost.ShouldAllowEviction(
		pod,
		n.args.NetworkGroupLabelKey,
		nodes,
		n.handle.GetPodsAssignedToNodeFunc(),
		nodes,
		nodesMap,
		costConfig,
	)
}
