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
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// initializeWireResult is the initialize response on the wire.
type initializeWireResult struct {
	Result     string `json:"result"`
	Repository struct {
		RepoKey  string        `json:"repo_key"`
		Path     string        `json:"path"`
		HasHead  bool          `json:"has_head"`
		Root     string        `json:"root"`
		Identity *wireIdentity `json:"identity"`
	} `json:"repository"`
	Error *struct {
		Code string `json:"code"`
	} `json:"error"`
}

// readinessRepository reads one repository row from the authoritative
// readiness snapshot.
func (j *cloneJourney) readinessRepository(name string) (bool, bool, *wireIdentity) {
	j.t.Helper()
	status, body := j.do("GET", "/api/v1/readiness", nil)
	if status != http.StatusOK {
		j.t.Fatalf("readiness status = %d", status)
	}
	var snapshot struct {
		Workspace struct {
			Repositories []struct {
				Name         string        `json:"name"`
				Valid        bool          `json:"valid"`
				FeatureReady bool          `json:"feature_ready"`
				Identity     *wireIdentity `json:"identity"`
			} `json:"repositories"`
		} `json:"workspace"`
	}
	if err := json.Unmarshal(body, &snapshot); err != nil {
		j.t.Fatalf("decode readiness: %v", err)
	}
	for _, repo := range snapshot.Workspace.Repositories {
		if repo.Name == name {
			return repo.Valid, repo.FeatureReady, repo.Identity
		}
	}
	j.t.Fatalf("repository %q missing from readiness", name)
	return false, false, nil
}

// initialize posts the explicit-initialization mutation.
func (j *cloneJourney) initialize(body map[string]any) (int, initializeWireResult) {
	j.t.Helper()
	status, raw := j.do("POST", "/api/v1/workspace/repositories/initialize", body)
	var resp initializeWireResult
	if err := json.Unmarshal(raw, &resp); err != nil {
		j.t.Fatalf("decode initialize response (%d): %v: %s", status, err, raw)
	}
	return status, resp
}

// journeyGit runs git with an isolated global configuration.
func initJourneyGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git -C %s %v: %v\n%s", dir, args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// journeyCloneEmptyRemote clones the given empty remote into destination
// through the real clone API and waits for the unborn success.
func (j *cloneJourney) journeyCloneEmptyRemote(t *testing.T, remote, destination, key string) cloneWireOperation {
	t.Helper()
	op := j.start(remote, destination, key)
	final := j.waitFor(op.ID, "succeeded")
	if final.Published == nil {
		t.Fatalf("no publication evidence: %+v", final)
	}
	if final.Published.HasHead {
		t.Fatalf("clone of empty remote unexpectedly has a head: %+v", final.Published)
	}
	if final.Published.Identity == nil {
		t.Fatalf("unborn publication carries no identity: %+v", final.Published)
	}
	return final
}

// emptyRemoteWithBranch serves an empty bare repository whose HEAD points
// at the given branch, over controlled local dumb HTTP.
func emptyRemoteWithBranch(t *testing.T, branch string) string {
	t.Helper()
	src := t.TempDir()
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run(src, "init", "--initial-branch="+branch)
	bare := filepath.Join(t.TempDir(), "remote.git")
	run(src, "clone", "--bare", src, bare)
	run(bare, "update-server-info")
	srv := httptest.NewServer(http.FileServer(http.Dir(filepath.Dir(bare))))
	t.Cleanup(srv.Close)
	return srv.URL + "/" + filepath.Base(bare)
}

// remoteRefs lists the refs a remote advertises, proving whether anything
// was ever pushed to it.
func remoteRefs(t *testing.T, remote string) []string {
	t.Helper()
	cmd := exec.Command("git", "ls-remote", remote)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git ls-remote %s: %v\n%s", remote, err, out)
	}
	var refs []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			refs = append(refs, line)
		}
	}
	return refs
}

// assertInitialCommitEvidence proves the promised commit shape with real
// git in the cloned repository.
func assertInitialCommitEvidence(t *testing.T, repo, wantBranch string) {
	t.Helper()
	if branch := initJourneyGit(t, repo, "symbolic-ref", "--short", "HEAD"); branch != wantBranch {
		t.Fatalf("branch = %q; want %q", branch, wantBranch)
	}
	if count := initJourneyGit(t, repo, "rev-list", "--count", "HEAD"); count != "1" {
		t.Fatalf("commit count = %q; want 1", count)
	}
	if author := initJourneyGit(t, repo, "log", "-1", "--format=%an <%ae>"); author != "Agentico <agentico@localhost>" {
		t.Fatalf("author = %q; want Agentico <agentico@localhost>", author)
	}
	if committer := initJourneyGit(t, repo, "log", "-1", "--format=%cn <%ce>"); committer != "Agentico <agentico@localhost>" {
		t.Fatalf("committer = %q; want Agentico <agentico@localhost>", committer)
	}
	if subject := initJourneyGit(t, repo, "log", "-1", "--format=%s"); subject != "Initial commit" {
		t.Fatalf("subject = %q", subject)
	}
	if tree := initJourneyGit(t, repo, "show", "-s", "--format=%T", "HEAD"); tree != initJourneyGit(t, repo, "mktree") {
		t.Fatalf("commit tree %q is not the empty tree", tree)
	}
}

// TestInitializeAPIJourney drives the full explicit-initialization journey
// against a real empty remote over controlled HTTP transport: clone the
// empty remote, initialize it with consent, verify the commit shape, the
// identity-preserving refresh, the truthful historical clone record, and
// the refresh-only replay.
func TestInitializeAPIJourney(t *testing.T) {
	if testing.Short() {
		t.Skip("journey runs real git over controlled HTTP transport")
	}
	j := newCloneJourney(t)
	remote := bareHTTPRemote(t, 0, true)

	final := j.journeyCloneEmptyRemote(t, remote, "empty-clone", "key-init-journey-1")
	repo := filepath.Join(j.root, "empty-clone")
	// A dumb-HTTP clone of an empty remote cannot learn the remote's HEAD
	// branch, so point the unborn HEAD where a smart-HTTP clone of an
	// empty main remote would: refs/heads/main.
	initJourneyGit(t, repo, "symbolic-ref", "HEAD", "refs/heads/main")

	valid, featureReady, identity := j.readinessRepository("empty-clone")
	if !valid || featureReady {
		t.Fatalf("unborn clone readiness: valid=%v feature_ready=%v", valid, featureReady)
	}
	if identity == nil {
		t.Fatal("unborn clone has no readiness identity")
	}

	// Absent consent is refused before any mutation.
	status, errResp := j.initialize(map[string]any{
		"repo_key": "empty-clone",
		"identity": identity,
	})
	if status != http.StatusBadRequest || errResp.Error == nil || errResp.Error.Code != "consent_required" {
		t.Fatalf("consentless initialize: status=%d resp=%+v", status, errResp)
	}

	status, resp := j.initialize(map[string]any{
		"repo_key": "empty-clone",
		"identity": identity,
		"consent":  true,
	})
	if status != http.StatusOK {
		t.Fatalf("initialize status = %d resp=%+v", status, resp)
	}
	if resp.Result != "initialized" {
		t.Fatalf("result = %q; want initialized", resp.Result)
	}
	if resp.Repository.RepoKey != "empty-clone" || !resp.Repository.HasHead {
		t.Fatalf("repository = %+v", resp.Repository)
	}
	if resp.Repository.Identity == nil || resp.Repository.Identity.Path != identity.Path ||
		resp.Repository.Identity.CommonDir != identity.CommonDir ||
		resp.Repository.Identity.Device != identity.Device || resp.Repository.Identity.Inode != identity.Inode {
		t.Fatalf("result identity %+v does not preserve the clone identity %+v", resp.Repository.Identity, identity)
	}

	assertInitialCommitEvidence(t, repo, "main")
	if origin := initJourneyGit(t, repo, "remote", "get-url", "origin"); origin != remote {
		t.Fatalf("origin = %q; want %q", origin, remote)
	}
	// Nothing was pushed: the empty remote still advertises no refs.
	if refs := remoteRefs(t, remote); len(refs) != 0 {
		t.Fatalf("remote refs after initialize = %v; want none", refs)
	}

	// Readiness flips under the same catalog key and identity.
	valid, featureReady, afterIdentity := j.readinessRepository("empty-clone")
	if !valid || !featureReady {
		t.Fatalf("readiness after initialize: valid=%v feature_ready=%v", valid, featureReady)
	}
	if afterIdentity == nil || afterIdentity.Path != identity.Path || afterIdentity.Inode != identity.Inode {
		t.Fatalf("identity changed across initialization: %+v -> %+v", identity, afterIdentity)
	}

	// The historical clone record stays truthful: it still reports the
	// publication was unborn, while current readiness is authoritative.
	historical := j.snapshot(final.ID)
	if historical.Published == nil || historical.Published.HasHead {
		t.Fatalf("historical clone record was rewritten: %+v", historical.Published)
	}

	// A duplicate request (e.g. a retry after a lost response) is a
	// refresh-only success without another commit.
	status, replay := j.initialize(map[string]any{
		"repo_key": "empty-clone",
		"identity": identity,
		"consent":  true,
	})
	if status != http.StatusOK || replay.Result != "already_initialized" {
		t.Fatalf("replay: status=%d resp=%+v", status, replay)
	}
	if count := initJourneyGit(t, repo, "rev-list", "--count", "HEAD"); count != "1" {
		t.Fatalf("replay created a second commit: count=%q", count)
	}

	// An already-initialized repository refreshes even with local changes.
	if err := os.WriteFile(filepath.Join(repo, "local-notes.txt"), []byte("user content"), 0o644); err != nil {
		t.Fatal(err)
	}
	status, dirty := j.initialize(map[string]any{
		"repo_key": "empty-clone",
		"identity": identity,
		"consent":  true,
	})
	if status != http.StatusOK || dirty.Result != "already_initialized" {
		t.Fatalf("dirty refresh: status=%d resp=%+v", status, dirty)
	}
	if count := initJourneyGit(t, repo, "rev-list", "--count", "HEAD"); count != "1" {
		t.Fatalf("dirty refresh created a commit: count=%q", count)
	}
	if _, err := os.Stat(filepath.Join(repo, "local-notes.txt")); err != nil {
		t.Fatalf("local change removed: %v", err)
	}
}

// quietHead resolves HEAD tolerantly: an unborn repository returns an
// empty head with a nil error.
func quietHead(t *testing.T, repo string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", "-C", repo, "rev-parse", "--verify", "--quiet", "HEAD")
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// TestInitializeAPIJourneySlashBranchPreserved clones an empty remote whose
// HEAD is a slash-containing branch and proves the initial commit lands on
// that branch verbatim, with the origin preserved and nothing pushed.
func TestInitializeAPIJourneySlashBranchPreserved(t *testing.T) {
	if testing.Short() {
		t.Skip("journey runs real git over controlled HTTP transport")
	}
	j := newCloneJourney(t)
	remote := emptyRemoteWithBranch(t, "feature/seed")

	j.journeyCloneEmptyRemote(t, remote, "seed-clone", "key-init-journey-slash")
	repo := filepath.Join(j.root, "seed-clone")
	// Mirror a smart-HTTP clone of an empty remote whose HEAD is the
	// slash-containing branch feature/seed.
	initJourneyGit(t, repo, "symbolic-ref", "HEAD", "refs/heads/feature/seed")

	_, _, identity := j.readinessRepository("seed-clone")
	if identity == nil {
		t.Fatal("unborn clone has no readiness identity")
	}
	status, resp := j.initialize(map[string]any{
		"repo_key": "seed-clone",
		"identity": identity,
		"consent":  true,
	})
	if status != http.StatusOK || resp.Result != "initialized" {
		t.Fatalf("initialize: status=%d resp=%+v", status, resp)
	}
	assertInitialCommitEvidence(t, repo, "feature/seed")
	if origin := initJourneyGit(t, repo, "remote", "get-url", "origin"); origin != remote {
		t.Fatalf("origin = %q; want %q", origin, remote)
	}
	if refs := remoteRefs(t, remote); len(refs) != 0 {
		t.Fatalf("remote refs after initialize = %v; want none", refs)
	}
	_, featureReady, _ := j.readinessRepository("seed-clone")
	if !featureReady {
		t.Fatal("seed-clone not feature-ready after initialize")
	}
}

// TestInitializeAPIJourneyRefusals proves content and selector refusals
// leave the clone and unrelated repositories untouched.
func TestInitializeAPIJourneyRefusals(t *testing.T) {
	if testing.Short() {
		t.Skip("journey runs real git over controlled HTTP transport")
	}
	j := newCloneJourney(t)
	remote := bareHTTPRemote(t, 0, true)

	j.journeyCloneEmptyRemote(t, remote, "user-content", "key-init-journey-content")
	repo := filepath.Join(j.root, "user-content")
	_, _, identity := j.readinessRepository("user-content")
	if err := os.WriteFile(filepath.Join(repo, "user.txt"), []byte("user content"), 0o644); err != nil {
		t.Fatal(err)
	}
	status, resp := j.initialize(map[string]any{
		"repo_key": "user-content",
		"identity": identity,
		"consent":  true,
	})
	if status != http.StatusConflict || resp.Error == nil || resp.Error.Code != "initialize_content_present" {
		t.Fatalf("content refusal: status=%d resp=%+v", status, resp)
	}
	if _, err := os.Stat(filepath.Join(repo, "user.txt")); err != nil {
		t.Fatalf("user content removed on refusal: %v", err)
	}
	if head, err := quietHead(t, repo); err != nil || head != "" {
		t.Fatalf("refusal created a commit: head=%q err=%v", head, err)
	}

	// Unknown selector key.
	status, resp = j.initialize(map[string]any{
		"repo_key": "missing-repo",
		"identity": identity,
		"consent":  true,
	})
	if status != http.StatusNotFound || resp.Error == nil || resp.Error.Code != "initialize_repository_not_found" {
		t.Fatalf("unknown key: status=%d resp=%+v", status, resp)
	}

	// Stale identity (the repository was replaced).
	status, resp = j.initialize(map[string]any{
		"repo_key": "user-content",
		"identity": map[string]any{
			"path":       "/definitely/not/it",
			"common_dir": "/definitely/not/it/.git",
			"device":     "1",
			"inode":      "1",
		},
		"consent": true,
	})
	if status != http.StatusConflict || resp.Error == nil || resp.Error.Code != "initialize_identity_stale" {
		t.Fatalf("stale identity: status=%d resp=%+v", status, resp)
	}
	if _, err := os.Stat(filepath.Join(repo, "user.txt")); err != nil {
		t.Fatalf("stale-identity refusal removed user content: %v", err)
	}

	// Untrusted requests never reach the mutation.
	req, err := http.NewRequest(http.MethodPost, j.baseURL+"/api/v1/workspace/repositories/initialize",
		strings.NewReader(`{"repo_key":"user-content","consent":true}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	httpResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = httpResp.Body.Close() }()
	if httpResp.StatusCode != http.StatusForbidden {
		t.Fatalf("untrusted initialize status = %d; want 403", httpResp.StatusCode)
	}
}

// TestInitializeAPIJourneyConcurrentDuplicatesCreateOneCommit proves
// duplicate concurrent requests produce at most one Agentico initial
// commit; every caller still receives a truthful result.
func TestInitializeAPIJourneyConcurrentDuplicatesCreateOneCommit(t *testing.T) {
	if testing.Short() {
		t.Skip("journey runs real git over controlled HTTP transport")
	}
	j := newCloneJourney(t)
	remote := bareHTTPRemote(t, 0, true)

	j.journeyCloneEmptyRemote(t, remote, "raced-clone", "key-init-journey-race")
	repo := filepath.Join(j.root, "raced-clone")
	initJourneyGit(t, repo, "symbolic-ref", "HEAD", "refs/heads/main")
	_, _, identity := j.readinessRepository("raced-clone")

	const attempts = 8
	results := make([]initializeWireResult, attempts)
	statuses := make([]int, attempts)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			statuses[i], results[i] = j.initialize(map[string]any{
				"repo_key": "raced-clone",
				"identity": identity,
				"consent":  true,
			})
		}(i)
	}
	close(start)
	wg.Wait()

	initialized := 0
	for i := range results {
		if statuses[i] != http.StatusOK {
			t.Fatalf("attempt %d status = %d resp=%+v", i, statuses[i], results[i])
		}
		switch results[i].Result {
		case "initialized":
			initialized++
		case "already_initialized":
		default:
			t.Fatalf("attempt %d result = %q", i, results[i].Result)
		}
	}
	if initialized != 1 {
		t.Fatalf("initialized results = %d; want exactly 1", initialized)
	}
	assertInitialCommitEvidence(t, repo, "main")
}

// TestInitializeAPIJourneySSEInvalidation proves a successful
// initialization publishes the runtime invalidation so connected clients
// re-read discovery and readiness.
func TestInitializeAPIJourneySSEInvalidation(t *testing.T) {
	if testing.Short() {
		t.Skip("journey runs real git over controlled HTTP transport")
	}
	j := newCloneJourney(t)
	remote := bareHTTPRemote(t, 0, true)

	j.journeyCloneEmptyRemote(t, remote, "sse-clone", "key-init-journey-sse")
	_, _, identity := j.readinessRepository("sse-clone")

	events := make(chan string, 16)
	req, err := http.NewRequest("GET", j.baseURL+"/api/v1/events?access_token=test-token", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req = req.WithContext(ctx)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				chunk := string(buf[:n])
				for _, line := range strings.Split(chunk, "\n") {
					if strings.HasPrefix(line, "event: ") {
						events <- strings.TrimSpace(strings.TrimPrefix(line, "event: "))
					}
				}
			}
			if err != nil {
				close(events)
				return
			}
		}
	}()

	// Drain the connection handshake events before the mutation.
drain:
	for {
		select {
		case <-events:
		case <-time.After(200 * time.Millisecond):
			break drain
		}
	}

	status, _ := j.initialize(map[string]any{
		"repo_key": "sse-clone",
		"identity": identity,
		"consent":  true,
	})
	if status != http.StatusOK {
		t.Fatalf("initialize status = %d", status)
	}

	// The runtime invalidation must arrive on the stream.
	deadline := time.Now().Add(10 * time.Second)
	for {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatal("event stream closed before the invalidation arrived")
			}
			if event == "config.updated" {
				return
			}
		case <-time.After(time.Until(deadline)):
			t.Fatal("config.updated invalidation never arrived on the SSE stream")
		}
	}
}
