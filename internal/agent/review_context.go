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

package agent

import (
	"path/filepath"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

func resolveSingleRepoCycleType(f *feature.Feature) feature.RepoCycleType {
	if f == nil {
		return ""
	}
	if f.ActiveCycleType() != "" {
		return f.ActiveCycleType()
	}

	switch {
	case f.AddressingReviews():
		return feature.CycleReviewComments
	default:
		if p := f.Artifacts["plan"]; p != "" {
			np := filepath.ToSlash(p)
			switch {
			case strings.Contains(np, "/rebase"):
				return feature.CycleRebase
			case strings.Contains(np, "/review-comments"):
				return feature.CycleReviewComments
			}
		}
	}

	return ""
}

func resolveCycleTypeForRepo(f *feature.Feature, repoName string) feature.RepoCycleType {
	if f == nil {
		return ""
	}
	if repoName != "" {
		if f.RepoCycles != nil {
			if rc, ok := f.RepoCycles[repoName]; ok && rc != nil {
				if rc.Type != "" {
					return rc.Type
				}
			}
		}
		return ""
	}
	if f.ActiveCycleType() != "" {
		return f.ActiveCycleType()
	}
	return resolveSingleRepoCycleType(f)
}

func cycleArtifactDirName(f *feature.Feature, repoName string, cycleType feature.RepoCycleType) string {
	if f == nil {
		return string(cycleType)
	}

	// Single-repo: use feature-level counts for enumerated dir names.
	if repoName == "" {
		switch cycleType {
		case feature.CycleRebase:
			if f.RebaseCount() > 0 {
				return feature.RepoCycleDirName(cycleType, f.RebaseCount())
			}
		case feature.CycleReviewComments:
			if f.ReviewCommentsCount() > 0 {
				return feature.RepoCycleDirName(cycleType, f.ReviewCommentsCount())
			}
		}
		return string(cycleType)
	}

	// Multi-repo: use per-repo count via RepoCycleDirName.
	if f.RepoCycles != nil {
		if rc, ok := f.RepoCycles[repoName]; ok && rc.Count > 0 {
			return feature.RepoCycleDirName(cycleType, rc.Count)
		}
	}

	return string(cycleType)
}
