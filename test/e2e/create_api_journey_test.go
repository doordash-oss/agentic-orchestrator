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

package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
)

// createWireResult is the create-repository response on the wire.
type createWireResult struct {
	Result     string `json:"result"`
	Repository struct {
		RepoKey  string        `json:"repo_key"`
		Path     string        `json:"path"`
		HasHead  bool          `json:"has_head"`
		Root     string        `json:"root"`
		Identity *wireIdentity `json:"identity"`
	} `json:"repository"`
}

// create posts one repository-creation request with explicit consent.
func (j *cloneJourney) create(destination, key string, consent any) (int, []byte, createWireResult) {
	j.t.Helper()
	body := map[string]any{
		"root_path":       j.root,
		"destination":     destination,
		"idempotency_key": key,
		"consent":         consent,
	}
	status, raw := j.do("POST", "/api/v1/workspace/repositories/create", body)
	var parsed createWireResult
	_ = json.Unmarshal(raw, &parsed)
	return status, raw, parsed
}

// createJourneyGit runs one git command in dir with an isolated global
// config and returns its trimmed output. It deliberately does not reuse
// journeyGit: created-repository assertions must not inherit any test
// identity environment.
func createJourneyGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v: %s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// assertJourneyCreatedRepository proves the promised repository shape with
// real git: exactly one empty initial commit on main, Agentico's author and
// committer identity, no origin remote, a resolvable HEAD.
func assertJourneyCreatedRepository(t *testing.T, dir string) {
	t.Helper()
	if got := createJourneyGit(t, dir, "symbolic-ref", "--short", "HEAD"); got != "main" {
		t.Errorf("initial branch = %q; want main", got)
	}
	if got := createJourneyGit(t, dir, "rev-list", "--count", "HEAD"); got != "1" {
		t.Errorf("commit count = %q; want exactly one initial commit", got)
	}
	if got := createJourneyGit(t, dir, "log", "-1", "--format=%an <%ae>"); got != "Agentico <agentico@localhost>" {
		t.Errorf("author = %q; want Agentico <agentico@localhost>", got)
	}
	if got := createJourneyGit(t, dir, "log", "-1", "--format=%cn <%ce>"); got != "Agentico <agentico@localhost>" {
		t.Errorf("committer = %q; want Agentico <agentico@localhost>", got)
	}
	if got := createJourneyGit(t, dir, "show", "--name-only", "--format=", "HEAD"); got != "" {
		t.Errorf("initial commit is not empty: changed %q", got)
	}
	if got := createJourneyGit(t, dir, "remote"); got != "" {
		t.Errorf("remotes = %q; want none (no origin, no push)", got)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git", "HEAD")); err != nil {
		t.Errorf("HEAD does not resolve: %v", err)
	}
}

func TestCreateAPIJourney(t *testing.T) {
	if testing.Short() {
		t.Skip("journey exercises the real server and real git")
	}
	j := newCloneJourney(t)

	// Absent consent is rejected before any filesystem mutation.
	status, raw, _ := j.create("consentless", "key-consent", false)
	if status != http.StatusBadRequest {
		t.Fatalf("consentless create status = %d body=%s; want 400", status, raw)
	}
	if _, err := os.Lstat(filepath.Join(j.root, "consentless")); !os.IsNotExist(err) {
		t.Fatal("consentless create mutated the filesystem")
	}

	// A consented create publishes a feature-ready repository.
	status, raw, created := j.create("widget-fresh", "key-create-1", true)
	if status != http.StatusCreated {
		t.Fatalf("create status = %d body=%s; want 201", status, raw)
	}
	dest := filepath.Join(j.root, "widget-fresh")
	if created.Result != "created" || created.Repository.RepoKey != "widget-fresh" ||
		created.Repository.Path != dest || !created.Repository.HasHead || created.Repository.Root != j.root {
		t.Fatalf("create result = %+v", created)
	}
	if created.Repository.Identity == nil {
		t.Fatal("create response carries no server-resolved identity")
	}
	assertJourneyCreatedRepository(t, dest)

	// The response identity equals the live catalog identity, and the
	// repository is feature-ready there.
	catalogIdentity := j.repositoryIdentity("widget-fresh")
	if catalogIdentity == nil {
		t.Fatal("created repository missing from readiness")
	}
	if *catalogIdentity != *created.Repository.Identity {
		t.Fatalf("readiness identity %+v != response identity %+v", catalogIdentity, created.Repository.Identity)
	}

	// Same key, same input: the retained success replays; the repository
	// is never initialized again.
	status, raw, replay := j.create("widget-fresh", "key-create-1", true)
	if status != http.StatusCreated {
		t.Fatalf("replay status = %d body=%s; want 201", status, raw)
	}
	if replay.Repository.Identity == nil || *replay.Repository.Identity != *created.Repository.Identity {
		t.Fatalf("replay identity = %+v; want the retained %+v", replay.Repository.Identity, created.Repository.Identity)
	}
	if got := createJourneyGit(t, dest, "rev-list", "--count", "HEAD"); got != "1" {
		t.Fatalf("replay re-initialized the repository: commit count = %q", got)
	}

	// A different key for the same destination conflicts, and the existing
	// repository is untouched.
	status, raw, _ = j.create("widget-fresh", "key-create-2", true)
	if status != http.StatusConflict {
		t.Fatalf("duplicate create status = %d body=%s; want 409", status, raw)
	}
	assertJourneyCreatedRepository(t, dest)

	// Invalid child names keep their canonical rejection.
	status, raw, _ = j.create("bad/name", "key-create-3", true)
	if status != http.StatusBadRequest {
		t.Fatalf("invalid destination status = %d body=%s; want 400", status, raw)
	}

	// The created repository never appears in the clone operation listing.
	status, raw = j.do("GET", "/api/v1/workspace/repositories/clone?limit=200", nil)
	if status != http.StatusOK {
		t.Fatalf("clone list status = %d body=%s", status, raw)
	}
	var list struct {
		Operations []struct {
			ID          string `json:"id"`
			Destination string `json:"destination"`
		} `json:"operations"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	for _, op := range list.Operations {
		if op.Destination == "widget-fresh" {
			t.Fatalf("create leaked into the clone listing as %s", op.ID)
		}
	}
}

func TestCreateSharesDestinationReservationWithCloneJourney(t *testing.T) {
	if testing.Short() {
		t.Skip("journey exercises the real server and real git")
	}
	j := newCloneJourney(t)
	stall := stalledRemote(t)

	// A running clone holds the destination: create is refused.
	op := j.start(stall, "contested", "key-race-clone")
	waitRunning := time.Now().Add(30 * time.Second)
	for {
		snap := j.snapshot(op.ID)
		if snap.State == "running" {
			break
		}
		if time.Now().After(waitRunning) {
			t.Fatalf("clone never reached running; last state %s", snap.State)
		}
		time.Sleep(50 * time.Millisecond)
	}
	status, raw, _ := j.create("contested", "key-race-create", true)
	if status != http.StatusConflict {
		t.Fatalf("create over clone reservation status = %d body=%s; want 409", status, raw)
	}

	// And a published create refuses a clone on the same destination.
	status, raw, _ = j.create("made-first", "key-made-first", true)
	if status != http.StatusCreated {
		t.Fatalf("create status = %d body=%s; want 201", status, raw)
	}
	status, raw = j.do("POST", "/api/v1/workspace/repositories/clone", map[string]any{
		"remote_url":      stall,
		"root_path":       j.root,
		"destination":     "made-first",
		"idempotency_key": "key-clone-late",
	})
	if status != http.StatusConflict {
		t.Fatalf("clone over created destination status = %d body=%s; want 409", status, raw)
	}
	assertJourneyCreatedRepository(t, filepath.Join(j.root, "made-first"))

	// Stop the stalled clone so the test server can shut down cleanly.
	final := j.action(op.ID, "cancel")
	deadline := time.Now().Add(30 * time.Second)
	for {
		snap := j.snapshot(op.ID)
		if snap.State == "cancelled" || snap.State == "failed" || snap.State == "interrupted" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stalled clone never resolved after cancel; last state %s (cancel snapshot %s)", snap.State, final.State)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestCreateIsImmuneToHostileGitConfigurationJourney(t *testing.T) {
	if testing.Short() {
		t.Skip("journey exercises the real server and real git")
	}
	// Not parallel: the hostile global configuration is process-wide for
	// the in-process server's git children.
	home := t.TempDir()
	hooks := filepath.Join(home, "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte("#!/bin/sh\ntouch \"$0.ran\"\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	hostile := strings.Join([]string{
		"[user]",
		" name = Imposter",
		" email = imposter@example.com",
		"[commit]",
		" gpgsign = true",
		"[core]",
		" hooksPath = " + hooks,
	}, "\n")
	hostileConfig := filepath.Join(home, "gitconfig")
	if err := os.WriteFile(hostileConfig, []byte(hostile), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", hostileConfig)

	j := newCloneJourney(t)
	status, raw, created := j.create("protected", "key-hostile", true)
	if status != http.StatusCreated {
		t.Fatalf("create under hostile configuration status = %d body=%s; want 201", status, raw)
	}
	if !created.Repository.HasHead {
		t.Fatal("created repository is not feature-ready under hostile configuration")
	}
	// The promised result is unchanged and no hook executed.
	assertJourneyCreatedRepository(t, filepath.Join(j.root, "protected"))
	if _, err := os.Stat(filepath.Join(hooks, "pre-commit.ran")); err == nil {
		t.Fatal("a hook executed during creation despite the explicit override")
	}
}

func TestCreateRestartJourney(t *testing.T) {
	if testing.Short() {
		t.Skip("journey boots the real runtime server lifecycle")
	}
	tmp := t.TempDir()
	root := filepath.Join(tmp, "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(tmp, "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}

	bootServer := func() *server.RuntimeServer {
		t.Helper()
		cfg := config.NewDefault()
		cfg.WorkspaceRoots = []string{root}
		ctx, cancel := context.WithCancel(context.Background())
		srv, err := server.Start(ctx, server.Options{
			Runtime:   server.RuntimeIdentity{RuntimeDir: tmp, StateDir: stateDir, Config: filepath.Join(tmp, "config.yaml")},
			AuthToken: "test-token",
			Config:    cfg,
			Mutations: &cloneJourneyMutations{},
		})
		if err != nil {
			t.Fatalf("server.Start: %v", err)
		}
		t.Cleanup(func() {
			cancel()
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer shutdownCancel()
			_ = srv.Close(shutdownCtx)
		})
		return srv
	}

	// Server A creates the repository.
	srvA := bootServer()
	j := &cloneJourney{t: t, root: root, stateDir: stateDir, baseURL: srvA.BaseURL(), lastSnap: map[string]cloneWireOperation{}}
	status, raw, created := j.create("durable", "key-restart-create", true)
	if status != http.StatusCreated {
		t.Fatalf("create status = %d body=%s; want 201", status, raw)
	}
	dest := filepath.Join(root, "durable")
	assertJourneyCreatedRepository(t, dest)

	// Server B restarts on the same state: the published repository is
	// reconciled through discovery (never initialized again), and the
	// same key replays the retained success.
	srvB := bootServer()
	jB := &cloneJourney{t: t, root: root, stateDir: stateDir, baseURL: srvB.BaseURL(), lastSnap: map[string]cloneWireOperation{}}
	if identity := jB.repositoryIdentity("durable"); identity == nil {
		t.Fatal("created repository missing from readiness after restart")
	} else if created.Repository.Identity == nil || *identity != *created.Repository.Identity {
		t.Fatalf("post-restart identity %+v != published identity %+v", identity, created.Repository.Identity)
	}
	status, raw, replay := jB.create("durable", "key-restart-create", true)
	if status != http.StatusCreated {
		t.Fatalf("replay after restart status = %d body=%s; want 201", status, raw)
	}
	if replay.Repository.Identity == nil || *replay.Repository.Identity != *created.Repository.Identity {
		t.Fatalf("replay identity after restart = %+v; want the retained %+v", replay.Repository.Identity, created.Repository.Identity)
	}
	if got := createJourneyGit(t, dest, "rev-list", "--count", "HEAD"); got != "1" {
		t.Fatalf("restart re-initialized the repository: commit count = %q", got)
	}
}
