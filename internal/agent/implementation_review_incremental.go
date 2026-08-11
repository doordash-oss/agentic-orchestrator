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
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent/roles"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
)

// latestAnchoredIteration returns the iteration number of the most recent
// on-disk iteration whose meta carries at least one repo anchor, or 0 when
// no anchored iteration exists. This is the per-phase analogue of walking
// back to the "previous reviewed round": RETRY, FAILED, and other
// non-anchored iterations are transparently skipped. The lookup is
// restart-safe (reads from disk).
func latestAnchoredIteration(artifactDir string) int {
	am := &ArtifactManager{BaseDir: artifactDir}
	latest := am.LatestIteration()
	for j := latest; j >= 1; j-- {
		jDir := filepath.Join(artifactDir, fmt.Sprintf("iteration-%02d", j))
		meta, err := am.ReadMeta(jDir)
		if err != nil {
			continue
		}
		if len(meta.Anchors) > 0 {
			return j
		}
	}
	return 0
}

// perPhasePriorAxisReport reads this axis's own verbatim round N-1
// review-feedback.md from the previous *reviewed* (anchored) iteration's
// on-disk directory. The per-phase layout nests axis reports under
// review/<axis-slug>/review-feedback.md (an extra "review/" segment vs the
// Final Review layout). Returns "" on round 1 (no prior anchor) or when
// the file is missing/unreadable, so the template omits the section
// (graceful degradation).
func perPhasePriorAxisReport(artifactDir string, iteration int, axisSlug string) string {
	prevReviewed := latestAnchoredIteration(artifactDir)
	if prevReviewed <= 0 || prevReviewed >= iteration {
		return ""
	}
	prevIterDir := filepath.Join(artifactDir, fmt.Sprintf("iteration-%02d", prevReviewed))
	path := filepath.Join(prevIterDir, "review", axisSlug, "review-feedback.md")
	data, err := readTrimmedFile(path)
	if err != nil {
		return ""
	}
	return data
}

// perPhasePreviousAggregateFeedback reads the previous *reviewed*
// (anchored) iteration's aggregate review-feedback.md, written by
// runReviewGate. Gate-synthesized rejections also produce this aggregate,
// so they correctly serve as the prior round's aggregate for the next
// reviewed round. Returns "" on round 1 (no prior anchor) or when the file
// is missing/unreadable.
func perPhasePreviousAggregateFeedback(artifactDir string, iteration int) string {
	prevReviewed := latestAnchoredIteration(artifactDir)
	if prevReviewed <= 0 || prevReviewed >= iteration {
		return ""
	}
	prevIterDir := filepath.Join(artifactDir, fmt.Sprintf("iteration-%02d", prevReviewed))
	path := filepath.Join(prevIterDir, "review-feedback.md")
	data, err := readTrimmedFile(path)
	if err != nil {
		return ""
	}
	return data
}

// assembleImplementRepoDeltas builds the per-repo incremental delta blocks
// for a per-phase implementation review round N>1 axis prompt. For each
// managed feature repo it resolves the diff range from the previous
// anchored iteration's recorded head to the current worktree HEAD using
// the shared Phase 1 delta-range helper, then collects the commit messages
// and full diff text (capped to ~200 KB). An empty range (head == base)
// yields an explicit empty-delta block. On round 1 (no prior recorded
// anchor for a repo) the repo is skipped. The lookup is restart-safe: it
// reads from on-disk metas.
func assembleImplementRepoDeltas(cfg ImplementConfig, artifactDir string) []roles.RepoDeltaBlock {
	if cfg.Feature == nil || len(cfg.Feature.Repos) == 0 {
		return nil
	}
	var deltas []roles.RepoDeltaBlock
	for _, repo := range cfg.Feature.Repos {
		name := repo.Name
		path := repo.WorktreePath
		if path == "" {
			path = repo.Path
		}
		if name == "" || path == "" {
			continue
		}
		priorHead := LatestAnchorHeadForRepo(artifactDir, name)
		if priorHead == "" {
			continue
		}
		rng := ReviewDiffRangeForRepo(artifactDir, name, cfg.Feature)
		if rng.Base == "" || rng.IsEmpty() {
			deltas = append(deltas, roles.RepoDeltaBlock{
				RepoName: name,
				IsEmpty:  true,
			})
			continue
		}
		commits, err := git.CommitBodiesRange(path, rng.Base, rng.Head)
		if err != nil {
			log.Printf("feature %s: implement incremental delta commit messages for repo %s: %v", cfg.Feature.ID, name, err)
			commits = ""
		}
		diffText, err := git.DiffRangeSHAs(path, rng.Base, rng.Head)
		if err != nil {
			log.Printf("feature %s: implement incremental delta diff for repo %s: %v", cfg.Feature.ID, name, err)
			diffText = ""
		}
		block := roles.RepoDeltaBlock{
			RepoName:       name,
			CommitMessages: strings.TrimSpace(commits),
			DiffText:       diffText,
		}
		if len(diffText) > incrementalDiffCapBytes {
			stat, statErr := git.DiffStatRange(path, rng.Base, rng.Head)
			if statErr != nil {
				log.Printf("feature %s: implement incremental delta stat fallback for repo %s: %v", cfg.Feature.ID, name, statErr)
				stat = ""
			}
			block.DiffText = strings.TrimSpace(stat)
			block.Capped = true
		}
		deltas = append(deltas, block)
	}
	return deltas
}

// readTrimmedFile reads a file and returns its trimmed string content, or
// an error.
func readTrimmedFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}
