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
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/selfupdate"
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
)

// installStopHTTPClient bounds JSON probes; upload trickles use the
// stream client with per-request contexts. Cancellation waits for a
// parked worker to settle, so the bound sits above the coordinator's
// 30-second settle deadline.
var installStopHTTPClient = &http.Client{Timeout: 35 * time.Second}

// writeInstallStopFakeClaude installs a scripted claude CLI on PATH: it
// answers auth readiness and version probes instantly, answers the model
// catalog discovery request with a one-model catalog, and otherwise speaks
// the session protocol — the init line, then parking until stdin closes.
// The stubborn variant ignores SIGTERM so a stop needs the SIGKILL
// escalation and overruns the stop budget.
func writeInstallStopFakeClaude(t *testing.T, dir string, stubborn bool) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
case "$1" in
auth)
  printf '{"loggedIn":true,"authMethod":"api_key","apiProvider":"fake"}\n'
  exit 0
  ;;
--version)
  printf '2.1.81 (Claude Code)\n'
  exit 0
  ;;
-p)
  # Model catalog discovery: answer the SDK initialize request so startup
  # completes immediately instead of burning the discovery timeout.
  IFS= read -r _req
  printf '%s\n' '{"type":"control_response","response":{"request_id":"agentico-model-catalog","subtype":"success","response":{"models":[{"value":"claude-sonnet-4-20250514","resolvedModel":"claude-sonnet-4-20250514","displayName":"Sonnet 4"}]}}}'
  while read -r _line; do :; done
  exit 0
  ;;
esac
printf '%s\n' '{"type":"system","subtype":"init","session_id":"fake","model":"claude-sonnet-4-20250514"}'
`
	if stubborn {
		script += "trap '' TERM\nwhile :; do sleep 1; done\n"
	} else {
		script += "while read -r _line; do :; done\n"
	}
	path := filepath.Join(dir, "claude")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// installStopEnv isolates the subprocess like selfupdateDriverEnv but puts
// the scripted provider directory first on PATH so the runtime boots with a
// ready provider and can launch real feature and chat sessions.
func installStopEnv(home, fakeBinDir string) []string {
	return []string{
		"HOME=" + home,
		"PATH=" + fakeBinDir + ":/usr/bin:/bin",
	}
}

// startInstallServer launches the installed driver copy as an ordinary
// serving runtime whose update install endpoint is wired to the fixture
// feed. No driver journey runs: the test drives the public install API.
func (j *selfupdateJourney) startInstallServer(feedURL, listen string, env []string, extra ...string) *driverProcess {
	args := []string{
		"selfupdate-driver",
		"--config", j.configPath,
		"--state-dir", j.stateDir,
		"--update-feed", feedURL,
	}
	if listen != "" {
		args = append(args, "--listen", listen)
	}
	args = append(args, extra...)
	return startDriverProcess(j.t, j.installPath, env, args...)
}

// installStopMutation sends one trusted authenticated JSON mutation and
// returns the status, body, and error.
func installStopMutation(method, baseURL, token, path, body string) (int, []byte, error) {
	req, err := http.NewRequest(method, baseURL+path, strings.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agentico-Client", "local")
	resp, err := installStopHTTPClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	return resp.StatusCode, raw, err
}

// postInstallStopCheck performs the explicit metadata check. The endpoint
// answers 202 with the post-request snapshot while the check settles; the
// install request only needs the discovered target to be pinned.
func postInstallStopCheck(t *testing.T, baseURL, token string) {
	t.Helper()
	status, body, err := installStopMutation(http.MethodPost, baseURL, token, "/api/v1/update/check", "{}")
	if err != nil || (status != http.StatusOK && status != http.StatusAccepted) {
		t.Fatalf("update check: status %d err %v body %s", status, err, body)
	}
}

// installStopErrorCode extracts the canonical error code from an error
// response body.
func installStopErrorCode(t *testing.T, body []byte) string {
	t.Helper()
	var parsed struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("decode error body %s: %v", body, err)
	}
	return parsed.Error.Code
}

// createStopJourneyFeature creates one repo-less feature through the public
// API and returns its id.
func createStopJourneyFeature(t *testing.T, baseURL, token, name string) string {
	t.Helper()
	status, body, err := installStopMutation(http.MethodPost, baseURL, token, "/api/v1/features", `{"name":"`+name+`"}`)
	if err != nil || status != http.StatusCreated {
		t.Fatalf("create feature: status %d err %v body %s", status, err, body)
	}
	var resp server.CreateFeatureResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode create response %s: %v", body, err)
	}
	if resp.FeatureID == "" {
		t.Fatalf("create response missing feature id: %s", body)
	}
	return resp.FeatureID
}

// startStopJourneyFeature starts the feature's orchestration.
func startStopJourneyFeature(t *testing.T, baseURL, token, featureID string) {
	t.Helper()
	status, body, err := installStopMutation(http.MethodPost, baseURL, token, "/api/v1/features/"+featureID+"/actions/start", "{}")
	if err != nil || status != http.StatusOK {
		t.Fatalf("start feature: status %d err %v body %s", status, err, body)
	}
}

// stopJourneyFeatureStatus reads the feature's current status through the
// public read model.
func stopJourneyFeatureStatus(t *testing.T, baseURL, token, featureID string) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, baseURL+"/api/v1/features/"+featureID, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := installStopHTTPClient.Do(req)
	if err != nil {
		t.Fatalf("get feature: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get feature status = %d", resp.StatusCode)
	}
	var parsed struct {
		Feature struct {
			Status string `json:"status"`
		} `json:"feature"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	// The read model reports display labels ("Interrupted"); the stop-work
	// assertions speak the lowercase status tokens.
	return strings.ToLower(parsed.Feature.Status)
}

// startStopJourneyChat sends one chat turn, launching the singleton chat
// session.
func startStopJourneyChat(t *testing.T, baseURL, token string) {
	t.Helper()
	status, body, err := installStopMutation(http.MethodPost, baseURL, token, "/api/v1/prompts/chat/start", `{"message":"hello"}`)
	if err != nil || status != http.StatusOK {
		t.Fatalf("chat start: status %d err %v body %s", status, err, body)
	}
}

// installStopServerFixture prepares one serving runtime on a free port: the
// lower-version installed binary, a signed fixture release for the target,
// a scripted provider on PATH, and a health/discovery handshake.
func installStopServerFixture(t *testing.T, stubborn bool, extraDriverFlags ...string) (*selfupdateJourney, *selfupdateBinaries, *releaseServeFixture, *driverProcess, string, string, string) {
	t.Helper()
	j, bins := newReleaseInstallJourney(t)
	f := newReleaseServeFixture(t)
	f.publishRelease(t, releaseServeSpec{tag: "v" + releaseInstallTarget, envelope: true})

	fakeBin := writeInstallStopFakeClaude(t, filepath.Join(t.TempDir(), "bin"), stubborn)
	env := installStopEnv(j.home, filepath.Dir(fakeBin))

	port := selfupdateFreePort(t, "127.0.0.1")
	baseURL := "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	p := j.startInstallServer(f.server.URL, "127.0.0.1:"+strconv.Itoa(port), env, extraDriverFlags...)
	j.waitHealthy(p, baseURL, releaseInstallLower, 45*time.Second)
	disc := j.waitDiscovery(p, 20*time.Second)
	return j, bins, f, p, baseURL, disc.AuthToken, disc.Epoch
}

// waitStopWorkActive polls the update snapshot until the active-work
// summary reports at least the wanted feature count and chat state.
func waitStopWorkActive(t *testing.T, baseURL, token string, wantFeatures int, wantChat bool) {
	t.Helper()
	waitUpdateSnapshot(t, baseURL, token, func(s server.UpdateSnapshot) bool {
		return s.ActiveWorkSummary.FeatureCount >= wantFeatures && s.ActiveWorkSummary.ChatActive == wantChat
	}, 45*time.Second)
}

// waitStopWorkUploadActive polls the update snapshot until the active-work
// summary observes at least one in-flight upload, so a request firing right
// behind a just-started trickle cannot race past the unregistered blocker.
func waitStopWorkUploadActive(t *testing.T, baseURL, token string) {
	t.Helper()
	waitUpdateSnapshot(t, baseURL, token, func(s server.UpdateSnapshot) bool {
		return s.ActiveWorkSummary.UploadCount >= 1
	}, 20*time.Second)
}

// trickleUpload streams one slowly-written attachment upload body. It holds
// the upload admission reservation for its whole duration; the returned
// done channel closes when the server has consumed the body.
func trickleUpload(baseURL, token string, chunks int, delay time.Duration) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		pr, pw := io.Pipe()
		go func() {
			defer pw.Close()
			chunk := []byte("agentico upload chunk\n")
			for i := 0; i < chunks; i++ {
				time.Sleep(delay)
				if _, err := pw.Write(chunk); err != nil {
					return
				}
			}
		}()
		req, err := http.NewRequest(http.MethodPost, baseURL+"/api/v1/uploads?kind=attachment&name=slow.bin", pr)
		if err != nil {
			return
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("X-Agentico-Client", "local")
		req.ContentLength = int64(len("agentico upload chunk\n") * chunks)
		resp, err := selfupdateStreamClient.Do(req)
		if err != nil {
			return
		}
		resp.Body.Close()
	}()
	return done
}

// TestInstallStopWorkJourneyRefusalKeepsWorkRunning proves an immediate
// install without stop permission is refused with update_blocked_active_work
// while real feature and chat sessions keep running and the old build keeps
// serving.
func TestInstallStopWorkJourneyRefusalKeepsWorkRunning(t *testing.T) {
	selfupdateJourneyGuard(t)
	j, bins, _, p, baseURL, token, _ := installStopServerFixture(t, false)
	defer func() { _ = p.terminate() }()

	postInstallStopCheck(t, baseURL, token)
	featureID := createStopJourneyFeature(t, baseURL, token, "refusal-keeps-work")
	startStopJourneyFeature(t, baseURL, token, featureID)
	startStopJourneyChat(t, baseURL, token)
	waitStopWorkActive(t, baseURL, token, 1, true)

	for _, body := range []string{
		`{"consent":true,"when":"now"}`,
		`{"consent":true,"when":"now","stop_active_work":false}`,
	} {
		status, raw, err := installStopMutation(http.MethodPost, baseURL, token, "/api/v1/update/install", body)
		if err != nil || status != http.StatusConflict {
			t.Fatalf("no-permission install: status %d err %v body %s", status, err, raw)
		}
		if code := installStopErrorCode(t, raw); code != "update_blocked_active_work" {
			t.Fatalf("no-permission install code = %q, want update_blocked_active_work", code)
		}
	}

	// The refused request stopped nothing: work keeps running, the runtime
	// keeps serving the old version with unchanged installed bytes, no
	// exec boundary was ever crossed, and no receipt exists.
	waitStopWorkActive(t, baseURL, token, 1, true)
	if got := stopJourneyFeatureStatus(t, baseURL, token, featureID); got == "interrupted" {
		t.Fatalf("feature status = %q; the refused install must not interrupt work", got)
	}
	if h, err := j.getHealth(baseURL); err != nil || h.Owner.Version != releaseInstallLower {
		t.Fatalf("health after refusal = %+v err %v; want %q serving", h, err, releaseInstallLower)
	}
	if got := j.installDigest(); got != bins.lowerDigest {
		t.Fatalf("installed digest after refusal = %s, want unchanged %s", got, bins.lowerDigest)
	}
	if _, err := os.Stat(j.receiptPath()); !os.IsNotExist(err) {
		t.Fatalf("refused install left a receipt: %v", err)
	}
}

// TestInstallStopWorkJourneySuccessStopsAndReplaces proves a consented
// explicit-stop install interrupts the running feature session and the
// singleton chat, confirms their completion before replacement, and hands
// the same process identity to the target build.
func TestInstallStopWorkJourneySuccessStopsAndReplaces(t *testing.T) {
	selfupdateJourneyGuard(t)
	j, bins, _, p, baseURL, token, epoch1 := installStopServerFixture(t, false)
	defer func() { _ = p.terminate() }()

	postInstallStopCheck(t, baseURL, token)
	featureID := createStopJourneyFeature(t, baseURL, token, "stop-work-success")
	startStopJourneyFeature(t, baseURL, token, featureID)
	startStopJourneyChat(t, baseURL, token)
	waitStopWorkActive(t, baseURL, token, 1, true)
	pre, err := j.getHealth(baseURL)
	if err != nil {
		t.Fatalf("pre-install health: %v", err)
	}
	prePGID, err := p.pgid()
	if err != nil {
		t.Fatalf("pre-install pgid: %v", err)
	}

	status, raw, err := installStopMutation(http.MethodPost, baseURL, token, "/api/v1/update/install", `{"consent":true,"when":"now","stop_active_work":true}`)
	if err != nil || status != http.StatusAccepted {
		t.Fatalf("stop install: status %d err %v body %s", status, err, raw)
	}

	// The confirmed interruption precedes any replacement: the process
	// keeps serving until the drain completes, then the same PID, process
	// group, and standard streams serve the target with a refreshed
	// discovery epoch and the same token.
	post := j.waitHealthy(p, baseURL, releaseInstallTarget, 60*time.Second)
	if post.Owner.PID != pre.Owner.PID {
		t.Fatalf("post-health pid = %d, want preserved %d", post.Owner.PID, pre.Owner.PID)
	}
	if post.Runtime != pre.Runtime {
		t.Fatalf("runtime identity changed across exec: %+v -> %+v", pre.Runtime, post.Runtime)
	}
	postPGID, err := p.pgid()
	if err != nil {
		t.Fatalf("post-install pgid: %v", err)
	}
	if postPGID != prePGID {
		t.Fatalf("pgid changed across exec: %d -> %d, want preserved %d", prePGID, postPGID, prePGID)
	}
	disc2 := j.waitDiscoveryEpochChange(p, epoch1, 20*time.Second)
	if disc2.AuthToken != token {
		t.Fatalf("auth token changed across exec")
	}
	if disc2.Owner.Version != releaseInstallTarget {
		t.Fatalf("discovery owner version = %q, want %q", disc2.Owner.Version, releaseInstallTarget)
	}
	// The production install path never runs the driver's journey
	// fingerprints; standard-stream continuity is proven by the adopted
	// image writing its confirmation milestone into the same stderr pipe
	// the parent has collected since launch. The receipt can be observed
	// just before the milestone lands, so the check polls briefly.
	confirmedOnStreams := false
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if p.stderrContains("selfupdate: confirmed ") {
			confirmedOnStreams = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !confirmedOnStreams {
		t.Fatalf("no adopted-image confirmation on the shared stderr stream (stderr tail:\n%s)", p.stderrTail())
	}
	if resetEpoch := selfupdateStreamResetEpoch(t, baseURL, token, epoch1); resetEpoch != disc2.Epoch {
		t.Fatalf("stream.reset epoch = %q, want new discovery epoch %q", resetEpoch, disc2.Epoch)
	}

	receipt := j.waitReceiptOutcome(selfupdate.OutcomeConfirmed, 30*time.Second)
	if receipt.FromVersion != releaseInstallLower || receipt.ToVersion != releaseInstallTarget {
		t.Fatalf("receipt versions = %q -> %q", receipt.FromVersion, receipt.ToVersion)
	}
	j.waitPathGone(filepath.Join(selfupdate.LeaseDir(j.installPath), "tx-"+receipt.TransactionID), "install transaction dir", 15*time.Second)
	releaseWaitNoStaging(t, j, 15*time.Second)
	if _, err := os.Stat(selfupdate.LeasePath(j.installPath)); err != nil {
		t.Fatalf("lease file must never be unlinked: %v", err)
	}
	if _, err := os.Stat(selfupdate.OwnershipRecordPath(j.installPath)); err != nil {
		t.Fatalf("ownership record must never be unlinked: %v", err)
	}
	if got := j.installDigest(); got != bins.higherDigest {
		t.Fatalf("installed digest = %s, want the target binary digest %s", got, bins.higherDigest)
	}
	if info, err := os.Stat(j.installPath); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("installed permissions changed: %v %o", err, info.Mode().Perm())
	}

	// The stopped work stays interrupted and the chat stays ended on the
	// replacement runtime: nothing resumes automatically.
	if got := stopJourneyFeatureStatus(t, baseURL, token, featureID); got != "interrupted" {
		t.Fatalf("feature status after interruption = %q, want interrupted", got)
	}
	waitUpdateSnapshot(t, baseURL, token, func(s server.UpdateSnapshot) bool {
		return !s.ActiveWorkSummary.ChatActive && s.ActiveWorkSummary.FeatureCount == 0
	}, 20*time.Second)
}

// TestInstallStopWorkJourneyRepositoryBlockersRefuse proves repository work
// refuses an explicit-stop install before staging and again after staging:
// nothing is stopped, the old build keeps serving, the failure is truthful,
// and normal admission is restored.
func TestInstallStopWorkJourneyRepositoryBlockersRefuse(t *testing.T) {
	selfupdateJourneyGuard(t)
	gateFile := filepath.Join(t.TempDir(), "stop-gate")
	j, bins, _, p, baseURL, token, _ := installStopServerFixture(t, false, "--stop-entry-gate", gateFile)
	defer func() { _ = p.terminate() }()

	postInstallStopCheck(t, baseURL, token)
	featureID := createStopJourneyFeature(t, baseURL, token, "repo-blockers")
	startStopJourneyFeature(t, baseURL, token, featureID)
	waitStopWorkActive(t, baseURL, token, 1, false)

	// A repository blocker before staging: an in-flight upload refuses the
	// request with update_blocked_active_work and stops nothing.
	uploadDone := trickleUpload(baseURL, token, 24, 250*time.Millisecond)
	waitStopWorkUploadActive(t, baseURL, token)
	status, raw, err := installStopMutation(http.MethodPost, baseURL, token, "/api/v1/update/install", `{"consent":true,"when":"now","stop_active_work":true}`)
	if err != nil || status != http.StatusConflict {
		t.Fatalf("blocked install: status %d err %v body %s", status, err, raw)
	}
	if code := installStopErrorCode(t, raw); code != "update_blocked_active_work" {
		t.Fatalf("blocked install code = %q, want update_blocked_active_work", code)
	}
	select {
	case <-uploadDone:
	case <-time.After(20 * time.Second):
		t.Fatal("upload never settled")
	}
	waitStopWorkActive(t, baseURL, token, 1, false)

	// A repository blocker introduced during staging: the accepted
	// operation repeats the protected-work check after staging and aborts
	// before any stop, cleaning owned staging and reopening admission.
	status, raw, err = installStopMutation(http.MethodPost, baseURL, token, "/api/v1/update/install", `{"consent":true,"when":"now","stop_active_work":true}`)
	if err != nil || status != http.StatusAccepted {
		t.Fatalf("stop install: status %d err %v body %s", status, err, raw)
	}
	deadline := time.Now().Add(30 * time.Second)
	for !p.stderrContains("stop-entry gate waiting") {
		if time.Now().After(deadline) {
			t.Fatalf("worker never reached the stop-entry gate (stderr tail:\n%s)", p.stderrTail())
		}
		time.Sleep(50 * time.Millisecond)
	}
	uploadDone = trickleUpload(baseURL, token, 24, 250*time.Millisecond)
	waitStopWorkUploadActive(t, baseURL, token)
	if err := os.WriteFile(gateFile, []byte("go"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The post-staging check aborts with the canonical blocked failure.
	waitUpdateSnapshot(t, baseURL, token, func(s server.UpdateSnapshot) bool {
		return string(s.Status) == "failed" && s.Error != nil && s.Error.Code == "update_blocked_active_work"
	}, 30*time.Second)
	select {
	case <-uploadDone:
	case <-time.After(20 * time.Second):
		t.Fatal("upload never settled after the abort")
	}

	// Nothing was stopped, the old build keeps serving unchanged bytes,
	// and no exec boundary was ever crossed.
	if got := stopJourneyFeatureStatus(t, baseURL, token, featureID); got == "interrupted" {
		t.Fatalf("feature status = %q; the aborted install must not interrupt work", got)
	}
	if h, err := j.getHealth(baseURL); err != nil || h.Owner.Version != releaseInstallLower {
		t.Fatalf("health after abort = %+v err %v; want %q serving", h, err, releaseInstallLower)
	}
	if got := j.installDigest(); got != bins.lowerDigest {
		t.Fatalf("installed digest after abort = %s, want unchanged %s", got, bins.lowerDigest)
	}
	if _, err := os.Stat(j.receiptPath()); !os.IsNotExist(err) {
		t.Fatalf("aborted install left a receipt: %v", err)
	}
	releaseWaitNoStaging(t, j, 15*time.Second)

	// Normal admission is restored: fresh work starts again without the
	// update_in_progress refusal.
	startStopJourneyChat(t, baseURL, token)
	waitStopWorkActive(t, baseURL, token, 1, true)
}

// TestInstallStopWorkJourneyTimeoutAbortsAndRetries proves a stubborn
// session that overruns the shared stop deadline aborts the installation:
// the current build keeps serving, new work is refused with 503
// update_in_progress during the stopping interval, cancellation returns 409
// there, already-stopped work stays interrupted, and fresh consent retries
// successfully once the blockers settle.
func TestInstallStopWorkJourneyTimeoutAbortsAndRetries(t *testing.T) {
	selfupdateJourneyGuard(t)
	j, bins, _, p, baseURL, token, _ := installStopServerFixture(t, true)
	defer func() { _ = p.terminate() }()

	postInstallStopCheck(t, baseURL, token)
	featureID := createStopJourneyFeature(t, baseURL, token, "stop-work-timeout")
	startStopJourneyFeature(t, baseURL, token, featureID)
	waitStopWorkActive(t, baseURL, token, 1, false)

	status, raw, err := installStopMutation(http.MethodPost, baseURL, token, "/api/v1/update/install", `{"consent":true,"when":"now","stop_active_work":true}`)
	if err != nil || status != http.StatusAccepted {
		t.Fatalf("stop install: status %d err %v body %s", status, err, raw)
	}

	// During the non-cancellable stopping interval, new work is refused
	// with the canonical 503 and cancellation returns 409.
	waitUpdateSnapshot(t, baseURL, token, func(s server.UpdateSnapshot) bool {
		return string(s.Status) == "draining"
	}, 30*time.Second)
	chatStatus, chatBody, err := installStopMutation(http.MethodPost, baseURL, token, "/api/v1/prompts/chat/start", `{"message":"during stop"}`)
	if err != nil || chatStatus != http.StatusServiceUnavailable {
		t.Fatalf("chat during stopping: status %d err %v body %s", chatStatus, err, chatBody)
	}
	if code := installStopErrorCode(t, chatBody); code != "update_in_progress" {
		t.Fatalf("chat during stopping code = %q, want update_in_progress", code)
	}
	cancelStatus, cancelBody, err := installStopMutation(http.MethodDelete, baseURL, token, "/api/v1/update/install", "{}")
	if err != nil || cancelStatus != http.StatusConflict {
		t.Fatalf("cancel during stopping: status %d err %v body %s", cancelStatus, err, cancelBody)
	}
	if code := installStopErrorCode(t, cancelBody); code != "update_in_progress" {
		t.Fatalf("cancel during stopping code = %q, want update_in_progress", code)
	}

	// The stubborn session overruns the ten-second budget: the install
	// aborts as failed update_blocked_active_work while the old build
	// keeps serving.
	waitUpdateSnapshot(t, baseURL, token, func(s server.UpdateSnapshot) bool {
		return string(s.Status) == "failed" && s.Error != nil && s.Error.Code == "update_blocked_active_work"
	}, 60*time.Second)
	if h, err := j.getHealth(baseURL); err != nil || h.Owner.Version != releaseInstallLower {
		t.Fatalf("health after timeout abort = %+v err %v; want %q serving", h, err, releaseInstallLower)
	}
	if got := j.installDigest(); got != bins.lowerDigest {
		t.Fatalf("installed digest = %s, want unchanged %s", got, bins.lowerDigest)
	}
	if _, err := os.Stat(j.receiptPath()); !os.IsNotExist(err) {
		t.Fatalf("timed-out install left a receipt: %v", err)
	}

	// The interrupted feature stays interrupted: no success, failure, or
	// restart resumes it, and admission is open again for new work.
	if got := stopJourneyFeatureStatus(t, baseURL, token, featureID); got != "interrupted" {
		t.Fatalf("feature status after timeout = %q, want interrupted", got)
	}
	startStopJourneyChat(t, baseURL, token)
	waitStopWorkActive(t, baseURL, token, 0, true)

	// The stubborn chat session can never settle inside the shared stop
	// budget, so the fresh attempt ends it first through the public
	// chat-end action — the same interruption surface the install uses.
	endStatus, endBody, endErr := installStopMutation(http.MethodPost, baseURL, token, "/api/v1/prompts/chat/end", "{}")
	if endErr != nil || endStatus != http.StatusOK {
		t.Fatalf("chat end: status %d err %v body %s", endStatus, endErr, endBody)
	}
	waitUpdateSnapshot(t, baseURL, token, func(s server.UpdateSnapshot) bool {
		return !s.ActiveWorkSummary.ChatActive
	}, 30*time.Second)

	// Fresh consent retries after the blockers settle and the interrupted
	// work no longer blocks: this attempt replaces the binary.
	postInstallStopCheck(t, baseURL, token)
	status, raw, err = installStopMutation(http.MethodPost, baseURL, token, "/api/v1/update/install", `{"consent":true,"when":"now","stop_active_work":true}`)
	if err != nil || status != http.StatusAccepted {
		t.Fatalf("fresh stop install: status %d err %v body %s", status, err, raw)
	}
	post := j.waitHealthy(p, baseURL, releaseInstallTarget, 60*time.Second)
	if post.Owner.PID != p.pid() {
		t.Fatalf("retry post-health pid = %d, want preserved %d", post.Owner.PID, p.pid())
	}
	receipt := j.waitReceiptOutcome(selfupdate.OutcomeConfirmed, 30*time.Second)
	if receipt.ToVersion != releaseInstallTarget {
		t.Fatalf("retry receipt target = %q, want %q", receipt.ToVersion, releaseInstallTarget)
	}
	if got := stopJourneyFeatureStatus(t, baseURL, token, featureID); got != "interrupted" {
		t.Fatalf("feature status after successful retry = %q, want still interrupted", got)
	}
}

// TestInstallStopWorkJourneyPartialStopFailureAborts proves one failing
// feature stop aborts the installation even though another stop succeeded:
// the old build keeps serving a truthful failed snapshot, the already-stopped
// feature stays interrupted, the failing one keeps running, and admission
// reopens.
func TestInstallStopWorkJourneyPartialStopFailureAborts(t *testing.T) {
	selfupdateJourneyGuard(t)
	// The driver injects a failure in front of the second feature stop
	// dispatch: the first stop succeeds, the second fails.
	j, bins, _, p, baseURL, token, _ := installStopServerFixture(t, false, "--fail-stop-nth", "2")
	defer func() { _ = p.terminate() }()

	postInstallStopCheck(t, baseURL, token)
	first := createStopJourneyFeature(t, baseURL, token, "partial-stop-first")
	startStopJourneyFeature(t, baseURL, token, first)
	waitStopWorkActive(t, baseURL, token, 1, false)
	second := createStopJourneyFeature(t, baseURL, token, "partial-stop-second")
	startStopJourneyFeature(t, baseURL, token, second)
	waitStopWorkActive(t, baseURL, token, 2, false)

	status, raw, err := installStopMutation(http.MethodPost, baseURL, token, "/api/v1/update/install", `{"consent":true,"when":"now","stop_active_work":true}`)
	if err != nil || status != http.StatusAccepted {
		t.Fatalf("stop install: status %d err %v body %s", status, err, raw)
	}
	waitUpdateSnapshot(t, baseURL, token, func(s server.UpdateSnapshot) bool {
		return string(s.Status) == "failed" && s.Error != nil && s.Error.Code == "update_blocked_active_work"
	}, 30*time.Second)
	if h, err := j.getHealth(baseURL); err != nil || h.Owner.Version != releaseInstallLower {
		t.Fatalf("health after partial stop = %+v err %v; want %q serving", h, err, releaseInstallLower)
	}
	if got := j.installDigest(); got != bins.lowerDigest {
		t.Fatalf("installed digest after partial stop = %s, want unchanged %s", got, bins.lowerDigest)
	}
	if _, err := os.Stat(j.receiptPath()); !os.IsNotExist(err) {
		t.Fatalf("partial-stop install left a receipt: %v", err)
	}
	firstStatus := stopJourneyFeatureStatus(t, baseURL, token, first)
	secondStatus := stopJourneyFeatureStatus(t, baseURL, token, second)
	interrupted := 0
	for _, st := range []string{firstStatus, secondStatus} {
		if st == "interrupted" {
			interrupted++
		}
	}
	if interrupted != 1 {
		t.Fatalf("feature statuses = %q/%q, want exactly one interrupted (the succeeded stop) and one left running", firstStatus, secondStatus)
	}

	// Normal admission is restored after the failure: fresh chat work is
	// admitted again without the update_in_progress refusal, while the
	// left-running feature keeps running.
	startStopJourneyChat(t, baseURL, token)
	waitStopWorkActive(t, baseURL, token, 1, true)
}

// TestInstallStopWorkJourneyCancellationRaces proves both outcomes of the
// cancellation-versus-stop-entry race deterministically: cancellation before
// the boundary prevents every stop and replacement, and cancellation after
// stop entry is refused with 409 update_in_progress while the install
// finishes.
func TestInstallStopWorkJourneyCancellationRaces(t *testing.T) {
	selfupdateJourneyGuard(t)

	// Cancellation wins before stop entry: the gate parks the worker
	// between staging and the protected-work recheck.
	gateFile := filepath.Join(t.TempDir(), "stop-gate")
	j, bins, _, p, baseURL, token, _ := installStopServerFixture(t, false, "--stop-entry-gate", gateFile)
	featureID := createStopJourneyFeature(t, baseURL, token, "cancel-before-stop")
	startStopJourneyFeature(t, baseURL, token, featureID)
	waitStopWorkActive(t, baseURL, token, 1, false)

	status, raw, err := installStopMutation(http.MethodPost, baseURL, token, "/api/v1/update/install", `{"consent":true,"when":"now","stop_active_work":true}`)
	if err != nil || status != http.StatusAccepted {
		t.Fatalf("stop install: status %d err %v body %s", status, err, raw)
	}
	deadline := time.Now().Add(30 * time.Second)
	for !p.stderrContains("stop-entry gate waiting") {
		if time.Now().After(deadline) {
			t.Fatalf("worker never reached the stop-entry gate (stderr tail:\n%s)", p.stderrTail())
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancelStatus, cancelBody, err := installStopMutation(http.MethodDelete, baseURL, token, "/api/v1/update/install", "{}")
	if err != nil || cancelStatus != http.StatusOK {
		t.Fatalf("cancel before stop entry: status %d err %v body %s", cancelStatus, err, cancelBody)
	}
	if err := os.WriteFile(gateFile, []byte("go"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The cancelled operation settles: nothing stopped, nothing replaced,
	// the installed bytes are unchanged, no exec boundary was crossed, the
	// snapshot returns to availability, and fresh consent is required.
	waitUpdateSnapshot(t, baseURL, token, func(s server.UpdateSnapshot) bool {
		return string(s.Status) == "available"
	}, 30*time.Second)
	if got := stopJourneyFeatureStatus(t, baseURL, token, featureID); got == "interrupted" {
		t.Fatalf("feature status = %q; the cancelled install must not interrupt work", got)
	}
	if h, err := j.getHealth(baseURL); err != nil || h.Owner.Version != releaseInstallLower {
		t.Fatalf("health after cancellation = %+v err %v; want %q serving", h, err, releaseInstallLower)
	}
	if got := j.installDigest(); got != bins.lowerDigest {
		t.Fatalf("installed digest after cancellation = %s, want unchanged %s", got, bins.lowerDigest)
	}
	if _, err := os.Stat(j.receiptPath()); !os.IsNotExist(err) {
		t.Fatalf("cancelled install left a receipt: %v", err)
	}
	if code := p.terminate(); code != 0 {
		t.Fatalf("SIGTERM exit code = %d (stderr tail:\n%s)", code, p.stderrTail())
	}

	// Stop entry wins: with a stubborn session the stopping interval lasts
	// long enough to observe the 409 while the install finishes replacing.
	j2, bins2, _, p2, baseURL2, token2, _ := installStopServerFixture(t, true)
	defer func() { _ = p2.terminate() }()
	postInstallStopCheck(t, baseURL2, token2)
	featureID2 := createStopJourneyFeature(t, baseURL2, token2, "cancel-after-stop")
	startStopJourneyFeature(t, baseURL2, token2, featureID2)
	waitStopWorkActive(t, baseURL2, token2, 1, false)

	status, raw, err = installStopMutation(http.MethodPost, baseURL2, token2, "/api/v1/update/install", `{"consent":true,"when":"now","stop_active_work":true}`)
	if err != nil || status != http.StatusAccepted {
		t.Fatalf("stop install (second server): status %d err %v body %s", status, err, raw)
	}
	waitUpdateSnapshot(t, baseURL2, token2, func(s server.UpdateSnapshot) bool {
		return string(s.Status) == "draining"
	}, 30*time.Second)
	cancelStatus, cancelBody, err = installStopMutation(http.MethodDelete, baseURL2, token2, "/api/v1/update/install", "{}")
	if err != nil || cancelStatus != http.StatusConflict {
		t.Fatalf("cancel after stop entry: status %d err %v body %s", cancelStatus, err, cancelBody)
	}
	if code := installStopErrorCode(t, cancelBody); code != "update_in_progress" {
		t.Fatalf("cancel after stop entry code = %q, want update_in_progress", code)
	}
	// The operation finishes installation or recovery: the stubborn stop
	// overruns the budget, the install aborts with unchanged installed
	// bytes and no exec boundary, and the old build keeps serving with the
	// work left interrupted.
	waitUpdateSnapshot(t, baseURL2, token2, func(s server.UpdateSnapshot) bool {
		return string(s.Status) == "failed" && s.Error != nil && s.Error.Code == "update_blocked_active_work"
	}, 60*time.Second)
	if h, err := j2.getHealth(baseURL2); err != nil || h.Owner.Version != releaseInstallLower {
		t.Fatalf("health after refused cancel = %+v err %v; want %q serving", h, err, releaseInstallLower)
	}
	if got := j2.installDigest(); got != bins2.lowerDigest {
		t.Fatalf("installed digest after refused cancel = %s, want unchanged %s", got, bins2.lowerDigest)
	}
	if got := stopJourneyFeatureStatus(t, baseURL2, token2, featureID2); got != "interrupted" {
		t.Fatalf("feature status after abort = %q, want interrupted", got)
	}
}

// TestInstallStopWorkJourneyDetectionFailureAborts proves failed activity
// detection refuses an explicit-stop install at the process level: once at
// the post-staging recheck before any stop, and once during the stopping
// interval's confirmation. Both aborts leave the current build serving with
// unchanged bytes, no exec crossing, a truthful failed snapshot, and
// restored admission.
func TestInstallStopWorkJourneyDetectionFailureAborts(t *testing.T) {
	selfupdateJourneyGuard(t)

	// A detection failure at the post-staging recheck: the gate parks the
	// worker between staging and the recheck while the detection-failure
	// trigger file appears, so the request-time check already passed.
	gateFile := filepath.Join(t.TempDir(), "stop-gate")
	detectFile := filepath.Join(t.TempDir(), "detect-fail")
	j, bins, _, p, baseURL, token, _ := installStopServerFixture(t, false,
		"--stop-entry-gate", gateFile, "--fail-stop-detection", detectFile)
	defer func() { _ = p.terminate() }()

	postInstallStopCheck(t, baseURL, token)
	featureID := createStopJourneyFeature(t, baseURL, token, "detect-fail-staged")
	startStopJourneyFeature(t, baseURL, token, featureID)
	waitStopWorkActive(t, baseURL, token, 1, false)

	status, raw, err := installStopMutation(http.MethodPost, baseURL, token, "/api/v1/update/install", `{"consent":true,"when":"now","stop_active_work":true}`)
	if err != nil || status != http.StatusAccepted {
		t.Fatalf("stop install: status %d err %v body %s", status, err, raw)
	}
	deadline := time.Now().Add(30 * time.Second)
	for !p.stderrContains("stop-entry gate waiting") {
		if time.Now().After(deadline) {
			t.Fatalf("worker never reached the stop-entry gate (stderr tail:\n%s)", p.stderrTail())
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := os.WriteFile(detectFile, []byte("fail"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gateFile, []byte("go"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The post-staging recheck fails closed: the operation aborts as
	// update_blocked_active_work with a truthful detection_failed summary,
	// nothing stopped, and unchanged installed bytes.
	waitUpdateSnapshot(t, baseURL, token, func(s server.UpdateSnapshot) bool {
		return string(s.Status) == "failed" && s.Error != nil && s.Error.Code == "update_blocked_active_work" &&
			s.ActiveWorkSummary.DetectionFailed
	}, 30*time.Second)
	if got := stopJourneyFeatureStatus(t, baseURL, token, featureID); got == "interrupted" {
		t.Fatalf("feature status = %q; the detection-failed install must not interrupt work", got)
	}
	if h, err := j.getHealth(baseURL); err != nil || h.Owner.Version != releaseInstallLower {
		t.Fatalf("health after detection failure = %+v err %v; want %q serving", h, err, releaseInstallLower)
	}
	if got := j.installDigest(); got != bins.lowerDigest {
		t.Fatalf("installed digest = %s, want unchanged %s", got, bins.lowerDigest)
	}
	releaseWaitNoStaging(t, j, 15*time.Second)
	// Normal admission is restored: disarm the injected failure so activity
	// detection observes real work again, then admit fresh chat work.
	if err := os.Remove(detectFile); err != nil {
		t.Fatal(err)
	}
	startStopJourneyChat(t, baseURL, token)
	waitStopWorkActive(t, baseURL, token, 1, true)

	// A detection failure during the confirmation observations: the
	// extended budget holds the stopping interval open while the stubborn
	// sessions settle, so the trigger file reliably precedes the first
	// confirmation observation after the dispatches.
	detectFile2 := filepath.Join(t.TempDir(), "detect-fail-2")
	j2, bins2, _, p2, baseURL2, token2, _ := installStopServerFixture(t, true,
		"--stop-work-budget", "30s", "--fail-stop-detection", detectFile2)
	defer func() { _ = p2.terminate() }()
	postInstallStopCheck(t, baseURL2, token2)
	featureID2 := createStopJourneyFeature(t, baseURL2, token2, "detect-fail-confirm")
	startStopJourneyFeature(t, baseURL2, token2, featureID2)
	startStopJourneyChat(t, baseURL2, token2)
	waitStopWorkActive(t, baseURL2, token2, 1, true)

	status, raw, err = installStopMutation(http.MethodPost, baseURL2, token2, "/api/v1/update/install", `{"consent":true,"when":"now","stop_active_work":true}`)
	if err != nil || status != http.StatusAccepted {
		t.Fatalf("stop install (confirmation leg): status %d err %v body %s", status, err, raw)
	}
	waitUpdateSnapshot(t, baseURL2, token2, func(s server.UpdateSnapshot) bool {
		return string(s.Status) == "draining"
	}, 30*time.Second)
	if err := os.WriteFile(detectFile2, []byte("fail"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitUpdateSnapshot(t, baseURL2, token2, func(s server.UpdateSnapshot) bool {
		return string(s.Status) == "failed" && s.Error != nil && s.Error.Code == "update_blocked_active_work" &&
			s.ActiveWorkSummary.DetectionFailed
	}, 60*time.Second)
	if got := stopJourneyFeatureStatus(t, baseURL2, token2, featureID2); got != "interrupted" {
		t.Fatalf("feature status after confirmation failure = %q, want interrupted", got)
	}
	if h, err := j2.getHealth(baseURL2); err != nil || h.Owner.Version != releaseInstallLower {
		t.Fatalf("health after confirmation failure = %+v err %v; want %q serving", h, err, releaseInstallLower)
	}
	if got := j2.installDigest(); got != bins2.lowerDigest {
		t.Fatalf("installed digest = %s, want unchanged %s", got, bins2.lowerDigest)
	}
	// Admission reopens after the abort: disarm the injected failure so
	// activity detection observes real work again, then admit fresh chat.
	if err := os.Remove(detectFile2); err != nil {
		t.Fatal(err)
	}
	startStopJourneyChat(t, baseURL2, token2)
	waitStopWorkActive(t, baseURL2, token2, 0, true)
}

// TestInstallStopWorkJourneyRollbackAfterConsentedStop exercises the
// existing rollback coverage through a consented interruption: the accepted
// install stops the active feature and chat, confirms their completion, and
// replaces the binary, but the target fails its startup; recovery restores
// the previous build on the same process identity, and the interrupted work
// stays interrupted after recovery with no automatic resumption.
func TestInstallStopWorkJourneyRollbackAfterConsentedStop(t *testing.T) {
	selfupdateJourneyGuard(t)
	j, bins, _, p, baseURL, token, _ := installStopServerFixture(t, false, "--fail-at", "target-startup")
	defer func() { _ = p.terminate() }()

	postInstallStopCheck(t, baseURL, token)
	featureID := createStopJourneyFeature(t, baseURL, token, "rollback-after-stop")
	startStopJourneyFeature(t, baseURL, token, featureID)
	startStopJourneyChat(t, baseURL, token)
	waitStopWorkActive(t, baseURL, token, 1, true)
	pre, err := j.getHealth(baseURL)
	if err != nil {
		t.Fatalf("pre-install health: %v", err)
	}

	status, raw, err := installStopMutation(http.MethodPost, baseURL, token, "/api/v1/update/install", `{"consent":true,"when":"now","stop_active_work":true}`)
	if err != nil || status != http.StatusAccepted {
		t.Fatalf("stop install: status %d err %v body %s", status, err, raw)
	}

	// The verified candidate installs but fails its startup: recovery
	// restores the previous build and suppresses the exact failed target.
	r := j.waitReceiptRolledBack(60 * time.Second)
	if r.FromVersion != releaseInstallLower || r.ToVersion != releaseInstallTarget {
		t.Fatalf("receipt versions = %q -> %q", r.FromVersion, r.ToVersion)
	}
	if r.RecoveryKind != selfupdate.RecoveryKindRestore {
		t.Fatalf("recovery kind = %q, want restore", r.RecoveryKind)
	}
	post := j.waitHealthy(p, baseURL, releaseInstallLower, 45*time.Second)
	if post.Owner.PID != pre.Owner.PID {
		t.Fatalf("recovered owner pid = %d, want preserved %d", post.Owner.PID, pre.Owner.PID)
	}
	if got := j.installDigest(); got != bins.lowerDigest {
		t.Fatalf("installed digest = %s, want restored %s", got, bins.lowerDigest)
	}
	requireSuppressedTarget(t, j, r)

	// The interrupted work stays interrupted after recovery: nothing
	// resumes automatically and the ended chat stays ended.
	if got := stopJourneyFeatureStatus(t, baseURL, token, featureID); got != "interrupted" {
		t.Fatalf("feature status after recovery = %q, want interrupted", got)
	}
	waitUpdateSnapshot(t, baseURL, token, func(s server.UpdateSnapshot) bool {
		return !s.ActiveWorkSummary.ChatActive && s.ActiveWorkSummary.FeatureCount == 0
	}, 30*time.Second)
	if code := p.terminate(); code != 0 {
		t.Fatalf("SIGTERM exit code = %d (stderr tail:\n%s)", code, p.stderrTail())
	}
	// The restored image reported the canonical update_rolled_back warning
	// through the same stderr stream, after the process fully settled.
	if !p.stderrContains("warning[update_rolled_back]") {
		t.Fatalf("no update_rolled_back warning (stderr tail:\n%s)", p.stderrTail())
	}
}
