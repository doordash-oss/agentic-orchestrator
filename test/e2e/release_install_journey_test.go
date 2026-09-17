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
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/selfupdate"
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
)

// releaseInstallJourneyVersions reuse the availability journeys' clean
// stamped builds (update-eligible, deliberately tagged) so the release path
// exercises real native binaries without new build time.
const (
	releaseInstallLower  = updateAvailVersionLower  // "1.1.0"
	releaseInstallTarget = updateAvailVersionHigher // "1.2.0"
)

// releaseFixturePrivateKeyPEM is the committed test-only Ed25519 fixture key
// (desktop/test/e2e/helpers/update-fixtures.ts); its public half is the
// fixture trust root embedded in internal/selfupdate. Signing fixture
// releases with it never needs the production release private key.
const releaseFixturePrivateKeyPEM = `-----BEGIN PRIVATE KEY-----
MC4CAQAwBQYDK2VwBCIEINZMXBFPD1S98rCr5jnAqC4oCAf7E+GQBz6NrbxOncAr
-----END PRIVATE KEY-----`

func releaseFixtureSign(t *testing.T, payload []byte) string {
	t.Helper()
	block, _ := pem.Decode([]byte(releaseFixturePrivateKeyPEM))
	if block == nil {
		t.Fatal("fixture key PEM did not decode")
	}
	keyAny, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatalf("parse fixture key: %v", err)
	}
	key := keyAny.(ed25519.PrivateKey)
	return selfupdate.SignaturePrefix + base64.StdEncoding.EncodeToString(ed25519.Sign(key, payload))
}

// releaseServeMode selects the hostile mutation one published release
// carries; the default is a fully valid signed release.
type releaseServeMode string

const (
	releaseServeValid            releaseServeMode = "valid"
	releaseServeTamperManifest   releaseServeMode = "tamper-manifest"
	releaseServeForeignSigner    releaseServeMode = "foreign-signer"
	releaseServeEnvelopeMismatch releaseServeMode = "envelope-mismatch"
	releaseServeUnsafeArchive    releaseServeMode = "unsafe-archive"
	releaseServeWrongBanner      releaseServeMode = "wrong-banner"
	releaseServeNonIncreasing    releaseServeMode = "non-increasing"
	releaseServeNoEnvelope       releaseServeMode = "no-envelope"
)

// releaseServeFixture is the production-shaped signed release feed: one
// loopback origin serving GitHub-shaped release metadata and release assets.
type releaseServeFixture struct {
	server *httptest.Server
	mu     sync.Mutex

	releases string
	assets   map[string][]byte
	sawAuth  []string
}

func newReleaseServeFixture(t *testing.T) *releaseServeFixture {
	t.Helper()
	f := &releaseServeFixture{assets: map[string][]byte{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)
	return f
}

func (f *releaseServeFixture) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if auth := r.Header.Get("Authorization"); auth != "" {
		f.sawAuth = append(f.sawAuth, auth)
	}
	if strings.HasPrefix(r.URL.Path, "/repos/") && strings.HasSuffix(r.URL.Path, "/releases") {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(f.releases))
		return
	}
	if name, ok := strings.CutPrefix(r.URL.Path, "/release/"); ok {
		body, served := f.assets[name]
		if !served {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(body)
		return
	}
	w.WriteHeader(http.StatusNotFound)
}

// releaseServeSpec describes one published signed release.
type releaseServeSpec struct {
	// tag is the release tag (v-prefixed clean version).
	tag string
	// executable is the native binary placed in the archive's agentico
	// entry. The default is the target build; hostile modes may substitute.
	executable string
	// mode selects the hostile mutation.
	mode releaseServeMode
	// envelope advertises desktop-release.json with a typed server_contract.
	envelope bool
}

// publishRelease assembles the full production-shaped release: the platform
// tarball containing the native executable, the checksum manifest, its
// detached signature, and optionally the envelope, all served from this one
// loopback origin.
func (f *releaseServeFixture) publishRelease(t *testing.T, spec releaseServeSpec) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()

	version := strings.TrimPrefix(spec.tag, "v")
	tarballName := selfupdate.ReleaseTarballName(version, runtime.GOOS, runtime.GOARCH)
	executable := spec.executable
	if executable == "" {
		executable = updateAvailTestBinaries(t).higher
	}
	execBytes, err := os.ReadFile(executable)
	if err != nil {
		t.Fatalf("read executable %s: %v", executable, err)
	}

	// The platform tarball: one executable entry named agentico plus an
	// ordinary non-executable entry. Hostile modes add traversal or
	// substitute the executable bytes.
	entries := []tarEntrySpec{
		{name: "agentico-" + version + "/README.md", mode: 0o644, content: []byte("agentico release " + version + "\n")},
	}
	if spec.mode == releaseServeUnsafeArchive {
		entries = append(entries, tarEntrySpec{name: "../evil", mode: 0o755, content: []byte("#!/bin/sh\necho stolen\n")})
	}
	entries = append(entries, tarEntrySpec{name: "agentico", mode: 0o755, content: execBytes})
	tarball := buildReleaseTarGz(t, entries)
	f.assets[tarballName] = tarball

	digestFor := func(b []byte) string {
		sum := sha256.Sum256(b)
		return hex.EncodeToString(sum[:])
	}
	manifestLines := []string{digestFor(tarball) + "  " + tarballName}
	if spec.envelope && spec.mode != releaseServeNoEnvelope {
		envelopeBody := releaseEnvelopeJSON(t, version, true)
		f.assets["desktop-release.json"] = envelopeBody
		manifestLines = append(manifestLines, digestFor(envelopeBody)+"  desktop-release.json")
	}
	manifest := []byte(strings.Join(manifestLines, "\n") + "\n")
	signature := releaseFixtureSign(t, manifest)
	if spec.mode == releaseServeForeignSigner {
		_, foreign, err := ed25519.GenerateKey(nil)
		if err != nil {
			t.Fatal(err)
		}
		signature = selfupdate.SignaturePrefix + base64.StdEncoding.EncodeToString(ed25519.Sign(foreign, manifest))
	}
	if spec.mode == releaseServeTamperManifest {
		// Flip the first digest character after signing: only signature
		// verification can reject the forged manifest.
		flip := byte('0')
		if manifest[0] == '0' {
			flip = '1'
		}
		manifest[0] = flip
	}
	if spec.mode == releaseServeEnvelopeMismatch {
		// The manifest binds the original envelope bytes; the served bytes
		// differ, so the digest check must reject before parsing.
		f.assets["desktop-release.json"] = releaseEnvelopeJSON(t, version, false)
	}
	f.assets["checksums.txt"] = manifest
	f.assets["checksums.txt.sig"] = []byte(signature + "\n")

	type assetJSON struct {
		ID                 int64  `json:"id"`
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	}
	assets := []assetJSON{
		{ID: 1, Name: tarballName, BrowserDownloadURL: f.server.URL + "/release/" + tarballName},
		{ID: 2, Name: "checksums.txt", BrowserDownloadURL: f.server.URL + "/release/checksums.txt"},
		{ID: 3, Name: "checksums.txt.sig", BrowserDownloadURL: f.server.URL + "/release/checksums.txt.sig"},
	}
	if spec.envelope && spec.mode != releaseServeNoEnvelope {
		assets = append(assets, assetJSON{ID: 4, Name: "desktop-release.json", BrowserDownloadURL: f.server.URL + "/release/desktop-release.json"})
	}
	release := map[string]any{
		"tag_name":   spec.tag,
		"draft":      false,
		"prerelease": false,
		"html_url":   "https://github.com/doordash-oss/agentic-orchestrator/releases/tag/" + spec.tag,
		"assets":     assets,
	}
	raw, err := json.Marshal([]any{release})
	if err != nil {
		t.Fatal(err)
	}
	f.releases = string(raw)
}

// releaseEnvelopeJSON builds the desktop-shaped release envelope, optionally
// advertising the typed server contract.
func releaseEnvelopeJSON(t *testing.T, version string, withContract bool) []byte {
	t.Helper()
	contract := ""
	if withContract {
		contract = `,
  "server_contract": {"api_version": 1, "schema_version": 1, "min_client_schema": 1}`
	}
	return []byte(fmt.Sprintf(`{
  "schema_version": 1,
  "tag": "v%s",
  "version": "%s",
  "commit": "%s",
  "artifacts": [
    {"name": "Agentico-mac-universal.dmg", "sha256": "%s", "size": 1000000},
    {"name": "Agentico-x64.AppImage", "sha256": "%s", "size": 2000000}
  ]%s
}`, version, version, releaseFakeCommit(t), strings.Repeat("a", 64), strings.Repeat("b", 64), contract))
}

func releaseFakeCommit(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 20)
	if _, err := rand.Read(buf); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(buf)
}

// tarEntrySpec describes one entry for the release archive builder.
type tarEntrySpec struct {
	name    string
	mode    int64
	content []byte
}

// Many journeys sign the same immutable binary archive with different metadata.
// Cache by complete tar content so hostile entries and alternate binaries stay distinct.
var releaseArchiveCache = struct {
	sync.Mutex
	archives map[[sha256.Size]byte][]byte
}{archives: make(map[[sha256.Size]byte][]byte)}

func buildReleaseTarGz(t *testing.T, entries []tarEntrySpec) []byte {
	t.Helper()
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	for _, e := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: e.name, Mode: e.mode, Size: int64(len(e.content))}); err != nil {
			t.Fatalf("write header %q: %v", e.name, err)
		}
		if _, err := tw.Write(e.content); err != nil {
			t.Fatalf("write content %q: %v", e.name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	key := sha256.Sum256(raw.Bytes())
	releaseArchiveCache.Lock()
	defer releaseArchiveCache.Unlock()
	if archive, ok := releaseArchiveCache.archives[key]; ok {
		return bytes.Clone(archive)
	}
	var out bytes.Buffer
	zw, err := gzip.NewWriterLevel(&out, gzip.BestSpeed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := zw.Write(raw.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	releaseArchiveCache.archives[key] = bytes.Clone(out.Bytes())
	return out.Bytes()
}

// startRelease launches the installed driver copy on the signed-release
// journey: fixture feed routing plus the release-backed install selector.
func (j *selfupdateJourney) startRelease(feedURL, listen string, extra ...string) *driverProcess {
	args := []string{
		"selfupdate-driver",
		"--config", j.configPath,
		"--state-dir", j.stateDir,
		"--update-feed", feedURL,
		"--install-release",
		"--trigger-file", j.triggerPath,
	}
	if listen != "" {
		args = append(args, "--listen", listen)
	}
	args = append(args, extra...)
	return startDriverProcess(j.t, j.installPath, selfupdateDriverEnv(j.home), args...)
}

// newReleaseInstallJourney installs the clean-versioned lower build so the
// release path classifies it update-eligible.
func newReleaseInstallJourney(t *testing.T) (*selfupdateJourney, *selfupdateBinaries) {
	t.Helper()
	bins := updateAvailTestBinaries(t)
	return newSelfupdateJourney(t, bins.lower), bins
}

// releaseLeaseTxDirs lists the tx-* staging directories currently present.
func releaseLeaseTxDirs(t *testing.T, j *selfupdateJourney) []string {
	t.Helper()
	entries, err := os.ReadDir(selfupdate.LeaseDir(j.installPath))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var dirs []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "tx-") {
			dirs = append(dirs, e.Name())
		}
	}
	return dirs
}

func releaseWaitNoStaging(t *testing.T, j *selfupdateJourney, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if len(releaseLeaseTxDirs(t, j)) == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("staging dirs remain: %v", releaseLeaseTxDirs(t, j))
}

func TestReleaseInstallJourneySignedFixture(t *testing.T) {
	selfupdateJourneyGuard(t)
	j, bins := newReleaseInstallJourney(t)
	f := newReleaseServeFixture(t)
	f.publishRelease(t, releaseServeSpec{tag: "v" + releaseInstallTarget, envelope: true})

	port := selfupdateFreePort(t, "127.0.0.1")
	baseURL := "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	p := j.startRelease(f.server.URL, "127.0.0.1:"+strconv.Itoa(port))

	pre := j.waitHealthy(p, baseURL, releaseInstallLower, 30*time.Second)
	if pre.Owner.PID != p.pid() {
		t.Fatalf("pre-health owner pid = %d; want driver pid %d", pre.Owner.PID, p.pid())
	}
	disc1 := j.waitDiscovery(p, 20*time.Second)
	token, epoch1 := disc1.AuthToken, disc1.Epoch
	if disc1.Owner.Version != releaseInstallLower {
		t.Fatalf("discovery owner version = %q; want %q", disc1.Owner.Version, releaseInstallLower)
	}
	streamURL := baseURL + "/api/v1/events?access_token=" + url.QueryEscape(token) + "&heartbeat_ms=200"
	terminated := openSelfUpdateStream(t, streamURL)

	j.trigger()
	// Downloading, verifying and syncing precede the shutdown deadline.
	j.waitReceipt(45 * time.Second)
	select {
	case err := <-terminated:
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			t.Fatalf("SSE client expired instead of observing server shutdown: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("pre-handoff SSE stream never terminated (stderr tail:\n%s)", p.stderrTail())
	}

	post := j.waitHealthy(p, baseURL, releaseInstallTarget, 45*time.Second)
	if post.Owner.PID != pre.Owner.PID {
		t.Fatalf("post-health owner pid = %d; want preserved %d", post.Owner.PID, pre.Owner.PID)
	}
	if post.Runtime != pre.Runtime {
		t.Fatalf("runtime identity changed across exec: %+v -> %+v", pre.Runtime, post.Runtime)
	}
	disc2 := j.waitDiscoveryEpochChange(p, epoch1, 20*time.Second)
	if disc2.AuthToken != token {
		t.Fatalf("auth token changed across exec")
	}
	if disc2.Owner.Version != releaseInstallTarget {
		t.Fatalf("discovery owner version = %q; want %q", disc2.Owner.Version, releaseInstallTarget)
	}
	if code := j.featuresStatus(baseURL, token); code != http.StatusOK {
		t.Fatalf("post-exec authenticated features status = %d", code)
	}

	receipt := j.waitReceiptOutcome(selfupdate.OutcomeConfirmed, 30*time.Second)
	if receipt.Phase != selfupdate.PhaseReplacementComplete {
		t.Fatalf("receipt phase = %q; want %q", receipt.Phase, selfupdate.PhaseReplacementComplete)
	}
	if receipt.FromVersion != releaseInstallLower || receipt.ToVersion != releaseInstallTarget {
		t.Fatalf("receipt versions = %q -> %q", receipt.FromVersion, receipt.ToVersion)
	}
	if receipt.Bind.Port != port {
		t.Fatalf("receipt bind port = %d; want %d", receipt.Bind.Port, port)
	}
	// Both staging areas are gone: the install transaction dir after the
	// confirmed cleanup, and the release download staging after the driver's
	// validated cleanup.
	j.waitPathGone(filepath.Join(selfupdate.LeaseDir(j.installPath), "tx-"+receipt.TransactionID), "install transaction dir", 15*time.Second)
	releaseWaitNoStaging(t, j, 15*time.Second)
	if _, err := os.Stat(selfupdate.LeasePath(j.installPath)); err != nil {
		t.Fatalf("lease file must never be unlinked: %v", err)
	}
	if _, err := os.Stat(selfupdate.OwnershipRecordPath(j.installPath)); err != nil {
		t.Fatalf("ownership record must never be unlinked: %v", err)
	}
	if got := j.installDigest(); got != bins.higherDigest {
		t.Fatalf("installed digest = %s; want target binary digest %s", got, bins.higherDigest)
	}
	if info, err := os.Stat(j.installPath); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("installed permissions changed: %v %o", err, info.Mode().Perm())
	}
	argsDigests := p.digestTokens("args-digest")
	if len(argsDigests) != 2 || argsDigests[0] != argsDigests[1] {
		t.Fatalf("args-digest fingerprints = %v; want exactly two equal", argsDigests)
	}
	envDigests := p.digestTokens("env-digest")
	if len(envDigests) != 2 || envDigests[0] != envDigests[1] {
		t.Fatalf("env-digest fingerprints = %v; want exactly two equal", envDigests)
	}
	if resetEpoch := selfupdateStreamResetEpoch(t, baseURL, token, epoch1); resetEpoch != disc2.Epoch {
		t.Fatalf("stream.reset epoch = %q; want %q", resetEpoch, disc2.Epoch)
	}
	if !p.stderrContains("selfupdate-driver: release staged ") {
		t.Fatalf("no release-staged milestone (stderr tail:\n%s)", p.stderrTail())
	}
	if code := p.terminate(); code != 0 {
		t.Fatalf("SIGTERM exit code = %d; want 0 (stderr tail:\n%s)", code, p.stderrTail())
	}
	registryEntry := server.RegistryEntryPath(server.RegistryDir(filepath.Join(j.home, ".agentic-orchestrator")), j.runtimeDir)
	j.waitPathGone(registryEntry, "registry entry", 10*time.Second)

	f.mu.Lock()
	sawAuth := len(f.sawAuth) > 0
	f.mu.Unlock()
	if sawAuth {
		t.Fatal("the signed-release fixture must never receive credentials")
	}
}

func TestReleaseInstallJourneyTargetStartupFailure(t *testing.T) {
	selfupdateJourneyGuard(t)
	j, bins := newReleaseInstallJourney(t)
	f := newReleaseServeFixture(t)
	f.publishRelease(t, releaseServeSpec{tag: "v" + releaseInstallTarget})

	port := selfupdateFreePort(t, "127.0.0.1")
	baseURL := "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	p := j.startRelease(f.server.URL, "127.0.0.1:"+strconv.Itoa(port), "--fail-at", "target-startup")
	j.waitHealthy(p, baseURL, releaseInstallLower, 30*time.Second)
	j.trigger()

	// The verified signed candidate installs but fails target startup: the
	// existing recovery boundary restores the previous build and suppresses
	// the exact failed target.
	r := j.waitReceiptRolledBack(60 * time.Second)
	if r.FromVersion != releaseInstallLower || r.ToVersion != releaseInstallTarget {
		t.Fatalf("receipt versions = %q -> %q", r.FromVersion, r.ToVersion)
	}
	if r.RecoveryKind != selfupdate.RecoveryKindRestore {
		t.Fatalf("recovery kind = %q; want restore", r.RecoveryKind)
	}
	post := j.waitHealthy(p, baseURL, releaseInstallLower, 45*time.Second)
	if post.Owner.PID != p.pid() {
		t.Fatalf("recovered owner pid = %d; want preserved %d", post.Owner.PID, p.pid())
	}
	if got := j.installDigest(); got != bins.lowerDigest {
		t.Fatalf("installed digest = %s; want restored %s", got, bins.lowerDigest)
	}
	if n := p.stderrPrefixCount("selfupdate-driver: exec /"); n != 1 {
		t.Fatalf("exec milestones = %d; want 1 (stderr tail:\n%s)", n, p.stderrTail())
	}
	if !p.stderrContains("warning[update_rolled_back]") {
		t.Fatalf("no update_rolled_back warning (stderr tail:\n%s)", p.stderrTail())
	}
	requireSuppressedTarget(t, j, r)
	if code := p.terminate(); code != 0 {
		t.Fatalf("SIGTERM exit code = %d; want 0 (stderr tail:\n%s)", code, p.stderrTail())
	}
}

func TestReleaseInstallProductionBoundary(t *testing.T) {
	selfupdateJourneyGuard(t)
	bins := selfupdateTestBinaries(t)
	j := newSelfupdateJourney(t, bins.prod)
	before := j.installDigest()

	// An ordinarily built binary rejects the driver subcommand outright —
	// including the release-backed flags — and no fixture, failure-injection,
	// or legacy override environment value can change that.
	env := append(selfupdateDriverEnv(j.home),
		"AGENTICO_UPDATE_FIXTURE=1",
		"AGENTICO_SELFUPDATE_HANDOFF={\"json\":\"garbage\"}",
		"AGENTICO_UPDATES=auto",
		"GITHUB_API_URL=http://127.0.0.1:1",
	)
	p := startDriverProcess(t, j.installPath, env, "selfupdate-driver", "--install-release", "--update-feed", "http://127.0.0.1:1")
	code := p.waitBounded(30 * time.Second)
	if code == 0 {
		t.Fatalf("production binary accepted the driver subcommand (stderr tail:\n%s)", p.stderrTail())
	}
	if !p.stderrContains("unknown command: selfupdate-driver") {
		t.Fatalf("expected unknown-command refusal (stderr tail:\n%s)", p.stderrTail())
	}
	if got := j.installDigest(); got != before {
		t.Fatal("production boundary probe changed the installed bytes")
	}
	if dirs := releaseLeaseTxDirs(t, j); len(dirs) != 0 {
		t.Fatalf("production boundary probe left staging dirs: %v", dirs)
	}
}
