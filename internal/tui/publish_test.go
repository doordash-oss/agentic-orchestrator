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

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
)

// newTestPublishModel wraps the canonical NewPublishModel with a stub
// *feature.Feature (1 repo, so len(f.Repos) > 1 == false → auto-select)
// and a 1-entry repos slice. The supplied diff/commitLog/prTitle/prBody
// override the values set by the constructor (which loads diff/commitLog
// from git.DiffSummary/CommitLog against /tmp/test-wt — non-existent in tests).
// Existing tests that don't care about repo selection continue to work; tests
// that need multi-repo behavior call NewPublishModel directly with a
// 2+ repos feature.
func newTestPublishModel(featureID, diff, commitLog, prTitle, prBody string, width, height int) PublishModel {
	f := &feature.Feature{
		ID:    featureID,
		Repos: []feature.FeatureRepo{{Name: "test-repo"}},
	}
	repos := []publishRepoEntry{{
		Name:        "test-repo",
		Branch:      "test-branch",
		WorktreeDir: "/tmp/test-wt",
		RepoPath:    "/tmp/test-repo",
		BaseBranch:  "main",
		PRStatus:    "pending",
	}}
	m := NewPublishModel(f, repos, "", "", width, height)
	// Override the test-supplied diff/commitLog/title/body. The constructor
	// loaded empty strings from the non-existent worktree; the tests rely on
	// these explicit values for their assertions.
	m.diff = diff
	m.commitLog = commitLog
	m.prTitle = prTitle
	m.prBody = prBody
	if prTitle != "" {
		m.titleInput.SetValue(prTitle)
	}
	if prBody != "" {
		m.bodyInput.SetValue(prBody)
	}
	if diff != "" {
		m.viewport.SetContent(colorizeDiff(diff))
	}
	return m
}

// newTestFeatureFromRepos builds a stub *feature.Feature whose Repos slice
// matches the publish entries 1:1. Used by migrated multi-repo publish
// callsites: the chrome decision routes through len(f.Repos) > 1, so
// f.Repos must reflect the same repo count for the test to behave as before.
func newTestFeatureFromRepos(featureID, featureName string, repos []publishRepoEntry) *feature.Feature {
	f := &feature.Feature{ID: featureID, Name: featureName}
	for _, r := range repos {
		f.Repos = append(f.Repos, feature.FeatureRepo{Name: r.Name})
	}
	return f
}

// newTestPublishModelWithGit wraps the canonical NewPublishModel with a stub
// *feature.Feature (1 repo, so chrome=false) and a 1-entry repos slice
// carrying git context. Mirrors the legacy single-repo-with-git constructor
// for migrating tests.
func newTestPublishModelWithGit(featureID, diff, commitLog, prTitle, prBody, worktreeDir, branch, repoPath, planText, descModel, baseBranch string, width, height int) PublishModel {
	f := &feature.Feature{
		ID:    featureID,
		Repos: []feature.FeatureRepo{{Name: "test-repo"}},
	}
	repos := []publishRepoEntry{{
		Name:        "test-repo",
		Branch:      branch,
		WorktreeDir: worktreeDir,
		RepoPath:    repoPath,
		BaseBranch:  baseBranch,
		PRStatus:    "pending",
	}}
	m := NewPublishModel(f, repos, planText, descModel, width, height)
	m.diff = diff
	m.commitLog = commitLog
	if prTitle != "" {
		m.prTitle = prTitle
		m.titleInput.SetValue(prTitle)
	}
	if prBody != "" {
		m.prBody = prBody
		m.bodyInput.SetValue(prBody)
	}
	m.viewport.SetContent(colorizeDiff(diff))
	return m
}

func TestPublishSteps(t *testing.T) {
	m := newTestPublishModel("feat-1", "diff content", "commit log", "PR Title", "PR Body", 120, 40)

	// Step 1: Diff
	view := m.View()
	if !strings.Contains(view, "Diff Review") {
		t.Error("expected diff review step")
	}

	// Advance to commits
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.step != publishStepCommits {
		t.Errorf("expected commits step, got %d", m.step)
	}

	// Advance to PR description
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.step != publishStepPRDesc {
		t.Errorf("expected PR desc step, got %d", m.step)
	}

	// Advance to confirm
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.step != publishStepConfirm {
		t.Errorf("expected confirm step, got %d", m.step)
	}
}

func TestPublishCancel(t *testing.T) {
	m := newTestPublishModel("feat-1", "diff", "log", "title", "body", 120, 40)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !m.IsDone() {
		t.Error("expected publish to be done after cancel")
	}
}

func TestPublishWithGit(t *testing.T) {
	m := newTestPublishModelWithGit("feat-1", "diff", "log", "My PR", "body", "/tmp/wt", "feature/test", "/tmp/repo", "plan text", "opus", "", 120, 40)
	if m.worktreeDir != "/tmp/wt" {
		t.Errorf("worktreeDir = %q, want /tmp/wt", m.worktreeDir)
	}
	if m.branch != "feature/test" {
		t.Errorf("branch = %q, want feature/test", m.branch)
	}
	if m.repoPath != "/tmp/repo" {
		t.Errorf("repoPath = %q, want /tmp/repo", m.repoPath)
	}

	// Confirm step should show branch
	m.step = publishStepConfirm
	view := m.View()
	if !strings.Contains(view, "feature/test") {
		t.Error("expected branch in confirm view")
	}
}

func TestPublishExecuteResult(t *testing.T) {
	m := newTestPublishModel("feat-1", "diff", "log", "title", "body", 120, 40)

	// Simulate success result
	m, _ = m.Update(publishExecuteResultMsg{prURL: "https://github.com/org/repo/pull/42"})
	if m.prURL != "https://github.com/org/repo/pull/42" {
		t.Errorf("prURL = %q", m.prURL)
	}
	if !m.IsDone() {
		t.Error("expected done after result")
	}
	view := m.View()
	if !strings.Contains(view, "https://github.com/org/repo/pull/42") {
		t.Error("expected PR URL in done view")
	}
}

func TestPublishExecuteError(t *testing.T) {
	m := newTestPublishModel("feat-1", "diff", "log", "title", "body", 120, 40)

	// Simulate error result
	m, _ = m.Update(publishExecuteResultMsg{err: fmt.Errorf("push failed")})
	if m.errMsg != "push failed" {
		t.Errorf("errMsg = %q", m.errMsg)
	}
	if !m.IsDone() {
		t.Error("expected done after error")
	}
	view := m.View()
	if !strings.Contains(view, "push failed") {
		t.Error("expected error in done view")
	}
}

func TestPublishDescriptionGenerated(t *testing.T) {
	m := newTestPublishModel("feat-1", "diff", "log", "", "", 120, 40)
	// Simulate description generation result
	m, _ = m.Update(publishDescGeneratedMsg{title: "Generated Title", body: "Generated body"})
	if m.prTitle != "Generated Title" {
		t.Errorf("prTitle = %q, want 'Generated Title'", m.prTitle)
	}
	if m.prBody != "Generated body" {
		t.Errorf("prBody = %q, want 'Generated body'", m.prBody)
	}
	if m.generating {
		t.Error("expected generating to be false after desc generated")
	}
	if m.titleInput.Value() != "Generated Title" {
		t.Errorf("titleInput = %q, want 'Generated Title'", m.titleInput.Value())
	}
	if m.bodyInput.Value() != "Generated body" {
		t.Errorf("bodyInput = %q, want 'Generated body'", m.bodyInput.Value())
	}
}

func TestPublishGenerateDescription_UsesHelperRunner(t *testing.T) {
	m := newTestPublishModelWithGit("feat-1", "diff", "log", "", "", "/tmp/wt", "feature/test", "/tmp/repo", "plan", "opus", "", 120, 40)
	m.prCtx = agent.PRContext{FeatureName: "Feature", Roadmap: "plan"}

	called := false
	m.runDesc = func(ctx context.Context, model string, prCtx agent.PRContext) (string, string, error) {
		called = true
		if model != "opus" {
			t.Errorf("model = %q, want %q", model, "opus")
		}
		if prCtx.FeatureName != "Feature" {
			t.Errorf("prCtx.FeatureName = %q, want %q", prCtx.FeatureName, "Feature")
		}
		return "Generated Title", "Generated body", nil
	}

	m.step = publishStepCommits
	m, cmd := m.advanceStep()
	if m.step != publishStepPRDesc {
		t.Fatalf("step = %d, want %d", m.step, publishStepPRDesc)
	}
	if !m.generating {
		t.Fatal("expected generating to be true before command result")
	}
	if cmd == nil {
		t.Fatal("generateDescription command = nil")
	}
	msg := cmd()
	descMsg, ok := msg.(publishDescGeneratedMsg)
	if !ok {
		t.Fatalf("cmd() message = %T, want publishDescGeneratedMsg", msg)
	}
	if !called {
		t.Fatal("expected runDesc to be called")
	}

	m, _ = m.Update(descMsg)
	if m.prTitle != "Generated Title" || m.prBody != "Generated body" {
		t.Fatalf("PR description = (%q, %q), want generated values", m.prTitle, m.prBody)
	}
}

func TestPublishPlanText(t *testing.T) {
	m := newTestPublishModelWithGit("feat-1", "diff", "log", "", "", "/tmp/wt", "feature/test", "/tmp/repo", "plan content", "opus", "", 120, 40)
	if m.planText != "plan content" {
		t.Errorf("planText = %q, want 'plan content'", m.planText)
	}
}

func TestPublishDescModel(t *testing.T) {
	m := newTestPublishModelWithGit("feat-1", "diff", "log", "", "", "/tmp/wt", "feature/test", "/tmp/repo", "plan", "opus", "", 120, 40)
	if m.descModel != "opus" {
		t.Errorf("descModel = %q, want 'opus'", m.descModel)
	}
}

func TestPublishBodyEditable(t *testing.T) {
	m := newTestPublishModel("feat-1", "diff", "log", "title", "body content", 120, 40)

	// Advance to PR desc step
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // to commits
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // to PR desc

	if m.step != publishStepPRDesc {
		t.Fatalf("expected PR desc step, got %d", m.step)
	}

	// Initially editing title (not body)
	if m.editingBody {
		t.Error("expected editingBody to be false initially")
	}

	// Tab should switch to body editing
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if !m.editingBody {
		t.Error("expected editingBody to be true after Tab")
	}

	// Tab again should switch back to title editing
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.editingBody {
		t.Error("expected editingBody to be false after second Tab")
	}
}

func TestPublishExecuteWithoutGitConfig(t *testing.T) {
	// Create model without git config
	m := newTestPublishModel("feat-1", "diff", "log", "title", "body", 120, 40)

	// Advance to execute step
	m.step = publishStepConfirm
	m, cmd := m.advanceStep()

	if m.step != publishStepExecute {
		t.Errorf("expected execute step, got %d", m.step)
	}

	// Execute the command, should return error about missing config
	if cmd != nil {
		msg := cmd()
		result, ok := msg.(publishExecuteResultMsg)
		if !ok {
			t.Fatalf("expected publishExecuteResultMsg, got %T", msg)
		}
		if result.err == nil {
			t.Error("expected error for missing worktree config")
		}
	}
}

func TestPublishStepRepoSelect_MultiRepo(t *testing.T) {
	repos := []publishRepoEntry{
		{Name: "repo-a", Branch: "feature/test", WorktreeDir: "/tmp/a", RepoPath: "/tmp/a", PRStatus: "pending"},
		{Name: "repo-b", Branch: "feature/test", WorktreeDir: "/tmp/b", RepoPath: "/tmp/b", PRStatus: "published", PRURL: "https://github.com/org/repo-b/pull/42"},
		{Name: "repo-c", Branch: "feature/test", WorktreeDir: "/tmp/c", RepoPath: "/tmp/c", PRStatus: "pending"},
	}
	m := NewPublishModel(newTestFeatureFromRepos("feat-1", "My Feature", repos), repos, "", "", 80, 24)

	if m.step != publishStepRepoSelect {
		t.Errorf("step = %d, want publishStepRepoSelect (%d)", m.step, publishStepRepoSelect)
	}
	if !m.hasRepoSelect {
		t.Error("expected hasRepoSelect to be true")
	}

	// View should contain all repo names
	view := m.View()
	for _, r := range repos {
		if !strings.Contains(view, r.Name) {
			t.Errorf("view should contain repo name %q", r.Name)
		}
	}

	// View should show step 1/7
	if !strings.Contains(view, "1/7") {
		t.Errorf("view should show step 1/7, got: %s", view)
	}
}

func TestPublishStepRepoSelect_SingleRepo(t *testing.T) {
	repos := []publishRepoEntry{
		{Name: "repo-a", Branch: "feature/test", WorktreeDir: "/tmp/a", RepoPath: "/tmp/a", PRStatus: "pending"},
	}
	m := NewPublishModel(newTestFeatureFromRepos("feat-1", "My Feature", repos), repos, "", "", 80, 24)

	if m.step != publishStepDiff {
		t.Errorf("step = %d, want publishStepDiff (%d)", m.step, publishStepDiff)
	}
	if m.hasRepoSelect {
		t.Error("expected hasRepoSelect to be false for single repo")
	}

	// View should show step 1/6 (not 1/7)
	view := m.View()
	if !strings.Contains(view, "1/6") {
		t.Errorf("view should show step 1/6 for single repo, got: %s", view)
	}
}

func TestPublishRepoSelectView(t *testing.T) {
	repos := []publishRepoEntry{
		{Name: "repo-a", Branch: "feature/slug", PRStatus: "published", PRURL: "https://github.com/org/repo/pull/42"},
		{Name: "repo-b", Branch: "feature/slug", PRStatus: "pending"},
	}
	m := NewPublishModel(newTestFeatureFromRepos("feat-1", "My Feature", repos), repos, "", "", 80, 24)

	view := m.View()
	if !strings.Contains(view, "Select Repository") {
		t.Error("view should contain 'Select Repository'")
	}
	if !strings.Contains(view, "repo-a") {
		t.Error("view should contain repo-a")
	}
	if !strings.Contains(view, "repo-b") {
		t.Error("view should contain repo-b")
	}
	if !strings.Contains(view, "PR #42") {
		t.Error("published repo should show PR number")
	}
	if !strings.Contains(view, "pending") {
		t.Error("pending repo should show 'pending'")
	}
}

func TestPublishRepoSelectNavigation(t *testing.T) {
	repos := []publishRepoEntry{
		{Name: "repo-a", Branch: "feature/test", PRStatus: "pending"},
		{Name: "repo-b", Branch: "feature/test", PRStatus: "pending"},
		{Name: "repo-c", Branch: "feature/test", PRStatus: "pending"},
	}
	m := NewPublishModel(newTestFeatureFromRepos("feat-1", "My Feature", repos), repos, "", "", 80, 24)

	if m.selectedRepo != 0 {
		t.Fatalf("initial selectedRepo = %d, want 0", m.selectedRepo)
	}

	// Down, Down
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.selectedRepo != 2 {
		t.Errorf("after 2x down: selectedRepo = %d, want 2", m.selectedRepo)
	}

	// Down again — should clamp
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.selectedRepo != 2 {
		t.Errorf("after 3x down (clamp): selectedRepo = %d, want 2", m.selectedRepo)
	}

	// Up
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.selectedRepo != 1 {
		t.Errorf("after up: selectedRepo = %d, want 1", m.selectedRepo)
	}

	// Up, Up — should clamp at 0
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.selectedRepo != 0 {
		t.Errorf("after up up (clamp): selectedRepo = %d, want 0", m.selectedRepo)
	}
}

func TestPublishMultiRepoStepProgression(t *testing.T) {
	repos := []publishRepoEntry{
		{Name: "repo-a", Branch: "feature/test", PRStatus: "pending"},
		{Name: "repo-b", Branch: "feature/test", PRStatus: "pending"},
	}
	m := NewPublishModel(newTestFeatureFromRepos("feat-1", "My Feature", repos), repos, "", "", 80, 24)

	// Start at repo select
	if m.step != publishStepRepoSelect {
		t.Fatalf("initial step = %d, want publishStepRepoSelect", m.step)
	}

	// Press enter to advance from repo select to diff
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.step != publishStepDiff {
		t.Errorf("after enter from repo select: step = %d, want publishStepDiff (%d)", m.step, publishStepDiff)
	}
	if m.repoName != "repo-a" {
		t.Errorf("repoName = %q, want 'repo-a'", m.repoName)
	}

	// Enter to advance to commits
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.step != publishStepCommits {
		t.Errorf("step = %d, want publishStepCommits (%d)", m.step, publishStepCommits)
	}
}

// TestPublishExecuteErrorCarriesRepoName verifies that all error paths in
// executePublish carry the repoName field so the app handler can record
// repo-scoped publish errors instead of failing the entire feature.
func TestPublishExecuteErrorCarriesRepoName(t *testing.T) {
	m := newTestPublishModel("feat-1", "diff", "log", "title", "body", 80, 24)
	m.repoName = "repo-a"
	m.worktreeDir = "/nonexistent/path"
	m.branch = "feature/test"

	// Execute publish — will fail because the paths don't exist.
	m.step = publishStepConfirm
	m, cmd := m.advanceStep()
	if cmd == nil {
		t.Fatal("expected a command from advanceStep at confirm step")
	}

	// Execute the command to get the result message.
	result := cmd()
	msg, ok := result.(publishExecuteResultMsg)
	if !ok {
		t.Fatalf("expected publishExecuteResultMsg, got %T", result)
	}

	// The error should be set (some step will fail with nonexistent paths).
	if msg.err == nil {
		t.Fatal("expected an error from executePublish with nonexistent paths")
	}
	// The repoName must be carried through on error paths.
	if msg.repoName != "repo-a" {
		t.Errorf("repoName = %q, want %q; error results must carry repoName for repo-scoped error handling", msg.repoName, "repo-a")
	}
}

func TestPublishRepoSelectPublishedRepoSelectable(t *testing.T) {
	repos := []publishRepoEntry{
		{Name: "repo-a", Branch: "feature/test", WorktreeDir: "/tmp/a", RepoPath: "/tmp/a", PRStatus: "pending"},
		{Name: "repo-b", Branch: "feature/test", WorktreeDir: "/tmp/b", RepoPath: "/tmp/b", PRStatus: "published", PRURL: "https://github.com/org/repo-b/pull/99"},
		{Name: "repo-c", Branch: "feature/test", WorktreeDir: "/tmp/c", RepoPath: "/tmp/c", PRStatus: "pending"},
	}
	m := NewPublishModel(newTestFeatureFromRepos("feat-1", "My Feature", repos), repos, "", "", 80, 24)

	// Move down to the published repo (index 1)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.selectedRepo != 1 {
		t.Fatalf("after down: selectedRepo = %d, want 1", m.selectedRepo)
	}

	// Press Enter on the published repo — should advance to diff (selectable for re-publish)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.step != publishStepDiff {
		t.Errorf("pressing Enter on published repo: step = %d, want publishStepDiff (%d)", m.step, publishStepDiff)
	}
	if m.repoName != "repo-b" {
		t.Errorf("repoName = %q, want %q", m.repoName, "repo-b")
	}
	if m.existingPRURL != "https://github.com/org/repo-b/pull/99" {
		t.Errorf("existingPRURL = %q, want existing PR URL for re-publish", m.existingPRURL)
	}
}

func TestPublishRepoSelectPendingRepoNoExistingPRURL(t *testing.T) {
	repos := []publishRepoEntry{
		{Name: "repo-a", Branch: "feature/test", WorktreeDir: "/tmp/a", RepoPath: "/tmp/a", PRStatus: "pending"},
		{Name: "repo-b", Branch: "feature/test", WorktreeDir: "/tmp/b", RepoPath: "/tmp/b", PRStatus: "published", PRURL: "https://github.com/org/repo-b/pull/99"},
	}
	m := NewPublishModel(newTestFeatureFromRepos("feat-1", "My Feature", repos), repos, "", "", 80, 24)

	// Select the pending repo (index 0) and press Enter
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.step != publishStepDiff {
		t.Errorf("step = %d, want publishStepDiff", m.step)
	}
	if m.existingPRURL != "" {
		t.Errorf("existingPRURL = %q, want empty for pending repo", m.existingPRURL)
	}
}

func TestPublishRepoSelectViewShowsPRNumber(t *testing.T) {
	repos := []publishRepoEntry{
		{Name: "repo-a", Branch: "feature/test", PRStatus: "published", PRURL: "https://github.com/org/repo/pull/99"},
		{Name: "repo-b", Branch: "feature/test", PRStatus: "pending"},
	}
	m := NewPublishModel(newTestFeatureFromRepos("feat-1", "My Feature", repos), repos, "", "", 80, 24)
	view := m.View()

	// Published repo should show PR number without "(done)" — it's still selectable
	if !strings.Contains(view, "PR #99") {
		t.Error("view should contain 'PR #99' for published repo")
	}
	if strings.Contains(view, "(done)") {
		t.Error("view should NOT contain '(done)' — published repos are selectable for re-publish")
	}
}

func TestPublishRepublishSetsExistingPRURL(t *testing.T) {
	// Verify that executePublish uses existingPRURL for re-publish instead of CreatePR
	m := newTestPublishModel("feat-1", "diff", "log", "Updated Title", "Updated body", 80, 24)
	m.repoName = "repo-b"
	m.worktreeDir = "/nonexistent/path"
	m.branch = "feature/test"
	m.existingPRURL = "https://github.com/org/repo-b/pull/99"

	// Execute publish — will fail at push step because paths don't exist,
	// but we verify the existingPRURL field is correctly set on the model
	if m.existingPRURL != "https://github.com/org/repo-b/pull/99" {
		t.Errorf("existingPRURL = %q, want existing URL", m.existingPRURL)
	}

	// Also verify that advanceStep from confirm correctly invokes executePublish
	m.step = publishStepConfirm
	m, cmd := m.advanceStep()
	if m.step != publishStepExecute {
		t.Errorf("step = %d, want publishStepExecute", m.step)
	}
	if cmd == nil {
		t.Fatal("expected command from advanceStep at confirm")
	}

	// The cmd will fail (nonexistent paths), but the result should carry repoName
	result := cmd()
	msg, ok := result.(publishExecuteResultMsg)
	if !ok {
		t.Fatalf("expected publishExecuteResultMsg, got %T", result)
	}
	if msg.repoName != "repo-b" {
		t.Errorf("repoName = %q, want %q", msg.repoName, "repo-b")
	}
}

// TestPublishRepoSelectView_PublishErrorShowsFailed verifies that when a
// publishRepoEntry has PRStatus = "failed", the repo selector view renders
// "failed" for that repo.
func TestPublishRepoSelectView_PublishErrorShowsFailed(t *testing.T) {
	repos := []publishRepoEntry{
		{Name: "repo-a", Branch: "feature/slug", PRStatus: "published", PRURL: "https://github.com/org/repo/pull/42"},
		{Name: "repo-b", Branch: "feature/slug", PRStatus: "failed"},
		{Name: "repo-c", Branch: "feature/slug", PRStatus: "pending"},
	}
	m := NewPublishModel(newTestFeatureFromRepos("feat-1", "My Feature", repos), repos, "", "", 80, 24)

	view := m.View()
	if !strings.Contains(view, "failed") {
		t.Error("view should contain 'failed' for repo with PRStatus=failed")
	}
	if !strings.Contains(view, "repo-b") {
		t.Error("view should contain repo-b")
	}
}

func TestPublishExecuteResultWithRepoName(t *testing.T) {
	m := newTestPublishModel("feat-1", "diff", "log", "title", "body", 80, 24)
	m.repoName = "repo-a"

	m, _ = m.Update(publishExecuteResultMsg{
		prURL:    "https://github.com/org/repo/pull/42",
		repoName: "repo-a",
	})

	if m.prURL != "https://github.com/org/repo/pull/42" {
		t.Errorf("prURL = %q, want URL", m.prURL)
	}
	if !m.IsDone() {
		t.Error("expected IsDone() to be true")
	}
}

func boolPtrPub(b bool) *bool { return &b }

func TestPublishExecuteGuardUnpublishable(t *testing.T) {
	m := newTestPublishModel("feat-1", "diff", "log", "title", "body", 80, 24)
	m.worktreeDir = "/tmp/nonexistent-wt"
	m.branch = "feature/test"
	m.publishable = false

	cmd := m.executePublish()
	if cmd == nil {
		t.Fatal("expected non-nil cmd")
	}
	result := cmd()
	msg, ok := result.(publishExecuteResultMsg)
	if !ok {
		t.Fatalf("expected publishExecuteResultMsg, got %T", result)
	}
	if msg.err == nil {
		t.Fatal("expected error for unpublishable feature")
	}
	if !strings.Contains(msg.err.Error(), "not publishable") {
		t.Errorf("error = %q, want to contain 'not publishable'", msg.err.Error())
	}
}

func TestPublishExecuteGuardPublishable(t *testing.T) {
	m := newTestPublishModel("feat-1", "diff", "log", "title", "body", 80, 24)
	m.worktreeDir = "/tmp/nonexistent-wt"
	m.branch = "feature/test"
	// publishable defaults to true — don't set it

	cmd := m.executePublish()
	if cmd == nil {
		t.Fatal("expected non-nil cmd")
	}
	result := cmd()
	msg, ok := result.(publishExecuteResultMsg)
	if !ok {
		t.Fatalf("expected publishExecuteResultMsg, got %T", result)
	}
	// Should fail at git ops, NOT at publishability guard
	if msg.err == nil {
		t.Fatal("expected error (git ops should fail)")
	}
	if strings.Contains(msg.err.Error(), "not publishable") {
		t.Error("publishable feature should not be blocked by publishability guard")
	}
}

func TestPublishExecuteUsesForcePushWithLease(t *testing.T) {
	oldPush := git.PushFunc
	oldForcePush := git.ForcePushFunc
	t.Cleanup(func() {
		git.PushFunc = oldPush
		git.ForcePushFunc = oldForcePush
	})

	wantErr := errors.New("lease push attempted")
	git.PushFunc = func(worktreePath, branch string) error {
		t.Fatalf("executePublish must not plain-push %s from %s", branch, worktreePath)
		return nil
	}
	git.ForcePushFunc = func(worktreePath, branch string) error {
		if worktreePath != "/tmp/nonexistent-wt" || branch != "feature/test" {
			t.Fatalf("ForcePush(%q, %q), want /tmp/nonexistent-wt feature/test", worktreePath, branch)
		}
		return wantErr
	}

	m := newTestPublishModel("feat-1", "diff", "log", "title", "body", 80, 24)
	m.worktreeDir = "/tmp/nonexistent-wt"
	m.branch = "feature/test"
	m.leasePush = true

	result := m.executePublish()()
	msg, ok := result.(publishExecuteResultMsg)
	if !ok {
		t.Fatalf("expected publishExecuteResultMsg, got %T", result)
	}
	if msg.err == nil {
		t.Fatal("expected force push error")
	}
	if !strings.Contains(msg.err.Error(), wantErr.Error()) {
		t.Fatalf("error = %q, want force push sentinel", msg.err.Error())
	}
}

func TestPublishModelPublishableField(t *testing.T) {
	m := newTestPublishModel("feat-1", "diff", "log", "title", "body", 80, 24)
	if !m.publishable {
		t.Error("NewPublishModel should default publishable to true")
	}

	m.publishable = false
	if m.publishable {
		t.Error("publishable should be settable to false")
	}
}

func TestPublishGuardConditionUnpublishable(t *testing.T) {
	f := &feature.Feature{
		Status: feature.StatusCodeReady,
		Repos: []feature.FeatureRepo{
			{Name: "repo-a", Publishable: boolPtrPub(false)},
		},
	}
	f.Checkpoints.ManualPublish = true

	if f.IsPublishable() {
		t.Error("feature with Publishable=false should not be publishable")
	}
}

func TestPublishGuardConditionPublishable(t *testing.T) {
	f := &feature.Feature{
		Status: feature.StatusCodeReady,
		Repos: []feature.FeatureRepo{
			{Name: "repo-a"}, // Publishable: nil = publishable
		},
	}
	f.Checkpoints.ManualPublish = true

	if !f.IsPublishable() {
		t.Error("feature with Publishable=nil should be publishable")
	}
}

func TestPublishGuardConditionMixedRepos(t *testing.T) {
	f := &feature.Feature{
		Status: feature.StatusCodeReady,
		Repos: []feature.FeatureRepo{
			{Name: "repo-a"}, // Publishable: nil = publishable
			{Name: "repo-b", Publishable: boolPtrPub(false)},
		},
	}
	f.Checkpoints.ManualPublish = true

	if f.IsPublishable() {
		t.Error("feature with mixed publishable repos should not be publishable")
	}
}

func TestNewPublishModel_SingleRepoEntry_AutoSelects(t *testing.T) {
	f := &feature.Feature{ID: "feat-1", Name: "Feature Name", Repos: []feature.FeatureRepo{{Name: "r1"}}}
	repos := []publishRepoEntry{{
		Name:        "r1",
		Branch:      "feature/x",
		WorktreeDir: "/tmp/wt",
		RepoPath:    "/tmp/repo",
		BaseBranch:  "main",
		PRStatus:    "pending",
	}}
	m := NewPublishModel(f, repos, "", "", 80, 24)

	if m.step != publishStepDiff {
		t.Errorf("step = %d, want publishStepDiff (%d)", m.step, publishStepDiff)
	}
	if m.hasRepoSelect {
		t.Error("hasRepoSelect = true, want false (chrome hidden for single-repo feature)")
	}
	if m.repoName != "r1" {
		t.Errorf("repoName = %q, want r1", m.repoName)
	}
	if m.featureID != "feat-1" {
		t.Errorf("featureID = %q, want feat-1", m.featureID)
	}
	if m.featureName != "Feature Name" {
		t.Errorf("featureName = %q, want Feature Name", m.featureName)
	}
	view := m.View()
	if !strings.Contains(view, "1/6") {
		t.Errorf("view = %q, want it to show step 1/6 for single-repo (no repo-select step)", view)
	}
	if strings.Contains(view, "1/7") {
		t.Errorf("view = %q, did not expect step 1/7 for single-repo", view)
	}
}

func TestNewPublishModel_MultipleRepoEntries_StartsAtSelector(t *testing.T) {
	f := &feature.Feature{
		ID:    "feat-1",
		Name:  "Feature Name",
		Repos: []feature.FeatureRepo{{Name: "r1"}, {Name: "r2"}},
	}
	repos := []publishRepoEntry{
		{Name: "r1", Branch: "feature/test", PRStatus: "pending"},
		{Name: "r2", Branch: "feature/test", PRStatus: "pending"},
	}
	m := NewPublishModel(f, repos, "", "", 80, 24)

	if m.step != publishStepRepoSelect {
		t.Errorf("step = %d, want publishStepRepoSelect (%d)", m.step, publishStepRepoSelect)
	}
	if !m.hasRepoSelect {
		t.Error("hasRepoSelect = false, want true (chrome shown for multi-repo feature)")
	}
	if m.repoName != "" {
		t.Errorf("repoName = %q, want empty until user selects", m.repoName)
	}
	view := m.View()
	if !strings.Contains(view, "1/7") {
		t.Errorf("view = %q, want it to show step 1/7 for multi-repo", view)
	}
}

func TestNewPublishModel_ChromeGateConsultsHelper(t *testing.T) {
	tests := []struct {
		name              string
		featureRepoCount  int
		entriesCount      int
		wantHasRepoSelect bool
	}{
		{"chrome=false (N=1) with 1 entry → auto-select", 1, 1, false},
		{"chrome=true (N=2) with 2 entries → selector", 2, 2, true},
		{"chrome=true (N=2) with 1 entry → selector (chrome wins)", 2, 1, true},
		{"chrome=false (N=1) with 2 entries → auto-select (chrome wins)", 1, 2, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &feature.Feature{ID: "feat-1", Name: "Test"}
			for i := 0; i < tt.featureRepoCount; i++ {
				f.Repos = append(f.Repos, feature.FeatureRepo{Name: fmt.Sprintf("r%d", i)})
			}
			repos := make([]publishRepoEntry, 0, tt.entriesCount)
			for i := 0; i < tt.entriesCount; i++ {
				repos = append(repos, publishRepoEntry{Name: fmt.Sprintf("r%d", i), Branch: "x", PRStatus: "pending"})
			}
			m := NewPublishModel(f, repos, "", "", 80, 24)
			if m.hasRepoSelect != tt.wantHasRepoSelect {
				t.Errorf("hasRepoSelect = %v, want %v (chrome decision must route through len(f.Repos) > 1, not len(repos))", m.hasRepoSelect, tt.wantHasRepoSelect)
			}
		})
	}
}

func TestNewPublishModel_ZeroRepos_StaysAtDiff(t *testing.T) {
	f := &feature.Feature{ID: "feat-1", Name: "Empty"}
	m := NewPublishModel(f, nil, "", "", 80, 24)
	if m.step != publishStepDiff {
		t.Errorf("step = %d, want publishStepDiff fallback for zero repos", m.step)
	}
	if m.hasRepoSelect {
		t.Error("hasRepoSelect = true, want false for zero-repo degenerate feature")
	}
	view := m.View()
	if !strings.Contains(view, "1/6") {
		t.Errorf("view = %q, want it to show 1/6 fallback", view)
	}
}

func TestNewPublishViewport_BuildsViewportTitleBody(t *testing.T) {
	vp, ti, ta := newPublishViewport(120, 40, "My Title", "My Body", "+++ diff +++")
	if vp.Width() != max(120-6, 40) {
		t.Errorf("viewport.Width = %d, want %d", vp.Width(), max(120-6, 40))
	}
	if vp.Height() != max(40-8, 10) {
		t.Errorf("viewport.Height = %d, want %d", vp.Height(), max(40-8, 10))
	}
	if ti.Value() != "My Title" {
		t.Errorf("titleInput value = %q, want My Title", ti.Value())
	}
	if ta.Value() != "My Body" {
		t.Errorf("bodyInput value = %q, want My Body", ta.Value())
	}
}

// ---------------------------------------------------------------------------
// DraftPublish field tests
// ---------------------------------------------------------------------------

func TestNewPublishModel_DraftFromFeatureCheckpoints_True(t *testing.T) {
	f := &feature.Feature{
		ID:    "feat-draft",
		Repos: []feature.FeatureRepo{{Name: "r1"}},
		Checkpoints: feature.Checkpoints{
			DraftPublish: true,
		},
	}
	repos := []publishRepoEntry{{
		Name: "r1", Branch: "feature/x", WorktreeDir: "/tmp/wt", RepoPath: "/tmp/repo",
	}}
	m := NewPublishModel(f, repos, "", "", 80, 24)
	if !m.draft {
		t.Error("m.draft should be true when feature Checkpoints.DraftPublish is true")
	}
}

func TestNewPublishModel_DraftFromFeatureCheckpoints_False(t *testing.T) {
	f := &feature.Feature{
		ID:    "feat-nodraft",
		Repos: []feature.FeatureRepo{{Name: "r1"}},
		// DraftPublish defaults to false
	}
	repos := []publishRepoEntry{{
		Name: "r1", Branch: "feature/x", WorktreeDir: "/tmp/wt", RepoPath: "/tmp/repo",
	}}
	m := NewPublishModel(f, repos, "", "", 80, 24)
	if m.draft {
		t.Error("m.draft should be false when feature Checkpoints.DraftPublish is false")
	}
}

func TestPublishModel_StepCounter_TruthTable(t *testing.T) {
	tests := []struct {
		name         string
		repoCount    int
		step         publishStep
		wantContains string
	}{
		{"1 repo, publishStepDiff → 1/6", 1, publishStepDiff, "1/6"},
		{"1 repo, publishStepCommits → 2/6", 1, publishStepCommits, "2/6"},
		{"1 repo, publishStepConfirm → 4/6", 1, publishStepConfirm, "4/6"},
		{"2 repos, publishStepRepoSelect → 1/7", 2, publishStepRepoSelect, "1/7"},
		{"2 repos, publishStepDiff → 2/7", 2, publishStepDiff, "2/7"},
		{"2 repos, publishStepCommits → 3/7", 2, publishStepCommits, "3/7"},
		{"2 repos, publishStepConfirm → 5/7", 2, publishStepConfirm, "5/7"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &feature.Feature{ID: "feat-1", Name: "Test"}
			repos := []publishRepoEntry{}
			for i := 0; i < tt.repoCount; i++ {
				f.Repos = append(f.Repos, feature.FeatureRepo{Name: fmt.Sprintf("r%d", i)})
				repos = append(repos, publishRepoEntry{Name: fmt.Sprintf("r%d", i), Branch: "x", PRStatus: "pending"})
			}
			m := NewPublishModel(f, repos, "", "", 80, 24)
			m.step = tt.step
			view := m.View()
			if !strings.Contains(view, tt.wantContains) {
				t.Errorf("view = %q, want it to contain %q", view, tt.wantContains)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Draft toggle tests
// ---------------------------------------------------------------------------

func TestPublishConfirmStep_DraftIndicator_InitialTrue(t *testing.T) {
	f := &feature.Feature{
		ID:    "feat-draft",
		Repos: []feature.FeatureRepo{{Name: "r1"}},
		Checkpoints: feature.Checkpoints{
			DraftPublish: true,
		},
	}
	repos := []publishRepoEntry{{
		Name: "r1", Branch: "feature/x", WorktreeDir: "/tmp/wt", RepoPath: "/tmp/repo",
	}}
	m := NewPublishModel(f, repos, "", "", 80, 24)
	m.step = publishStepConfirm

	view := m.View()
	if !strings.Contains(view, "Draft PR") {
		t.Error("confirm view should contain 'Draft PR' when m.draft is true")
	}
	if strings.Contains(view, "Ready for review") {
		t.Error("confirm view should not contain 'Ready for review' when m.draft is true")
	}
}

func TestPublishConfirmStep_DraftIndicator_InitialFalse(t *testing.T) {
	f := &feature.Feature{
		ID:    "feat-nodraft",
		Repos: []feature.FeatureRepo{{Name: "r1"}},
		// DraftPublish defaults to false
	}
	repos := []publishRepoEntry{{
		Name: "r1", Branch: "feature/x", WorktreeDir: "/tmp/wt", RepoPath: "/tmp/repo",
	}}
	m := NewPublishModel(f, repos, "", "", 80, 24)
	m.step = publishStepConfirm

	view := m.View()
	if !strings.Contains(view, "Ready for review") {
		t.Error("confirm view should contain 'Ready for review' when m.draft is false")
	}
	if strings.Contains(view, "Draft PR") {
		t.Error("confirm view should not contain 'Draft PR' when m.draft is false")
	}
}

func TestPublishConfirmStep_DraftIndicator_AlwaysPresent(t *testing.T) {
	m := newTestPublishModel("feat-1", "diff", "log", "title", "body", 80, 24)
	m.step = publishStepConfirm

	m.draft = false
	viewOff := m.View()
	m.draft = true
	viewOn := m.View()

	// Both views must contain an indicator line — no layout shift.
	if !strings.Contains(viewOff, "Ready for review") {
		t.Error("draft=false: confirm view missing 'Ready for review' indicator")
	}
	if !strings.Contains(viewOn, "Draft PR") {
		t.Error("draft=true: confirm view missing 'Draft PR' indicator")
	}
}

func TestPublishDraftToggle_TurnsOn(t *testing.T) {
	m := newTestPublishModel("feat-1", "diff", "log", "title", "body", 80, 24)
	m.step = publishStepConfirm
	m.draft = false

	m, _ = m.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})

	if !m.draft {
		t.Error("pressing 'd' at confirm step should set m.draft to true")
	}
	if m.step != publishStepConfirm {
		t.Errorf("step = %d after 'd' press, want publishStepConfirm (no step advance)", m.step)
	}
	view := m.View()
	if !strings.Contains(view, "Draft PR") {
		t.Error("view after toggle-on should contain 'Draft PR'")
	}
}

func TestPublishDraftToggle_TurnsOff(t *testing.T) {
	m := newTestPublishModel("feat-1", "diff", "log", "title", "body", 80, 24)
	m.step = publishStepConfirm
	m.draft = true

	m, _ = m.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})

	if m.draft {
		t.Error("pressing 'd' at confirm step should set m.draft to false")
	}
	if m.step != publishStepConfirm {
		t.Errorf("step = %d after 'd' press, want publishStepConfirm (no step advance)", m.step)
	}
	view := m.View()
	if !strings.Contains(view, "Ready for review") {
		t.Error("view after toggle-off should contain 'Ready for review'")
	}
}

func TestPublishDraftToggle_NoEffectOnOtherSteps(t *testing.T) {
	otherSteps := []publishStep{publishStepDiff, publishStepCommits, publishStepPRDesc}
	for _, step := range otherSteps {
		m := newTestPublishModel("feat-1", "diff", "log", "title", "body", 80, 24)
		m.step = step
		m.draft = false

		m, _ = m.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})

		if m.draft {
			t.Errorf("pressing 'd' at step %d should have no effect on m.draft", step)
		}
	}
}

func TestPublishConfirmStep_KeyFooter_IncludesToggleDraft(t *testing.T) {
	m := newTestPublishModel("feat-1", "diff", "log", "title", "body", 80, 24)
	m.step = publishStepConfirm

	view := m.View()
	if !strings.Contains(view, "[d] Toggle draft") {
		t.Error("confirm step footer should contain '[d] Toggle draft'")
	}
}

func TestPublishConfirmStep_EnterStillAdvances(t *testing.T) {
	m := newTestPublishModel("feat-1", "diff", "log", "title", "body", 80, 24)
	m.step = publishStepConfirm

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if m.step != publishStepExecute {
		t.Errorf("step = %d after Enter at confirm step, want publishStepExecute", m.step)
	}
}
