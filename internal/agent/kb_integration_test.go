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
	"errors"
	"os"
	"path/filepath"
	"slices"
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
				Protocol:      testSessionProtocol(opts.Prompt),
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

	sessionID, err := pr.RunKnowledgeBaseForRepo(f, f.Repos[0])
	if err != nil {
		t.Fatalf("RunKnowledgeBaseForRepo error: %v", err)
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
				Protocol:      testSessionProtocol(opts.Prompt),
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

	sessionID, err := pr.RunKnowledgeBaseForRepo(f, f.Repos[0])
	if err != nil {
		t.Fatalf("RunKnowledgeBaseForRepo error: %v", err)
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
	lockPath := kbLockPath(kbDir)
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
				Protocol:      testSessionProtocol(opts.Prompt),
			}
			return []string{"bash", kbScript}, nil, sessOpts, nil
		},
	}

	f := &feature.Feature{
		ID: "abc123",
		Repos: []feature.FeatureRepo{
			{Name: "my service", Path: repoPath},
		},
		Models: config.ModelConfig{Research: "test"},
	}

	repo := f.Repos[0]
	sessionID, err := pr.RunKnowledgeBaseForRepo(f, repo)
	if err != nil {
		t.Fatalf("RunKnowledgeBaseForRepo error: %v", err)
	}

	// Repository display names may contain characters that are not valid in an
	// IPC path segment. The session ID must remain safe for the desktop IPC and
	// HTTP boundaries while session metadata retains the canonical name.
	expectedID := buildKBSessionID("abc123", repo.Name)
	if sessionID != expectedID {
		t.Errorf("session ID = %q, want %q", sessionID, expectedID)
	}
	if !validKBSessionIDPattern.MatchString(sessionID) {
		t.Errorf("session ID = %q, want an IPC-safe path segment", sessionID)
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

// TestRunKnowledgeBaseForRepo_MountsKBDirWritable guards the regression where the
// KB output dir (a sibling of StateDir under knowledge-base/<repo>) was not mounted
// on the session, so managed provider edit permissions denied every KB write and
// the phase could never produce its index.md. The KB dir must be an AdditionalDir,
// which makes it both readable and (via the default writable-root derivation) writable.
func TestRunKnowledgeBaseForRepo_MountsKBDirWritable(t *testing.T) {
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

	var captured BuildSessionOpts
	pr := &PhaseRunner{
		SessionManager: sm,
		CommandRunner:  &execCommandRunner{},
		StateDir:       stateDir,
		BuildSessionFn: func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
			captured = opts
			sessOpts := &session.SessionOpts{
				PIDDir:        opts.PIDDir,
				PermHandler:   opts.PermHandler,
				InitialPrompt: opts.Prompt,
				RepoName:      opts.RepoName,
				LogPath:       opts.LogPath,
				Protocol:      testSessionProtocol(opts.Prompt),
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
	if _, err := pr.RunKnowledgeBaseForRepo(f, repo); err != nil {
		t.Fatalf("RunKnowledgeBaseForRepo error: %v", err)
	}

	kbDir := KBStateDir(stateDir, repo.Name)
	if !slices.Contains(captured.AdditionalDirs, kbDir) {
		t.Errorf("KB session AdditionalDirs = %v, want it to include kbDir %q so KB output writes are permitted", captured.AdditionalDirs, kbDir)
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
	locked, err := acquireKBLockWithStatus(kbDir, "other-feature", nil)
	if err != nil {
		t.Fatalf("AcquireKBLockWithStatus: %v", err)
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
