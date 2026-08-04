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

// Package orchestrator — rebase.go owns shared rebase helpers used by the
// rebase-child creation path, publish-conflict metadata, and completion
// preflight freshness checks.
package orchestrator

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
)

// HarnessRebaseRepoOutcome captures the resolved rebase target and worktree
// identity for a single repository. The freshness/blocker helpers shared by
// completion preflight consume it; the legacy harness rebase that populated the
// status/conflict fields has been removed.
type HarnessRebaseRepoOutcome struct {
	RepoName      string
	RebaseTarget  string
	ConflictFiles []string
	Changed       bool
	Err           error
	Publishable   bool
	WorktreePath  string
	Branch        string
}

// resolveRebaseTarget picks the base ref a follow-up rebase should target
// for the given repo. The order matches the auto-rebase path so conflict
// recovery and the proactive rebase agree on the target:
//
//  1. The PR's base branch on GitHub (when a PRURL is recorded), via RemoteOps.
//     This is authoritative for published features.
//  2. repo.BaseBranch from the feature manifest.
//  3. The repo's default branch (origin/HEAD).
//
// Returns "" only when every fallback fails — callers should treat that as
// a hard error rather than silently rebasing onto an empty target.
func (o *Orchestrator) resolveRebaseTarget(f *feature.Feature, repo *feature.FeatureRepo) string {
	target := ""
	if state, ok := f.RepoStates[repo.Name]; ok && state != nil && state.PRURL != "" {
		target = o.deps.Remote.PRBaseBranch(repo.Path, state.PRURL)
	}
	if target == "" {
		target = repo.BaseBranch
	}
	if target == "" {
		target = git.DefaultBranch(repo.Path)
	}
	return target
}

// repoWorkDir returns repo's worktree path, falling back to its base path.
func repoWorkDir(repo feature.FeatureRepo) string {
	if repo.WorktreePath != "" {
		return repo.WorktreePath
	}
	return repo.Path
}

// repoBranch returns repo's branch, falling back to "feature/<slug>".
func repoBranch(f *feature.Feature, repo feature.FeatureRepo) string {
	if repo.Branch != "" {
		return repo.Branch
	}
	return "feature/" + f.Slug
}

// harnessRebaseOutcomeForRepo builds the per-repo outcome carrying the resolved
// rebase target and worktree identity. Completion preflight and the legacy
// rebase preflight share it for freshness/blocker decisions.
func (o *Orchestrator) harnessRebaseOutcomeForRepo(f *feature.Feature, repo feature.FeatureRepo) HarnessRebaseRepoOutcome {
	return HarnessRebaseRepoOutcome{
		RepoName:     repo.Name,
		RebaseTarget: o.resolveRebaseTarget(f, &repo),
		Publishable:  repo.Publishable == nil || *repo.Publishable,
		WorktreePath: repoWorkDir(repo),
		Branch:       repoBranch(f, repo),
	}
}

func (o *Orchestrator) rebaseWorktreeFingerprint(repo feature.FeatureRepo) string {
	fn := o.worktreeFingerprintFn
	if fn == nil {
		fn = gitWorktreeFingerprint
	}
	fingerprint, err := fn(repoWorkDir(repo))
	if err != nil {
		return "error:" + err.Error()
	}
	return fingerprint
}

func gitWorktreeFingerprint(worktreePath string) (string, error) {
	if strings.TrimSpace(worktreePath) == "" {
		return "", errors.New("empty worktree path")
	}
	head, err := exec.Command("git", "-C", worktreePath, "rev-parse", "HEAD").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("rev-parse HEAD: %s: %w", strings.TrimSpace(string(head)), err)
	}
	status, err := exec.Command("git", "-C", worktreePath, "status", "--porcelain=v1").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git status: %s: %w", strings.TrimSpace(string(status)), err)
	}
	diff, err := exec.Command("git", "-C", worktreePath, "diff", "--binary", "HEAD", "--").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git diff: %s: %w", strings.TrimSpace(string(diff)), err)
	}
	return strings.TrimSpace(string(head)) + "\n" + string(status) + "\n" + string(diff), nil
}
