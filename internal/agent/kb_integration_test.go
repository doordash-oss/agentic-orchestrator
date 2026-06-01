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
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

func mockKBSessionScript(body string) string {
	return "read -r _\nread -r _\n" + body
}

func TestRunKnowledgeBase_NoRepos(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	pr := &PhaseRunner{
		SessionManager: sm,
		CommandRunner:  &execCommandRunner{},
		StateDir:       filepath.Join(tmpDir, "state"),
	}

	f := &feature.Feature{
		ID:    "test-kb-no-repos",
		Repos: []feature.FeatureRepo{},
	}

	_, err := pr.RunKnowledgeBase(f)
	if err == nil {
		t.Fatal("expected error for empty repos")
	}
	if !strings.Contains(err.Error(), "no repos") {
		t.Errorf("expected error to contain 'no repos', got: %v", err)
	}
}

func TestRunKnowledgeBase_FreshKBSkips(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Create a real git repo so IsKBFresh can check HEAD commit
	repoPath := testutil.InitGitRepo(t)

	tmpDir := t.TempDir()
	stateDir := filepath.Join(tmpDir, "state")
	os.MkdirAll(stateDir, 0o755)

	// Set up KB directory with current commit so IsKBFresh returns true
	kbDir := KBStateDir(stateDir, "test-repo")
	os.MkdirAll(kbDir, 0o755)

	// Write index.md file
	os.WriteFile(KBPath(kbDir), []byte("# Knowledge Base\n"), 0o644)

	// Write state.json with current HEAD commit
	currentCommit := testutil.CommitFile(t, repoPath, "test.txt", "content", "test commit")
	SaveKBState(kbDir, &KBState{HeadCommit: currentCommit})

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	pr := &PhaseRunner{
		SessionManager: sm,
		CommandRunner:  &execCommandRunner{},
		StateDir:       stateDir,
	}

	f := &feature.Feature{
		ID: "test-kb-fresh",
		Repos: []feature.FeatureRepo{
			{Name: "test-repo", Path: repoPath},
		},
		Models: config.ModelConfig{Research: "test"},
	}

	sessionID, err := pr.RunKnowledgeBase(f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sessionID != "" {
		t.Errorf("expected empty session ID for fresh KB, got %q", sessionID)
	}
}

func TestRunKnowledgeBase_IncrementalBuild(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	repoPath := testutil.InitGitRepo(t)

	tmpDir := t.TempDir()
	stateDir := filepath.Join(tmpDir, "state")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	os.MkdirAll(stateDir, 0o755)
	os.MkdirAll(scriptsDir, 0o755)

	// Set up KB directory with an old commit so IsKBFresh returns false (incremental)
	kbDir := KBStateDir(stateDir, "test-repo")
	os.MkdirAll(kbDir, 0o755)

	// Write existing index.md
	os.WriteFile(KBPath(kbDir), []byte("# Existing Knowledge Base\n"), 0o644)

	// Save state with the initial commit (will become stale after new commit)
	initialCommit := testutil.CommitFile(t, repoPath, "file1.txt", "content1", "initial file")
	SaveKBState(kbDir, &KBState{HeadCommit: initialCommit})

	// Add a new commit to make the KB stale
	testutil.CommitFile(t, repoPath, "file2.txt", "content2", "second file")

	// Capture the prompt to verify incremental mode context
	var capturedPrompt string
	kbScript := testutil.WriteScript(t, scriptsDir, "kb.sh",
		mockKBSessionScript(testutil.JSONLInit+"\n"+testutil.JSONLAssistant("Updating knowledge base...")+"\n"+testutil.JSONLSuccess+"\n"))

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	pr := &PhaseRunner{
		SessionManager: sm,
		CommandRunner:  &execCommandRunner{},
		StateDir:       stateDir,
		BuildSessionFn: func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
			capturedPrompt = opts.Prompt
			sessOpts := &session.SessionOpts{
				PIDDir:        opts.PIDDir,
				PermHandler:   opts.PermHandler,
				InitialPrompt: opts.Prompt,
				RepoName:      opts.RepoName,
				LogPath:       opts.LogPath,
			}
			return []string{"bash", kbScript}, nil, sessOpts, nil
		},
	}

	f := &feature.Feature{
		ID: "test-kb-incremental",
		Repos: []feature.FeatureRepo{
			{Name: "test-repo", Path: repoPath},
		},
		Models: config.ModelConfig{Research: "test"},
	}

	sessionID, err := pr.RunKnowledgeBase(f)
	if err != nil {
		t.Fatalf("RunKnowledgeBase error: %v", err)
	}
	if sessionID == "" {
		t.Fatal("expected non-empty session ID for incremental build")
	}

	// Verify the prompt includes existingKBPath context for incremental mode
	if !strings.Contains(capturedPrompt, "INCREMENTAL UPDATE") {
		t.Errorf("expected prompt to contain 'INCREMENTAL UPDATE' for incremental build, got prompt: %s", capturedPrompt[:min(200, len(capturedPrompt))])
	}
	if !strings.Contains(capturedPrompt, initialCommit) {
		t.Errorf("expected prompt to contain initial commit %s for incremental build", initialCommit)
	}

	// Wait for session to complete
	sess := sm.GetSession(sessionID)
	if sess == nil {
		t.Fatal("session not found")
	}

	select {
	case <-sess.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for KB session")
	}
}

func TestRunKnowledgeBase_FullBuild(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	repoPath := testutil.InitGitRepo(t)

	tmpDir := t.TempDir()
	stateDir := filepath.Join(tmpDir, "state")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	os.MkdirAll(stateDir, 0o755)
	os.MkdirAll(scriptsDir, 0o755)

	kbScript := testutil.WriteScript(t, scriptsDir, "kb.sh",
		mockKBSessionScript(testutil.JSONLInit+"\n"+testutil.JSONLAssistant("Building knowledge base...")+"\n"+testutil.JSONLSuccess+"\n"))

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	pr := &PhaseRunner{
		SessionManager: sm,
		CommandRunner:  &execCommandRunner{},
		StateDir:       stateDir,
		BuildSessionFn: func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
			sessOpts := &session.SessionOpts{
				PIDDir:        opts.PIDDir,
				PermHandler:   opts.PermHandler,
				InitialPrompt: opts.Prompt,
				RepoName:      opts.RepoName,
				LogPath:       opts.LogPath,
			}
			return []string{"bash", kbScript}, nil, sessOpts, nil
		},
	}

	f := &feature.Feature{
		ID: "test-kb-full",
		Repos: []feature.FeatureRepo{
			{Name: "test-repo", Path: repoPath},
		},
		Models: config.ModelConfig{Research: "test"},
	}

	sessionID, err := pr.RunKnowledgeBase(f)
	if err != nil {
		t.Fatalf("RunKnowledgeBase error: %v", err)
	}
	if sessionID == "" {
		t.Fatal("expected non-empty session ID for full build")
	}

	// Wait for session to complete
	sess := sm.GetSession(sessionID)
	if sess == nil {
		t.Fatal("session not found")
	}

	select {
	case <-sess.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for KB session")
	}

	// Verify lock was released after session cleanup.
	// The session's AddCleanupFunc registers ReleaseKBLock, which runs on exit.
	kbDir := KBStateDir(stateDir, "test-repo")
	lockPath := KBLockPath(kbDir)
	if _, err := os.Stat(lockPath); err == nil {
		t.Error("expected KB lock to be released after session exit, but lock file still exists")
	}
}

func TestRunKnowledgeBaseForRepo_SessionID(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	repoPath := testutil.InitGitRepo(t)

	tmpDir := t.TempDir()
	stateDir := filepath.Join(tmpDir, "state")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	os.MkdirAll(stateDir, 0o755)
	os.MkdirAll(scriptsDir, 0o755)

	kbScript := testutil.WriteScript(t, scriptsDir, "kb.sh",
		mockKBSessionScript(testutil.JSONLInit+"\n"+testutil.JSONLAssistant("Building KB...")+"\n"+testutil.JSONLSuccess+"\n"))

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	pr := &PhaseRunner{
		SessionManager: sm,
		CommandRunner:  &execCommandRunner{},
		StateDir:       stateDir,
		BuildSessionFn: func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
			sessOpts := &session.SessionOpts{
				PIDDir:        opts.PIDDir,
				PermHandler:   opts.PermHandler,
				InitialPrompt: opts.Prompt,
				RepoName:      opts.RepoName,
				LogPath:       opts.LogPath,
			}
			return []string{"bash", kbScript}, nil, sessOpts, nil
		},
	}

	f := &feature.Feature{
		ID: "abc123",
		Repos: []feature.FeatureRepo{
			{Name: "my-service", Path: repoPath},
		},
		Models: config.ModelConfig{Research: "test"},
	}

	repo := f.Repos[0]
	sessionID, err := pr.RunKnowledgeBaseForRepo(f, repo)
	if err != nil {
		t.Fatalf("RunKnowledgeBaseForRepo error: %v", err)
	}

	// Verify session ID format: "<featureID>-kb-<repoName>"
	expectedID := "abc123-kb-my-service"
	if sessionID != expectedID {
		t.Errorf("session ID = %q, want %q", sessionID, expectedID)
	}

	// Wait for session to complete
	sess := sm.GetSession(sessionID)
	if sess == nil {
		t.Fatal("session not found")
	}
	select {
	case <-sess.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for KB session")
	}
}

func TestRunKnowledgeBaseForRepo_RequiresLockOwnership(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	repoPath := testutil.InitGitRepo(t)

	tmpDir := t.TempDir()
	stateDir := filepath.Join(tmpDir, "state")
	os.MkdirAll(stateDir, 0o755)

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	pr := &PhaseRunner{
		SessionManager: sm,
		CommandRunner:  &execCommandRunner{},
		StateDir:       stateDir,
	}

	kbDir := KBStateDir(stateDir, "locked-repo")
	locked, err := AcquireKBLock(kbDir, "other-feature")
	if err != nil {
		t.Fatalf("AcquireKBLock: %v", err)
	}
	if !locked {
		t.Fatal("expected other feature to acquire KB lock")
	}

	f := &feature.Feature{
		ID: "blocked-feature",
		Repos: []feature.FeatureRepo{
			{Name: "locked-repo", Path: repoPath},
		},
		Models: config.ModelConfig{Research: "test"},
	}

	sessionID, err := pr.RunKnowledgeBaseForRepo(f, f.Repos[0])
	if !errors.Is(err, ErrKBLocked) {
		t.Fatalf("RunKnowledgeBaseForRepo error = %v, want ErrKBLocked", err)
	}
	if sessionID != "" {
		t.Fatalf("expected no KB session to start, got %q", sessionID)
	}
	if sess := sm.GetSession("blocked-feature-kb-locked-repo"); sess != nil {
		t.Fatal("unexpected KB session started without lock ownership")
	}
}

func TestRunKnowledgeBaseForRepo_FreshSkips(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	repoPath := testutil.InitGitRepo(t)

	tmpDir := t.TempDir()
	stateDir := filepath.Join(tmpDir, "state")
	os.MkdirAll(stateDir, 0o755)

	// Set up KB directory with current commit so IsKBFresh returns true
	kbDir := KBStateDir(stateDir, "fresh-repo")
	os.MkdirAll(kbDir, 0o755)
	os.WriteFile(KBPath(kbDir), []byte("# Knowledge Base\n"), 0o644)

	currentCommit := testutil.CommitFile(t, repoPath, "test.txt", "content", "test commit")
	SaveKBState(kbDir, &KBState{HeadCommit: currentCommit})

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	pr := &PhaseRunner{
		SessionManager: sm,
		CommandRunner:  &execCommandRunner{},
		StateDir:       stateDir,
	}

	f := &feature.Feature{
		ID:     "test-fresh-repo",
		Models: config.ModelConfig{Research: "test"},
	}
	repo := feature.FeatureRepo{Name: "fresh-repo", Path: repoPath}

	sessionID, err := pr.RunKnowledgeBaseForRepo(f, repo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sessionID != "" {
		t.Errorf("expected empty session ID for fresh KB, got %q", sessionID)
	}
}

func TestRunAllKnowledgeBuilds_MultiRepo(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	repo1Path := testutil.InitGitRepo(t)
	repo2Path := testutil.InitGitRepo(t)

	tmpDir := t.TempDir()
	stateDir := filepath.Join(tmpDir, "state")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	os.MkdirAll(stateDir, 0o755)
	os.MkdirAll(scriptsDir, 0o755)

	kbScript := testutil.WriteScript(t, scriptsDir, "kb.sh",
		mockKBSessionScript(testutil.JSONLInit+"\n"+testutil.JSONLAssistant("Building KB...")+"\n"+testutil.JSONLSuccess+"\n"))

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	pr := &PhaseRunner{
		SessionManager: sm,
		CommandRunner:  &execCommandRunner{},
		StateDir:       stateDir,
		BuildSessionFn: func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
			sessOpts := &session.SessionOpts{
				PIDDir:        opts.PIDDir,
				PermHandler:   opts.PermHandler,
				InitialPrompt: opts.Prompt,
				RepoName:      opts.RepoName,
				LogPath:       opts.LogPath,
			}
			return []string{"bash", kbScript}, nil, sessOpts, nil
		},
	}

	f := &feature.Feature{
		ID: "multi-repo-test",
		Repos: []feature.FeatureRepo{
			{Name: "service-a", Path: repo1Path},
			{Name: "service-b", Path: repo2Path},
		},
		Models: config.ModelConfig{Research: "test"},
	}

	sessions, err := pr.RunAllKnowledgeBuilds(f)
	if err != nil {
		t.Fatalf("RunAllKnowledgeBuilds error: %v", err)
	}

	// Both repos should have started sessions
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d: %v", len(sessions), sessions)
	}

	// Verify session IDs use the correct repo-specific format
	expectedA := "multi-repo-test-kb-service-a"
	expectedB := "multi-repo-test-kb-service-b"
	if sessions["service-a"] != expectedA {
		t.Errorf("service-a session ID = %q, want %q", sessions["service-a"], expectedA)
	}
	if sessions["service-b"] != expectedB {
		t.Errorf("service-b session ID = %q, want %q", sessions["service-b"], expectedB)
	}

	// Wait for both sessions to complete
	for repoName, sid := range sessions {
		sess := sm.GetSession(sid)
		if sess == nil {
			t.Fatalf("session for %s not found", repoName)
		}
		select {
		case <-sess.Done():
		case <-time.After(10 * time.Second):
			t.Fatalf("timed out waiting for %s KB session", repoName)
		}
	}
}

func TestRunAllKnowledgeBuilds_PartialFresh(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	repo1Path := testutil.InitGitRepo(t)
	repo2Path := testutil.InitGitRepo(t)

	tmpDir := t.TempDir()
	stateDir := filepath.Join(tmpDir, "state")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	os.MkdirAll(stateDir, 0o755)
	os.MkdirAll(scriptsDir, 0o755)

	// Make repo1 fresh: set up KB directory with current commit
	kbDir1 := KBStateDir(stateDir, "fresh-svc")
	os.MkdirAll(kbDir1, 0o755)
	os.WriteFile(KBPath(kbDir1), []byte("# Knowledge Base\n"), 0o644)
	freshCommit := testutil.CommitFile(t, repo1Path, "test.txt", "content", "test commit")
	SaveKBState(kbDir1, &KBState{HeadCommit: freshCommit})

	// repo2 has no KB state, so it will need a build

	kbScript := testutil.WriteScript(t, scriptsDir, "kb.sh",
		mockKBSessionScript(testutil.JSONLInit+"\n"+testutil.JSONLAssistant("Building KB...")+"\n"+testutil.JSONLSuccess+"\n"))

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	pr := &PhaseRunner{
		SessionManager: sm,
		CommandRunner:  &execCommandRunner{},
		StateDir:       stateDir,
		BuildSessionFn: func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
			sessOpts := &session.SessionOpts{
				PIDDir:        opts.PIDDir,
				PermHandler:   opts.PermHandler,
				InitialPrompt: opts.Prompt,
				RepoName:      opts.RepoName,
				LogPath:       opts.LogPath,
			}
			return []string{"bash", kbScript}, nil, sessOpts, nil
		},
	}

	f := &feature.Feature{
		ID: "partial-fresh-test",
		Repos: []feature.FeatureRepo{
			{Name: "fresh-svc", Path: repo1Path},
			{Name: "stale-svc", Path: repo2Path},
		},
		Models: config.ModelConfig{Research: "test"},
	}

	sessions, err := pr.RunAllKnowledgeBuilds(f)
	if err != nil {
		t.Fatalf("RunAllKnowledgeBuilds error: %v", err)
	}

	// Only stale repo should have a session
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session (fresh repo skipped), got %d: %v", len(sessions), sessions)
	}
	if _, ok := sessions["fresh-svc"]; ok {
		t.Error("fresh-svc should have been skipped, but got a session")
	}
	if _, ok := sessions["stale-svc"]; !ok {
		t.Error("stale-svc should have a session, but was not found")
	}

	expectedID := "partial-fresh-test-kb-stale-svc"
	if sessions["stale-svc"] != expectedID {
		t.Errorf("stale-svc session ID = %q, want %q", sessions["stale-svc"], expectedID)
	}

	// Wait for the stale session to complete
	sess := sm.GetSession(sessions["stale-svc"])
	if sess == nil {
		t.Fatal("session for stale-svc not found")
	}
	select {
	case <-sess.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for stale-svc KB session")
	}
}

func TestRunCodebaseIndexForRepo_FreshKBStaleIndex(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// This regression test verifies that RunCodebaseIndexForRepo builds the
	// codebase index independently of KB freshness. The TUI calls it in every
	// KB path (including "all KBs fresh" and "no KB changes") to ensure the
	// structural index is always up-to-date.

	repoPath := testutil.InitGitRepo(t)
	originalRepoPath := t.TempDir()
	// Add a Go file so the codebase indexer has something to index
	testutil.CommitFile(t, repoPath, "main.go", "package main\nfunc main() {}\n", "add main.go")

	tmpDir := t.TempDir()
	stateDir := filepath.Join(tmpDir, "state")
	os.MkdirAll(stateDir, 0o755)

	// Set up KB directory as fresh (state.json + index.md with current commit)
	kbDir := KBStateDir(stateDir, "index-test-repo")
	os.MkdirAll(kbDir, 0o755)
	os.WriteFile(KBPath(kbDir), []byte("# Knowledge Base\n"), 0o644)
	currentCommit := testutil.CommitFile(t, repoPath, "data.txt", "data", "add data")
	SaveKBState(kbDir, &KBState{HeadCommit: currentCommit})

	// Verify KB is fresh but codebase index is NOT fresh
	runner := &execCommandRunner{}
	ctx := context.Background()
	if !IsKBFresh(ctx, runner, kbDir, repoPath) {
		t.Fatal("precondition: expected KB to be fresh")
	}
	if IsCodebaseIndexFresh(ctx, runner, kbDir, repoPath) {
		t.Fatal("precondition: expected codebase index to NOT be fresh")
	}

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	pr := &PhaseRunner{
		SessionManager: sm,
		CommandRunner:  runner,
		StateDir:       stateDir,
	}

	repo := feature.FeatureRepo{Name: "index-test-repo", Path: originalRepoPath, WorktreePath: repoPath}
	err := pr.RunCodebaseIndexForRepo(repo)
	if err != nil {
		t.Fatalf("RunCodebaseIndexForRepo error: %v", err)
	}

	// Verify codebase index was built
	if !IsCodebaseIndexFresh(ctx, runner, kbDir, repoPath) {
		t.Error("expected codebase index to be fresh after RunCodebaseIndexForRepo")
	}

	// Verify the index file was actually written
	idx, err := LoadCodebaseIndex(kbDir)
	if err != nil {
		t.Fatalf("LoadCodebaseIndex error: %v", err)
	}
	if idx == nil {
		t.Fatal("expected non-nil codebase index")
	}
}
