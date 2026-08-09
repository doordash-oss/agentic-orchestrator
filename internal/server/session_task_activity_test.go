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

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
)

type taskActivityAPISession struct {
	*session.Session
	activities []llm.TaskActivity
}

func (s *taskActivityAPISession) TaskActivities() []llm.TaskActivity {
	return append([]llm.TaskActivity(nil), s.activities...)
}

func TestSessionSummaryDTOExposesProviderNeutralTaskRegistry(t *testing.T) {
	startedAt := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	finishedAt := startedAt.Add(time.Minute)
	sess := &taskActivityAPISession{
		Session: session.NewSession("session-1", "feature-1", feature.PhaseImplement),
		activities: []llm.TaskActivity{
			{
				TaskID:       "task-running",
				Description:  "Refactor execution",
				State:        llm.TaskActivityRunning,
				LastToolName: "apply_patch",
				StartedAt:    startedAt,
				UpdatedAt:    startedAt,
			},
			{
				TaskID:     "task-done",
				State:      llm.TaskActivityCompleted,
				Summary:    "Tests pass",
				StartedAt:  startedAt,
				UpdatedAt:  finishedAt,
				FinishedAt: finishedAt,
			},
		},
	}

	got := sessionSummaryDTO(sess)
	if got.RunningTaskCount != 1 || len(got.TaskActivities) != 2 {
		t.Fatalf("sessionSummaryDTO() running=%d tasks=%+v, want one of two running", got.RunningTaskCount, got.TaskActivities)
	}
	if got.TaskActivities[0].TaskID != "task-running" ||
		got.TaskActivities[0].State != TaskActivityStateRunning ||
		got.TaskActivities[0].LastToolName != "apply_patch" {
		t.Fatalf("running task DTO = %+v", got.TaskActivities[0])
	}
	if got.TaskActivities[1].FinishedAt == nil ||
		!got.TaskActivities[1].FinishedAt.Equal(finishedAt) {
		t.Fatalf("completed task DTO = %+v", got.TaskActivities[1])
	}
}
