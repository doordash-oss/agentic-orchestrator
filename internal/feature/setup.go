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

import (
	"strconv"
	"time"
)

type SetupStatus string

const (
	SetupStatusQueued  SetupStatus = "queued"
	SetupStatusRunning SetupStatus = "running"
	SetupStatusDone    SetupStatus = "done"
	SetupStatusFailed  SetupStatus = "failed"
)

type SetupTaskKind string

const (
	SetupTaskWorktree   SetupTaskKind = "worktree"
	SetupTaskImage      SetupTaskKind = "image"
	SetupTaskAttachment SetupTaskKind = "attachment"
)

type SetupTask struct {
	Key              string        `yaml:"key"`
	Kind             SetupTaskKind `yaml:"kind"`
	Label            string        `yaml:"label"`
	Repo             string        `yaml:"repo,omitempty"`
	Status           SetupStatus   `yaml:"status"`
	Path             string        `yaml:"path,omitempty"`
	SourcePath       string        `yaml:"source_path,omitempty"`
	Branch           string        `yaml:"branch,omitempty"`
	StartPoint       string        `yaml:"start_point,omitempty"`
	UseCurrentBranch bool          `yaml:"use_current_branch,omitempty"`
	Attempt          int           `yaml:"attempt,omitempty"`
	StartedAt        *time.Time    `yaml:"started_at,omitempty"`
	EndedAt          *time.Time    `yaml:"ended_at,omitempty"`
	LastError        string        `yaml:"last_error,omitempty"`
}

type SetupState struct {
	Status        SetupStatus          `yaml:"status"`
	Attempt       int                  `yaml:"attempt"`
	StartedAt     *time.Time           `yaml:"started_at,omitempty"`
	CompletedAt   *time.Time           `yaml:"completed_at,omitempty"`
	LatestLogPath string               `yaml:"latest_log_path,omitempty"`
	Tasks         map[string]SetupTask `yaml:"tasks,omitempty"`
	TaskOrder     []string             `yaml:"task_order,omitempty"`
	LastError     string               `yaml:"last_error,omitempty"`
}

type SetupInitOptions struct {
	UseCurrentBranch        bool
	UseCurrentBranchPerRepo map[string]bool
}

func NewActiveSetupState(repos []FeatureRepo, images, attachments []string, now time.Time, opts ...SetupInitOptions) *SetupState {
	var opt SetupInitOptions
	if len(opts) > 0 {
		opt = opts[0]
	}
	tasks := make(map[string]SetupTask, len(repos)+len(images)+len(attachments))
	var order []string
	for _, repo := range repos {
		useCurrent := opt.UseCurrentBranch
		if v, ok := opt.UseCurrentBranchPerRepo[repo.Name]; ok {
			useCurrent = v
		}
		startPoint := repo.BaseBranch
		if useCurrent {
			startPoint = ""
		}
		key := "worktree:" + repo.Name
		tasks[key] = SetupTask{
			Key:              key,
			Kind:             SetupTaskWorktree,
			Label:            "Worktree: " + repo.Name,
			Repo:             repo.Name,
			Status:           SetupStatusQueued,
			Branch:           repo.Branch,
			StartPoint:       startPoint,
			UseCurrentBranch: useCurrent,
			Attempt:          1,
		}
		order = append(order, key)
	}
	for i := range images {
		n := strconv.Itoa(i + 1)
		key := "image:" + n
		tasks[key] = SetupTask{
			Key:        key,
			Kind:       SetupTaskImage,
			Label:      "Image " + n,
			Status:     SetupStatusQueued,
			SourcePath: images[i],
			Attempt:    1,
		}
		order = append(order, key)
	}
	for i := range attachments {
		n := strconv.Itoa(i + 1)
		key := "attachment:" + n
		tasks[key] = SetupTask{
			Key:        key,
			Kind:       SetupTaskAttachment,
			Label:      "Attachment " + n,
			Status:     SetupStatusQueued,
			SourcePath: attachments[i],
			Attempt:    1,
		}
		order = append(order, key)
	}
	return &SetupState{
		Status:    SetupStatusRunning,
		Attempt:   1,
		StartedAt: &now,
		Tasks:     tasks,
		TaskOrder: order,
	}
}
