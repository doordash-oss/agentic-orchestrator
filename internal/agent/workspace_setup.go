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
	"sort"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// WorkspaceSetup is the bootstrap shape for any unified-flow Claude session
// (phase implement, phase review, Final Review, rebase, review-comments,
// refactor, tweak). It is the single source of truth for "where should the
// agent run and which directories should it see?" — replacing the ad-hoc
// resolveUnifiedWorkDir + per-call AdditionalDirs assembly that the per-repo
// flow scattered across phase.go / refactor.go / final_review.go.
//
// Construction is pure: feature → workspace. Callers may further filter the
// AdditionalDirs slice (e.g. add a guidelines dir) but the canonical
// "feature state dir + every Feature.Repos worktree" set lives here.
type WorkspaceSetup struct {
	// Cwd is the working directory the session runs in. Always set to the
	// feature state dir (the per-feature artifacts root) so progress.md,
	// verification-report.yaml, and review-feedback.md write-paths are
	// stable across cycles. Per-repo loops historically used the repo's
	// worktree path; the unified flow sits at the state dir and reaches
	// each repo through AdditionalDirs.
	Cwd string

	// AdditionalDirs is the deduplicated, deterministically-ordered list of
	// directories to mount via --add-dir. Always contains the feature state
	// dir at index 0 (so the agent can navigate ./<run>/<phase>/... without
	// needing the absolute path), followed by every Feature.Repos worktree
	// (or repo.Path when worktrees aren't in use), sorted by repo name for
	// stable test output.
	AdditionalDirs []string

	// RepoPaths is the per-repo absolute path map, keyed by repo name.
	// Provided alongside AdditionalDirs so prompt builders can splice
	// "Repo `<name>`: <path>" lines into the cross-repo workspace
	// section without redoing path resolution.
	RepoPaths map[string]string
}

// BuildWorkspace returns the canonical workspace bootstrap for the given
// feature. The state dir is the per-feature root (i.e. the directory that
// contains `feature.yaml` and `runs/`).
//
// Behavior:
//   - Cwd is always stateDir. Single-repo and multi-repo cases share this.
//   - Every Feature.Repos entry contributes one AdditionalDirs path: the
//     worktree path when set, falling back to repo.Path. Repos missing both
//     fields are skipped silently (the validator catches this earlier).
//   - The state dir is always present at AdditionalDirs[0] to keep
//     navigation paths stable in the agent's prompt.
//   - Output is order-stable for tests: state dir first, then repos sorted
//     by name.
func BuildWorkspace(feat *feature.Feature, stateDir string) (WorkspaceSetup, error) {
	if feat == nil {
		return WorkspaceSetup{}, fmt.Errorf("workspace setup: feature is nil")
	}
	if stateDir == "" {
		return WorkspaceSetup{}, fmt.Errorf("workspace setup: state dir is empty")
	}
	abs, err := filepath.Abs(stateDir)
	if err != nil {
		return WorkspaceSetup{}, fmt.Errorf("workspace setup: state dir abs: %w", err)
	}

	// Canonical sort: by repo name. Stable output for tests, predictable
	// --add-dir ordering for the agent.
	repos := append([]feature.FeatureRepo(nil), feat.Repos...)
	sort.Slice(repos, func(i, j int) bool { return repos[i].Name < repos[j].Name })

	dirs := []string{abs}
	seen := map[string]bool{abs: true}
	paths := make(map[string]string, len(repos))
	for _, r := range repos {
		p := r.WorktreePath
		if p == "" {
			p = r.Path
		}
		if p == "" {
			continue
		}
		ap, err := filepath.Abs(p)
		if err != nil {
			return WorkspaceSetup{}, fmt.Errorf("workspace setup: repo %q abs: %w", r.Name, err)
		}
		paths[r.Name] = ap
		if !seen[ap] {
			seen[ap] = true
			dirs = append(dirs, ap)
		}
	}

	return WorkspaceSetup{
		Cwd:            abs,
		AdditionalDirs: dirs,
		RepoPaths:      paths,
	}, nil
}

// WorkspaceForRepos is a filtering helper for cycles that legitimately mount
// only a subset of Feature.Repos (e.g. a rebase cycle scoped to the behind
// repos). The base WorkspaceSetup is computed first so the state dir is
// always present; the AdditionalDirs slice is then narrowed to the requested
// repo names plus the state dir.
//
// Repo names not in feat.Repos are silently dropped. RepoPaths is filtered
// to match. Returns the unfiltered workspace when repos is empty (treated as
// "all repos" for a no-arg sentinel).
func WorkspaceForRepos(feat *feature.Feature, stateDir string, repos []string) (WorkspaceSetup, error) {
	full, err := BuildWorkspace(feat, stateDir)
	if err != nil {
		return WorkspaceSetup{}, err
	}
	if len(repos) == 0 {
		return full, nil
	}
	want := make(map[string]bool, len(repos))
	for _, r := range repos {
		want[r] = true
	}
	dirs := []string{full.Cwd}
	seen := map[string]bool{full.Cwd: true}
	paths := make(map[string]string)
	for name, p := range full.RepoPaths {
		if !want[name] {
			continue
		}
		paths[name] = p
		if !seen[p] {
			seen[p] = true
			dirs = append(dirs, p)
		}
	}
	// Re-sort the non-Cwd entries by repo path for determinism — paths are
	// keyed by name, so we can re-derive order from the sorted want set.
	sortedNames := make([]string, 0, len(paths))
	for n := range paths {
		sortedNames = append(sortedNames, n)
	}
	sort.Strings(sortedNames)
	dirs = []string{full.Cwd}
	seen = map[string]bool{full.Cwd: true}
	for _, n := range sortedNames {
		p := paths[n]
		if !seen[p] {
			seen[p] = true
			dirs = append(dirs, p)
		}
	}
	return WorkspaceSetup{
		Cwd:            full.Cwd,
		AdditionalDirs: dirs,
		RepoPaths:      paths,
	}, nil
}
