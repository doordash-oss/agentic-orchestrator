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

func updateAvailTestBinaries(t *testing.T) *selfupdateBinaries {
	t.Helper()
	lower := selfupdateBinary(t, updateAvailVersionLower, true)
	higher := selfupdateBinary(t, updateAvailVersionHigher, true)
	return &selfupdateBinaries{lower: lower.path, higher: higher.path,
		lowerDigest: lower.digest, higherDigest: higher.digest}
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
