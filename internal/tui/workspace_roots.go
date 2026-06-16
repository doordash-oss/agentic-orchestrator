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

import "github.com/doordash-oss/agentic-orchestrator/internal/config"

func containsRootExpanded(roots []string, candidate string) bool {
	expandedCandidate := config.ExpandHome(candidate)
	for _, r := range roots {
		if config.ExpandHome(r) == expandedCandidate {
			return true
		}
	}
	return false
}

func removeRoot(roots []string, path string) []string {
	expandedPath := config.ExpandHome(path)
	result := make([]string, 0, len(roots))
	for _, r := range roots {
		if config.ExpandHome(r) != expandedPath {
			result = append(result, r)
		}
	}
	return result
}
