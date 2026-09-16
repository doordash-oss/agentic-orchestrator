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
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/selfupdate"
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
)

// The update-availability journeys boot real, deliberately built driver
// binaries against a local metadata-feed fixture. The fixture routing is
// compile-time isolated: ordinary builds have a nil updateFeedHook and no
// flag or environment value can redirect their fixed production feed. The
// clean stamped versions make the installed copies update-eligible, which
// the -selfupdate-test suffixed builds of the other journeys are not.

const (
	updateAvailVersionLower  = "1.1.0"
	updateAvailVersionHigher = "1.2.0"
)

type updateAvailBinaries struct {
	lower, higher string
	lowerDigest   string
	higherDigest  string
}

var (
	updateAvailBuildMu sync.Mutex
	updateAvailBuilt   *updateAvailBinaries
)

func updateAvailTestBinaries(t *testing.T) *updateAvailBinaries {
	t.Helper()
	updateAvailBuildMu.Lock()
	defer updateAvailBuildMu.Unlock()
	if updateAvailBuilt != nil {
		return updateAvailBuilt
	}
	root := selfupdateRepoRoot(t)
	lower := filepath.Join(selfupdateBuildDir, "agentico-avail-lower")
	higher := filepath.Join(selfupdateBuildDir, "agentico-avail-higher")
	selfupdateGoBuild(t, root, lower, updateAvailVersionLower, true)
	selfupdateGoBuild(t, root, higher, updateAvailVersionHigher, true)
	b := &updateAvailBinaries{lower: lower, higher: higher}
	var err error
	if b.lowerDigest, err = selfupdate.DigestFile(lower); err != nil {
		t.Fatalf("digest lower: %v", err)
	}
	if b.higherDigest, err = selfupdate.DigestFile(higher); err != nil {
		t.Fatalf("digest higher: %v", err)
	}
	updateAvailBuilt = b
	return b
}

// updateFeedFixture is the test-only metadata feed: a local, loopback
// httptest server serving a mutable release list. It records every request
// and every authorization header it received.
type updateFeedFixture struct {
	mu       sync.Mutex
	releases []map[string]any
	status   int
	requests int
	sawAuth  []string
	server   *httptest.Server
}

func newUpdateFeedFixture(t *testing.T, releases ...string) *updateFeedFixture {
	t.Helper()
	f := &updateFeedFixture{}
	f.setReleases(releases...)
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)
	return f
}

func (f *updateFeedFixture) setReleases(releases ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releases = f.releases[:0]
	f.status = 0
	for _, tag := range releases {
		f.releases = append(f.releases, map[string]any{
			"tag_name":   tag,
			"draft":      false,
			"prerelease": false,
			"html_url":   "https://github.com/doordash-oss/agentic-orchestrator/releases/tag/" + tag,
			"assets":     []map[string]any{},
		})
	}
}

func (f *updateFeedFixture) failWith(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = status
}

func (f *updateFeedFixture) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests
}

func (f *updateFeedFixture) authHeaders() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sawAuth...)
}

func (f *updateFeedFixture) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests++
	if auth := r.Header.Get("Authorization"); auth != "" {
		f.sawAuth = append(f.sawAuth, auth)
	}
	status, releases := f.status, f.releases
	f.mu.Unlock()
	if status != 0 {
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(releases)
}

// updateSnapshot fetches the authenticated availability snapshot.
func updateSnapshot(baseURL, token string) (server.UpdateSnapshot, error) {
	req, err := http.NewRequest(http.MethodGet, baseURL+"/api/v1/update", nil)
	if err != nil {
		return server.UpdateSnapshot{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := selfupdateHTTPClient.Do(req)
	if err != nil {
		return server.UpdateSnapshot{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return server.UpdateSnapshot{}, fmt.Errorf("GET /api/v1/update status %d", resp.StatusCode)
	}
	var body server.UpdateSnapshotResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return server.UpdateSnapshot{}, err
	}
	return body.Update, nil
}

// waitUpdateSnapshot polls until pred accepts the snapshot.
func waitUpdateSnapshot(t *testing.T, baseURL, token string, pred func(server.UpdateSnapshot) bool, timeout time.Duration) server.UpdateSnapshot {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last server.UpdateSnapshot
	for time.Now().Before(deadline) {
		snapshot, err := updateSnapshot(baseURL, token)
		if err == nil {
			last = snapshot
			if pred(snapshot) {
				return snapshot
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("snapshot never satisfied the wait: last = %+v", last)
	return last
}

// postUpdateCheck sends one explicit check and returns the HTTP status.
func postUpdateCheck(baseURL, token string) (int, []byte, error) {
	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/v1/update/check", strings.NewReader("{}"))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agentico-Client", "local")
	resp, err := selfupdateHTTPClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return resp.StatusCode, body, err
}

// openUpdateEventStream subscribes to /api/v1/events and reports the first
// update.updated frame on the returned channel. The stream is opened before
// the triggering request so the invalidation cannot race past the
// subscription.
func openUpdateEventStream(t *testing.T, baseURL, token string, timeout time.Duration) <-chan error {
	t.Helper()
	u := baseURL + "/api/v1/events?access_token=" + url.QueryEscape(token) + "&heartbeat_ms=200"
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		t.Fatalf("new event stream: %v", err)
	}
	resp, err := selfupdateStreamClient.Do(req)
	if err != nil {
		t.Fatalf("open event stream: %v", err)
	}
	seen := make(chan error, 1)
	go func() {
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 64*1024), 64*1024)
		var data string
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "event: update.updated") {
				continue
			}
			for scanner.Scan() {
				dataLine := scanner.Text()
				if strings.HasPrefix(dataLine, "data: ") {
					data = strings.TrimPrefix(dataLine, "data: ")
					break
				}
			}
			var evt struct {
				Kind     string `json:"kind"`
				Resource struct {
					Type string `json:"type"`
				} `json:"resource"`
				SnapshotRequired bool `json:"snapshot_required"`
			}
			if err := json.Unmarshal([]byte(data), &evt); err != nil {
				seen <- fmt.Errorf("parse update.updated frame %q: %w", data, err)
				return
			}
			if evt.Kind != "update.updated" || evt.Resource.Type != "update" || !evt.SnapshotRequired {
				seen <- fmt.Errorf("update.updated frame = %+v", evt)
				return
			}
			seen <- nil
			return
		}
		seen <- fmt.Errorf("event stream ended without an update.updated frame")
	}()
	return seen
}

// assertNoInstallSideEffects proves a metadata-only journey: no receipts, no
// suppression records, no staging or transaction directories, and an
// unchanged executable.
func assertNoInstallSideEffects(t *testing.T, j *selfupdateJourney, wantDigest string) {
	t.Helper()
	entries, err := os.ReadDir(selfupdate.LeaseDir(j.installPath))
	if err != nil {
		t.Fatalf("read lease dir: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, "receipt-") || strings.HasPrefix(name, "suppression-") || strings.HasPrefix(name, "tx-") {
			t.Fatalf("metadata-only journey created installation artifact %q", name)
		}
	}
	if got := j.installDigest(); got != wantDigest {
		t.Fatalf("installed digest changed: %s, want %s", got, wantDigest)
	}
}

// TestUpdateAvailabilityJourney drives the full metadata-only availability
// surface of a real eligible server against a test-only feed: the initial
// check discovers the greatest stable release, the explicit check accepts,
// update.updated drives SSE invalidations, and nothing is installed,
// staged, probed, or written.
func TestUpdateAvailabilityJourney(t *testing.T) {
	selfupdateJourneyGuard(t)
	bins := updateAvailTestBinaries(t)
	fixture := newUpdateFeedFixture(t, "v1.1.0", "v1.3.0")
	j := newSelfupdateJourney(t, bins.lower)
	port := selfupdateFreePort(t, "127.0.0.1")
	baseURL := "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	p := j.launch("selfupdate-driver",
		"--config", j.configPath,
		"--state-dir", j.stateDir,
		"--listen", "127.0.0.1:"+strconv.Itoa(port),
		"--update-feed", fixture.server.URL,
		"--updates", "notify")
	j.waitHealthy(p, baseURL, updateAvailVersionLower, 30*time.Second)
	disc := j.waitDiscovery(p, 20*time.Second)
	token := disc.AuthToken

	// The initial asynchronous check discovers the newest stable release.
	snapshot := waitUpdateSnapshot(t, baseURL, token, func(s server.UpdateSnapshot) bool {
		return string(s.Status) == "available"
	}, 30*time.Second)
	if snapshot.LatestVersion == nil || *snapshot.LatestVersion != "1.3.0" {
		t.Fatalf("latest_version = %v, want 1.3.0", snapshot.LatestVersion)
	}
	if snapshot.CurrentVersion != updateAvailVersionLower {
		t.Fatalf("current_version = %q", snapshot.CurrentVersion)
	}
	if string(snapshot.Policy) != "notify" || string(snapshot.Channel) != "stable" {
		t.Fatalf("policy/channel = %q/%q", snapshot.Policy, snapshot.Channel)
	}
	if string(snapshot.Signature) != "unverified" {
		t.Fatalf("signature = %q, want unverified", snapshot.Signature)
	}
	if snapshot.TargetVersion != nil {
		t.Fatalf("target_version = %v, want null", *snapshot.TargetVersion)
	}
	if string(snapshot.Installation) != "tarball" {
		t.Fatalf("installation = %q, want tarball", snapshot.Installation)
	}
	if snapshot.Error != nil {
		t.Fatalf("healthy availability carries an error: %+v", snapshot.Error)
	}
	if snapshot.Receipt != nil {
		t.Fatalf("metadata-only run fabricated a receipt: %+v", snapshot.Receipt)
	}

	// Subscribe first, then trigger with an explicit check: the SSE
	// invalidation must drive snapshot-then-subscribe clients.
	updateEvents := openUpdateEventStream(t, baseURL, token, 30*time.Second)
	code, _, err := postUpdateCheck(baseURL, token)
	if err != nil || code != http.StatusAccepted {
		t.Fatalf("explicit check = %d, %v; want 202", code, err)
	}
	select {
	case err := <-updateEvents:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("update.updated never arrived on the event stream")
	}

	// The fixture received no credentials.
	for _, auth := range fixture.authHeaders() {
		if auth != "" {
			t.Fatalf("credential sent to the fixture feed: %q", auth)
		}
	}

	// Metadata-only: no installation side effects of any kind.
	assertNoInstallSideEffects(t, j, bins.lowerDigest)
	if code := p.terminate(); code != 0 {
		t.Fatalf("SIGTERM exit = %d, want 0 (stderr tail:\n%s)", code, p.stderrTail())
	}
}

// TestUpdateAvailabilityOffJourney proves the desktop's exact launch form —
// --updates=off — yields a disabled snapshot with no feed traffic, refused
// explicit checks, and no side effects.
func TestUpdateAvailabilityOffJourney(t *testing.T) {
	selfupdateJourneyGuard(t)
	bins := updateAvailTestBinaries(t)
	fixture := newUpdateFeedFixture(t, "v1.1.0", "v1.3.0")
	j := newSelfupdateJourney(t, bins.lower)
	port := selfupdateFreePort(t, "127.0.0.1")
	baseURL := "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	p := j.launch("selfupdate-driver",
		"--config", j.configPath,
		"--state-dir", j.stateDir,
		"--listen", "127.0.0.1:"+strconv.Itoa(port),
		"--update-feed", fixture.server.URL,
		"--updates=off")
	j.waitHealthy(p, baseURL, updateAvailVersionLower, 30*time.Second)
	disc := j.waitDiscovery(p, 20*time.Second)
	token := disc.AuthToken

	snapshot := waitUpdateSnapshot(t, baseURL, token, func(s server.UpdateSnapshot) bool {
		return string(s.Status) == "disabled"
	}, 10*time.Second)
	if string(snapshot.Policy) != "off" {
		t.Fatalf("policy = %q, want off", snapshot.Policy)
	}
	if snapshot.NextCheckAt != nil {
		t.Fatal("off policy must never schedule a check")
	}
	// Give any wrongly-scheduled initial check a moment to surface.
	time.Sleep(2 * time.Second)
	if got := fixture.requestCount(); got != 0 {
		t.Fatalf("off policy made %d feed requests, want none", got)
	}
	code, body, err := postUpdateCheck(baseURL, token)
	if err != nil || code != http.StatusForbidden {
		t.Fatalf("explicit check under off = %d, %v; want 403", code, err)
	}
	var refusal server.ErrorResponse
	if err := json.Unmarshal(body, &refusal); err != nil {
		t.Fatalf("decode refusal: %v", err)
	}
	if refusal.Error.Code != "forbidden" {
		t.Fatalf("refusal code = %q, want forbidden", refusal.Error.Code)
	}
	if got := fixture.requestCount(); got != 0 {
		t.Fatalf("refused check made %d feed requests", got)
	}
	assertNoInstallSideEffects(t, j, bins.lowerDigest)
	if code := p.terminate(); code != 0 {
		t.Fatalf("SIGTERM exit = %d, want 0 (stderr tail:\n%s)", code, p.stderrTail())
	}
}

// TestUpdateAvailabilityRollbackOutcomeJourney proves recovery outcomes on
// the public snapshot: an operator launch under off still recovers a failed
// replacement first and then exposes disabled with the sanitized historical
// outcome; a later notify launch reports the suppressed newest release as
// failed/update_rolled_back with latest_version intact, until a newer
// unsuppressed release becomes available — all while the durable
// suppression survives every successful check.
func TestUpdateAvailabilityRollbackOutcomeJourney(t *testing.T) {
	selfupdateJourneyGuard(t)
	bins := updateAvailTestBinaries(t)
	fixture := newUpdateFeedFixture(t, "v1.1.0", "v1.2.0")
	j := newSelfupdateJourney(t, bins.lower)
	port := selfupdateFreePort(t, "127.0.0.1")
	baseURL := "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	driverArgs := func(policy string) []string {
		return []string{"selfupdate-driver",
			"--config", j.configPath,
			"--state-dir", j.stateDir,
			"--listen", "127.0.0.1:" + strconv.Itoa(port),
			"--candidate", bins.higher,
			"--trigger-file", j.triggerPath,
			"--update-feed", fixture.server.URL,
			"--fail-at", "post-rename",
			"--updates", policy,
		}
	}

	// The rollback happens under the off policy: mandatory recovery resolves
	// before policy, then the runtime serves disabled with the historical
	// outcome and no feed traffic.
	p := j.launch(driverArgs("off")...)
	j.waitHealthy(p, baseURL, updateAvailVersionLower, 30*time.Second)
	j.trigger()
	rolled := j.waitReceiptRolledBack(45 * time.Second)
	j.waitHealthy(p, baseURL, updateAvailVersionLower, 30*time.Second)
	disc := j.waitDiscovery(p, 20*time.Second)
	token := disc.AuthToken
	snapshot := waitUpdateSnapshot(t, baseURL, token, func(s server.UpdateSnapshot) bool {
		return string(s.Status) == "disabled"
	}, 10*time.Second)
	if snapshot.Receipt == nil || string(snapshot.Receipt.Outcome) != "rolled_back" {
		t.Fatalf("disabled snapshot must carry the sanitized historical outcome: %+v", snapshot.Receipt)
	}
	if snapshot.Receipt.FromVersion != updateAvailVersionLower || snapshot.Receipt.ToVersion != updateAvailVersionHigher {
		t.Fatalf("receipt versions = %q/%q", snapshot.Receipt.FromVersion, snapshot.Receipt.ToVersion)
	}
	if snapshot.Receipt.Error == nil || *snapshot.Receipt.Error == "" {
		t.Fatal("rolled_back receipt must carry its sanitized error")
	}
	if got := fixture.requestCount(); got != 0 {
		t.Fatalf("off policy made %d feed requests after recovery", got)
	}
	if code := p.terminate(); code != 0 {
		t.Fatalf("SIGTERM exit = %d, want 0 (stderr tail:\n%s)", code, p.stderrTail())
	}

	// A later notify launch reads the durable outcome: the suppressed newest
	// release stays visible as latest_version with failed/update_rolled_back.
	notify := j.launch(driverArgs("notify")...)
	j.waitHealthy(notify, baseURL, updateAvailVersionLower, 30*time.Second)
	disc = j.waitDiscovery(notify, 20*time.Second)
	token = disc.AuthToken
	snapshot = waitUpdateSnapshot(t, baseURL, token, func(s server.UpdateSnapshot) bool {
		return string(s.Status) == "failed" &&
			s.LatestVersion != nil && *s.LatestVersion == "1.2.0"
	}, 30*time.Second)
	if snapshot.Error == nil || snapshot.Error.Code != "update_rolled_back" {
		t.Fatalf("error = %+v, want update_rolled_back for the suppressed newest release", snapshot.Error)
	}

	// A newer unsuppressed release becomes available while the exact-version
	// suppression and its receipt remain intact.
	fixture.setReleases("v1.1.0", "v1.2.0", "v1.3.0")
	code, _, err := postUpdateCheck(baseURL, token)
	if err != nil || code != http.StatusAccepted {
		t.Fatalf("explicit check = %d, %v; want 202", code, err)
	}
	snapshot = waitUpdateSnapshot(t, baseURL, token, func(s server.UpdateSnapshot) bool {
		return string(s.Status) == "available"
	}, 30*time.Second)
	if snapshot.LatestVersion == nil || *snapshot.LatestVersion != "1.3.0" {
		t.Fatalf("latest_version = %v, want 1.3.0", snapshot.LatestVersion)
	}
	suppressed, err := selfupdate.SuppressedVersions(j.installPath)
	if err != nil {
		t.Fatalf("read suppression: %v", err)
	}
	foundSuppressed := false
	for _, target := range suppressed {
		if target.Version == "1.2.0" && target.TransactionID == rolled.TransactionID {
			foundSuppressed = true
		}
	}
	if !foundSuppressed {
		t.Fatalf("suppression for the rolled-back target was erased: %+v", suppressed)
	}
	if snapshot.Receipt == nil || string(snapshot.Receipt.Outcome) != "rolled_back" {
		t.Fatalf("the historical receipt must survive successful checks: %+v", snapshot.Receipt)
	}

	// A transient feed failure reports update_check_failed while retaining
	// the last successful metadata and its timestamp.
	lastSuccess := snapshot.LastSuccessAt
	fixture.failWith(http.StatusBadGateway)
	code, _, err = postUpdateCheck(baseURL, token)
	if err != nil || code != http.StatusAccepted {
		t.Fatalf("explicit check = %d, %v; want 202", code, err)
	}
	snapshot = waitUpdateSnapshot(t, baseURL, token, func(s server.UpdateSnapshot) bool {
		return string(s.Status) == "failed" && s.Error != nil && s.Error.Code == "update_check_failed"
	}, 30*time.Second)
	if snapshot.LatestVersion == nil || *snapshot.LatestVersion != "1.3.0" {
		t.Fatalf("latest_version = %v, want retained 1.3.0", snapshot.LatestVersion)
	}
	if snapshot.LastSuccessAt == nil || !snapshot.LastSuccessAt.Equal(*lastSuccess) {
		t.Fatalf("last_success_at = %v, want retained %v", snapshot.LastSuccessAt, lastSuccess)
	}
	if code := notify.terminate(); code != 0 {
		t.Fatalf("SIGTERM exit = %d, want 0 (stderr tail:\n%s)", code, notify.stderrTail())
	}
}

// TestUpdateAvailabilityConfirmedJourney proves a successful handoff
// confirmation exposes the confirmed receipt on the public snapshot — even
// when the validated cleanup fails, the healthy target keeps serving and
// later checks keep answering.
func TestUpdateAvailabilityConfirmedJourney(t *testing.T) {
	selfupdateJourneyGuard(t)
	bins := updateAvailTestBinaries(t)
	fixture := newUpdateFeedFixture(t, "v1.2.0")
	j := newSelfupdateJourney(t, bins.lower)
	port := selfupdateFreePort(t, "127.0.0.1")
	baseURL := "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	p := j.launch("selfupdate-driver",
		"--config", j.configPath,
		"--state-dir", j.stateDir,
		"--listen", "127.0.0.1:"+strconv.Itoa(port),
		"--candidate", bins.higher,
		"--trigger-file", j.triggerPath,
		"--update-feed", fixture.server.URL,
		"--fail-at", "cleanup-remove",
		"--updates", "notify")
	j.waitHealthy(p, baseURL, updateAvailVersionLower, 30*time.Second)
	j.trigger()

	// The update confirms and keeps serving; only the cleanup fails.
	j.waitHealthy(p, baseURL, updateAvailVersionHigher, 30*time.Second)
	receipt := j.waitReceiptOutcome(selfupdate.OutcomeConfirmed, 30*time.Second)
	if !p.stderrContains("selfupdate cleanup") {
		t.Fatalf("no cleanup failure warning:\n%s", p.stderrTail())
	}
	disc := j.waitDiscovery(p, 20*time.Second)
	token := disc.AuthToken

	// The public snapshot exposes the confirmed receipt; the availability
	// check on the confirmed build settles to up_to_date (the fixture's
	// greatest release equals the running version).
	snapshot := waitUpdateSnapshot(t, baseURL, token, func(s server.UpdateSnapshot) bool {
		return s.Receipt != nil && string(s.Receipt.Outcome) == "confirmed" && string(s.Status) == "up_to_date"
	}, 30*time.Second)
	if snapshot.Receipt.FromVersion != updateAvailVersionLower || snapshot.Receipt.ToVersion != updateAvailVersionHigher {
		t.Fatalf("receipt versions = %q/%q", snapshot.Receipt.FromVersion, snapshot.Receipt.ToVersion)
	}
	if snapshot.Receipt.CompletedAt == nil {
		t.Fatal("confirmed receipt must carry its completion time")
	}
	if snapshot.LatestVersion == nil || *snapshot.LatestVersion != "1.2.0" {
		t.Fatalf("latest_version = %v, want 1.2.0", snapshot.LatestVersion)
	}
	// A later explicit check still answers after the cleanup failure.
	code, _, err := postUpdateCheck(baseURL, token)
	if err != nil || code != http.StatusAccepted {
		t.Fatalf("explicit check after cleanup failure = %d, %v; want 202", code, err)
	}
	if code := p.terminate(); code != 0 {
		t.Fatalf("SIGTERM exit = %d, want 0 — a cleanup failure never breaks a confirmed build (stderr tail:\n%s)", code, p.stderrTail())
	}
	if receipt.TransactionID == "" {
		t.Fatal("confirmed receipt missing its transaction identity")
	}
}
