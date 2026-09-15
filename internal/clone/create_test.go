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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
)

// createInput builds a well-formed create request against the fixture root.
func (fx *serviceFixture) createInput(key, destination string) CreateStartInput {
	return CreateStartInput{
		Root:           fx.root,
		Destination:    destination,
		IdempotencyKey: key,
	}
}

func (fx *serviceFixture) create(key, destination string) Record {
	fx.t.Helper()
	rec, err := fx.svc.Create(context.Background(), fx.createInput(key, destination))
	if err != nil {
		fx.t.Fatalf("Create: %v", err)
	}
	return rec
}

func (fx *serviceFixture) createErr(input CreateStartInput) *ServiceError {
	fx.t.Helper()
	_, err := fx.svc.Create(context.Background(), input)
	if err == nil {
		fx.t.Fatal("Create unexpectedly succeeded")
	}
	var serr *ServiceError
	if !errors.As(err, &serr) {
		fx.t.Fatalf("Create error = %T (%v), want *ServiceError", err, err)
	}
	return serr
}

// gitOut runs one git command in dir and returns its trimmed output.
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v: %s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// assertCreatedRepository proves the promised shape of a created repository:
// exactly one empty initial commit on main with Agentico's author and
// committer identity, a resolvable HEAD, no origin remote and no push.
func assertCreatedRepository(t *testing.T, dir string) {
	t.Helper()
	if got := gitOut(t, dir, "symbolic-ref", "--short", "HEAD"); got != "main" {
		t.Errorf("initial branch = %q, want main", got)
	}
	if got := gitOut(t, dir, "rev-list", "--count", "HEAD"); got != "1" {
		t.Errorf("commit count = %q, want 1 (exactly one initial commit)", got)
	}
	if got := gitOut(t, dir, "log", "-1", "--format=%an <%ae>"); got != "Agentico <agentico@localhost>" {
		t.Errorf("author = %q, want Agentico <agentico@localhost>", got)
	}
	if got := gitOut(t, dir, "log", "-1", "--format=%cn <%ce>"); got != "Agentico <agentico@localhost>" {
		t.Errorf("committer = %q, want Agentico <agentico@localhost>", got)
	}
	if got := gitOut(t, dir, "show", "--name-only", "--format=", "HEAD"); got != "" {
		t.Errorf("initial commit is not empty: changed %q", got)
	}
	if got := gitOut(t, dir, "remote"); got != "" {
		t.Errorf("created repository has remotes %q, want none (no origin, no push)", got)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git", "HEAD")); err != nil {
		t.Errorf("HEAD does not resolve: %v", err)
	}
}

func TestCreatePublishesFeatureReadyRepository(t *testing.T) {
	t.Parallel()
	fx := newServiceFixture(t, succeedScript)

	rec := fx.create("create-1", "fresh")
	if rec.Kind != KindCreate {
		t.Errorf("record kind = %q, want create", rec.Kind)
	}
	if rec.State != StateSucceeded {
		t.Fatalf("final state = %s, want succeeded", rec.State)
	}
	dest := filepath.Join(fx.root, "fresh")
	assertCreatedRepository(t, dest)
	if rec.Published == nil {
		t.Fatal("succeeded record has no publication evidence")
	}
	if rec.Published.RepoKey != "fresh" || rec.Published.Path != dest || !rec.Published.HasHead {
		t.Errorf("publication = %+v", rec.Published)
	}
	// The publication identity is the server-resolved identity of the
	// repository actually published.
	resolved, ok := git.ResolveRepoIdentity(dest)
	if !ok {
		t.Fatal("created repository identity is unresolvable")
	}
	if rec.Published.Identity == nil || *rec.Published.Identity != *wirePublicationIdentity(resolved) {
		t.Errorf("publication identity = %+v, want %+v", rec.Published.Identity, wirePublicationIdentity(resolved))
	}
	// No staging residue and no second repository.
	entries, err := os.ReadDir(fx.root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), stagingPrefix) {
			t.Errorf("staging residue visible in root: %s", e.Name())
		}
	}
	// Publication changed discovery: the workspace hook fired.
	if fx.hooks.workspaceEvents() == 0 {
		t.Error("workspace hook never fired after publication")
	}
}

func TestCreateIdempotencyAndDestinationConflicts(t *testing.T) {
	t.Parallel()
	fx := newServiceFixture(t, succeedScript)

	first := fx.create("create-1", "fresh")
	// Same key, same input: the retained result replays without a second
	// repository or git execution.
	again := fx.create("create-1", "fresh")
	if again.ID != first.ID || again.State != StateSucceeded {
		t.Errorf("replay = %s/%s, want %s/succeeded", again.ID, again.State, first.ID)
	}
	if got := gitOut(t, filepath.Join(fx.root, "fresh"), "rev-list", "--count", "HEAD"); got != "1" {
		t.Errorf("replay re-created the repository: commit count = %q", got)
	}

	// Same key, changed input: conflict.
	changed := fx.createInput("create-1", "other")
	if serr := fx.createErr(changed); serr.Code != CodeIdempotencyConflict {
		t.Errorf("changed-input code = %s, want %s", serr.Code, CodeIdempotencyConflict)
	}

	// A different key for the now-published destination: exists.
	if serr := fx.createErr(fx.createInput("create-2", "fresh")); serr.Code != CodeDestinationExists {
		t.Errorf("existing destination code = %s, want %s", serr.Code, CodeDestinationExists)
	}

	// An empty directory at the destination is still an existing
	// destination, never silently adopted.
	empty := filepath.Join(fx.root, "occupied")
	if err := os.Mkdir(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	if serr := fx.createErr(fx.createInput("create-3", "occupied")); serr.Code != CodeDestinationExists {
		t.Errorf("empty-dir destination code = %s, want %s", serr.Code, CodeDestinationExists)
	}
	if entries, _ := os.ReadDir(empty); len(entries) != 0 {
		t.Error("existing empty directory was mutated")
	}

	// An explicitly registered repository name shadows the child name.
	fx.cfg.Repos = map[string]config.RepoConfig{"shadowed": {Path: "/elsewhere/shadowed"}}
	if serr := fx.createErr(fx.createInput("create-4", "shadowed")); serr.Code != CodeDestinationShadowed {
		t.Errorf("shadowed name code = %s, want %s", serr.Code, CodeDestinationShadowed)
	}
}

func TestCreateValidatesChildNameAndRoot(t *testing.T) {
	t.Parallel()
	fx := newServiceFixture(t, succeedScript)

	for i, destination := range []string{"", ".hidden", "a/b", "..", "a b", "-dash", "ends.", strings.Repeat("x", 129)} {
		if serr := fx.createErr(fx.createInput(fmt.Sprintf("bad-%d", i), destination)); serr.Code != ValidationDestinationInvalid {
			t.Errorf("destination %q code = %s, want %s", destination, serr.Code, ValidationDestinationInvalid)
		}
	}

	// The root must be a configured, clone-eligible workspace root.
	unconfigured := fx.createInput("root-1", "fresh")
	unconfigured.Root = filepath.Join(fx.root, "..")
	if serr := fx.createErr(unconfigured); serr.Code != CodeRootIneligible {
		t.Errorf("unconfigured root code = %s, want %s", serr.Code, CodeRootIneligible)
	}
	repoRoot := filepath.Join(fx.root, "isa-repo")
	if err := os.Mkdir(repoRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", repoRoot, "init").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	fx.cfg.WorkspaceRoots = []string{fx.root, repoRoot}
	intoRepoRoot := fx.createInput("root-2", "fresh")
	intoRepoRoot.Root = repoRoot
	if serr := fx.createErr(intoRepoRoot); serr.Code != CodeRootIneligible {
		t.Errorf("repository root accepted: code = %s", serr.Code)
	}
}

// blockingCreateGit holds every creation until released, then builds a
// real repository, so a test can observe an in-flight create record and
// its destination reservation before the creation completes.
func blockingCreateGit(release chan struct{}) func(context.Context, string) error {
	return func(ctx context.Context, dir string) error {
		<-release
		return git.CreateRepository(ctx, dir)
	}
}

func TestCreateSharesDestinationReservationWithClone(t *testing.T) {
	t.Parallel()
	fx := newServiceFixture(t, hangScript)

	// A running clone holds the destination: a create is refused and
	// starts no git.
	cloneRec := fx.start("clone-hold")
	for {
		snapshot, err := fx.svc.Snapshot(cloneRec.ID)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.State == StateRunning {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if serr := fx.createErr(fx.createInput("create-vs-clone", "widget")); serr.Code != CodeDestinationReserved {
		t.Errorf("create over clone reservation code = %s, want %s", serr.Code, CodeDestinationReserved)
	}

	// And the reverse: an in-flight create refuses a clone start on the
	// same destination.
	release := make(chan struct{})
	fx.createGit = blockingCreateGit(release)
	createDone := make(chan struct{})
	go func() {
		defer close(createDone)
		rec, err := fx.svc.Create(context.Background(), fx.createInput("create-hold", "second"))
		if err == nil && rec.State != StateSucceeded {
			fx.t.Errorf("blocking create ended as %s", rec.State)
		}
	}()
	waitForCreateRunning(t, fx, "second")
	cloneOverCreate := fx.startInput("clone-vs-create")
	cloneOverCreate.Destination = "second"
	_, err := fx.svc.Start(context.Background(), cloneOverCreate)
	var serr *ServiceError
	if !errors.As(err, &serr) || serr.Code != CodeDestinationReserved {
		t.Errorf("clone over create reservation error = %v, want code %s", err, CodeDestinationReserved)
	}
	close(release)
	<-createDone
}

// waitForCreateRunning polls the store until a create record for the given
// destination is running (its reservation is observable then).
func waitForCreateRunning(t *testing.T, fx *serviceFixture, destination string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		found := false
		for _, rec := range fx.storeForTest().all() {
			if recordKind(&rec) == KindCreate && rec.Destination == destination && ActiveStates[rec.State] {
				found = true
			}
		}
		if found {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("create for %s never became active", destination)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// storeForTest exposes the fixture service's store to test helpers in this
// file (both live in package clone).
func (fx *serviceFixture) storeForTest() *store { return fx.svc.store }

func TestCreateFailureCleansOwnedStagingAndReleasesReservation(t *testing.T) {
	t.Parallel()
	fx := newServiceFixture(t, succeedScript)
	fx.createGit = func(_ context.Context, dir string) error {
		// Leave provably owned incomplete data behind, then fail: only
		// that staging may be removed, and the reservation must release.
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "partial"), []byte("x"), 0o644); err != nil {
			return err
		}
		return errors.New("scripted create failure")
	}

	rec := fx.create("create-fail", "doomed")
	if rec.State != StateFailed {
		t.Fatalf("final state = %s, want failed", rec.State)
	}
	if rec.Error == nil || rec.Error.Code != FailureExecution {
		t.Errorf("failure error = %+v, want %s", rec.Error, FailureExecution)
	}
	if _, err := os.Lstat(filepath.Join(fx.root, stagingNameFor(KindCreate, rec.ID))); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("owned staging survived a failed create: %v", err)
	}
	// A fresh key may take the destination once the failed attempt
	// resolved: the reservation is gone.
	fx.createGit = nil
	fresh := fx.create("create-retry", "doomed")
	if fresh.State != StateSucceeded {
		t.Fatalf("retry after cleanup state = %s, want succeeded", fresh.State)
	}
}

func TestCreateNeverTouchesDataItDoesNotOwn(t *testing.T) {
	t.Parallel()
	fx := newServiceFixture(t, succeedScript)
	fx.createGit = func(_ context.Context, dir string) error {
		_ = os.MkdirAll(dir, 0o755)
		return errors.New("scripted create failure")
	}
	rec := fx.create("create-uncertain", "doomed")
	if rec.State != StateFailed {
		t.Fatalf("state = %s, want failed", rec.State)
	}
	// The failed attempt cleaned its own staging. A foreign directory
	// planted at the same path is not owned staging: a cleanup retry must
	// leave it untouched — a path alone is never ownership.
	foreign := filepath.Join(fx.root, stagingNameFor(KindCreate, rec.ID))
	if err := os.Mkdir(foreign, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(foreign, "not-yours"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	after, err := fx.svc.Cleanup(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != StateFailed {
		t.Errorf("cleanup retry reclassified a resolved record to %s", after.State)
	}
	if _, err := os.Lstat(filepath.Join(foreign, "not-yours")); err != nil {
		t.Fatalf("foreign data at the staging path was deleted: %v", err)
	}
}

func TestCreateConcurrentAttemptsReserveExactlyOneWinner(t *testing.T) {
	t.Parallel()
	fx := newServiceFixture(t, succeedScript)

	const attempts = 8
	start := make(chan struct{})
	results := make([]Record, attempts)
	errs := make([]*ServiceError, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			rec, err := fx.svc.Create(context.Background(), fx.createInput(fmt.Sprintf("race-%d", i), "raced"))
			if err != nil {
				var serr *ServiceError
				if !errors.As(err, &serr) {
					fx.t.Errorf("unexpected error type: %v", err)
					return
				}
				errs[i] = serr
				return
			}
			results[i] = rec
		}(i)
	}
	close(start)
	wg.Wait()

	succeeded := 0
	for i := 0; i < attempts; i++ {
		if errs[i] == nil {
			if results[i].State != StateSucceeded {
				t.Errorf("attempt %d ended as %s", i, results[i].State)
			} else {
				succeeded++
			}
			continue
		}
		if errs[i].Code != CodeDestinationReserved && errs[i].Code != CodeDestinationExists {
			t.Errorf("attempt %d rejected with %s, want reserved/exists", i, errs[i].Code)
		}
	}
	if succeeded != 1 {
		t.Fatalf("exactly one winner expected, got %d", succeeded)
	}
	assertCreatedRepository(t, filepath.Join(fx.root, "raced"))
}

// hostileGitHome builds a HOME whose global git configuration tries to
// change every promise: a different identity, forced signing, a template
// directory with hooks, and a hooks path that would execute a script.
func hostileGitHome(t *testing.T, templateHead string) string {
	t.Helper()
	home := t.TempDir()
	template := filepath.Join(home, "hostile-template")
	hooks := filepath.Join(home, "hostile-hooks")
	if err := os.MkdirAll(template, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	if templateHead != "" {
		if err := os.MkdirAll(filepath.Join(template, "refs", "heads"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(template, "HEAD"), []byte("ref: refs/heads/"+templateHead+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	hook := filepath.Join(hooks, "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\ntouch \"$0.ran\"\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitconfig := strings.Join([]string{
		"[user]",
		" name = Imposter",
		" email = imposter@example.com",
		"[commit]",
		" gpgsign = true",
		"[core]",
		" hooksPath = " + hooks,
		"[init]",
		" templateDir = " + template,
	}, "\n")
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(gitconfig), 0o644); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestCreateIsImmuneToHostileGitConfiguration(t *testing.T) {
	// Not parallel: it repoints the process HOME for the real git child.
	home := hostileGitHome(t, "")
	t.Setenv("HOME", home)
	fx := newServiceFixture(t, succeedScript)

	rec := fx.create("hostile-1", "protected")
	if rec.State != StateSucceeded {
		t.Fatalf("state = %s, want succeeded under hostile configuration", rec.State)
	}
	// Inherited default-branch, signing, hook and template settings cannot
	// change the promised result or execute hooks.
	assertCreatedRepository(t, filepath.Join(fx.root, "protected"))
	if _, err := os.Stat(filepath.Join(home, "hostile-hooks", "pre-commit.ran")); err == nil {
		t.Error("a hook executed during creation despite core.hooksPath override")
	}
}

func TestCreateHostileTemplateCannotStealTheBranch(t *testing.T) {
	// Not parallel: it repoints the process HOME for the real git child.
	home := hostileGitHome(t, "not-main")
	t.Setenv("HOME", home)
	fx := newServiceFixture(t, succeedScript)

	rec := fx.create("hostile-2", "stolen")
	// --initial-branch wins over a hostile template HEAD: the promise is
	// verified before publication, and a template that tried to steal the
	// branch cannot change the result.
	if rec.State != StateSucceeded {
		t.Fatalf("state = %s, want succeeded (initial-branch overrides the template)", rec.State)
	}
	assertCreatedRepository(t, filepath.Join(fx.root, "stolen"))
	if _, err := os.Stat(filepath.Join(home, "hostile-hooks", "pre-commit.ran")); err == nil {
		t.Error("a hook executed during creation despite core.hooksPath override")
	}
}

func TestCreateReplaysAndRecoversAcrossRestart(t *testing.T) {
	t.Parallel()
	fx := newServiceFixture(t, succeedScript)
	first := fx.create("restart-1", "durable")
	if first.State != StateSucceeded {
		t.Fatalf("state = %s, want succeeded", first.State)
	}

	// A restarted process reloads the durable record: the same key replays
	// the retained success instead of initializing again.
	restarted, err := New(Options{
		StateDir: fx.stateDir,
		Config:   func() *config.Config { return fx.cfg },
		Now:      fx.now,
	})
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := restarted.Create(context.Background(), fx.createInput("restart-1", "durable"))
	if err != nil {
		t.Fatalf("replay after restart: %v", err)
	}
	if replayed.ID != first.ID || replayed.State != StateSucceeded || replayed.Published == nil {
		t.Fatalf("replay after restart = %s/%s, want %s/succeeded with publication", replayed.ID, replayed.State, first.ID)
	}
	if got := gitOut(t, filepath.Join(fx.root, "durable"), "rev-list", "--count", "HEAD"); got != "1" {
		t.Errorf("replay re-initialized the repository: commit count = %q", got)
	}
	// A create record never appears in the clone operation listing.
	listed, err := restarted.List(ListQuery{Limit: MaxListLimit})
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range listed.Operations {
		if rec.ID == first.ID {
			t.Error("create record leaked into the clone operation listing")
		}
	}
}

func TestCreateRecoversInterruptedAttemptAtStartup(t *testing.T) {
	t.Parallel()
	// A create record left mid-flight by a crash (running, staging with
	// ownership marker, no process) is recovered exactly like a clone
	// attempt: interrupted, owned staging removed, reservation released —
	// and the crashed key replays its truthful outcome instead of
	// initializing again.
	fx := newServiceFixture(t, succeedScript)
	id := newID(KindCreate)
	nonce := newNonce()
	stagingRel := stagingNameFor(KindCreate, id)
	stagingPath := filepath.Join(fx.root, stagingRel)
	if err := os.MkdirAll(filepath.Join(stagingPath, stagingWorkDir), 0o700); err != nil {
		t.Fatal(err)
	}
	handle, _, err := OpenRootDir(fx.root)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	_, idty, err := OpenRootDir(fx.root)
	if err != nil {
		t.Fatal(err)
	}
	marker, err := json.Marshal(ownershipMarker{
		OperationID: id, Nonce: nonce,
		RootDevice: idty.Device, RootInode: idty.Inode,
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stagingPath, ownershipMarkerName), marker, 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	crashed := Record{
		ID: id, Kind: KindCreate, IdempotencyKey: "crash-1",
		InputFingerprint: inputFingerprint("", fx.root, "crashed"),
		RootPath:         fx.root, RootResolved: fx.root,
		RootDevice: idty.Device, RootInode: idty.Inode,
		Destination: "crashed", DestinationPath: filepath.Join(fx.root, "crashed"),
		StagingPath: stagingPath, OwnershipNonce: nonce,
		State: StateRunning, Stage: StagePreparing,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := fx.svc.save(&crashed); err != nil {
		t.Fatalf("craft create record: %v", err)
	}

	// The in-flight reservation refuses competing mutations before
	// recovery runs.
	if serr := fx.createErr(fx.createInput("compete-1", "crashed")); serr.Code != CodeDestinationReserved {
		t.Errorf("pre-recovery competing create code = %s, want %s", serr.Code, CodeDestinationReserved)
	}

	if err := fx.svc.Recover(); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	final, err := fx.svc.Snapshot(crashed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.State != StateInterrupted {
		t.Fatalf("recovered state = %s, want interrupted", final.State)
	}
	if _, err := os.Lstat(stagingPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("owned staging survived recovery: %v", err)
	}

	// The reservation is released: a fresh create may take the destination.
	fresh := fx.create("crash-2", "crashed")
	if fresh.State != StateSucceeded {
		t.Fatalf("fresh create after recovery state = %s, want succeeded", fresh.State)
	}
	assertCreatedRepository(t, filepath.Join(fx.root, "crashed"))

	// Replaying the crashed key returns its truthful interrupted record,
	// never a second initialization of the now-published destination.
	replay, err := fx.svc.Create(context.Background(), fx.createInput("crash-1", "crashed"))
	if err != nil {
		t.Fatalf("crashed-key replay: %v", err)
	}
	if replay.ID != crashed.ID || replay.State != StateInterrupted {
		t.Errorf("crashed-key replay = %s/%s, want %s/interrupted", replay.ID, replay.State, crashed.ID)
	}
	if got := gitOut(t, filepath.Join(fx.root, "crashed"), "rev-list", "--count", "HEAD"); got != "1" {
		t.Errorf("crashed-key replay re-initialized the repository: commit count = %q", got)
	}
}
