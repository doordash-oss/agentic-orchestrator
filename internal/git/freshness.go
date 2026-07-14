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

package git

import (
	"os/exec"
	"strconv"
	"strings"
)

// Freshness status values returned by RepoFreshness. Exported since callers
// outside this package (e.g. cmd/agentico) switch on these literal strings.
const (
	FreshnessUnknown      = "unknown"
	FreshnessLocalChanges = "local changes"
)

func RepoFreshness(worktreePath string) string {
	if worktreePath == "" {
		return FreshnessUnknown
	}
	if out, err := exec.Command("git", "-C", worktreePath, "status", "--porcelain").Output(); err != nil {
		return FreshnessUnknown
	} else if strings.TrimSpace(string(out)) != "" {
		return FreshnessLocalChanges
	}
	if err := exec.Command("git", "-C", worktreePath, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}").Run(); err != nil {
		return "local only"
	}
	out, err := exec.Command("git", "-C", worktreePath, "rev-list", "--left-right", "--count", "HEAD...@{upstream}").Output()
	if err != nil {
		return FreshnessUnknown
	}
	fields := strings.Fields(string(out))
	if len(fields) != 2 {
		return FreshnessUnknown
	}
	ahead, errA := strconv.Atoi(fields[0])
	behind, errB := strconv.Atoi(fields[1])
	if errA != nil || errB != nil {
		return FreshnessUnknown
	}
	if ahead == 0 && behind == 0 {
		return "in sync"
	}
	return FreshnessLocalChanges
}
