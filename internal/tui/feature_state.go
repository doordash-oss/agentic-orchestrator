// Copyright 2026 DoorDash, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package tui

import (
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

func featureHasRunningCycle(f *feature.Feature) bool {
	if f == nil {
		return false
	}
	if f.ActiveCycle != nil && f.ActiveCycle.Status == feature.RepoCycleRunning {
		return true
	}
	for _, rc := range f.RepoCycles {
		if rc != nil && rc.Status == feature.RepoCycleRunning {
			return true
		}
	}
	return false
}

func isRunningFeature(f *feature.Feature) bool {
	if f == nil {
		return false
	}
	if f.Status == feature.StatusBuildingKB ||
		f.Status == feature.StatusResearching ||
		f.Status == feature.StatusInquiring ||
		f.Status == feature.StatusDesigning ||
		f.Status == feature.StatusPlanning ||
		f.Status == feature.StatusImplementing ||
		f.IsReviewing() {
		return true
	}
	if f.Status == feature.StatusPublished || f.Status == feature.StatusCodeReady {
		return hasActiveRepoCycles(f)
	}
	return false
}

func hasActiveRepoCycles(f *feature.Feature) bool {
	return f != nil && f.HasActiveRepoCycles()
}

// canEditFeatureConfig reports whether feature-level config editing should be
// offered. Runtime changes are persisted immediately; running work observes
// them at the next phase boundary or restart.
func canEditFeatureConfig(f *feature.Feature) bool {
	return f != nil
}

func featureConfigChangesDeferred(f *feature.Feature) bool {
	if f == nil {
		return false
	}
	return f.Status.IsRunning() || f.HasActiveRepoCycles()
}
