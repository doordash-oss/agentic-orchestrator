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

package orchestrator

import (
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// TestRestartRepoCycleExitCriteriaMatchesLiveDriver asserts a restarted
// review-comments cycle judges against the same synthesized "resolve every
// aggregated comment" criteria the live driver used, not the feature's raw
// ExitCriteria.
func TestRestartRepoCycleExitCriteriaMatchesLiveDriver(t *testing.T) {
	planPath := "/state/feat-1/runs/run-1/review-comments-1/review-plan.md"

	got := restartRepoCycleExitCriteria(feature.CycleReviewComments, planPath)

	wantResolutionsPath := "/state/feat-1/runs/run-1/review-comments-1/review-resolutions.json"
	if !strings.Contains(got, wantResolutionsPath) {
		t.Fatalf("restart exit criteria = %q, want it to cite resolutions path %q", got, wantResolutionsPath)
	}
	if got != agent.ReviewCommentsExitCriteria(wantResolutionsPath) {
		t.Fatalf("restart exit criteria diverges from the live driver's synthesized criteria:\ngot:  %q\nwant: %q",
			got, agent.ReviewCommentsExitCriteria(wantResolutionsPath))
	}
}
