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

package integration

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	serverruntime "github.com/doordash-oss/agentic-orchestrator/internal/server"
)

// remoteMutations satisfies the trusted-mutation gate without dispatching
// any orchestration: this journey exercises the repository source-update
// routes, whose handlers never invoke the mutation target.
type remoteMutations struct {
	serverruntime.MutationTarget
}

// originalCheckoutIntegrationFixture is one workspace repository whose
// original checkout holds its default branch main, clean and behind its
// bare origin by two commits with real file changes.
type originalCheckoutIntegrationFixture struct {
	t      *testing.T
	root   string
	repo   string
	bare   string
	writer string
}

func newOriginalCheckoutIntegrationFixture(t *testing.T) *originalCheckoutIntegrationFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "source-lab")
	bare := filepath.Join(t.TempDir(), "source-lab-origin.git")
	runGit(t, "", "init", "--bare", bare)
	runGit(t, bare, "symbolic-ref", "HEAD", "refs/heads/main")
	runGit(t, "", "init", "--initial-branch=main", repo)
	writeFile(t, repo, "README.md", "# Test\n")
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "initial")
	runGit(t, repo, "remote", "add", "origin", bare)
	runGit(t, bare, "fetch", repo, "main:refs/heads/main")
	runGit(t, repo, "fetch", "origin")
	runGit(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	writer := filepath.Join(t.TempDir(), "writer")
	runGit(t, "", "clone", bare, writer)
	writeFile(t, writer, "README.md", "remote edit\n")
	writeFile(t, writer, "added.txt", "added remotely\n")
	runGit(t, writer, "add", "-A")
	runGit(t, writer, "commit", "-m", "remote one")
	runGit(t, writer, "commit", "--allow-empty", "-m", "remote two")
	runGit(t, writer, "push", "origin", "main")
	return &originalCheckoutIntegrationFixture{t: t, root: root, repo: repo, bare: bare, writer: writer}
}

func (fx *originalCheckoutIntegrationFixture) originTip() string {
	fx.t.Helper()
	return runGit(fx.t, fx.bare, "rev-parse", "refs/heads/main")
}

func (fx *originalCheckoutIntegrationFixture) localTip() string {
	fx.t.Helper()
	return runGit(fx.t, fx.repo, "rev-parse", "refs/heads/main")
}

// postSourceMutation posts one trusted local-client mutation against the
// running server.
func postSourceMutation(t *testing.T, srv *httptest.Server, path string, body any) (int, []byte) {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+path, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("build POST %s: %v", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agentico-Client", "local")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	out := new(bytes.Buffer)
	if _, err := out.ReadFrom(resp.Body); err != nil {
		t.Fatalf("read %s body: %v", path, err)
	}
	return resp.StatusCode, out.Bytes()
}

// wireSelector builds the repository selector the renderer sends.
func wireSelector(t *testing.T, repo string) map[string]any {
	t.Helper()
	identity, ok := git.ResolveRepoIdentity(repo)
	if !ok {
		t.Fatalf("resolve identity for %s", repo)
	}
	return map[string]any{
		"repo_key": filepath.Base(repo),
		"identity": map[string]any{
			"path":       identity.Path,
			"common_dir": identity.CommonDir,
			"device":     git.FormatIdentityDevice(identity.Device),
			"inode":      git.FormatIdentityInode(identity.Inode),
		},
	}
}

// awaitBehindRow polls the origin-status route until the repository's row
// settles on a completed comparison.
func awaitBehindRow(t *testing.T, srv *httptest.Server, selector map[string]any) map[string]any {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		status, body := postSourceMutation(t, srv, "/api/v1/workspace/repositories/origin-status", map[string]any{
			"mode":         "default",
			"repositories": []map[string]any{selector},
		})
		if status == http.StatusOK {
			var resp struct {
				Repositories []map[string]any `json:"repositories"`
			}
			if err := json.Unmarshal(body, &resp); err != nil {
				t.Fatalf("decode origin-status: %v: %s", err, body)
			}
			if len(resp.Repositories) == 1 {
				row := resp.Repositories[0]
				if row["status"] != "checking" {
					return row
				}
			}
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("origin status never completed: status=%d body=%s", status, body)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestSourceUpdateOriginalCheckoutJourney drives the real REST handler over
// HTTP: the behind comparison marks the clean original checkout eligible,
// the update fast-forwards its branch, HEAD, index, and working files to the
// fetched origin tip, and the settlement read observes the completed
// checkout before claiming it.
func TestSourceUpdateOriginalCheckoutJourney(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	fx := newOriginalCheckoutIntegrationFixture(t)
	cfg := config.NewDefault()
	cfg.WorkspaceRoots = []string{fx.root}
	handler := serverruntime.NewHandler(serverruntime.HandlerOptions{
		Config:                cfg,
		Mutations:             remoteMutations{},
		DisableHostValidation: true,
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	selector := wireSelector(t, fx.repo)
	row := awaitBehindRow(t, srv, selector)
	if row["status"] != "behind" {
		t.Fatalf("row status = %v; want behind (%v)", row["status"], row)
	}
	if eligible, _ := row["update_eligible"].(bool); !eligible {
		t.Fatalf("row eligibility = %v blockers = %v; want an eligible clean original checkout", row["update_eligible"], row["update_blockers"])
	}
	headRef, _ := row["checkout_head_ref"].(string)
	if headRef != "refs/heads/main" {
		t.Fatalf("row checkout head ref = %q; want refs/heads/main", headRef)
	}
	headSHA, _ := row["checkout_head_sha"].(string)
	localTip := fx.localTip()
	originTip := fx.originTip()
	if headSHA != localTip {
		t.Fatalf("row checkout head sha = %q; want the local tip %q", headSHA, localTip)
	}

	update := map[string]any{
		"repo_key":            selector["repo_key"],
		"identity":            selector["identity"],
		"mode":                "default",
		"branch":              "main",
		"origin_branch":       "main",
		"expected_local_sha":  localTip,
		"expected_origin_sha": originTip,
		"checkout_head_ref":   headRef,
		"checkout_head_sha":   headSHA,
	}
	status, body := postSourceMutation(t, srv, "/api/v1/workspace/repositories/update-source", update)
	if status != http.StatusOK {
		t.Fatalf("update status = %d body=%s; want 200", status, body)
	}
	var updated struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(body, &updated); err != nil {
		t.Fatalf("decode update response: %v: %s", err, body)
	}
	if updated.Result != "updated" {
		t.Fatalf("update result = %q body=%s; want updated", updated.Result, body)
	}
	// The branch, symbolic HEAD, index, and working files all advanced.
	if got := runGit(t, fx.repo, "rev-parse", "refs/heads/main"); got != originTip {
		t.Fatalf("refs/heads/main = %s; want the fetched origin tip %s", got, originTip)
	}
	if got := runGit(t, fx.repo, "symbolic-ref", "--quiet", "HEAD"); got != "refs/heads/main" {
		t.Fatalf("HEAD = %s; want refs/heads/main", got)
	}
	if got := runGit(t, fx.repo, "rev-parse", "HEAD^{commit}"); got != originTip {
		t.Fatalf("HEAD commit = %s; want the fetched origin tip", got)
	}
	if got := runGit(t, fx.repo, "status", "--porcelain"); got != "" {
		t.Fatalf("status --porcelain = %q; want a clean checkout", got)
	}
	added, err := os.ReadFile(filepath.Join(fx.repo, "added.txt"))
	if err != nil || string(added) != "added remotely\n" {
		t.Fatalf("added.txt = %q err=%v; want the target content", added, err)
	}
	readme, err := os.ReadFile(filepath.Join(fx.repo, "README.md"))
	if err != nil || string(readme) != "remote edit\n" {
		t.Fatalf("README.md = %q err=%v; want the target content", readme, err)
	}

	reconcile := map[string]any{
		"repo_key":            selector["repo_key"],
		"identity":            selector["identity"],
		"mode":                "default",
		"branch":              "main",
		"origin_branch":       "main",
		"expected_local_sha":  localTip,
		"expected_origin_sha": originTip,
		"checkout_head_ref":   headRef,
		"checkout_head_sha":   headSHA,
	}
	status, body = postSourceMutation(t, srv, "/api/v1/workspace/repositories/reconcile-source-update", reconcile)
	if status != http.StatusOK {
		t.Fatalf("reconcile status = %d body=%s; want 200", status, body)
	}
	var settled struct {
		Outcome  string `json:"outcome"`
		Checkout *struct {
			State   string  `json:"state"`
			HeadRef string  `json:"head_ref"`
			HeadSha *string `json:"head_sha"`
		} `json:"checkout"`
	}
	if err := json.Unmarshal(body, &settled); err != nil {
		t.Fatalf("decode reconcile response: %v: %s", err, body)
	}
	if settled.Outcome != "expected_target_present" {
		t.Fatalf("reconcile outcome = %q; want expected_target_present", settled.Outcome)
	}
	if settled.Checkout == nil || settled.Checkout.State != "clean" || settled.Checkout.HeadRef != "refs/heads/main" ||
		settled.Checkout.HeadSha == nil || *settled.Checkout.HeadSha != originTip {
		t.Fatalf("reconcile checkout = %+v; want a clean checkout at the advanced tip", settled.Checkout)
	}
}

// TestSourceUpdateOriginalCheckoutDirtyRefusalKeepsBytes drives the same
// journey with a dirty original checkout: the comparison reports the
// advisory blocker, the update refuses with a typed stale result, and every
// local byte survives.
func TestSourceUpdateOriginalCheckoutDirtyRefusalKeepsBytes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	fx := newOriginalCheckoutIntegrationFixture(t)
	if err := os.WriteFile(filepath.Join(fx.repo, "local.txt"), []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.NewDefault()
	cfg.WorkspaceRoots = []string{fx.root}
	handler := serverruntime.NewHandler(serverruntime.HandlerOptions{
		Config:                cfg,
		Mutations:             remoteMutations{},
		DisableHostValidation: true,
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	selector := wireSelector(t, fx.repo)
	row := awaitBehindRow(t, srv, selector)
	blockers, _ := row["update_blockers"].([]any)
	found := false
	for _, blocker := range blockers {
		if s, _ := blocker.(string); s == "dirty_target_checkout" {
			found = true
		}
	}
	if !found {
		t.Fatalf("row blockers = %v; want dirty_target_checkout", blockers)
	}
	if eligible, _ := row["update_eligible"].(bool); eligible {
		t.Fatalf("row eligibility = %v; want ineligible", row["update_eligible"])
	}
	headRef, _ := row["checkout_head_ref"].(string)
	headSHA, _ := row["checkout_head_sha"].(string)
	localTip := fx.localTip()
	originTip := fx.originTip()

	// The renderer never offers the action, but the server still revalidates
	// advisory eligibility at execution and refuses safely.
	update := map[string]any{
		"repo_key":            selector["repo_key"],
		"identity":            selector["identity"],
		"mode":                "default",
		"branch":              "main",
		"origin_branch":       "main",
		"expected_local_sha":  localTip,
		"expected_origin_sha": originTip,
		"checkout_head_ref":   headRef,
		"checkout_head_sha":   headSHA,
	}
	status, body := postSourceMutation(t, srv, "/api/v1/workspace/repositories/update-source", update)
	if status != http.StatusOK {
		t.Fatalf("update status = %d body=%s; want 200", status, body)
	}
	var refused struct {
		Result string `json:"result"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(body, &refused); err != nil {
		t.Fatalf("decode update response: %v: %s", err, body)
	}
	if refused.Result != "stale" || refused.Reason != "dirty_checkout" {
		t.Fatalf("update result = %q reason = %q; want stale dirty_checkout", refused.Result, refused.Reason)
	}
	if got := runGit(t, fx.repo, "rev-parse", "refs/heads/main"); got != localTip {
		t.Fatalf("refs/heads/main = %s; want the displayed tip preserved", got)
	}
	local, err := os.ReadFile(filepath.Join(fx.repo, "local.txt"))
	if err != nil || string(local) != "local\n" {
		t.Fatalf("local.txt = %q err=%v; want the local content preserved", local, err)
	}
	if _, err := os.Stat(filepath.Join(fx.repo, "added.txt")); !os.IsNotExist(err) {
		t.Fatalf("added.txt exists; want no incoming file written on refusal")
	}
}
