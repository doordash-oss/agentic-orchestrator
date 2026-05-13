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
	"os"
	"path/filepath"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// FinalReviewContext holds the resolved review inputs for a post-publish cycle.
type FinalReviewContext struct {
	ArtifactDir string
	RoadmapPath string
	PhaseType   string
	DiffBase    string
	CycleFocus  string
}

// BuildPostPublishReviewContext resolves review inputs for post-publish
// tweak/rebase/review-comment cycles. Per-repo only — the single-repo
// cycle path is no longer available.
func BuildPostPublishReviewContext(
	stateDir string,
	f *feature.Feature,
	repoName string,
	cycleType feature.RepoCycleType,
) (FinalReviewContext, error) {
	if f == nil {
		return FinalReviewContext{}, fmt.Errorf("nil feature")
	}
	if len(f.Repos) == 0 {
		return FinalReviewContext{}, fmt.Errorf("feature %s has no repos", f.ID)
	}

	repo := &f.Repos[0]
	if repoName != "" {
		repo = findRepo(f, repoName)
		if repo == nil {
			return FinalReviewContext{}, fmt.Errorf("repo %q not found", repoName)
		}
	}

	return FinalReviewContext{
		ArtifactDir: repoCycleReviewArtifactDir(stateDir, f, repoName, cycleType),
		RoadmapPath: resolvePhaseArtifactPath(stateDir, f, "roadmap"),
		PhaseType:   f.RoadmapPhaseType,
		DiffBase:    repo.BaseBranch,
		CycleFocus:  buildCycleFocus(repoName, cycleType),
	}, nil
}

// repoCycleReviewArtifactDir resolves the per-repo review artifact directory
// for a post-publish cycle. Per-repo only — the single-repo cycle path is
// no longer available.
func repoCycleReviewArtifactDir(stateDir string, f *feature.Feature, repoName string, cycleType feature.RepoCycleType) string {
	dirName := string(cycleType)
	if f != nil && f.RepoCycles != nil {
		if rc, ok := f.RepoCycles[repoName]; ok && rc.Count > 0 {
			dirName = feature.RepoCycleDirName(cycleType, rc.Count)
		}
	}
	dir := filepath.Join(ActiveRunDir(stateDir, f), dirName)
	if repoName != "" {
		dir = filepath.Join(dir, repoName)
	}
	return filepath.Join(dir, feature.PhaseReview.DirName())
}

func resolvePhaseArtifactPath(stateDir string, f *feature.Feature, phase string) string {
	if f == nil {
		return ""
	}

	if path := resolveStoredArtifactPath(stateDir, f, phase, f.Artifacts[phase]); path != "" {
		return path
	}

	return globLatestArtifact(resolvePhaseDirForKey(stateDir, f, phase))
}

func resolveStoredArtifactPath(stateDir string, f *feature.Feature, phase, artifactPath string) string {
	if f == nil || artifactPath == "" {
		return ""
	}

	if filepath.IsAbs(artifactPath) {
		if _, err := os.Stat(artifactPath); err == nil {
			return artifactPath
		}
		return ""
	}

	phaseDir := resolvePhaseDirForKey(stateDir, f, phase)
	if _, err := os.Stat(filepath.Join(phaseDir, artifactPath)); err == nil {
		return filepath.Join(phaseDir, artifactPath)
	}

	for _, repo := range f.Repos {
		if repo.WorktreePath != "" {
			candidate := filepath.Join(repo.WorktreePath, artifactPath)
			if _, err := os.Stat(candidate); err == nil {
				return candidate
			}
		}
		if repo.Path != "" {
			candidate := filepath.Join(repo.Path, artifactPath)
			if _, err := os.Stat(candidate); err == nil {
				return candidate
			}
		}
	}

	return ""
}

func resolvePhaseDirForKey(stateDir string, f *feature.Feature, key string) string {
	var phaseNum int
	if _, err := fmt.Sscanf(key, "phase-%d-plan", &phaseNum); err == nil && phaseNum > 0 {
		return PhasePlanDir(stateDir, f, phaseNum)
	}
	return filepath.Join(ActiveRunDir(stateDir, f), key)
}

func globLatestArtifact(dir string) string {
	var bestPath string
	var bestModTime int64

	matches, _ := filepath.Glob(filepath.Join(dir, "*.md"))
	for _, match := range matches {
		if IsArtifactExcluded(filepath.Base(match)) {
			continue
		}
		info, err := os.Stat(match)
		if err != nil {
			continue
		}
		if mt := info.ModTime().UnixNano(); bestPath == "" || mt > bestModTime {
			bestPath = match
			bestModTime = mt
		}
	}

	return bestPath
}

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
	case f.IsRefactoring():
		return feature.CycleRefactor
	default:
		if p := f.Artifacts["plan"]; p != "" {
			np := filepath.ToSlash(p)
			switch {
			case strings.Contains(np, "/rebase"):
				return feature.CycleRebase
			case strings.Contains(np, "/tweak"):
				return feature.CycleTweak
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

func cycleReviewArtifactDir(stateDir string, f *feature.Feature, repoName string, cycleType feature.RepoCycleType) string {
	dir := filepath.Join(ActiveRunDir(stateDir, f), cycleArtifactDirName(f, repoName, cycleType))
	if repoName != "" {
		dir = filepath.Join(dir, repoName)
	}
	return filepath.Join(dir, feature.PhaseReview.DirName())
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
		case feature.CycleTweak:
			if f.TweakCount() > 0 {
				return feature.RepoCycleDirName(cycleType, f.TweakCount())
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

func buildCycleFocus(repoName string, cycleType feature.RepoCycleType) string {
	repoScope := "this repository"
	if repoName != "" {
		repoScope = fmt.Sprintf("repo %q", repoName)
	}

	var lead string
	switch cycleType {
	case feature.CycleTweak:
		lead = fmt.Sprintf("Focus on the new post-publish tweak edits in %s. Review the current change set rather than the original implementation thread.", repoScope)
	case feature.CycleRebase:
		lead = fmt.Sprintf("Focus on the new post-publish rebase resolution changes in %s. Verify the current diff cleanly resolves the rebase without regressions.", repoScope)
	case feature.CycleReviewComments:
		lead = fmt.Sprintf("Focus on the new post-publish review-comment follow-up changes in %s. Verify the current diff addresses the requested feedback without reopening prior issues.", repoScope)
	default:
		return ""
	}

	return lead + "\n\n" + cycleScopeBound()
}

// cycleScopeBound returns the cycle-scope boundary text that prevents
// reviewers from regrading post-publish cycle diffs against the originating
// phase plan's `### Manual Verification` list. The cycle's own
// testing-contract.yaml is the authoritative scope; the phase plan's manual
// gate already fired during the original phase review.
func cycleScopeBound() string {
	return "**Scope bound**: This is a post-publish cycle review. The review contract is the cycle's `testing-contract.yaml` (referenced above), NOT the originating phase plan's `### Manual Verification` list — that gate fired during the original phase review and must not be regraded here. Do not request changes for unattested phase-plan manual bullets unless the cycle itself reintroduced that surface."
}
