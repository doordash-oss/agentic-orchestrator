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

package testutil

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/codex"
)

// The fake Codex app-server is the test binary itself, re-executed with
// FakeCodexEnv set. A test package that launches it calls
// RunFakeCodexIfRequested first thing in TestMain.
const (
	// FakeCodexEnv selects fake app-server mode in a re-executed test binary.
	FakeCodexEnv = "AGENTICO_FAKE_CODEX"
	// FakeCodexScriptEnv names the JSON script file driving the fake.
	FakeCodexScriptEnv = "AGENTICO_FAKE_CODEX_SCRIPT"
)

// Markers a test puts in a turn's input text to script the fake Codex. A
// turn without a marker streams a plain reply.
const (
	// FakeCodexPermBash runs a command item behind a command approval.
	FakeCodexPermBash = "CODEX_PERM_BASH"
	// FakeCodexPermWrite runs a file-change item behind a file-change
	// approval.
	FakeCodexPermWrite = "CODEX_PERM_WRITE"
	// FakeCodexPermHelper asks approval for a bare `agentico api` command,
	// which the supervisor handler auto-allows.
	FakeCodexPermHelper = "CODEX_PERM_HELPER"
	// FakeCodexAsk sends item/tool/requestUserInput and echoes the answer.
	FakeCodexAsk = "CODEX_ASK"
	// FakeCodexHold keeps the turn open until turn/interrupt arrives.
	FakeCodexHold = "CODEX_HOLD"
	// FakeCodexStubborn keeps the turn open and ignores turn/interrupt.
	FakeCodexStubborn = "CODEX_STUBBORN"
	// FakeCodexPartial completes FakeSupervisorPartialText, starts a command
	// item, then holds like FakeCodexHold.
	FakeCodexPartial = "CODEX_PARTIAL"
	// FakeCodexResumed answers with what the launch read from the rollout
	// at resume time.
	FakeCodexResumed = "CODEX_RESUMED"
	// FakeCodexCompact additionally completes a contextCompaction item.
	FakeCodexCompact   = "CODEX_COMPACT"
	FakeCodexUsageHigh = "CODEX_USAGE_HIGH"
)

// Command and output the scripted command items carry.
const (
	FakeCodexBashCommand   = "make test"
	FakeCodexBashOutput    = "ok all tests passed\n"
	FakeCodexHelperCommand = "agentico api GET /api/v1/health"
	FakeCodexWritePath     = "main.go"
)

// Files the fake writes beside its script.
const (
	// FakeCodexRequestsFile holds every inbound request, notification and
	// response, one FakeCodexRequest JSON line each.
	FakeCodexRequestsFile = "requests.jsonl"
	// FakeCodexResumesFile holds one FakeCodexResume line per thread/resume.
	FakeCodexResumesFile = "resumes.jsonl"
	// FakeCodexThreadsFile holds one line per minted thread id.
	FakeCodexThreadsFile = "threads"
	// FakeCodexSignalsFile holds one line per SIGINT the fake received.
	FakeCodexSignalsFile = "signals"
)

// FakeCodexModel and FakeCodexSecondModel are chat-eligible fake models.
const (
	FakeCodexModel       = "gpt-fake[200K]"
	FakeCodexSecondModel = "gpt-fake-next[200K]"
)

// FakeCodexRPCError is a scripted JSON-RPC error.
type FakeCodexRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// FakeCodexScript configures the fake. The fake re-reads it at every
// launch, so a test can change behaviour between generations.
type FakeCodexScript struct {
	// ResumeError fails thread/resume with this error.
	ResumeError *FakeCodexRPCError `json:"resume_error,omitempty"`
	// StartError fails thread/start with this error.
	StartError *FakeCodexRPCError `json:"start_error,omitempty"`
	// ApprovalPolicy is the effective policy reported in thread responses.
	ApprovalPolicy string `json:"approval_policy,omitempty"`
	// Model is the effective model reported in thread responses; empty
	// echoes the requested model.
	Model string `json:"model,omitempty"`
	// RejectSettingsUpdate fails thread/settings/update.
	RejectSettingsUpdate bool `json:"reject_settings_update,omitempty"`
	// DelaySettingsUpdateMS delays its response to exercise caller timeouts.
	DelaySettingsUpdateMS int `json:"delay_settings_update_ms,omitempty"`
	// ExitOnLaunch makes the fake exit at once with this status.
	ExitOnLaunch int `json:"exit_on_launch,omitempty"`
}

// FakeCodexRequest is one recorded inbound line.
type FakeCodexRequest struct {
	Launch int             `json:"launch"`
	ID     *int64          `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}

// FakeCodexResume is what one thread/resume found under CODEX_HOME.
type FakeCodexResume struct {
	Launch      int    `json:"launch"`
	ThreadID    string `json:"thread_id"`
	RolloutPath string `json:"rollout_path,omitempty"`
	Found       bool   `json:"found"`
	Compacted   bool   `json:"compacted"`
	UserItems   int    `json:"user_items"`
	FirstPrompt string `json:"first_prompt,omitempty"`
	LastPrompt  string `json:"last_prompt,omitempty"`
	LastReply   string `json:"last_reply,omitempty"`
}

// FakeCodexProvider is the real Codex provider launched against the fake
// app-server: BuildCommand and NewProtocol are the real adapter's, with the
// binary swapped for the test executable.
type FakeCodexProvider struct {
	Script string
	inner  *codex.Provider
}

// NewFakeCodexProvider returns a provider whose sessions run the fake
// scripted by the JSON file at script.
func NewFakeCodexProvider(t testing.TB, script string) *FakeCodexProvider {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	inner := codex.NewProvider(exe)
	inner.SetModelCatalog(fakeCodexCatalog())
	return &FakeCodexProvider{Script: script, inner: inner}
}

func fakeCodexCatalog() []llm.ModelInfo {
	first := llm.ModelInfo{
		ID:                 FakeCodexModel,
		DisplayName:        "GPT Fake",
		Aliases:            []string{"gpt-fake"},
		ContextWindow:      200000,
		Category:           "cheap",
		EffortCapabilities: []llm.EffortLevel{llm.EffortLow, llm.EffortMedium, llm.EffortHigh},
	}
	second := first
	second.ID = FakeCodexSecondModel
	second.DisplayName = "GPT Fake Next"
	second.Aliases = []string{"gpt-fake-next"}
	return []llm.ModelInfo{first, second}
}

func (p *FakeCodexProvider) Name() string                  { return "codex" }
func (p *FakeCodexProvider) DetectCLI() bool               { return true }
func (p *FakeCodexProvider) InstallHint() string           { return "" }
func (p *FakeCodexProvider) VersionInfo() (string, error)  { return "codex-cli 0.0.0-fake", nil }
func (p *FakeCodexProvider) MinVersion() [3]int            { return [3]int{} }
func (p *FakeCodexProvider) EnvVarsToExclude() []string    { return nil }
func (p *FakeCodexProvider) SupportsSessionResume() bool   { return true }
func (p *FakeCodexProvider) ModelCatalog() []llm.ModelInfo { return fakeCodexCatalog() }
func (p *FakeCodexProvider) AvailableModels() []string {
	return []string{FakeCodexModel, FakeCodexSecondModel}
}
func (p *FakeCodexProvider) MatchesModel(model string) bool {
	return strings.EqualFold(model, FakeCodexModel) || strings.EqualFold(model, "gpt-fake") ||
		strings.EqualFold(model, FakeCodexSecondModel) || strings.EqualFold(model, "gpt-fake-next")
}
func (p *FakeCodexProvider) ComputeCost(string, int64, int64) float64 { return 0 }
func (p *FakeCodexProvider) ContextWindowForModel(string) int         { return 200000 }

// BuildCommand is the real Codex command with the fake's environment added.
func (p *FakeCodexProvider) BuildCommand(opts llm.CommandBuildOpts) ([]string, []string, error) {
	args, env, err := p.inner.BuildCommand(opts)
	if err != nil {
		return nil, nil, err
	}
	return args, append(env, FakeCodexEnv+"=1", FakeCodexScriptEnv+"="+p.Script), nil
}

// NewProtocol is the real Codex protocol.
func (p *FakeCodexProvider) NewProtocol(opts llm.ProtocolOpts) llm.Protocol {
	return p.inner.NewProtocol(opts)
}

// WriteFakeCodexScript writes script as JSON into a fresh temp directory
// and returns its path; the fake's recordings land beside it.
func WriteFakeCodexScript(t testing.TB, script FakeCodexScript) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake Codex is Unix-only")
	}
	path := filepath.Join(t.TempDir(), "fake-codex.json")
	UpdateFakeCodexScript(t, path, script)
	return path
}

// UpdateFakeCodexScript rewrites the script; the next launch reads it.
func UpdateFakeCodexScript(t testing.TB, path string, script FakeCodexScript) {
	t.Helper()
	data, err := json.Marshal(script)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write fake Codex script: %v", err)
	}
}

// NewFakeCodexRegistry creates a Registry with a single fake Codex provider.
func NewFakeCodexRegistry(t testing.TB, script string) *llm.Registry {
	t.Helper()
	reg := llm.NewRegistry()
	reg.Register(NewFakeCodexProvider(t, script))
	return reg
}

// FakeCodexRequests reads every recorded inbound line.
func FakeCodexRequests(t testing.TB, script string) []FakeCodexRequest {
	t.Helper()
	var out []FakeCodexRequest
	readJSONLines(t, filepath.Join(filepath.Dir(script), FakeCodexRequestsFile), func(line []byte) {
		var req FakeCodexRequest
		if err := json.Unmarshal(line, &req); err != nil {
			t.Fatalf("decode fake Codex request %s: %v", line, err)
		}
		out = append(out, req)
	})
	return out
}

// FakeCodexMethods lists the recorded inbound methods (responses to the
// fake's own requests are skipped).
func FakeCodexMethods(t testing.TB, script string) []string {
	t.Helper()
	var out []string
	for _, req := range FakeCodexRequests(t, script) {
		if req.Method != "" {
			out = append(out, req.Method)
		}
	}
	return out
}

// FakeCodexResumes reads every recorded thread/resume.
func FakeCodexResumes(t testing.TB, script string) []FakeCodexResume {
	t.Helper()
	var out []FakeCodexResume
	readJSONLines(t, filepath.Join(filepath.Dir(script), FakeCodexResumesFile), func(line []byte) {
		var r FakeCodexResume
		if err := json.Unmarshal(line, &r); err != nil {
			t.Fatalf("decode fake Codex resume %s: %v", line, err)
		}
		out = append(out, r)
	})
	return out
}

// FakeCodexThreads lists the thread ids the fake minted, in order.
func FakeCodexThreads(t testing.TB, script string) []string {
	t.Helper()
	var out []string
	readJSONLines(t, filepath.Join(filepath.Dir(script), FakeCodexThreadsFile), func(line []byte) {
		out = append(out, string(line))
	})
	return out
}

// FakeCodexSignals counts the SIGINTs the fake received.
func FakeCodexSignals(t testing.TB, script string) int {
	t.Helper()
	n := 0
	readJSONLines(t, filepath.Join(filepath.Dir(script), FakeCodexSignalsFile), func([]byte) { n++ })
	return n
}

// FakeCodexInvocations counts the fake's launches.
func FakeCodexInvocations(t testing.TB, script string) int {
	t.Helper()
	n := 0
	readJSONLines(t, filepath.Join(filepath.Dir(script), FakeSupervisorInvocationsFile), func([]byte) { n++ })
	return n
}

// TurnText returns the concatenated input text of a turn/start request.
func (r FakeCodexRequest) TurnText() string {
	var params struct {
		Input []struct {
			Text string `json:"text"`
		} `json:"input"`
	}
	_ = json.Unmarshal(r.Params, &params)
	var b strings.Builder
	for _, in := range params.Input {
		b.WriteString(in.Text)
	}
	return b.String()
}

func readJSONLines(t testing.TB, path string, fn func([]byte)) {
	t.Helper()
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 16<<20)
	for scanner.Scan() {
		if line := strings.TrimSpace(scanner.Text()); line != "" {
			fn([]byte(line))
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
}
