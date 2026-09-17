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
