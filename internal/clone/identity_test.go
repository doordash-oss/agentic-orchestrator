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
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/git"
)

// realRepoScript stages a real committed git repository, like a genuine
// clone would, so repository identity resolution has something true to bind.
func realRepoScript(h *fakeHandle) {
	if err := os.MkdirAll(h.spec.Staging, 0o755); err != nil {
		h.finish(RunResult{ExitCode: 128, OutputTail: err.Error()})
		return
	}
	failed := false
	run := func(args ...string) {
		if failed {
			return
		}
		cmd := exec.Command("git", append([]string{"-C", h.spec.Staging}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			failed = true
			h.finish(RunResult{ExitCode: 128, OutputTail: string(out)})
		}
	}
	run("init", "-b", "main")
	_ = os.WriteFile(filepath.Join(h.spec.Staging, "README.md"), []byte("# cloned\n"), 0o644)
	run("add", ".")
	run("commit", "-m", "initial")
	if failed {
		return
	}
	h.emitLine("Receiving objects: 100% (3/3), done.")
	h.finish(RunResult{ExitCode: 0})
}

func TestPublicationIdentityBindsTheActuallyPublishedRepository(t *testing.T) {
	t.Parallel()
	fx := newServiceFixture(t, realRepoScript)
	rec := fx.start("identity-1")
	final := fx.waitForState(rec.ID, StateSucceeded)

	if final.Published == nil {
		t.Fatal("succeeded record has no publication evidence")
	}
	if final.Published.Identity == nil {
		t.Fatal("publication carries no provable repository identity")
	}
	dest := filepath.Join(fx.root, "widget")
	resolved, ok := git.ResolveRepoIdentity(dest)
	if !ok {
		t.Fatal("expected resolvable identity at the published destination")
	}
	want := wirePublicationIdentity(resolved)
	if *final.Published.Identity != *want {
		t.Errorf("publication identity = %+v, want %+v", final.Published.Identity, want)
	}
	if final.Published.Identity.Path != dest {
		t.Errorf("identity path = %q, want %q", final.Published.Identity.Path, dest)
	}

	// The durable marker pins the same identity for recovery.
	data, err := os.ReadFile(filepath.Join(dest, ".git", publicationMarkerName))
	if err != nil {
		t.Fatalf("read publication marker: %v", err)
	}
	var marker publicationMarker
	if err := json.Unmarshal(data, &marker); err != nil {
		t.Fatalf("parse publication marker: %v", err)
	}
	if marker.Identity == nil || *marker.Identity != *final.Published.Identity {
		t.Errorf("marker identity = %+v, want the pinned publication identity", marker.Identity)
	}
	if marker.OperationID != final.ID || marker.Nonce != final.OwnershipNonce {
		t.Errorf("marker binding = %s/%s, want %s/%s", marker.OperationID, marker.Nonce, final.ID, final.OwnershipNonce)
	}
}

func TestPublicationIdentityRefusesReplacementAtTheSamePath(t *testing.T) {
	t.Parallel()
	fx := newServiceFixture(t, realRepoScript)
	rec := fx.start("identity-2")
	final := fx.waitForState(rec.ID, StateSucceeded)
	if final.Published == nil || final.Published.Identity == nil {
		t.Fatal("expected a provable publication identity before replacement")
	}

	// Replace the checkout's git directory at the same path: the pinned
	// identity must not transfer to the replacement.
	dest := filepath.Join(fx.root, "widget")
	if err := os.RemoveAll(filepath.Join(dest, ".git")); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "init", "-b", "main", dest)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init replacement: %v\n%s", err, out)
	}

	pub := fx.svc.publicationIdentity(&final)
	if pub.Identity != nil {
		t.Errorf("replacement inherited the publication identity: %+v", pub.Identity)
	}
	// The publication itself stays viewable with its key.
	if pub.RepoKey != "widget" {
		t.Errorf("repo key = %q, want widget", pub.RepoKey)
	}
}

func TestPublicationIdentityAbsentForLegacyMarkers(t *testing.T) {
	t.Parallel()
	fx := newServiceFixture(t, realRepoScript)
	rec := fx.start("identity-3")
	final := fx.waitForState(rec.ID, StateSucceeded)
	dest := filepath.Join(fx.root, "widget")

	// A marker written before identities existed pins no identity: the
	// publication stays viewable but cannot be adopted by key/path fallback.
	legacy, err := json.Marshal(publicationMarker{
		OperationID: final.ID,
		Nonce:       final.OwnershipNonce,
		PublishedAt: final.Published.PublishedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, ".git", publicationMarkerName), legacy, 0o600); err != nil {
		t.Fatal(err)
	}

	pub := fx.svc.publicationIdentity(&final)
	if pub.Identity != nil {
		t.Errorf("legacy marker produced an identity: %+v", pub.Identity)
	}
}

func TestStagedPublicationIdentityPinsStagedGitDirectory(t *testing.T) {
	t.Parallel()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, stagingName("clone-x"), stagingWorkDir)
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "init", "-b", "main", work)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	dest := filepath.Join(root, "widget")
	rec := Record{
		StagingPath:     filepath.Join(root, stagingName("clone-x")),
		DestinationPath: dest,
	}

	pinned := stagedPublicationIdentity(&rec)
	if pinned == nil {
		t.Fatal("staged identity missing")
	}
	if pinned.Path != dest {
		t.Errorf("pinned path = %q, want the destination %q", pinned.Path, dest)
	}
	if pinned.CommonDir != filepath.Join(dest, ".git") {
		t.Errorf("pinned common dir = %q, want %q", pinned.CommonDir, filepath.Join(dest, ".git"))
	}
	resolved, ok := git.ResolveRepoIdentity(work)
	if !ok {
		t.Fatal("expected resolvable staged identity")
	}
	// The rename preserves the .git directory's filesystem identity, so the
	// staged pin must agree with the staged repository on device and inode.
	if pinned.Device != git.FormatIdentityDevice(resolved.Device) ||
		pinned.Inode != git.FormatIdentityInode(resolved.Inode) {
		t.Errorf("pinned filesystem identity = %s/%s, want %s/%s",
			pinned.Device, pinned.Inode,
			git.FormatIdentityDevice(resolved.Device), git.FormatIdentityInode(resolved.Inode))
	}
}
