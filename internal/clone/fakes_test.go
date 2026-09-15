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

package clone

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
)

// fakeHandle is a scripted in-process stand-in for a git clone process.
type fakeHandle struct {
	pid        int
	identity   string
	done       chan struct{}
	termCh     chan struct{}
	result     RunResult
	terminated atomic.Bool
	spec       RunSpec
	mu         sync.Mutex
}

func (h *fakeHandle) Pid() int              { return h.pid }
func (h *fakeHandle) Pgid() int             { return h.pid }
func (h *fakeHandle) StartIdentity() string { return h.identity }

func (h *fakeHandle) Wait() RunResult {
	<-h.done
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.result
}

func (h *fakeHandle) Terminate() {
	if h.terminated.CompareAndSwap(false, true) {
		close(h.termCh)
	}
}

func (h *fakeHandle) terminatedCh() <-chan struct{} { return h.termCh }

// emitLine feeds one stderr line through the handle's captured stage
// callback and tail, mirroring the real runner's ingest path.
func (h *fakeHandle) emitLine(line string) {
	clean := stripControls(line)
	if h.spec.OnStage != nil && progressPattern.MatchString(clean) {
		h.spec.OnStage(StageTransferring, boundProgress(clean))
	}
}

// finish completes the handle with a run result.
func (h *fakeHandle) finish(res RunResult) {
	h.mu.Lock()
	h.result = res
	h.mu.Unlock()
	close(h.done)
}

// fakeRunner scripts clone executions. The script and onStart callbacks
// are guarded so tests may retarget them while earlier workers still run.
type fakeRunner struct {
	mu       sync.Mutex
	starts   []*fakeHandle
	nextPid  int
	script   func(h *fakeHandle)
	onStart  func(spec RunSpec)
	startErr error
}

func newFakeRunner(script func(h *fakeHandle)) *fakeRunner {
	return &fakeRunner{script: script, nextPid: 40000}
}

func (r *fakeRunner) setScript(script func(h *fakeHandle)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.script = script
}

func (r *fakeRunner) setOnStart(onStart func(spec RunSpec)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onStart = onStart
}

func (r *fakeRunner) setStartErr(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.startErr = err
}

func (r *fakeRunner) Start(spec RunSpec) (RunHandle, error) {
	r.mu.Lock()
	r.nextPid++
	pid := r.nextPid
	script := r.script
	onStart := r.onStart
	startErr := r.startErr
	r.mu.Unlock()
	if onStart != nil {
		onStart(spec)
	}
	if startErr != nil {
		return nil, startErr
	}
	h := &fakeHandle{
		pid:      pid,
		identity: "fake-start-" + time.Now().Format("150405.000000000"),
		done:     make(chan struct{}),
		termCh:   make(chan struct{}),
		spec:     spec,
	}
	r.mu.Lock()
	r.starts = append(r.starts, h)
	r.mu.Unlock()
	go script(h)
	return h, nil
}

func (r *fakeRunner) handles() []*fakeHandle {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*fakeHandle, len(r.starts))
	copy(out, r.starts)
	return out
}

// succeedScript finishes with exit code 0 after staging a minimal git
// repository skeleton in staging, like a real clone would.
func succeedScript(h *fakeHandle) {
	if err := os.MkdirAll(filepath.Join(h.spec.Staging, ".git"), 0o755); err == nil {
		_ = os.WriteFile(filepath.Join(h.spec.Staging, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644)
		_ = os.WriteFile(filepath.Join(h.spec.Staging, "README.md"), []byte("# cloned\n"), 0o644)
	}
	h.emitLine("Counting objects: 3, done.")
	h.emitLine("Receiving objects: 100% (3/3), done.")
	h.finish(RunResult{ExitCode: 0})
}

// hangScript blocks until terminated, then exits non-zero like a killed
// git process.
func hangScript(h *fakeHandle) {
	<-h.terminatedCh()
	h.finish(RunResult{ExitCode: -1})
}

// failScript finishes with exit code 128 and an authentication-flavored
// tail.
func failAuthScript(h *fakeHandle) {
	h.finish(RunResult{ExitCode: 128, OutputTail: "fatal: Authentication failed for 'https://example.com/acme/widget.git'"})
}

// serviceFixture wires a Service against a temp state dir and root.
type serviceFixture struct {
	t        *testing.T
	root     string
	rootLink string // alias path that expands to the same root
	stateDir string
	cfg      *config.Config
	runner   *fakeRunner
	// createGit overrides the repository-creation executor; nil uses the
	// real bounded git implementation.
	createGit func(ctx context.Context, dir string) error
	svc       *Service
	hooks     *recordingHooks
	now       func() time.Time
	// Per-fixture completion budget. Real-Git scripts need extra headroom
	// under the full race sweep; in-memory scripts keep the fast default.
	waitTimeout time.Duration
}

type recordingHooks struct {
	mu        sync.Mutex
	ops       []string
	workspace int
}

func (h *recordingHooks) operationChanged(kind, id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ops = append(h.ops, kind+"/"+id)
}

func (h *recordingHooks) workspaceChanged() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.workspace++
}

func (h *recordingHooks) workspaceEvents() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.workspace
}

func (h *recordingHooks) operationEvents() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.ops)
}

func newServiceFixture(t *testing.T, script func(h *fakeHandle)) *serviceFixture {
	t.Helper()
	// Resolve the temp dir through symlinks (macOS /var -> /private/var)
	// so path assertions match the canonical root the service records.
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	fx := &serviceFixture{
		t:           t,
		root:        root,
		stateDir:    stateDir,
		cfg:         &config.Config{WorkspaceRoots: []string{root}},
		runner:      newFakeRunner(script),
		hooks:       &recordingHooks{},
		waitTimeout: 5 * time.Second,
	}
	fx.now = time.Now
	svc, err := New(Options{
		StateDir: stateDir,
		Config:   func() *config.Config { return fx.cfg },
		Runner:   fx.runner,
		// Read through the fixture so tests may retarget the create
		// executor after construction; nil means the real bounded git.
		CreateGit: func(ctx context.Context, dir string) error {
			if fx.createGit != nil {
				return fx.createGit(ctx, dir)
			}
			return git.CreateRepository(ctx, dir)
		},
		Now: fx.now,
		Hooks: Hooks{
			OperationChanged: fx.hooks.operationChanged,
			WorkspaceChanged: fx.hooks.workspaceChanged,
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	fx.svc = svc
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = svc.Shutdown(ctx)
	})
	return fx
}

// startInput builds a well-formed start request against the fixture root.
func (fx *serviceFixture) startInput(key string) StartInput {
	return StartInput{
		Remote:         "https://example.com/acme/widget.git",
		Root:           fx.root,
		Destination:    "widget",
		IdempotencyKey: key,
	}
}

func (fx *serviceFixture) start(key string) Record {
	fx.t.Helper()
	rec, err := fx.svc.Start(context.Background(), fx.startInput(key))
	if err != nil {
		fx.t.Fatalf("Start: %v", err)
	}
	return rec
}

// waitForState polls the authoritative snapshot until the operation
// reaches one of the want states.
func (fx *serviceFixture) waitForState(id string, want ...State) Record {
	fx.t.Helper()
	deadline := time.Now().Add(fx.waitTimeout)
	for {
		rec, err := fx.svc.Snapshot(id)
		if err != nil {
			fx.t.Fatalf("Snapshot(%s): %v", id, err)
		}
		for _, w := range want {
			if rec.State == w {
				return rec
			}
		}
		if time.Now().After(deadline) {
			fx.t.Fatalf("operation %s never reached %v; last state %s", id, want, rec.State)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// makeGitRepo creates a real throwaway git repository with one commit.
func makeGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
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
	run("init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "initial")
	return dir
}

// makeEmptyGitRepo creates a repository with no commits (unborn HEAD).
func makeEmptyGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "--initial-branch=main", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	return dir
}
