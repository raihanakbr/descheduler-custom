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
	"encoding/json"
	"strings"
	"testing"
)

func TestResourceDefragmentationC2ArgsRejectsUsageMode(t *testing.T) {
	var args ResourceDefragmentationC2Args
	err := json.Unmarshal([]byte(`{"usageMode":"actual-ewma"}`), &args)
	if err == nil {
		t.Fatal("expected removed usageMode field to be rejected")
	}
	if !strings.Contains(err.Error(), `unknown field "usageMode"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResourceDefragmentationC2ArgsAcceptsCurrentFields(t *testing.T) {
	var args ResourceDefragmentationC2Args
	err := json.Unmarshal([]byte(`{
		"maxEvictions": 3,
		"consolidationThreshold": 0.4,
		"consolidationTarget": 0.9
	}`), &args)
	if err != nil {
		t.Fatalf("unmarshal current fields: %v", err)
	}
	if args.MaxEvictions != 3 {
		t.Fatalf("maxEvictions = %d, want 3", args.MaxEvictions)
	}
}
