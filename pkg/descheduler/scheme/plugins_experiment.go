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

package scheme

import (
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/descheduler/pkg/framework/plugins/actualusageevictor"
	"sigs.k8s.io/descheduler/pkg/framework/plugins/defaultevictor"
	"sigs.k8s.io/descheduler/pkg/framework/plugins/networkcostevictor"
	"sigs.k8s.io/descheduler/pkg/framework/plugins/nodeutilization"
	"sigs.k8s.io/descheduler/pkg/framework/plugins/resourcedefragmentationc2"
)

func addPluginSchemes(scheme *runtime.Scheme) {
	utilruntime.Must(actualusageevictor.AddToScheme(scheme))
	utilruntime.Must(defaultevictor.AddToScheme(scheme))
	utilruntime.Must(networkcostevictor.AddToScheme(scheme))
	utilruntime.Must(nodeutilization.AddToScheme(scheme))
	utilruntime.Must(resourcedefragmentationc2.AddToScheme(scheme))
}
