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

package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
)

func TestParseLaunchArgsCapabilitySubcommands(t *testing.T) {
	t.Parallel()
	opts, err := parseLaunchArgs([]string{cliSubcommandCapabilityProbe, "authenticated-browser(slack.com)"})
	if err != nil || opts.mode != launchModeCapabilityProbe || opts.capabilityProbe.name != "authenticated-browser(slack.com)" {
		t.Fatalf("capability-probe parse = %+v, %v", opts.capabilityProbe, err)
	}
	if _, err := parseLaunchArgs([]string{cliSubcommandCapabilityProbe}); err == nil {
		t.Fatal("capability-probe without a name accepted")
	}
	opts, err = parseLaunchArgs([]string{cliSubcommandReportBlocker, cliFlagContract, "c.yaml", cliFlagDir, "iter",
		"--items", "visual_1, visual_2,", "--capability", "authenticated-browser(slack.com)", "--reason", "no session"})
	if err != nil || opts.mode != launchModeReportBlocker {
		t.Fatalf("report-blocker parse error = %v", err)
	}
	if strings.Join(opts.reportBlocker.items, "|") != "visual_1|visual_2" || opts.reportBlocker.reason != "no session" {
		t.Fatalf("report-blocker opts = %+v", opts.reportBlocker)
	}
	if _, err := parseLaunchArgs([]string{cliSubcommandReportBlocker, cliFlagContract, "c.yaml", cliFlagDir, "iter", "--capability", "x", "--reason", "r"}); err == nil || !strings.Contains(err.Error(), "--items") {
		t.Fatalf("report-blocker without items error = %v", err)
	}
}

func TestRunArgsCapabilityProbeReportsVerdict(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runArgs([]string{cliSubcommandCapabilityProbe, "network(127.0.0.1:1)"}, &stdout, &stderr, failingServerLauncher(t), failingUpdater(t))
	if code != 1 || !strings.Contains(stderr.String(), "unavailable") {
		t.Fatalf("closed port probe: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code = runArgs([]string{cliSubcommandCapabilityProbe, "okta-session"}, &stdout, &stderr, failingServerLauncher(t), failingUpdater(t))
	if code != 1 || !strings.Contains(stderr.String(), "unknown built-in capability") {
		t.Fatalf("unknown capability: code=%d stderr=%q", code, stderr.String())
	}
}

func TestRunArgsReportBlockerWritesAgentGate(t *testing.T) {
	dir := t.TempDir()
	contractPath := filepath.Join(dir, "testing-contract.yaml")
	contract := agent.CompileTestingContract(strings.Join([]string{
		"## Success Criteria",
		"### Visual Evidence",
		"- [ ] Block Kit Builder preview of the root card [size: 1280x800]",
	}, "\n"), filepath.Join(dir, "plan.md"), "collapsed")
	if err := agent.WriteTestingContract(contractPath, contract); err != nil {
		t.Fatal(err)
	}
	iterDir := filepath.Join(dir, "iteration-01")
	itemID := contract.Items[0].ID

	var stdout, stderr bytes.Buffer
	code := runArgs([]string{cliSubcommandReportBlocker, cliFlagContract, contractPath, cliFlagDir, iterDir,
		"--items", itemID, "--capability", "authenticated-browser(slack.com)", "--reason", "Builder requires a signed-in workspace session"},
		&stdout, &stderr, failingServerLauncher(t), failingUpdater(t))
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "RETRY") {
		t.Fatalf("stdout = %q, want the RETRY handoff instruction", stdout.String())
	}
	rec, err := agent.ReadNeedUserInputRecord(agent.NeedUserInputPath(iterDir))
	if err != nil {
		t.Fatal(err)
	}
	if rec.Source != agent.NeedUserInputSourceAgent || rec.VerificationDecision == nil || rec.VerificationDecision.ItemIDs[0] != itemID {
		t.Fatalf("gate = %+v", rec)
	}
	if rec.Verification == nil || rec.Verification.Blockers[0].Name != "Block Kit Builder preview of the root card" {
		t.Fatalf("blocker name must come from the contract: %+v", rec.Verification)
	}

	stdout.Reset()
	stderr.Reset()
	code = runArgs([]string{cliSubcommandReportBlocker, cliFlagContract, contractPath, cliFlagDir, iterDir,
		"--items", "visual_nope", "--capability", "display", "--reason", "r"},
		&stdout, &stderr, failingServerLauncher(t), failingUpdater(t))
	if code != 1 || !strings.Contains(stderr.String(), "unknown contract item") {
		t.Fatalf("unknown item: code=%d stderr=%q", code, stderr.String())
	}
}

func TestResolveCapabilityPolicy(t *testing.T) {
	t.Parallel()
	if p := resolveCapabilityPolicy(nil, "/rt", false); !p.AllowBrowserState || p.RuntimeDir != "/rt" {
		t.Fatalf("loopback default = %+v", p)
	}
	if p := resolveCapabilityPolicy(nil, "/rt", true); p.AllowBrowserState {
		t.Fatalf("network bind must deny browser state by default: %+v", p)
	}
	cfg := &config.Config{}
	cfg.Server.Capabilities.BrowserState = "allow"
	if p := resolveCapabilityPolicy(cfg, "/rt", true); !p.AllowBrowserState {
		t.Fatalf("explicit allow ignored: %+v", p)
	}
	cfg.Server.Capabilities.BrowserState = "deny"
	if p := resolveCapabilityPolicy(cfg, "/rt", false); p.AllowBrowserState {
		t.Fatalf("explicit deny ignored: %+v", p)
	}
}
