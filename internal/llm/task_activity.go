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

package llm

import "time"

// EventOriginKind identifies whether a normalized provider event belongs to
// the root phase agent or one of its delegated tasks.
type EventOriginKind string

const (
	EventOriginRoot EventOriginKind = "root"
	EventOriginTask EventOriginKind = "task"
)

// EventOrigin carries provider-neutral provenance. Only root events may
// authorize phase completion or create a feature-level user-input gate.
type EventOrigin struct {
	Kind           EventOriginKind `json:"kind"`
	TaskID         string          `json:"task_id,omitempty"`
	ChildSessionID string          `json:"child_session_id,omitempty"`
}

// IsRoot reports whether an event belongs to the phase's root agent.
func (o EventOrigin) IsRoot() bool {
	return o.Kind == EventOriginRoot
}

// TaskActivityState is the durable lifecycle state of a delegated task.
type TaskActivityState string

const (
	TaskActivityRunning   TaskActivityState = "running"
	TaskActivityCompleted TaskActivityState = "completed"
	TaskActivityFailed    TaskActivityState = "failed"
	TaskActivityCancelled TaskActivityState = "cancelled"
)

// TaskActivity is the provider-neutral snapshot presented to lifecycle
// decisions and user interfaces.
type TaskActivity struct {
	TaskID         string            `json:"task_id"`
	ToolUseID      string            `json:"tool_use_id,omitempty"`
	ChildSessionID string            `json:"child_session_id,omitempty"`
	Description    string            `json:"description,omitempty"`
	State          TaskActivityState `json:"state"`
	LastToolName   string            `json:"last_tool_name,omitempty"`
	LastPath       string            `json:"last_path,omitempty"`
	Status         string            `json:"status,omitempty"`
	Summary        string            `json:"summary,omitempty"`
	OutputFile     string            `json:"output_file,omitempty"`
	Usage          *TaskUsage        `json:"usage,omitempty"`
	StartedAt      time.Time         `json:"started_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
	FinishedAt     time.Time         `json:"finished_at,omitempty"`
}

// IsRunning reports whether the task has not reached a provider terminal
// event.
func (a TaskActivity) IsRunning() bool {
	return a.State == TaskActivityRunning
}
