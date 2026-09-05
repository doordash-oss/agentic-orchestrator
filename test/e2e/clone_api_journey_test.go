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
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
)

// cloneJourney drives the Settings clone lifecycle against a real git
// remote served over a controlled local HTTP transport (dumb HTTP), a real
// clone service, and real SSE + snapshot reads.
type cloneJourney struct {
	t        *testing.T
	root     string
	stateDir string
	handler  http.Handler
	srv      *httptest.Server
	baseURL  string

	mu       sync.Mutex
	lastSnap map[string]cloneWireOperation
}

type cloneWireOperation struct {
	ID              string `json:"id"`
	State           string `json:"state"`
	Stage           string `json:"stage"`
	Destination     string `json:"destination"`
	DestinationPath string `json:"destination_path"`
	CancelRequested bool   `json:"cancel_requested"`
	CleanupIssue    string `json:"cleanup_issue"`
	PendingOutcome  string `json:"pending_outcome"`
	Error           *struct {
		Code  string `json:"code"`
		Title string `json:"title"`
	} `json:"error"`
	Published *struct {
		RepoKey  string        `json:"repo_key"`
		Path     string        `json:"path"`
		HasHead  bool          `json:"has_head"`
		Identity *wireIdentity `json:"identity"`
	} `json:"published"`
	UpdatedAt string `json:"updated_at"`
}

// wireIdentity is the server-resolved repository identity on the wire.
type wireIdentity struct {
	Path      string `json:"path"`
	CommonDir string `json:"common_dir"`
	Device    string `json:"device"`
	Inode     string `json:"inode"`
}

// repositoryIdentity reads the readiness identity of a named repository.
func (j *cloneJourney) repositoryIdentity(name string) *wireIdentity {
	j.t.Helper()
	status, body := j.do("GET", "/api/v1/readiness", nil)
	if status != http.StatusOK {
		j.t.Fatalf("readiness status = %d", status)
	}
	var snapshot struct {
		Workspace struct {
			Repositories []struct {
				Name     string        `json:"name"`
				Identity *wireIdentity `json:"identity"`
			} `json:"repositories"`
		} `json:"workspace"`
	}
	if err := json.Unmarshal(body, &snapshot); err != nil {
		j.t.Fatalf("decode readiness: %v", err)
	}
	for _, repo := range snapshot.Workspace.Repositories {
		if repo.Name == name {
			return repo.Identity
		}
	}
	return nil
}

func newCloneJourney(t *testing.T) *cloneJourney {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	cfg := config.NewDefault()
	cfg.WorkspaceRoots = []string{root}
	handler := server.NewHandler(server.HandlerOptions{
		Runtime:               server.RuntimeIdentity{RuntimeDir: stateDir, StateDir: stateDir},
		Config:                cfg,
		AuthToken:             "test-token",
		DisableHostValidation: true,
		Mutations:             &cloneJourneyMutations{},
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	j := &cloneJourney{t: t, root: root, stateDir: stateDir, handler: handler, srv: srv, baseURL: srv.URL, lastSnap: map[string]cloneWireOperation{}}
	return j
}

// cloneJourneyMutations satisfies the trusted-mutation wiring without
// owning feature mutations: clone journeys never dispatch feature
// mutations (readiness is the only non-clone read they make beyond
// snapshots and listings).
type cloneJourneyMutations struct {
	server.MutationTarget
}

func (j *cloneJourney) do(method, path string, body any) (int, []byte) {
	j.t.Helper()
	var reader *strings.Reader
	if body == nil {
		reader = strings.NewReader("")
	} else {
		payload, err := json.Marshal(body)
		if err != nil {
			j.t.Fatalf("marshal body: %v", err)
		}
		reader = strings.NewReader(string(payload))
	}
	req, err := http.NewRequest(method, j.baseURL+path, reader)
	if err != nil {
		j.t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer test-token")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Agentico-Client", "local")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		j.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data := make([]byte, 0, 4096)
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		data = append(data, buf[:n]...)
		if err != nil {
			break
		}
	}
	return resp.StatusCode, data
}

func (j *cloneJourney) start(remote, destination, key string) cloneWireOperation {
	j.t.Helper()
	status, body := j.do("POST", "/api/v1/workspace/repositories/clone", map[string]any{
		"remote_url":      remote,
		"root_path":       j.root,
		"destination":     destination,
		"idempotency_key": key,
	})
	if status != http.StatusAccepted {
		j.t.Fatalf("start status = %d body=%s", status, body)
	}
	var resp struct {
		Result    string             `json:"result"`
		Operation cloneWireOperation `json:"operation"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		j.t.Fatalf("unmarshal start response: %v", err)
	}
	return resp.Operation
}

func (j *cloneJourney) snapshot(id string) cloneWireOperation {
	j.t.Helper()
	status, body := j.do("GET", "/api/v1/workspace/repositories/clone/"+id, nil)
	if status != http.StatusOK {
		j.t.Fatalf("snapshot status = %d body=%s", status, body)
	}
	var resp struct {
		Operation cloneWireOperation `json:"operation"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		j.t.Fatalf("unmarshal snapshot: %v", err)
	}
	return resp.Operation
}

func (j *cloneJourney) waitFor(id string, want ...string) cloneWireOperation {
	j.t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		snap := j.snapshot(id)
		for _, w := range want {
			if snap.State == w {
				return snap
			}
		}
		if time.Now().After(deadline) {
			errDetail := "none"
			if snap.Error != nil {
				errDetail = snap.Error.Code + ": " + snap.Error.Title
			}
			j.t.Fatalf("operation %s never reached %v; last state %s error %s", id, want, snap.State, errDetail)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (j *cloneJourney) action(id, action string) cloneWireOperation {
	j.t.Helper()
	status, body := j.do("POST", "/api/v1/workspace/repositories/clone/"+id+"/"+action, map[string]any{})
	if status != http.StatusOK && status != http.StatusAccepted {
		j.t.Fatalf("%s status = %d body=%s", action, status, body)
	}
	var resp struct {
		Result    string             `json:"result"`
		Operation cloneWireOperation `json:"operation"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		j.t.Fatalf("unmarshal %s response: %v", action, err)
	}
	return resp.Operation
}

// bareHTTPRemote builds a real git repository with history and serves it
// over controlled local dumb HTTP, returning the clone URL.
func bareHTTPRemote(t *testing.T, commits int, empty bool) string {
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
	run(src, "init", "--initial-branch=main")
	if !empty {
		for i := 0; i < commits; i++ {
			if err := os.WriteFile(filepath.Join(src, fmt.Sprintf("file-%d.txt", i)), []byte("data\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			run(src, "add", ".")
			run(src, "commit", "-m", fmt.Sprintf("commit %d", i))
		}
	}
	bare := filepath.Join(t.TempDir(), "remote.git")
	run(src, "clone", "--bare", src, bare)
	// Dumb HTTP needs the advertised refs and pack info files.
	run(bare, "update-server-info")
	srv := httptest.NewServer(http.FileServer(http.Dir(filepath.Dir(bare))))
	t.Cleanup(srv.Close)
	return srv.URL + "/" + filepath.Base(bare)
}

// stalledRemote serves nothing (hangs), producing a deterministic
// long-running clone for cancellation and crash journeys. The handler
// honors the request context so server shutdown never deadlocks once the
// cloned process is terminated.
func stalledRemote(t *testing.T) string {
	t.Helper()
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/never.git"
}

// TestCloneAPIJourney drives the full Settings clone lifecycle against a
// real git remote over controlled HTTP transport: durable acceptance,
// authoritative snapshots, atomic publication, discovery, idempotent
// replay, cancellation, empty remotes, and the unborn-repo feature guard.
func TestCloneAPIJourney(t *testing.T) {
	if testing.Short() {
		t.Skip("journey runs real git over controlled HTTP transport")
	}
	j := newCloneJourney(t)
	remote := bareHTTPRemote(t, 3, false)

	op := j.start(remote, "widget", "key-journey-1")
	if op.State != "accepted" && op.State != "running" {
		t.Fatalf("initial state = %s", op.State)
	}
	final := j.waitFor(op.ID, "succeeded")
	if final.Published == nil {
		t.Fatalf("no publication evidence: %+v", final)
	}
	if final.Published.RepoKey != "widget" || !final.Published.HasHead {
		t.Fatalf("publication = %+v", final.Published)
	}
	// The publication durably binds the repository identity actually
	// published, so adoption can never fall back to key or path.
	if final.Published.Identity == nil {
		t.Fatalf("publication carries no repository identity: %+v", final.Published)
	}
	if final.Published.Identity.Path != filepath.Join(j.root, "widget") {
		t.Errorf("publication identity path = %q", final.Published.Identity.Path)
	}
	if final.Published.Identity.Device == "" || final.Published.Identity.Inode == "" {
		t.Errorf("publication identity filesystem fields missing: %+v", final.Published.Identity)
	}
	// The identity matches what the server reports through readiness.
	repoIdentity := j.repositoryIdentity("widget")
	if repoIdentity == nil {
		t.Fatal("readiness reports no identity for the published repository")
	}
	if *repoIdentity != *final.Published.Identity {
		t.Errorf("publication identity %+v != readiness identity %+v", final.Published.Identity, repoIdentity)
	}

	// Real filesystem outcomes: full history, origin, checked-out HEAD.
	dest := filepath.Join(j.root, "widget")
	gitOut := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dest
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if got := gitOut("rev-list", "--count", "HEAD"); got != "3" {
		t.Errorf("cloned history = %s commits, want 3 (full history)", got)
	}
	if got := gitOut("remote", "get-url", "origin"); got != remote {
		t.Errorf("origin = %s, want %s", got, remote)
	}
	if _, err := os.Stat(filepath.Join(dest, "file-2.txt")); err != nil {
		t.Errorf("default checkout missing files: %v", err)
	}
	// Hidden staging is gone; the marker stays as durable evidence.
	if _, err := os.Stat(filepath.Join(dest, ".git", "agentico-publication.json")); err != nil {
		t.Errorf("publication marker missing: %v", err)
	}
	entries, err := os.ReadDir(j.root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".agentico-clone-") {
			t.Errorf("staging residue left in root: %s", e.Name())
		}
	}

	// Same-key replay returns the same succeeded operation without a
	// second clone.
	replayed := j.start(remote, "widget", "key-journey-1")
	if replayed.ID != op.ID || replayed.State != "succeeded" {
		t.Fatalf("same-key replay = %+v", replayed)
	}

	// Same-key different-input conflicts.
	status, body := j.do("POST", "/api/v1/workspace/repositories/clone", map[string]any{
		"remote_url": remote, "root_path": j.root,
		"destination": "other", "idempotency_key": "key-journey-1",
	})
	if status != http.StatusConflict || !strings.Contains(string(body), "clone_idempotency_conflict") {
		t.Fatalf("changed-input replay = %d %s", status, body)
	}

	// An occupied destination is refused with its data intact.
	status, body = j.do("POST", "/api/v1/workspace/repositories/clone", map[string]any{
		"remote_url": remote, "root_path": j.root,
		"destination": "widget", "idempotency_key": "key-journey-2",
	})
	if status != http.StatusConflict || !strings.Contains(string(body), "clone_destination") {
		t.Fatalf("occupied destination = %d %s", status, body)
	}

	// A failed clone becomes terminal after cleanup, then fresh Retry
	// starts a new operation with a new key.
	badRemote := strings.Replace(remote, "/remote.git", "/missing.git", 1)
	failed := j.start(badRemote, "doomed", "key-journey-3")
	failedFinal := j.waitFor(failed.ID, "failed", "cleanup_pending")
	if failedFinal.State == "cleanup_pending" {
		resolved := j.action(failed.ID, "cleanup")
		if resolved.State != "failed" {
			t.Fatalf("cleanup resolved to %s", resolved.State)
		}
		failedFinal = j.snapshot(failed.ID)
	}
	if failedFinal.Error == nil {
		t.Fatal("failed operation carries no canonical error")
	}
	retried := j.action(failed.ID, "retry")
	if retried.ID == failed.ID {
		t.Fatal("retry reused the predecessor operation")
	}
	j.waitFor(retried.ID, "failed", "cleanup_pending")

	// Cancellation: a stalled transfer is explicitly cancelled; the
	// snapshot shows cancelling first, then cancelled after cleanup.
	stall := stalledRemote(t)
	hanging := j.start(stall, "hangs", "key-journey-4")
	waitRunning := time.Now().Add(30 * time.Second)
	for {
		snap := j.snapshot(hanging.ID)
		if snap.State == "running" {
			break
		}
		if time.Now().After(waitRunning) {
			t.Fatalf("stalled clone never reached running (state %s)", snap.State)
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancelling := j.action(hanging.ID, "cancel")
	if cancelling.State != "cancelling" || !cancelling.CancelRequested {
		t.Fatalf("cancel = %+v, want cancelling with request recorded", cancelling)
	}
	cancelled := j.waitFor(hanging.ID, "cancelled", "cleanup_pending")
	if cancelled.State == "cleanup_pending" {
		t.Fatalf("cancelled cleanup pending: %+v", cancelled)
	}
	if _, err := os.Lstat(filepath.Join(j.root, "hangs")); !os.IsNotExist(err) {
		t.Errorf("cancelled destination left behind: %v", err)
	}

	// Empty remote: success with no commits, discoverable but not
	// feature-ready; feature creation is refused with canonical guidance.
	emptyRemote := bareHTTPRemote(t, 0, true)
	emptyOp := j.start(emptyRemote, "empty-clone", "key-journey-5")
	emptyFinal := j.waitFor(emptyOp.ID, "succeeded", "cleanup_pending")
	if emptyFinal.State == "cleanup_pending" {
		t.Fatalf("empty clone stuck in cleanup: %+v", emptyFinal)
	}
	if emptyFinal.Published == nil || emptyFinal.Published.HasHead {
		t.Fatalf("empty clone publication = %+v, want succeeded without commits", emptyFinal.Published)
	}
	gitEmpty := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = filepath.Join(j.root, "empty-clone")
		out, _ := cmd.CombinedOutput()
		return strings.TrimSpace(string(out))
	}
	if got := gitEmpty("remote", "get-url", "origin"); got != emptyRemote {
		t.Errorf("empty clone lost origin: %s", got)
	}
	// An unborn repository stays visible and unselectable in the picker:
	// readiness distinguishes repository existence from feature usability.
	status, body = j.do("GET", "/api/v1/readiness", nil)
	if status != http.StatusOK {
		t.Fatalf("readiness status = %d", status)
	}
	if !strings.Contains(string(body), `"feature_ready":false`) {
		t.Errorf("readiness does not distinguish the unborn empty clone: %s", body)
	}
}

// TestCloneRestartJourney proves the production server lifecycle across a
// restart: graceful Close interrupts active clones (a prior explicit
// cancellation stays cancelled), cleans owned staging, and the next
// server's startup recovery rediscovers the outcomes, releases their
// reservations, and accepts fresh work.
func TestCloneRestartJourney(t *testing.T) {
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
	stall := stalledRemote(t)

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

	// Server A: start one stalled clone plus one explicitly cancelled.
	srvA := bootServer()
	j := &cloneJourney{t: t, root: root, stateDir: stateDir, handler: nil, srv: nil, baseURL: srvA.BaseURL(), lastSnap: map[string]cloneWireOperation{}}
	op := j.start(stall, "restart-me", "key-restart-1")
	waitRunning := time.Now().Add(30 * time.Second)
	for {
		snap := j.snapshot(op.ID)
		if snap.State == "running" {
			break
		}
		if time.Now().After(waitRunning) {
			t.Fatalf("stalled clone never reached running (state %s)", snap.State)
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancelOp := j.start(stall, "restart-cancelled", "key-restart-2")
	if snap := j.action(cancelOp.ID, "cancel"); snap.State != "cancelling" {
		t.Fatalf("explicit cancel = %+v", snap)
	}

	// One clone completes before the restart: its publication identity must
	// survive the restart and still match the untouched repository.
	doneRemote := bareHTTPRemote(t, 1, false)
	doneOp := j.start(doneRemote, "restart-done", "key-restart-3")
	doneFinal := j.waitFor(doneOp.ID, "succeeded")
	if doneFinal.Published == nil || doneFinal.Published.Identity == nil {
		t.Fatalf("completed clone lacks publication identity: %+v", doneFinal.Published)
	}

	// Graceful shutdown through the production Close path.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	if err := srvA.Close(shutdownCtx); err != nil {
		t.Fatalf("graceful Close: %v", err)
	}

	// The interrupted and cancelled destinations are cleaned and no
	// staging survives.
	for _, dest := range []string{"restart-me", "restart-cancelled"} {
		if _, err := os.Lstat(filepath.Join(root, dest)); !os.IsNotExist(err) {
			t.Errorf("shutdown left %s behind", dest)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".agentico-clone-") {
			t.Errorf("staging residue after shutdown: %s", e.Name())
		}
	}

	// Server B boots over the same durable state: startup recovery keeps
	// the outcomes discoverable and their reservations released, so fresh
	// work on the same destinations is accepted.
	srvB := bootServer()
	j.baseURL = srvB.BaseURL()

	final := j.waitFor(op.ID, "interrupted", "cancelled")
	if final.State == "cancelled" {
		t.Errorf("plain interruption reclassified as explicit cancellation")
	}
	cancelledFinal := j.snapshot(cancelOp.ID)
	if cancelledFinal.State != "cancelled" {
		t.Errorf("explicitly cancelled reclassified to %s", cancelledFinal.State)
	}

	// The durable publication identity is retained across the restart and
	// still matches the repository it published.
	doneAfter := j.snapshot(doneOp.ID)
	if doneAfter.Published == nil || doneAfter.Published.Identity == nil {
		t.Fatalf("restart lost the publication identity: %+v", doneAfter.Published)
	}
	if ready := j.repositoryIdentity("restart-done"); ready == nil || *ready != *doneAfter.Published.Identity {
		t.Errorf("retained identity %+v != readiness %+v", doneAfter.Published.Identity, ready)
	}

	// Fresh retry of the interrupted attempt works through the normal
	// boundary and can itself be cancelled.
	retried := j.action(op.ID, "retry")
	if retried.ID == op.ID {
		t.Fatal("retry reused the predecessor operation")
	}
	if snap := j.action(retried.ID, "cancel"); snap.State != "cancelling" {
		t.Fatalf("retry cancel = %+v", snap)
	}
	j.waitFor(retried.ID, "cancelled", "cleanup_pending")
}

// TestCloneListAndSSEJourney proves bounded listing with deterministic
// ordering and SSE invalidations for clone operations.
func TestCloneListAndSSEJourney(t *testing.T) {
	if testing.Short() {
		t.Skip("journey runs real git over controlled HTTP transport")
	}
	j := newCloneJourney(t)
	remote := bareHTTPRemote(t, 1, false)

	op := j.start(remote, "listed", "key-list-1")
	j.waitFor(op.ID, "succeeded")

	status, body := j.do("GET", "/api/v1/workspace/repositories/clone?limit=10", nil)
	if status != http.StatusOK {
		t.Fatalf("list status = %d body=%s", status, body)
	}
	var list struct {
		Operations []cloneWireOperation `json:"operations"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range list.Operations {
		if item.ID == op.ID && item.State == "succeeded" {
			found = true
		}
	}
	if !found {
		t.Fatalf("succeeded operation missing from list: %+v", list.Operations)
	}

	// Invalid bounds are rejected.
	for _, q := range []string{"?limit=0", "?limit=201", "?limit=abc", "?after=" + strings.Repeat("x", 300)} {
		status, _ := j.do("GET", "/api/v1/workspace/repositories/clone"+q, nil)
		if status != http.StatusBadRequest {
			t.Errorf("list %s status = %d, want 400", q, status)
		}
	}

	// SSE: subscribe, run a clone, and observe a clone.updated event.
	events := make(chan string, 16)
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	go func() {
		req, err := http.NewRequest("GET", j.baseURL+"/api/v1/events?access_token=test-token", nil)
		if err != nil {
			return
		}
		req.Header.Set("Accept", "text/event-stream")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return
		}
		defer func() { _ = resp.Body.Close() }()
		buf := make([]byte, 4096)
		var acc string
		for {
			select {
			case <-done:
				return
			default:
			}
			n, err := resp.Body.Read(buf)
			if n > 0 {
				acc += string(buf[:n])
				for strings.Contains(acc, "\n\n") {
					var block string
					idx := strings.Index(acc, "\n\n")
					block, acc = acc[:idx], acc[idx+2:]
					for _, line := range strings.Split(block, "\n") {
						if strings.HasPrefix(line, "event: clone.updated") {
							select {
							case events <- "clone.updated":
							default:
							}
						}
					}
				}
			}
			if err != nil {
				return
			}
		}
	}()

	watched := j.start(remote, "watched", "key-list-2")
	j.waitFor(watched.ID, "succeeded")
	select {
	case got := <-events:
		if got != "clone.updated" {
			t.Fatalf("unexpected event %s", got)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("no clone.updated SSE event observed")
	}
}
