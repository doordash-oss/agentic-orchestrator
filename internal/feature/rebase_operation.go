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

package feature

import "time"

type RebaseStage string

const (
	RebaseStageHarness     RebaseStage = "harness"
	RebaseStageSmartRebase RebaseStage = "smart_rebase"
	RebaseStageFinalReview RebaseStage = "final_review"
	RebaseStagePublish     RebaseStage = "publish"
)

type RebaseRepoStatus string

const (
	RebaseRepoStatusChecking RebaseRepoStatus = "checking"
	RebaseRepoStatusRebasing RebaseRepoStatus = "rebasing"
	RebaseRepoStatusUpToDate RebaseRepoStatus = "up_to_date"
	RebaseRepoStatusChanged  RebaseRepoStatus = "changed"
	RebaseRepoStatusConflict RebaseRepoStatus = "conflict"
	RebaseRepoStatusFailed   RebaseRepoStatus = "failed"
)

type RebaseOperationState struct {
	Stage     RebaseStage                    `yaml:"stage"`
	StartedAt time.Time                      `yaml:"started_at"`
	UpdatedAt time.Time                      `yaml:"updated_at"`
	Repos     map[string]*RebaseRepoProgress `yaml:"repos,omitempty"`
}

type RebaseRepoProgress struct {
	Status        RebaseRepoStatus `yaml:"status"`
	RebaseTarget  string           `yaml:"rebase_target,omitempty"`
	ConflictFiles []string         `yaml:"conflict_files,omitempty"`
	LastError     string           `yaml:"last_error,omitempty"`
	Changed       bool             `yaml:"changed,omitempty"`
}
