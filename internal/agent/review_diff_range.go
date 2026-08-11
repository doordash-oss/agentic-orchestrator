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
	"fmt"
	"path/filepath"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// ReviewDiffRange describes the git range to diff for a Final Review round.
// Base is the starting ref (SHA or branch name); Head is the ending ref
// (typically "HEAD"). An empty Head means "HEAD".
type ReviewDiffRange struct {
	Base string
	Head string
}

// IsEmpty reports whether the range describes no changes (base equals head
// or both are empty). Callers should treat an empty range as a valid
// no-op, not an error.
func (r ReviewDiffRange) IsEmpty() bool {
	if r.Head == "" {
		r.Head = "HEAD"
	}
	return r.Base == r.Head
}

// DiffBase returns the BaseBranch for the named repo from the feature, or
// "main" when the repo is not found or BaseBranch is unset. This is the
// per-repo generalization of featureDefaultDiffBase.
func DiffBaseForRepo(f *feature.Feature, repoName string) string {
	if f != nil {
		for _, r := range f.Repos {
			if r.Name == repoName {
				if strings.TrimSpace(r.BaseBranch) != "" {
					return r.BaseBranch
				}
				break
			}
		}
	}
	return "main"
}

// ReviewDiffRangeForRepo computes the diff range for the current Final
// Review round for a single repo. It walks on-disk iteration metas in
// artifactDir to find the most recent iteration that recorded an anchor
// for repoName. If found, the range is [anchor.Head, HEAD]. If no prior
// anchor exists (round 1), the range falls back to the repo's BaseBranch
// (or "main"). The lookup is restart-safe: it reads from disk, so a
// resumed process resolves identically.
//
// When no prior anchor and no usable base branch exist, the helper
// degrades to the feature's default diff base rather than failing.
func ReviewDiffRangeForRepo(artifactDir, repoName string, f *feature.Feature) ReviewDiffRange {
	am := &ArtifactManager{BaseDir: artifactDir}
	latest := am.LatestIteration()
	for j := latest; j >= 1; j-- {
		jDir := filepath.Join(artifactDir, fmt.Sprintf("iteration-%02d", j))
		meta, err := am.ReadMeta(jDir)
		if err != nil {
			continue
		}
		if anchor, ok := meta.Anchors[repoName]; ok && anchor.Head != "" {
			return ReviewDiffRange{Base: anchor.Head, Head: "HEAD"}
		}
	}
	// Round 1 fallback: per-repo branch-name diff base.
	return ReviewDiffRange{Base: DiffBaseForRepo(f, repoName), Head: "HEAD"}
}

// LatestAnchorHeadForRepo returns the head SHA from the most recent
// on-disk iteration meta that recorded an anchor for repoName, or the
// empty string when no anchor exists. This is the restart-safe lookup
// used by callers that need the "previous reviewed round" head.
func LatestAnchorHeadForRepo(artifactDir, repoName string) string {
	am := &ArtifactManager{BaseDir: artifactDir}
	latest := am.LatestIteration()
	for j := latest; j >= 1; j-- {
		jDir := filepath.Join(artifactDir, fmt.Sprintf("iteration-%02d", j))
		meta, err := am.ReadMeta(jDir)
		if err != nil {
			continue
		}
		if anchor, ok := meta.Anchors[repoName]; ok && anchor.Head != "" {
			return anchor.Head
		}
	}
	return ""
}
