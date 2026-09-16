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

package selfupdate

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stagedReleaseFixture assembles the full signed-release staging inputs: an
// installed executable, a fixture release server serving a REAL platform
// tarball whose executable entry prints the target banner, and the verified
// release handle.
type stagedReleaseFixture struct {
	exec     Executable
	server   *releaseFixtureServer
	client   *FeedClient
	release  VerifiedRelease
	resolved ResolvedRelease
	tarball  []byte
}

// stagedExecutableBody is the archive's agentico entry: a real executable
// script whose --version banner reports the target version.
func stagedExecutableBody(version string) []byte {
	return []byte("#!/bin/sh\ncase \"$1\" in --version) echo 'agentico v" + version + "';; esac\n")
}

func newStagedReleaseFixture(t *testing.T, version string) *stagedReleaseFixture {
	t.Helper()
	f := &stagedReleaseFixture{exec: installExecutable(t, "installed build")}
	f.tarball = buildTarGz(t, []tarEntry{
		{name: "agentico-" + version + "/", typeflag: tar.TypeDir, mode: 0o755},
		regEntry("agentico-"+version+"/README.md", 0o644, []byte("notes\n")),
		regEntry("agentico", 0o755, stagedExecutableBody(version)),
	})
	f.server = newReleaseFixtureServer(t)
	f.resolved = f.server.publishRelease(t, fixtureReleaseOptions{
		tag:          "v" + version,
		goos:         "darwin",
		goarch:       "arm64",
		tarballBytes: f.tarball,
	})
	f.client = f.server.fixtureClient(t)
	release, err := f.client.VerifyResolvedRelease(context.Background(), f.resolved)
	if err != nil {
		t.Fatalf("verify release: %v", err)
	}
	f.release = release
	return f
}

func eligibleStageOpts(current string) StageReleaseOptions {
	return StageReleaseOptions{
		CurrentVersion: current,
		Eligibility:    Eligibility{Supported: true, Install: InstallTarball},
	}
}

func TestStageVerifiedReleaseHappyPath(t *testing.T) {
	f := newStagedReleaseFixture(t, "2.1.0")
	staged, err := f.client.StageVerifiedRelease(context.Background(), f.exec, f.release, eligibleStageOpts("2.0.0"))
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	// Staging lives on the executable's filesystem inside the lease dir.
	leaseDir := LeaseDir(f.exec.Path)
	if !strings.HasPrefix(staged.TxDir(), leaseDir+string(filepath.Separator)) {
		t.Fatalf("staging dir %s is not inside the lease dir %s", staged.TxDir(), leaseDir)
	}
	// Nothing is written into the installed executable's directory beyond
	// the hidden lease dir that already anchors update ownership.
	installDir := filepath.Dir(f.exec.Path)
	entries, err := os.ReadDir(installDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != filepath.Base(f.exec.Path) && e.Name() != filepath.Base(leaseDirPath(f.exec)) {
			t.Fatalf("staging wrote unexpected entry %q into the install dir", e.Name())
		}
	}
	for _, name := range []string{"staging.json", releaseArchiveName, releaseCandidateName} {
		if _, err := os.Stat(filepath.Join(staged.TxDir(), name)); err != nil {
			t.Fatalf("staging object %s missing: %v", name, err)
		}
	}
	info, err := os.Stat(filepath.Join(staged.TxDir(), releaseArchiveName))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("archive mode = %v, want 0600", info.Mode().Perm())
	}
	if staged.CandidateDigest() != mustDigest(t, staged.CandidatePath()) {
		t.Fatal("candidate digest mismatch")
	}
	// The install root is untouched: no receipt, no suppression, same bytes.
	if digest := mustDigest(t, f.exec.Path); digest != f.exec.Digest {
		t.Fatal("staging changed the installed executable")
	}
	if _, err := os.Stat(ReceiptPath(f.exec.Path)); !os.IsNotExist(err) {
		t.Fatalf("staging wrote an install receipt: %v", err)
	}
}

func TestStageVerifiedReleaseRejectsDigestMismatch(t *testing.T) {
	f := newStagedReleaseFixture(t, "2.1.0")
	// Swap the served tarball bytes after verification: the manifest entry
	// still binds the original digest, so the complete archive digest check
	// must reject before extraction.
	f.server.mu.Lock()
	f.server.assets["/assets/"+f.resolved.Tarball.Name] = buildTarGz(t, []tarEntry{
		regEntry("agentico", 0o755, []byte("#!/bin/sh\necho forged\n")),
	})
	f.server.mu.Unlock()
	_, err := f.client.StageVerifiedRelease(context.Background(), f.exec, f.release, eligibleStageOpts("2.0.0"))
	if err == nil || !strings.Contains(err.Error(), "does not match the signed manifest entry") {
		t.Fatalf("expected digest mismatch rejection, got %v", err)
	}
	assertNoStagingResidue(t, f.exec.Path)
}

func TestStageVerifiedReleaseRejectsUnsafeArchive(t *testing.T) {
	f := newStagedReleaseFixture(t, "2.1.0")
	// A tarball whose digest matches the manifest (re-signed) but whose
	// content is unsafe: a traversal entry plus a decoy executable.
	unsafe := buildTarGz(t, []tarEntry{
		regEntry("agentico", 0o755, stagedExecutableBody("2.1.0")),
		regEntry("../evil", 0o755, []byte("#!/bin/sh\ntrue\n")),
	})
	f.server.mu.Lock()
	f.server.assets["/assets/"+f.resolved.Tarball.Name] = unsafe
	f.server.republishTarball(t, unsafe)
	f.server.mu.Unlock()
	// Re-verify against the mutated fixture: the signed manifest now binds
	// the malicious tarball, so only the archive safety rules can reject.
	verified, err := f.client.VerifyResolvedRelease(context.Background(), f.resolved)
	if err != nil {
		t.Fatalf("re-verify mutated fixture: %v", err)
	}
	_, err = f.client.StageVerifiedRelease(context.Background(), f.exec, verified, eligibleStageOpts("2.0.0"))
	if err == nil || !strings.Contains(err.Error(), "escapes the archive root") {
		t.Fatalf("expected unsafe archive rejection, got %v", err)
	}
	assertNoStagingResidue(t, f.exec.Path)
}

func assertNoStagingResidue(t *testing.T, execPath string) {
	t.Helper()
	entries, err := os.ReadDir(LeaseDir(execPath))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), txDirPrefix) {
			t.Fatalf("failed staging left transaction dir %s behind", e.Name())
		}
	}
}

func TestStageVerifiedReleaseDownloadBounds(t *testing.T) {
	restore := shrinkDownloadBounds(t, 4096, 10*time.Minute, 60*time.Second)
	defer restore()
	f := newStagedReleaseFixture(t, "2.1.0")

	// Compressed cap: an incompressible body just past the lowered cap.
	big := bytes.Repeat([]byte{0x00}, 0)
	for i := 0; i < 4097; i++ {
		big = append(big, byte(i%251+1))
	}
	f.server.mu.Lock()
	f.server.assets["/assets/"+f.resolved.Tarball.Name] = big
	f.server.mu.Unlock()
	_, err := f.client.StageVerifiedRelease(context.Background(), f.exec, f.release, eligibleStageOpts("2.0.0"))
	if err == nil || !strings.Contains(err.Error(), "compressed limit") {
		t.Fatalf("expected compressed cap rejection, got %v", err)
	}

	// Exact cap boundary succeeds (the real fixture tarball is far under the
	// lowered cap).
	restore()
	f2 := newStagedReleaseFixture(t, "2.1.0")
	staged, err := f2.client.StageVerifiedRelease(context.Background(), f2.exec, f2.release, eligibleStageOpts("2.0.0"))
	if err != nil {
		t.Fatalf("boundary stage: %v", err)
	}
	if staged.CandidatePath() == "" {
		t.Fatal("no candidate extracted")
	}
}

func TestStageVerifiedReleaseDeceptiveContentLength(t *testing.T) {
	f := newStagedReleaseFixture(t, "2.1.0")
	// A declared Content-Length beyond the cap is rejected before any byte
	// is streamed, even when the real body is small.
	f.server.mu.Lock()
	f.server.assets["/assets/"+f.resolved.Tarball.Name] = []byte("tiny body")
	f.server.assetHeaders = map[string]string{"Content-Length": fmt.Sprintf("%d", ArchiveMaxCompressedBytes+1)}
	f.server.mu.Unlock()
	_, err := f.client.StageVerifiedRelease(context.Background(), f.exec, f.release, eligibleStageOpts("2.0.0"))
	if err == nil || !strings.Contains(err.Error(), "declares more than") {
		t.Fatalf("expected declared-length rejection, got %v", err)
	}
	assertNoStagingResidue(t, f.exec.Path)
}

func TestStageVerifiedReleaseInactivityBound(t *testing.T) {
	restore := shrinkDownloadBounds(t, ArchiveMaxCompressedBytes, 400*time.Millisecond, 10*time.Minute)
	defer restore()
	// Serve the tarball in two chunks with a long stall between them: the
	// inactivity watchdog must abort the download.
	stall := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		half := len(testStallBody) / 2
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(testStallBody[:half])
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
		_, _ = w.Write(testStallBody[half:])
	}))
	t.Cleanup(stall.Close)
	f := newStagedReleaseFixture(t, "2.1.0")
	f.server.mu.Lock()
	f.server.assets["/assets/"+f.resolved.Tarball.Name] = testStallBody
	f.server.assetRedirect = stall.URL
	f.server.mu.Unlock()
	start := time.Now()
	_, err := f.client.StageVerifiedRelease(context.Background(), f.exec, f.release, eligibleStageOpts("2.0.0"))
	if err == nil {
		t.Fatal("expected inactivity rejection")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("inactivity bound not enforced promptly: %v", elapsed)
	}
	assertNoStagingResidue(t, f.exec.Path)
}

var testStallBody = func() []byte {
	body := make([]byte, 1<<16)
	for i := range body {
		body[i] = byte(i%251 + 1)
	}
	return body
}()

func TestStageVerifiedReleaseTotalDeadline(t *testing.T) {
	restore := shrinkDownloadBounds(t, ArchiveMaxCompressedBytes, 10*time.Minute, 500*time.Millisecond)
	defer restore()
	// An endless trickle of progress never trips the inactivity bound; only
	// the total deadline stops it.
	endless := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		chunk := bytes.Repeat([]byte{7}, 512)
		for {
			if _, err := w.Write(chunk); err != nil {
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			time.Sleep(50 * time.Millisecond)
		}
	}))
	t.Cleanup(endless.Close)
	f := newStagedReleaseFixture(t, "2.1.0")
	f.server.mu.Lock()
	f.server.assetRedirect = endless.URL
	f.server.mu.Unlock()
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := f.client.StageVerifiedRelease(ctx, f.exec, f.release, eligibleStageOpts("2.0.0"))
	if err == nil {
		t.Fatal("expected total-deadline rejection")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("total deadline not enforced promptly: %v", elapsed)
	}
	assertNoStagingResidue(t, f.exec.Path)
}

func TestStageVerifiedReleaseCancellation(t *testing.T) {
	f := newStagedReleaseFixture(t, "2.1.0")
	// Cancel mid-download: responses and files close, no candidate appears.
	block := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		<-block
	}))
	t.Cleanup(slow.Close)
	t.Cleanup(func() { close(block) })
	f.server.mu.Lock()
	f.server.assetRedirect = slow.URL
	f.server.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	_, err := f.client.StageVerifiedRelease(ctx, f.exec, f.release, eligibleStageOpts("2.0.0"))
	if err == nil {
		t.Fatal("expected cancellation rejection")
	}
	assertNoStagingResidue(t, f.exec.Path)
}

func TestValidateReleaseTargetGates(t *testing.T) {
	f := newStagedReleaseFixture(t, "2.1.0")
	cases := []struct {
		name string
		opts StageReleaseOptions
		want string
	}{
		{"non-increasing", eligibleStageOpts("2.1.0"), "not newer"},
		{"decreasing", eligibleStageOpts("9.9.9"), "not newer"},
		{"unordered current", eligibleStageOpts("dev"), "not orderable"},
		{"unsupported install", StageReleaseOptions{CurrentVersion: "2.0.0", Eligibility: Eligibility{Supported: false, Reason: UnsupportedHomebrew}}, "unsupported"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateReleaseTarget(f.exec, f.release, tc.opts)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q rejection, got %v", tc.want, err)
			}
		})
	}
	// A suppressed exact target is refused, and newer targets stay eligible.
	if err := SuppressTarget(f.exec.Path, SuppressedTarget{Version: "2.1.0", TransactionID: strings.Repeat("a", 32)}); err != nil {
		t.Fatal(err)
	}
	err := ValidateReleaseTarget(f.exec, f.release, eligibleStageOpts("2.0.0"))
	if err == nil || !strings.Contains(err.Error(), "suppressed") {
		t.Fatalf("expected suppression rejection, got %v", err)
	}
}

func TestStageVerifiedReleaseRefusesSuppressedTarget(t *testing.T) {
	f := newStagedReleaseFixture(t, "2.1.0")
	if err := SuppressTarget(f.exec.Path, SuppressedTarget{Version: "2.1.0", TransactionID: strings.Repeat("b", 32)}); err != nil {
		t.Fatal(err)
	}
	_, err := f.client.StageVerifiedRelease(context.Background(), f.exec, f.release, eligibleStageOpts("2.0.0"))
	if err == nil || !strings.Contains(err.Error(), "suppressed") {
		t.Fatalf("expected suppression refusal, got %v", err)
	}
	assertNoStagingResidue(t, f.exec.Path)
}

func TestAbandonedReleaseStagingReconciles(t *testing.T) {
	f := newStagedReleaseFixture(t, "2.1.0")
	staged, err := f.client.StageVerifiedRelease(context.Background(), f.exec, f.release, eligibleStageOpts("2.0.0"))
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	// An ordinary restart (no live transaction, no install receipt) cleans
	// the recognized abandoned staging under the existing recovery rules.
	if err := ReconcileStaging(f.exec.Path, "", CleanupSeams{}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if _, err := os.Stat(staged.TxDir()); !os.IsNotExist(err) {
		t.Fatalf("abandoned release staging was not cleaned: %v", err)
	}

	// Unknown entries are never removed by cleanup: the staging dir with an
	// unexpected object is retained for a human.
	staged2, err := f.client.StageVerifiedRelease(context.Background(), f.exec, f.release, eligibleStageOpts("2.0.0"))
	if err != nil {
		t.Fatalf("stage 2: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staged2.TxDir(), "unexpected"), []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ReconcileStaging(f.exec.Path, "", CleanupSeams{}); err == nil {
		t.Fatal("expected cleanup refusal with unexpected entries")
	}
	if _, err := os.Stat(filepath.Join(staged2.TxDir(), "unexpected")); err != nil {
		t.Fatalf("unexpected entry was removed: %v", err)
	}
	// The recognized owned objects were still cleaned; only the dir and the
	// unexpected entry remain.
	for _, name := range []string{releaseArchiveName, releaseCandidateName, stagingRecordName} {
		if _, err := os.Stat(filepath.Join(staged2.TxDir(), name)); !os.IsNotExist(err) {
			t.Fatalf("recognized object %s was not cleaned", name)
		}
	}
}

// republishTarball re-signs the manifest around new tarball bytes so only
// the rule under test can reject them.
func (f *releaseFixtureServer) republishTarball(t *testing.T, tarball []byte) {
	t.Helper()
	sum := sha256Sum(tarball)
	lines := []string{}
	for _, line := range strings.Split(string(f.assets["/assets/"+checksumManifestName]), "\n") {
		if line == "" {
			continue
		}
		if strings.Contains(line, "tar.gz") && !strings.Contains(line, releaseEnvelopeName) {
			line = sum + "  " + f.tarballAssetName()
		}
		lines = append(lines, line)
	}
	manifestText := strings.Join(lines, "\n") + "\n"
	f.assets["/assets/"+checksumManifestName] = []byte(manifestText)
	f.assets["/assets/"+checksumSignatureName] = []byte(signFixturePayload(t, []byte(manifestText)) + "\n")
}

func (f *releaseFixtureServer) tarballAssetName() string {
	for name := range f.assets {
		if strings.HasSuffix(name, ".tar.gz") {
			return strings.TrimPrefix(name, "/assets/")
		}
	}
	return ""
}

func sha256Sum(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func shrinkDownloadBounds(t *testing.T, compressed int64, inactivity, total time.Duration) func() {
	t.Helper()
	oldCompressed, oldInactivity, oldTotal := archiveMaxCompressedBytes, downloadInactivityTimeout, downloadTotalTimeout
	archiveMaxCompressedBytes, downloadInactivityTimeout, downloadTotalTimeout = compressed, inactivity, total
	return func() {
		archiveMaxCompressedBytes, downloadInactivityTimeout, downloadTotalTimeout = oldCompressed, oldInactivity, oldTotal
	}
}

func TestAdmitStagedReleaseRequiresMatchingProbe(t *testing.T) {
	f := newStagedReleaseFixture(t, "2.1.0")
	staged, err := f.client.StageVerifiedRelease(context.Background(), f.exec, f.release, eligibleStageOpts("2.0.0"))
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	// Wrong probe version refuses admission.
	wrongVersion := ProbeResult{Version: "9.9.9", Banner: "agentico v9.9.9", ExecutableDigest: staged.CandidateDigest()}
	if _, err := AdmitStagedRelease(staged, wrongVersion); err == nil || !strings.Contains(err.Error(), "release target") {
		t.Fatalf("expected probe-version refusal, got %v", err)
	}
	// Digest mismatch refuses admission.
	wrongDigest := ProbeResult{Version: "2.1.0", Banner: "agentico v2.1.0", ExecutableDigest: strings.Repeat("0", 64)}
	if _, err := AdmitStagedRelease(staged, wrongDigest); err == nil || !strings.Contains(err.Error(), "staged candidate digest") {
		t.Fatalf("expected digest refusal, got %v", err)
	}
	// The real probe of the staged executable admits the candidate.
	probe, err := ProbeStagedExecutable(context.Background(), staged.CandidatePath(), "2.1.0")
	if err != nil {
		t.Fatalf("probe staged executable: %v", err)
	}
	cand, err := AdmitStagedRelease(staged, probe)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if cand.ExecutablePath() != staged.CandidatePath() || cand.ExecutableDigest() != staged.CandidateDigest() {
		t.Fatal("admitted candidate does not bind the staged executable")
	}
	if cand.Release().TarballDigest() != f.release.TarballDigest() {
		t.Fatal("admitted candidate lost release provenance")
	}
}

func TestBeginVerifiedReleaseRefusesSubstitutedBytes(t *testing.T) {
	f := newStagedReleaseFixture(t, "2.1.0")
	staged, err := f.client.StageVerifiedRelease(context.Background(), f.exec, f.release, eligibleStageOpts("2.0.0"))
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	probe, err := ProbeStagedExecutable(context.Background(), staged.CandidatePath(), "2.1.0")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	cand, err := AdmitStagedRelease(staged, probe)
	if err != nil {
		t.Fatal(err)
	}
	opts := BeginOptions{
		RuntimeDir: t.TempDir(), StateDir: t.TempDir(), Config: "config.yaml",
		PID: 1, PGID: 1, FromVersion: "2.0.0", ToVersion: "2.1.0",
		Bind:        BindEndpoint{Host: "127.0.0.1", Port: 1},
		ReceiptDest: ReceiptPath(f.exec.Path),
	}

	// A ToVersion that disagrees with the verified release is refused while
	// the candidate bytes are still exactly the admitted bytes.
	mismatchedOpts := opts
	mismatchedOpts.ToVersion = "9.9.9"
	if _, err := BeginVerifiedRelease(f.exec, cand, mismatchedOpts, eligibleStageOpts("2.0.0"), TxSeams{}); err == nil || !strings.Contains(err.Error(), "does not match the verified release") {
		t.Fatalf("expected target mismatch refusal, got %v", err)
	}

	// Substituting the staged executable's bytes after admission must fail
	// preparation: the disk digest no longer matches the admitted digest.
	tampered := append([]byte{}, stagedExecutableBody("2.1.0")...)
	tampered = append(tampered, []byte("# extra\n")...)
	if err := os.WriteFile(staged.CandidatePath(), tampered, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := BeginVerifiedRelease(f.exec, cand, opts, eligibleStageOpts("2.0.0"), TxSeams{}); err == nil {
		t.Fatal("expected refusal for substituted candidate bytes")
	}
	if digest := mustDigest(t, f.exec.Path); digest != f.exec.Digest {
		t.Fatal("refused preparation changed the installed bytes")
	}

	// A fabricated candidate (hostile caller) with forged manifest
	// provenance cannot pass the trust checks.
	forged := cand
	forged.release.manifestBytes = []byte("0000000000000000000000000000000000000000000000000000000000000000  forged.tar.gz\n")
	if _, err := BeginVerifiedRelease(f.exec, forged, opts, eligibleStageOpts("2.0.0"), TxSeams{}); err == nil {
		t.Fatal("expected refusal for forged manifest provenance")
	}

	// A fabricated candidate with an unknown trust root is refused.
	unknown := cand
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	unknown.release.trustRoot = priv.Public().(ed25519.PublicKey)
	if _, err := BeginVerifiedRelease(f.exec, unknown, opts, eligibleStageOpts("2.0.0"), TxSeams{}); err == nil || !strings.Contains(err.Error(), "not a known release embedding") {
		t.Fatalf("expected trust-root refusal, got %v", err)
	}
}

func TestBeginVerifiedReleaseCommitBoundary(t *testing.T) {
	f := newStagedReleaseFixture(t, "2.1.0")
	staged, err := f.client.StageVerifiedRelease(context.Background(), f.exec, f.release, eligibleStageOpts("2.0.0"))
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	probe, err := ProbeStagedExecutable(context.Background(), staged.CandidatePath(), "2.1.0")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	cand, err := AdmitStagedRelease(staged, probe)
	if err != nil {
		t.Fatal(err)
	}
	opts := BeginOptions{
		RuntimeDir: t.TempDir(), StateDir: t.TempDir(), Config: "config.yaml",
		PID: 1, PGID: 1, FromVersion: "2.0.0", ToVersion: "2.1.0",
		Bind:        BindEndpoint{Host: "127.0.0.1", Port: 1},
		ReceiptDest: ReceiptPath(f.exec.Path),
	}
	tx, err := BeginVerifiedRelease(f.exec, cand, opts, eligibleStageOpts("2.0.0"), TxSeams{})
	if err != nil {
		t.Fatalf("begin verified release: %v", err)
	}
	if tx.Receipt().NewDigest != staged.CandidateDigest() {
		t.Fatal("transaction did not bind the verified candidate digest")
	}

	// Tamper the transaction-staged executable between Begin and Commit:
	// the commit-boundary revalidation refuses before replacement.
	if err := os.WriteFile(tx.Receipt().StagingPath, []byte("#!/bin/sh\necho stolen\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err == nil {
		t.Fatal("expected commit-boundary refusal for tampered staging")
	}
	if digest := mustDigest(t, f.exec.Path); digest != f.exec.Digest {
		t.Fatal("failed commit replaced the installed bytes")
	}
	// The receipt stays truthful and actionable: pending, backup-ready.
	r := tx.Receipt()
	if r.Outcome != OutcomePending || r.Phase != PhaseBackupReady {
		t.Fatalf("refused commit left receipt %s/%s", r.Outcome, r.Phase)
	}
}

func TestBeginVerifiedReleaseCommitsVerifiedCandidate(t *testing.T) {
	f := newStagedReleaseFixture(t, "2.1.0")
	staged, err := f.client.StageVerifiedRelease(context.Background(), f.exec, f.release, eligibleStageOpts("2.0.0"))
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	probe, err := ProbeStagedExecutable(context.Background(), staged.CandidatePath(), "2.1.0")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	cand, err := AdmitStagedRelease(staged, probe)
	if err != nil {
		t.Fatal(err)
	}
	opts := BeginOptions{
		RuntimeDir: t.TempDir(), StateDir: t.TempDir(), Config: "config.yaml",
		PID: 1, PGID: 1, FromVersion: "2.0.0", ToVersion: "2.1.0",
		Bind:        BindEndpoint{Host: "127.0.0.1", Port: 1},
		ReceiptDest: ReceiptPath(f.exec.Path),
	}
	tx, err := BeginVerifiedRelease(f.exec, cand, opts, eligibleStageOpts("2.0.0"), TxSeams{})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if digest := mustDigest(t, f.exec.Path); digest != staged.CandidateDigest() {
		t.Fatal("commit did not install the verified candidate bytes")
	}
	if mode := statMode(t, f.exec.Path); mode != 0o755 {
		t.Fatalf("installed mode = %o, want 755", mode)
	}
	r, err := ReadReceipt(ReceiptPath(f.exec.Path))
	if err != nil {
		t.Fatal(err)
	}
	if r.Outcome != OutcomePending || r.Phase != PhaseReplacementComplete {
		t.Fatalf("receipt = %s/%s", r.Outcome, r.Phase)
	}
}

func leaseDirPath(exec Executable) string { return LeaseDir(exec.Path) }

func statMode(t *testing.T, path string) uint32 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return uint32(info.Mode().Perm())
}
