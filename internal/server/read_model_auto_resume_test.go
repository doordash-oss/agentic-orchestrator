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

package server

import (
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// TestFeatureDetailProjectsPendingAutoResume pins the usage-limit
// auto-resume projection: a Failed feature with a pending schedule carries
// auto_resume and an enabled pause-stop action (stopping cancels the
// resume); a plain failure carries neither.
func TestFeatureDetailProjectsPendingAutoResume(t *testing.T) {
	t.Parallel()
	resetAt := time.Date(2026, time.October, 29, 18, 10, 0, 0, time.UTC)

	for _, tc := range []struct {
		name     string
		schedule *feature.AutoResume
	}{
		{name: "pending schedule", schedule: &feature.AutoResume{
			Provider:    "claude",
			ResetAt:     resetAt,
			ResumeAt:    resetAt.Add(2 * time.Minute),
			ScheduledAt: resetAt.Add(-3 * time.Hour),
		}},
		{name: "plain failure"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, f := seedReadFeature(t)
			if err := store.Modify(f.ID, func(ff *feature.Feature) error {
				ff.Status = feature.StatusFailed
				ff.Run().Failure = &errcat.FailureRecord{
					Code:        errcat.InfrastructureFailure,
					Diagnostics: "You've hit your 5-hour limit · resets 11:10am (America/Los_Angeles)",
				}
				ff.Run().AutoResume = tc.schedule
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			handler := NewHandler(baseReadHandlerOptions(store))
			detail := getJSONMap(t, handler, "/api/v1/features/"+f.ID)[entityFeature].(map[string]any)

			var stop map[string]any
			for _, raw := range detail["actions"].([]any) {
				if action := raw.(map[string]any); action["id"] == string(FeatureActionPauseStop) {
					stop = action
				}
			}
			autoResume, hasAutoResume := detail["auto_resume"].(map[string]any)

			if tc.schedule == nil {
				if hasAutoResume || stop["enabled"] != false {
					t.Fatalf("plain failure projected auto_resume %#v / pause-stop %#v", autoResume, stop)
				}
				return
			}
			if !hasAutoResume {
				t.Fatalf("auto_resume missing from detail: %#v", detail)
			}
			if autoResume["provider"] != "claude" ||
				autoResume["reset_at"] != resetAt.Format(time.RFC3339) ||
				autoResume["resume_at"] != resetAt.Add(2*time.Minute).Format(time.RFC3339) {
				t.Errorf("auto_resume = %#v", autoResume)
			}
			if stop["enabled"] != true || stop["disabled_reasons"] != nil {
				t.Errorf("pause-stop action = %#v, want enabled", stop)
			}
		})
	}
}
