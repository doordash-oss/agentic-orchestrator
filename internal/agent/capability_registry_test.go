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

package agent

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

func TestParseCapabilityName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in        string
		name, arg string
		ok        bool
	}{
		{"authenticated-browser(slack.com)", "authenticated-browser", "slack.com", true},
		{"display", "display", "", true},
		{" network( db.internal:5432 ) ", "network", "db.internal:5432", true},
		{"Okta session", "", "", false},
		{"bad((x))", "", "", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			name, arg, ok := ParseCapabilityName(tc.in)
			if ok != tc.ok || name != tc.name || arg != tc.arg {
				t.Fatalf("ParseCapabilityName(%q) = %q, %q, %v; want %q, %q, %v", tc.in, name, arg, ok, tc.name, tc.arg, tc.ok)
			}
		})
	}
}

func TestValidateRegistryCapability(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, arg string
		wantErr   string
	}{
		{CapabilityAuthenticatedBrowser, "slack.com", ""},
		{CapabilityAuthenticatedBrowser, "", "requires a host"},
		{CapabilityAuthenticatedBrowser, "https://slack.com/x", "invalid host"},
		{CapabilityNetwork, "db:5432", ""},
		{CapabilityDisplay, "", ""},
		{CapabilityDisplay, "x", "takes no argument"},
		{"okta-session", "", "unknown built-in capability"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name+"/"+tc.arg, func(t *testing.T) {
			t.Parallel()
			err := ValidateRegistryCapability(tc.name, tc.arg)
			if tc.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("error = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestBrowserStateEnvNameAndPath(t *testing.T) {
	t.Parallel()
	if got := BrowserStateEnvName("slack.com"); got != "AGENTICO_BROWSER_STATE_SLACK_COM" {
		t.Fatalf("env name = %q", got)
	}
	want := filepath.Join("/rt", "capabilities", "browser", "app.example-host.com", "storageState.json")
	if got := BrowserStatePath("/rt", "App.Example-Host.com"); got != want {
		t.Fatalf("state path = %q, want %q", got, want)
	}
}

// redirectingTransport answers every request with a redirect to a sign-in
// URL, or with 200 when signedIn is set, without touching the network.
type redirectingTransport struct {
	signedIn bool
	seen     *[]*http.Request
}

func (rt redirectingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if rt.seen != nil {
		*rt.seen = append(*rt.seen, req)
	}
	resp := &http.Response{Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: req}
	if rt.signedIn || strings.Contains(req.URL.Path, "signin") {
		resp.StatusCode = http.StatusOK
		return resp, nil
	}
	resp.StatusCode = http.StatusFound
	resp.Header.Set("Location", "https://"+req.URL.Host+"/signin?redir=x")
	return resp, nil
}

func writeBrowserState(t *testing.T, runtimeDir, host, body string) string {
	t.Helper()
	path := BrowserStatePath(runtimeDir, host)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAuthenticatedBrowserProbe(t *testing.T) {
	t.Parallel()
	future := time.Now().Add(24 * time.Hour).Unix()
	liveState := `{"cookies":[{"name":"d","value":"secret","domain":".slack.com","expires":` + strconv.FormatInt(future, 10) + `}]}`

	t.Run("denied on remote servers", func(t *testing.T) {
		t.Parallel()
		ctx := WithCapabilityPolicy(context.Background(), CapabilityPolicy{RuntimeDir: t.TempDir(), AllowBrowserState: false})
		got := RunRegistryCapabilityProbe(ctx, CapabilityAuthenticatedBrowser, "slack.com")
		if got.Available || !strings.Contains(got.Reason, "not permitted") {
			t.Fatalf("result = %+v, want policy denial", got)
		}
	})
	t.Run("missing state names the expected path", func(t *testing.T) {
		t.Parallel()
		rt := t.TempDir()
		ctx := WithCapabilityPolicy(context.Background(), CapabilityPolicy{RuntimeDir: rt, AllowBrowserState: true})
		got := RunRegistryCapabilityProbe(ctx, CapabilityAuthenticatedBrowser, "slack.com")
		if got.Available || !strings.Contains(got.Reason, BrowserStatePath(rt, "slack.com")) {
			t.Fatalf("result = %+v, want missing-state reason with path", got)
		}
	})
	t.Run("expired cookies fail before any request", func(t *testing.T) {
		t.Parallel()
		rt := t.TempDir()
		writeBrowserState(t, rt, "slack.com", `{"cookies":[{"name":"d","value":"x","domain":".slack.com","expires":100}]}`)
		var seen []*http.Request
		ctx := WithCapabilityPolicy(context.Background(), CapabilityPolicy{RuntimeDir: rt, AllowBrowserState: true,
			HTTPClient: &http.Client{Transport: redirectingTransport{seen: &seen}}})
		got := RunRegistryCapabilityProbe(ctx, CapabilityAuthenticatedBrowser, "slack.com")
		if got.Available || !strings.Contains(got.Reason, "expired") || len(seen) != 0 {
			t.Fatalf("result = %+v (requests %d), want expiry failure without liveness request", got, len(seen))
		}
	})
	t.Run("sign-in redirect means the session is dead", func(t *testing.T) {
		t.Parallel()
		rt := t.TempDir()
		writeBrowserState(t, rt, "slack.com", liveState)
		ctx := WithCapabilityPolicy(context.Background(), CapabilityPolicy{RuntimeDir: rt, AllowBrowserState: true,
			HTTPClient: &http.Client{Transport: redirectingTransport{}}})
		got := RunRegistryCapabilityProbe(ctx, CapabilityAuthenticatedBrowser, "slack.com")
		if got.Available || !strings.Contains(got.Reason, "sign-in") {
			t.Fatalf("result = %+v, want sign-in redirect failure", got)
		}
		if strings.Contains(got.Reason, "redir=x") {
			t.Fatalf("reason leaks the redirect query: %q", got.Reason)
		}
	})
	t.Run("live session exports the state path", func(t *testing.T) {
		t.Parallel()
		rt := t.TempDir()
		path := writeBrowserState(t, rt, "slack.com", liveState)
		var seen []*http.Request
		ctx := WithCapabilityPolicy(context.Background(), CapabilityPolicy{RuntimeDir: rt, AllowBrowserState: true,
			HTTPClient: &http.Client{Transport: redirectingTransport{signedIn: true, seen: &seen}}})
		got := RunRegistryCapabilityProbe(ctx, CapabilityAuthenticatedBrowser, "slack.com")
		if !got.Available {
			t.Fatalf("result = %+v, want available", got)
		}
		if len(got.Env) != 1 || got.Env[0] != "AGENTICO_BROWSER_STATE_SLACK_COM="+path {
			t.Fatalf("env = %v", got.Env)
		}
		if len(seen) != 1 || seen[0].URL.Host != "app.slack.com" || seen[0].Header.Get("Cookie") == "" {
			t.Fatalf("liveness request = %+v, want one cookie-bearing request to app.slack.com", seen)
		}
	})
}

func TestNetworkAndDockerProbesUseDialer(t *testing.T) {
	var dialed []string
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		dialed = append(dialed, network+" "+address)
		if strings.HasSuffix(address, ":5432") {
			return nil, errors.New("connection refused")
		}
		c1, c2 := net.Pipe()
		_ = c2.Close()
		return c1, nil
	}
	ctx := WithCapabilityPolicy(context.Background(), CapabilityPolicy{DialContext: dial})
	if got := RunRegistryCapabilityProbe(ctx, CapabilityNetwork, "api.example.com"); !got.Available {
		t.Fatalf("network default port = %+v", got)
	}
	if got := RunRegistryCapabilityProbe(ctx, CapabilityNetwork, "db.internal:5432"); got.Available || !strings.Contains(got.Reason, "db.internal:5432") {
		t.Fatalf("network refused = %+v", got)
	}
	if dialed[0] != "tcp api.example.com:443" {
		t.Fatalf("dialed = %v", dialed)
	}
	t.Setenv("DOCKER_HOST", "tcp://docker.local:2375")
	if got := RunRegistryCapabilityProbe(ctx, CapabilityDocker, ""); !got.Available {
		t.Fatalf("docker = %+v", got)
	}
}

func TestDisplayProbeReadsEnvironment(t *testing.T) {
	t.Setenv("DISPLAY", ":1")
	t.Setenv("WAYLAND_DISPLAY", "")
	if got := probeDisplay(); !got.Available {
		t.Fatalf("display with DISPLAY set = %+v", got)
	}
}

func TestPlanCapabilityTagsOnEvidenceRows(t *testing.T) {
	t.Parallel()
	plan := strings.Join([]string{
		"### Automated Verification",
		"- [ ] Protected [agentico capability: Okta session; probe: test -f /tmp/okta]: `printf ok`",
		"- [ ] Reachable [agentico capability: network(db.internal:5432)]: `printf db`",
		"### Manual Verification",
		"- [ ] Judge the Builder rendering [agentico capability: authenticated-browser(slack.com)] reads naturally.",
		"### Visual Evidence",
		"- [ ] Block Kit Builder preview of the root card [agentico capability: authenticated-browser(slack.com)], light theme [size: 1280x800]",
	}, "\n")

	steps := ParsePlanVerification(plan)
	if len(steps) != 2 {
		t.Fatalf("steps = %+v", steps)
	}
	if steps[0].Capabilities[0] != (VerificationCapability{Name: "Okta session", Probe: "test -f /tmp/okta"}) {
		t.Fatalf("explicit probe capability = %+v", steps[0].Capabilities)
	}
	if steps[1].Capabilities[0] != (VerificationCapability{Name: "network(db.internal:5432)"}) || steps[1].Description != "Reachable" {
		t.Fatalf("registry capability = %+v / %q", steps[1].Capabilities, steps[1].Description)
	}
	manual := ParsePlanManualVerification(plan)
	if len(manual) != 1 || manual[0].Description != "Judge the Builder rendering reads naturally." || len(manual[0].Capabilities) != 1 {
		t.Fatalf("manual = %+v", manual)
	}
	visual := ParsePlanVisualEvidence(plan)
	if len(visual) != 1 || visual[0].Width != 1280 || strings.Contains(visual[0].Description, "capability") ||
		visual[0].Capabilities[0].Name != "authenticated-browser(slack.com)" {
		t.Fatalf("visual = %+v", visual)
	}

	contract := CompileTestingContract(plan, "/tmp/phase-01/plan.md", "collapsed")
	var visualItem, manualItem, netItem *TestingContractItem
	for i := range contract.Items {
		switch {
		case contract.Items[i].Source == testingContractVisualSource:
			visualItem = &contract.Items[i]
		case contract.Items[i].Source == testingContractManualSource:
			manualItem = &contract.Items[i]
		case contract.Items[i].Name == "Reachable":
			netItem = &contract.Items[i]
		}
	}
	if visualItem == nil || manualItem == nil || netItem == nil {
		t.Fatalf("items = %+v", contract.Items)
	}
	want := TestingContractCapability{Name: "authenticated-browser(slack.com)", Registry: CapabilityAuthenticatedBrowser, Arg: "slack.com", OnMissing: "need_user_input"}
	if visualItem.Capabilities[0] != want || manualItem.Capabilities[0] != want {
		t.Fatalf("compiled evidence capabilities = %+v / %+v", visualItem.Capabilities, manualItem.Capabilities)
	}
	if netItem.Capabilities[0].Registry != CapabilityNetwork || netItem.Capabilities[0].Arg != "db.internal:5432" || netItem.Capabilities[0].Probe != "" {
		t.Fatalf("network capability = %+v", netItem.Capabilities)
	}
	if strings.Contains(visualItem.Command, "capability") {
		t.Fatalf("visual command keeps the tag: %q", visualItem.Command)
	}
}

func TestExecuteTestingContractRegistryProbeBlocksAgentOwnedEvidence(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	iterDir := t.TempDir()
	contractPath := filepath.Join(t.TempDir(), "testing-contract.yaml")
	contract := CompileTestingContract(strings.Join([]string{
		"### Visual Evidence",
		"- [ ] Builder preview [agentico capability: authenticated-browser(slack.com)] [size: 100x100]",
	}, "\n"), contractPath, "collapsed")
	report := BuildContractVerificationReportStub(&contract, contractPath)
	ctx := WithCapabilityPolicy(context.Background(), CapabilityPolicy{RuntimeDir: t.TempDir(), AllowBrowserState: false})

	out, err := ExecuteTestingContract(ctx, NewExecCommandRunner(), &contract, &report, contractPath, iterDir, repo, []feature.FeatureRepo{{Name: "repo", Path: repo, WorktreePath: repo}})
	if err != nil {
		t.Fatalf("ExecuteTestingContract() error = %v", err)
	}
	if len(out.BlockedItems) != 1 || out.Report.Results[0].Status != VerificationStatusBlocked {
		t.Fatalf("outcome = %+v, want the missing capture blocked rather than failed", out.Report.Results[0])
	}
	if !strings.Contains(out.Report.Results[0].BlockedReason, "not permitted") {
		t.Fatalf("blocked reason = %q", out.Report.Results[0].BlockedReason)
	}
}

func TestExecuteTestingContractUnknownRegistryCapabilityIsContractError(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	contract := CompileTestingContract("### Automated Verification\n- [ ] Check [agentico capability: okta-session]: `printf ok`\n", "", "collapsed")
	report := BuildContractVerificationReportStub(&contract, "")
	out, err := ExecuteTestingContract(context.Background(), NewExecCommandRunner(), &contract, &report, "", "", repo, []feature.FeatureRepo{{Name: "repo", Path: repo, WorktreePath: repo}})
	if err != nil {
		t.Fatalf("ExecuteTestingContract() error = %v", err)
	}
	if len(out.ContractErrors) != 1 || len(out.BlockedItems) != 0 {
		t.Fatalf("outcome = %+v, want a planner contract error and no user gate", out)
	}
}

func TestProbeTestingContractCapabilitiesSkipsWaivedAndUndeclared(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	contract := TestingContract{Version: 2, Revision: 1, Items: []TestingContractItem{
		{ID: "plain", Source: testingContractPlanSource, Repo: "repo", Name: "plain", Command: "exit 1",
			Run: &TestingContractRun{Shell: "exit 1", Cwd: "."}, Policy: TestingContractItemPolicy{Required: true}},
		{ID: "blocked", Source: testingContractPlanSource, Repo: "repo", Name: "needs okta", Command: "printf ok",
			Run:          &TestingContractRun{Shell: "printf ok", Cwd: "."},
			Capabilities: []TestingContractCapability{{Name: "Okta session", Probe: "exit 1"}},
			Policy:       TestingContractItemPolicy{Required: true, AllowBlocked: true, AllowWaiver: true}},
		{ID: "waived", Source: testingContractVisualSource, Repo: "repo", Name: "waived capture", Command: "visual: x",
			Capabilities: []TestingContractCapability{{Name: "display", Registry: CapabilityDisplay}},
			Policy:       TestingContractItemPolicy{Required: true, AllowWaiver: true},
			Disposition:  TestingContractItemDisposition{Status: TestingContractDispositionWaived, ChangedBy: "user"}},
	}}
	finalizeTestingContractOwnership(&contract)
	out, err := ProbeTestingContractCapabilities(context.Background(), NewExecCommandRunner(), &contract, "", repo, []feature.FeatureRepo{{Name: "repo", Path: repo, WorktreePath: repo}})
	if err != nil {
		t.Fatalf("ProbeTestingContractCapabilities() error = %v", err)
	}
	if len(out.BlockedItems) != 1 || out.BlockedItems[0] != "blocked" {
		t.Fatalf("BlockedItems = %v, want only the probed item", out.BlockedItems)
	}
	if out.Report.Results[0].Status != VerificationStatusNotRun {
		t.Fatalf("undeclared item ran during the probe pass: %+v", out.Report.Results[0])
	}
}

func TestReviseTestingContractAllowSubstitutionRequiresUser(t *testing.T) {
	t.Parallel()
	contract := &TestingContract{Version: 2, Revision: 1, Items: []TestingContractItem{{
		ID: "visual_1", Source: testingContractVisualSource, Policy: TestingContractItemPolicy{Required: true, AllowBlocked: true, AllowWaiver: true},
	}}}
	if _, err := ReviseTestingContract(contract, []TestingContractChange{{ItemID: "visual_1", Action: TestingContractChangeAllowSubstitution, ChangeReason: "r", ChangedBy: "agent"}}); err == nil {
		t.Fatal("agent-authored allow_substitution accepted")
	}
	revised, err := ReviseTestingContract(contract, []TestingContractChange{{ItemID: "visual_1", Action: TestingContractChangeAllowSubstitution, ChangeReason: "r", ChangedBy: "user"}})
	if err != nil {
		t.Fatalf("ReviseTestingContract() error = %v", err)
	}
	if !revised.Items[0].Policy.AllowSubstitution || revised.Revision != 2 || IsTestingContractItemWaived(revised.Items[0]) {
		t.Fatalf("revised = %+v", revised)
	}
}

func TestVerificationGateOffersSubstituteForAgentEvidence(t *testing.T) {
	t.Parallel()
	contractPath := filepath.Join(t.TempDir(), "testing-contract.yaml")
	contract := TestingContract{Version: 2, Revision: 1, Items: []TestingContractItem{
		{ID: "visual_1", Source: testingContractVisualSource, Owner: TestingContractOwnerAgent, Name: "capture", Command: "visual: capture",
			Policy: TestingContractItemPolicy{Required: true, AllowBlocked: true, AllowWaiver: true}},
		{ID: "plan_1", Source: testingContractPlanSource, Owner: TestingContractOwnerHarness, Name: "cmd", Command: "printf ok",
			Policy: TestingContractItemPolicy{Required: true, AllowSubstitution: true, AllowBlocked: true, AllowWaiver: true}},
	}}
	if err := WriteTestingContract(contractPath, contract); err != nil {
		t.Fatal(err)
	}
	report := BuildContractVerificationReportStub(&contract, contractPath)
	report.Results[0].Status, report.Results[0].BlockedReason = VerificationStatusBlocked, "missing declared capability"

	harnessOnly := SynthesizeVerificationNeedUserInputGateWithContext(contractPath, &contract, &report, []string{"plan_1"}, 1)
	if strings.Join(harnessOnly.VerificationDecision.AllowedActions, ",") != "WAIVE,RETRY_AFTER_AUTH" {
		t.Fatalf("harness-only actions = %v", harnessOnly.VerificationDecision.AllowedActions)
	}
	rec := SynthesizeVerificationNeedUserInputGateWithContext(contractPath, &contract, &report, []string{"visual_1"}, 1)
	if strings.Join(rec.VerificationDecision.AllowedActions, ",") != "WAIVE,RETRY_AFTER_AUTH,ALLOW_SUBSTITUTE" {
		t.Fatalf("agent-evidence actions = %v", rec.VerificationDecision.AllowedActions)
	}
	rec.Questions[0].Answer = NeedUserVerificationAllowSubstitute
	gatePath := filepath.Join(filepath.Dir(contractPath), "iteration-01", NeedUserInputArtifactName)
	if err := ApplyNeedUserVerificationDecision(gatePath, rec); err != nil {
		t.Fatalf("ApplyNeedUserVerificationDecision() error = %v", err)
	}
	got, err := ReadTestingContract(contractPath)
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != 2 || !got.Items[0].Policy.AllowSubstitution || IsTestingContractItemWaived(got.Items[0]) {
		t.Fatalf("contract after ALLOW_SUBSTITUTE = %+v", got.Items[0])
	}
	if err := ApplyNeedUserVerificationDecision(gatePath, rec); err != nil {
		t.Fatalf("second apply should be idempotent: %v", err)
	}
}

func TestAgentReportedBlockerGateRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	contractPath := filepath.Join(dir, "testing-contract.yaml")
	contract := CompileTestingContract(strings.Join([]string{
		"### Automated Verification",
		"- [ ] Build: `go build ./...`",
		"### Visual Evidence",
		"- [ ] Builder preview [size: 100x100]",
	}, "\n"), contractPath, "collapsed")
	if err := WriteTestingContract(contractPath, contract); err != nil {
		t.Fatal(err)
	}
	var visualID, planID string
	for _, item := range contract.Items {
		if item.Source == testingContractVisualSource {
			visualID = item.ID
		} else {
			planID = item.ID
		}
	}
	if _, err := SynthesizeAgentReportedBlockerGate(contractPath, &contract, []string{"nope"}, "authenticated-browser(slack.com)", "no session", 1); err == nil {
		t.Fatal("unknown item accepted")
	}
	if _, err := SynthesizeAgentReportedBlockerGate(contractPath, &contract, []string{visualID}, "", "no session", 1); err == nil {
		t.Fatal("empty capability accepted")
	}
	rec, err := SynthesizeAgentReportedBlockerGate(contractPath, &contract, []string{visualID, visualID}, "authenticated-browser(slack.com)", "Builder needs a signed-in session", 1)
	if err != nil {
		t.Fatalf("SynthesizeAgentReportedBlockerGate() error = %v", err)
	}
	if rec.Source != NeedUserInputSourceAgent || len(rec.VerificationDecision.ItemIDs) != 1 || len(rec.Verification.Blockers) != 1 {
		t.Fatalf("gate = %+v", rec)
	}
	if rec.Verification.Blockers[0].Name != "Builder preview" || !strings.Contains(rec.Verification.Blockers[0].Reason, "signed-in session") {
		t.Fatalf("blocker = %+v, want contract name and agent reason", rec.Verification.Blockers[0])
	}
	if !strings.Contains(strings.Join(rec.VerificationDecision.AllowedActions, ","), NeedUserVerificationAllowSubstitute) {
		t.Fatalf("actions = %v, want substitute offered for agent evidence", rec.VerificationDecision.AllowedActions)
	}

	iterDir := filepath.Join(dir, "iteration-01")
	if err := WriteNeedUserInputRecord(NeedUserInputPath(iterDir), rec); err != nil {
		t.Fatal(err)
	}
	result, err := agentReportedBlockerGate(iterDir, contractPath, 1)
	if err != nil {
		t.Fatalf("agentReportedBlockerGate() error = %v", err)
	}
	if result == nil || result.FinalStatus != "need_user_input" || result.NeedUserInputPath != NeedUserInputPath(iterDir) {
		t.Fatalf("result = %+v", result)
	}

	// A stale gate (contract revised since) is discarded, not trusted.
	revised, err := ReviseTestingContract(&contract, []TestingContractChange{{ItemID: planID, Action: TestingContractChangeWaive, ChangeReason: "r", ChangedBy: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteTestingContract(contractPath, *revised); err != nil {
		t.Fatal(err)
	}
	result, err = agentReportedBlockerGate(iterDir, contractPath, 1)
	if err != nil || result != nil {
		t.Fatalf("stale gate result = %+v, err = %v; want ignored", result, err)
	}
	if _, statErr := os.Stat(NeedUserInputPath(iterDir)); !os.IsNotExist(statErr) {
		t.Fatalf("stale gate artifact survived: %v", statErr)
	}
	// Harness-authored gates are never routed by this path.
	harness := SynthesizeVerificationNeedUserInputGate(contractPath, revised.Revision, []string{planID}, 1)
	if err := WriteNeedUserInputRecord(NeedUserInputPath(iterDir), harness); err != nil {
		t.Fatal(err)
	}
	if result, err = agentReportedBlockerGate(iterDir, contractPath, 1); err != nil || result != nil {
		t.Fatalf("harness gate routed as agent gate: %+v, %v", result, err)
	}
}

func TestEvidenceCapabilityViolations(t *testing.T) {
	t.Parallel()
	plan := strings.Join([]string{
		"### Manual Verification",
		"- [ ] Read the Slack thread at https://doordash.slack.com/archives/C123 and judge the copy.",
		"### Visual Evidence",
		"- [ ] Block Kit Builder preview on app.slack.com, light theme [size: 1280x800]",
		"- [ ] Builder preview [agentico capability: authenticated-browser(slack.com)] on app.slack.com [size: 1280x800]",
		"- [ ] Settings page populated, dark theme [size: 1440x900]",
	}, "\n")
	violations := evidenceCapabilityViolations(plan)
	if len(violations) != 2 {
		t.Fatalf("violations = %+v, want the undeclared manual and visual rows only", violations)
	}
	for _, v := range violations {
		if !strings.Contains(v.Reason, "authenticated-browser(<host>)") {
			t.Fatalf("violation lacks remediation: %q", v.Reason)
		}
	}
	if got := verificationScopeViolations(plan); len(got) != 2 {
		t.Fatalf("scope violations = %+v", got)
	}
}

func TestPreIterationCapabilityGateHandsStateToImplementer(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	rt := t.TempDir()
	future := time.Now().Add(time.Hour).Unix()
	statePath := writeBrowserState(t, rt, "slack.com", `{"cookies":[{"name":"d","value":"v","domain":".slack.com","expires":`+strconv.FormatInt(future, 10)+`}]}`)
	contractPath := filepath.Join(dir, "testing-contract.yaml")
	contract := CompileTestingContract("### Visual Evidence\n- [ ] Builder preview [agentico capability: authenticated-browser(slack.com)] [size: 10x10]\n", contractPath, "collapsed")
	if err := WriteTestingContract(contractPath, contract); err != nil {
		t.Fatal(err)
	}
	cfg := ImplementConfig{
		Feature:       &feature.Feature{ID: "f", Repos: []feature.FeatureRepo{{Name: "repo", Path: dir, WorktreePath: dir}}},
		WorkDir:       dir,
		CommandRunner: NewExecCommandRunner(),
		CapabilityPolicy: &CapabilityPolicy{RuntimeDir: rt, AllowBrowserState: true,
			HTTPClient: &http.Client{Transport: redirectingTransport{signedIn: true}}},
	}
	gated, env, err := preIterationCapabilityGate(cfg, contractPath, filepath.Join(dir, "iteration-01"), 1)
	if err != nil || gated != nil {
		t.Fatalf("gate = %+v, err = %v; want no gate", gated, err)
	}
	if len(env) != 1 || env[0] != "AGENTICO_BROWSER_STATE_SLACK_COM="+statePath {
		t.Fatalf("env = %v", env)
	}

	cfg.CapabilityPolicy.AllowBrowserState = false
	gated, env, err = preIterationCapabilityGate(cfg, contractPath, filepath.Join(dir, "iteration-02"), 2)
	if err != nil || gated == nil || gated.FinalStatus != "need_user_input" || len(env) != 0 {
		t.Fatalf("denied policy: gate = %+v, env = %v, err = %v", gated, env, err)
	}
}
