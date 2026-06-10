//go:build experiment_plugins

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

package descheduler

import (
	"sigs.k8s.io/descheduler/pkg/framework/pluginregistry"
	"sigs.k8s.io/descheduler/pkg/framework/plugins/actualusageevictor"
	"sigs.k8s.io/descheduler/pkg/framework/plugins/defaultevictor"
	"sigs.k8s.io/descheduler/pkg/framework/plugins/networkcostevictor"
	"sigs.k8s.io/descheduler/pkg/framework/plugins/nodeutilization"
	"sigs.k8s.io/descheduler/pkg/framework/plugins/resourcedefragmentationc2"
)

// SetupPlugins registers only the plugins required by the thesis experiments.
func SetupPlugins() {
	pluginregistry.PluginRegistry = pluginregistry.NewRegistry()
	RegisterDefaultPlugins(pluginregistry.PluginRegistry)
}

func RegisterDefaultPlugins(registry pluginregistry.Registry) {
	pluginregistry.Register(actualusageevictor.PluginName, actualusageevictor.New, &actualusageevictor.ActualUsageEvictor{}, &actualusageevictor.ActualUsageEvictorArgs{}, actualusageevictor.ValidateActualUsageEvictorArgs, actualusageevictor.SetDefaults_ActualUsageEvictorArgs, registry)
	pluginregistry.Register(defaultevictor.PluginName, defaultevictor.New, &defaultevictor.DefaultEvictor{}, &defaultevictor.DefaultEvictorArgs{}, defaultevictor.ValidateDefaultEvictorArgs, defaultevictor.SetDefaults_DefaultEvictorArgs, registry)
	pluginregistry.Register(networkcostevictor.PluginName, networkcostevictor.New, &networkcostevictor.NetworkCostEvictor{}, &networkcostevictor.NetworkCostEvictorArgs{}, networkcostevictor.ValidateNetworkCostEvictorArgs, networkcostevictor.SetDefaults_NetworkCostEvictorArgs, registry)
	pluginregistry.Register(nodeutilization.LowNodeUtilizationPluginName, nodeutilization.NewLowNodeUtilization, &nodeutilization.LowNodeUtilization{}, &nodeutilization.LowNodeUtilizationArgs{}, nodeutilization.ValidateLowNodeUtilizationArgs, nodeutilization.SetDefaults_LowNodeUtilizationArgs, registry)
	pluginregistry.Register(nodeutilization.HighNodeUtilizationPluginName, nodeutilization.NewHighNodeUtilization, &nodeutilization.HighNodeUtilization{}, &nodeutilization.HighNodeUtilizationArgs{}, nodeutilization.ValidateHighNodeUtilizationArgs, nodeutilization.SetDefaults_HighNodeUtilizationArgs, registry)
	pluginregistry.Register(resourcedefragmentationc2.PluginName, resourcedefragmentationc2.New, &resourcedefragmentationc2.ResourceDefragmentationC2{}, &resourcedefragmentationc2.ResourceDefragmentationC2Args{}, resourcedefragmentationc2.ValidateResourceDefragmentationC2Args, resourcedefragmentationc2.SetDefaults_ResourceDefragmentationC2Args, registry)
}
