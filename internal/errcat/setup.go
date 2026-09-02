// Copyright 2026 DoorDash, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package errcat

import (
	"fmt"
	"strings"
)

// Setup failure codes. A setup failure has exactly one owner: the failing
// setup task. All three are blocking, reference the setup action, and
// declare the repositories, command, and setup_task context blocks.
const (
	SetupAssetCopyFailed Code = "setup_asset_copy_failed"
	SetupInterrupted     Code = "setup_interrupted"
)

// setupFailureCodes is the closed set of codes a failed setup task or its
// run-level record can carry. Every predicate that needs to know whether a
// failure is a setup failure keys on IsSetupFailure instead of comparing
// codes directly.
var setupFailureCodes = map[Code]bool{
	WorktreeSetupFailed:  true,
	SetupAssetCopyFailed: true,
	SetupInterrupted:     true,
}

// IsSetupFailure reports whether code is one of the setup failure codes.
func IsSetupFailure(code Code) bool {
	return setupFailureCodes[code]
}

// SetupFailureParams carries the setup-task label and repository names a
// setup-failure summary interpolates. RenderRecord derives them from a
// stored record's context blocks: a run-level record names the owning task,
// a task-level record names the repositories. The static (or, for the
// worktree code, repository-based) summary applies when neither is present.
type SetupFailureParams struct {
	TaskLabel    string
	Repositories []string
}

func (SetupFailureParams) params() {}

// setupTaskLabelSummary renders the owning-task sentence when the params
// name the setup task, and "" otherwise.
func setupTaskLabelSummary(p Params) string {
	params, ok := p.(SetupFailureParams)
	if !ok {
		return ""
	}
	label := strings.TrimSpace(params.TaskLabel)
	if label == "" {
		return ""
	}
	return fmt.Sprintf("Setup task %q failed.", label)
}

// worktreeSetupRepoSummary renders the repository-based worktree-setup
// summary, or "" when no repository is named.
func worktreeSetupRepoSummary(repositories []string) string {
	names := make([]string, 0, len(repositories))
	for _, name := range repositories {
		if trimmed := strings.TrimSpace(name); trimmed != "" {
			names = append(names, trimmed)
		}
	}
	switch len(names) {
	case 0:
		return ""
	case 1:
		return fmt.Sprintf("Setting up the worktree for repository %q failed.", names[0])
	default:
		return fmt.Sprintf("Setting up worktrees for repositories %s failed.", joinQuoted(names))
	}
}
