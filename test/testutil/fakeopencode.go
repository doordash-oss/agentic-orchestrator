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
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/opencode"
)

// The fake OpenCode is the test binary itself, re-executed with
// FakeOpenCodeEnv set. It speaks ACP on stdio and, when launched with
// --port and --hostname, serves the slice of OpenCode's HTTP API the adapter
// uses. A test package that launches it calls RunFakeOpenCodeIfRequested
// first thing in TestMain.
const (
	// FakeOpenCodeEnv selects fake `opencode acp` mode in a re-executed test
	// binary.
	FakeOpenCodeEnv = "AGENTICO_FAKE_OPENCODE"
	// FakeOpenCodeScriptEnv names the JSON script file driving the fake.
	FakeOpenCodeScriptEnv = "AGENTICO_FAKE_OPENCODE_SCRIPT"
)

// Markers a test puts in a prompt's text to script the fake OpenCode. A
// prompt without a marker streams "Hello from turn N".
const (
	// FakeOpenCodePermBash raises a root `execute` session/request_permission
	// (also published on the HTTP stream, as OpenCode does) and replies
	// "Command allowed" or "Command denied".
	FakeOpenCodePermBash = "OPENCODE_PERM_BASH"
	// FakeOpenCodePermHelper is FakeOpenCodePermBash for a bare
	// `agentico api` command, which the supervisor handler auto-allows.
	FakeOpenCodePermHelper = "OPENCODE_PERM_HELPER"
	// FakeOpenCodePermEdit raises a root `edit` request for
	// FakeOpenCodeEditPath and replies "Edit allowed" or "Edit denied".
	FakeOpenCodePermEdit = "OPENCODE_PERM_EDIT"
	// FakeOpenCodePermTask raises a root `think` request for a task spawn and
	// replies "Task allowed" or "Task denied".
	FakeOpenCodePermTask = "OPENCODE_PERM_TASK"
	// FakeOpenCodeAsk publishes a root question.asked on the HTTP stream and
	// replies "You chose <labels>" or "Question rejected".
	FakeOpenCodeAsk = "OPENCODE_ASK"
	// FakeOpenCodeChildPermBash starts a task, publishes a child-session
	// `bash` permission.asked on the HTTP stream only, and replies
	// "Child allowed" or "Child denied" once it is answered over HTTP.
	FakeOpenCodeChildPermBash = "OPENCODE_CHILD_PERM_BASH"
	// FakeOpenCodeChildAsk starts a task, publishes a child-session
	// question.asked and replies "Child chose <labels>" or
	// "Child question rejected".
	FakeOpenCodeChildAsk = "OPENCODE_CHILD_ASK"
	// FakeOpenCodeHold keeps the prompt open until session/cancel.
	FakeOpenCodeHold = "OPENCODE_HOLD"
	// FakeOpenCodeStubborn keeps the prompt open and ignores session/cancel.
	FakeOpenCodeStubborn = "OPENCODE_STUBBORN"
	// FakeOpenCodePartial streams FakeSupervisorPartialText, starts a shell
	// tool call, then holds like FakeOpenCodeHold.
	FakeOpenCodePartial = "OPENCODE_PARTIAL"
	// FakeOpenCodeSeeded replies "Seeded with <n> prior messages: <first
	// prior user text>" from the last noReply prompt; the first prompt after
	// any seed replies the same way without the marker.
	FakeOpenCodeSeeded    = "OPENCODE_SEEDED"
	FakeOpenCodeUsageHigh = "OPENCODE_USAGE_HIGH"
)

// Details the scripted requests carry.
const (
	FakeOpenCodeBashCommand   = "make test"
	FakeOpenCodeHelperCommand = "agentico api GET /api/v1/health"
	FakeOpenCodeEditPath      = "main.go"
	FakeOpenCodeQuestion      = "Which branch?"
	FakeOpenCodeVersion       = "1.17.9"
	// FakeOpenCodeServerUser is the HTTP basic-auth username OpenCode
	// expects beside OPENCODE_SERVER_PASSWORD.
	FakeOpenCodeServerUser = "opencode"
)

// Files the fake writes beside its script, in addition to
// FakeSupervisorInvocationsFile (one line per launch) and
// FakeSupervisorArgvFile (the latest launch's argv, one per line).
const (
	// FakeOpenCodeACPFile holds every ACP line in both directions, one
	// FakeOpenCodeACPLine each.
	FakeOpenCodeACPFile = "acp.jsonl"
	// FakeOpenCodeHTTPFile holds every HTTP request, one FakeOpenCodeHTTPRequest
	// each.
	FakeOpenCodeHTTPFile = "http.jsonl"
	// FakeOpenCodeEnvFile holds one FakeOpenCodeLaunchEnv per launch.
	FakeOpenCodeEnvFile = "env.jsonl"
	// FakeOpenCodeSeedsFile holds one FakeOpenCodeSeed per noReply prompt.
	FakeOpenCodeSeedsFile = "seeds.jsonl"
	// FakeOpenCodeModelRepliesFile holds one FakeOpenCodeSeed per HTTP prompt
	// that asked for a model reply.
	FakeOpenCodeModelRepliesFile = "model_replies.jsonl"
	// FakeOpenCodeSetModelsFile holds one FakeOpenCodeSetModel per
	// session/set_model or session/set_config_option.
	FakeOpenCodeSetModelsFile = "set_models.jsonl"
	// FakeOpenCodeSessionsFile holds one FakeOpenCodeSession per minted or
	// loaded session id.
	FakeOpenCodeSessionsFile = "sessions.jsonl"
)

// FakeOpenCodeModel is the fake provider's default chat model.
const FakeOpenCodeModel = "fake/opencode-fake"
const FakeOpenCodeSecondModel = "fake/opencode-second"

// FakeOpenCodeScript configures the fake. The fake re-reads it at every
// launch, so a test can change behaviour between generations.
type FakeOpenCodeScript struct {
	// RejectSetModel makes ACP session/set_model return a scripted refusal.
	RejectSetModel bool `json:"reject_set_model,omitempty"`
	// NoHTTP never starts the HTTP server.
	NoHTTP bool `json:"no_http,omitempty"`
	// RejectPassword answers 401 to every HTTP request.
	RejectPassword bool `json:"reject_password,omitempty"`
	// ChildWhileDisconnected drops every event stream and refuses new ones
	// while it publishes a child request, so only GET /permission and
	// GET /question can reveal it.
	ChildWhileDisconnected bool `json:"child_while_disconnected,omitempty"`
	// ResolveChildElsewhere answers a child request itself shortly after
	// publishing it, as another OpenCode client would.
	ResolveChildElsewhere bool `json:"resolve_child_elsewhere,omitempty"`
	// SeedError fails every POST /session/{id}/message with this HTTP status.
	SeedError int `json:"seed_error,omitempty"`
	// ExitOnLaunch makes the fake exit at once with this status.
	ExitOnLaunch int `json:"exit_on_launch,omitempty"`
}

// FakeOpenCodeACPLine is one recorded ACP line. Dir is "in" for lines the
// adapter sent and "out" for lines the fake sent.
type FakeOpenCodeACPLine struct {
	Launch int             `json:"launch"`
	Dir    string          `json:"dir"`
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}

// FakeOpenCodeHTTPRequest is one recorded HTTP request. Auth is "ok",
// "missing" or "rejected"; the credential itself is never recorded.
type FakeOpenCodeHTTPRequest struct {
	Launch int             `json:"launch"`
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Auth   string          `json:"auth"`
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body,omitempty"`
}

// FakeOpenCodeLaunchEnv is one launch's environment filtered to OPENCODE_
// keys. A non-empty OPENCODE_SERVER_PASSWORD is recorded as
// FakeOpenCodeRedacted.
type FakeOpenCodeLaunchEnv struct {
	Launch int               `json:"launch"`
	Env    map[string]string `json:"env"`
}

// FakeOpenCodeRedacted stands in for a recorded secret.
const FakeOpenCodeRedacted = "[set]"

// FakeOpenCodeSeed is one recorded POST /session/{id}/message.
type FakeOpenCodeSeed struct {
	Launch    int    `json:"launch"`
	SessionID string `json:"session_id"`
	Text      string `json:"text"`
	NoReply   bool   `json:"no_reply"`
}

// FakeOpenCodeSetModel is one recorded model or config-option change.
type FakeOpenCodeSetModel struct {
	Launch    int    `json:"launch"`
	Method    string `json:"method"`
	SessionID string `json:"session_id"`
	ModelID   string `json:"model_id,omitempty"`
	ConfigID  string `json:"config_id,omitempty"`
	Value     string `json:"value,omitempty"`
}

// FakeOpenCodeSession is one session id the fake minted or loaded. Kind is
// "root", "child" or "loaded"; ParentID is set for a child.
type FakeOpenCodeSession struct {
	Launch   int    `json:"launch"`
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	ParentID string `json:"parent_id,omitempty"`
}

// FakeOpenCodeProvider is the real OpenCode provider launched against the
// fake: BuildCommand and NewProtocol are the real adapter's, with the binary
// swapped for the test executable.
type FakeOpenCodeProvider struct {
	Script string
	inner  *opencode.Provider
}

// NewFakeOpenCodeProvider returns a provider whose sessions run the fake
// scripted by the JSON file at script.
func NewFakeOpenCodeProvider(t testing.TB, script string) *FakeOpenCodeProvider {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	inner := opencode.NewWithBinary(exe)
	inner.SetModelCatalog(fakeOpenCodeCatalog())
	return &FakeOpenCodeProvider{Script: script, inner: inner}
}

func fakeOpenCodeCatalog() []llm.ModelInfo {
	return []llm.ModelInfo{{
		ID:                 FakeOpenCodeModel,
		DisplayName:        "OpenCode Fake",
		ContextWindow:      200000,
		Category:           "cheap",
		EffortCapabilities: []llm.EffortLevel{llm.EffortLow, llm.EffortHigh},
		EffortVariants: map[llm.EffortLevel]map[string]any{
			llm.EffortLow:  {"reasoningEffort": "low"},
			llm.EffortHigh: {"reasoningEffort": "high"},
		},
	}, {
		ID:                 FakeOpenCodeSecondModel,
		DisplayName:        "OpenCode Second",
		ContextWindow:      200000,
		Category:           "cheap",
		EffortCapabilities: []llm.EffortLevel{llm.EffortLow, llm.EffortHigh},
		EffortVariants: map[llm.EffortLevel]map[string]any{
			llm.EffortLow:  {"reasoningEffort": "low"},
			llm.EffortHigh: {"reasoningEffort": "high"},
		},
	}}
}

func (p *FakeOpenCodeProvider) Name() string                  { return "opencode" }
func (p *FakeOpenCodeProvider) DetectCLI() bool               { return true }
func (p *FakeOpenCodeProvider) InstallHint() string           { return "" }
func (p *FakeOpenCodeProvider) VersionInfo() (string, error)  { return FakeOpenCodeVersion, nil }
func (p *FakeOpenCodeProvider) MinVersion() [3]int            { return [3]int{} }
func (p *FakeOpenCodeProvider) EnvVarsToExclude() []string    { return nil }
func (p *FakeOpenCodeProvider) SupportsSessionResume() bool   { return p.inner.SupportsSessionResume() }
func (p *FakeOpenCodeProvider) ModelCatalog() []llm.ModelInfo { return fakeOpenCodeCatalog() }
func (p *FakeOpenCodeProvider) AvailableModels() []string {
	return []string{FakeOpenCodeModel, FakeOpenCodeSecondModel}
}
func (p *FakeOpenCodeProvider) MatchesModel(model string) bool {
	backend := opencode.BackendModel(model)
	return backend == FakeOpenCodeModel || backend == FakeOpenCodeSecondModel
}
func (p *FakeOpenCodeProvider) ComputeCost(string, int64, int64) float64 { return 0 }
func (p *FakeOpenCodeProvider) ContextWindowForModel(string) int         { return 200000 }

// BuildCommand is the real OpenCode command with the fake's environment
// added.
func (p *FakeOpenCodeProvider) BuildCommand(opts llm.CommandBuildOpts) ([]string, []string, error) {
	args, env, err := p.inner.BuildCommand(opts)
	if err != nil {
		return nil, nil, err
	}
	return args, append(env, FakeOpenCodeEnv+"=1", FakeOpenCodeScriptEnv+"="+p.Script), nil
}

// NewProtocol is the real OpenCode protocol.
func (p *FakeOpenCodeProvider) NewProtocol(opts llm.ProtocolOpts) llm.Protocol {
	return p.inner.NewProtocol(opts)
}

// WriteFakeOpenCodeScript writes script as JSON into a fresh temp directory
// and returns its path; the fake's recordings land beside it.
func WriteFakeOpenCodeScript(t testing.TB, script FakeOpenCodeScript) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake OpenCode is Unix-only")
	}
	path := filepath.Join(t.TempDir(), "fake-opencode.json")
	UpdateFakeOpenCodeScript(t, path, script)
	return path
}

// UpdateFakeOpenCodeScript rewrites the script; the next launch reads it.
func UpdateFakeOpenCodeScript(t testing.TB, path string, script FakeOpenCodeScript) {
	t.Helper()
	data, err := json.Marshal(script)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write fake OpenCode script: %v", err)
	}
}

// NewFakeOpenCodeRegistry creates a Registry with a single fake OpenCode
// provider.
func NewFakeOpenCodeRegistry(t testing.TB, script string) *llm.Registry {
	t.Helper()
	reg := llm.NewRegistry()
	reg.Register(NewFakeOpenCodeProvider(t, script))
	return reg
}

func fakeOpenCodeRecords[T any](t testing.TB, script, name string) []T {
	t.Helper()
	var out []T
	readJSONLines(t, filepath.Join(filepath.Dir(script), name), func(line []byte) {
		var v T
		if err := json.Unmarshal(line, &v); err != nil {
			t.Fatalf("decode fake OpenCode %s line %s: %v", name, line, err)
		}
		out = append(out, v)
	})
	return out
}

// FakeOpenCodeACP reads every recorded ACP line.
func FakeOpenCodeACP(t testing.TB, script string) []FakeOpenCodeACPLine {
	t.Helper()
	return fakeOpenCodeRecords[FakeOpenCodeACPLine](t, script, FakeOpenCodeACPFile)
}

// FakeOpenCodeMethods lists the ACP methods the adapter called or notified,
// in order (responses to the fake's own requests are skipped).
func FakeOpenCodeMethods(t testing.TB, script string) []string {
	t.Helper()
	var out []string
	for _, line := range FakeOpenCodeACP(t, script) {
		if line.Dir == "in" && line.Method != "" {
			out = append(out, line.Method)
		}
	}
	return out
}

// FakeOpenCodeHTTP reads every recorded HTTP request.
func FakeOpenCodeHTTP(t testing.TB, script string) []FakeOpenCodeHTTPRequest {
	t.Helper()
	return fakeOpenCodeRecords[FakeOpenCodeHTTPRequest](t, script, FakeOpenCodeHTTPFile)
}

// FakeOpenCodeEnvs reads each launch's OPENCODE_ environment.
func FakeOpenCodeEnvs(t testing.TB, script string) []FakeOpenCodeLaunchEnv {
	t.Helper()
	return fakeOpenCodeRecords[FakeOpenCodeLaunchEnv](t, script, FakeOpenCodeEnvFile)
}

// FakeOpenCodeSeeds reads every recorded noReply prompt.
func FakeOpenCodeSeeds(t testing.TB, script string) []FakeOpenCodeSeed {
	t.Helper()
	return fakeOpenCodeRecords[FakeOpenCodeSeed](t, script, FakeOpenCodeSeedsFile)
}

// FakeOpenCodeModelReplies counts the HTTP prompts that asked for a model
// reply.
func FakeOpenCodeModelReplies(t testing.TB, script string) int {
	t.Helper()
	return len(fakeOpenCodeRecords[FakeOpenCodeSeed](t, script, FakeOpenCodeModelRepliesFile))
}

// FakeOpenCodeSetModels reads every recorded model or config-option change.
func FakeOpenCodeSetModels(t testing.TB, script string) []FakeOpenCodeSetModel {
	t.Helper()
	return fakeOpenCodeRecords[FakeOpenCodeSetModel](t, script, FakeOpenCodeSetModelsFile)
}

// FakeOpenCodeSessions reads every minted or loaded session id.
func FakeOpenCodeSessions(t testing.TB, script string) []FakeOpenCodeSession {
	t.Helper()
	return fakeOpenCodeRecords[FakeOpenCodeSession](t, script, FakeOpenCodeSessionsFile)
}

// FakeOpenCodeArgv returns the latest launch's argv, without the binary.
func FakeOpenCodeArgv(t testing.TB, script string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(filepath.Dir(script), FakeSupervisorArgvFile))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

// FakeOpenCodeInvocations counts the fake's launches.
func FakeOpenCodeInvocations(t testing.TB, script string) int {
	t.Helper()
	n := 0
	readJSONLines(t, filepath.Join(filepath.Dir(script), FakeSupervisorInvocationsFile), func([]byte) { n++ })
	return n
}

// PromptText returns the concatenated text of a session/prompt request.
func (l FakeOpenCodeACPLine) PromptText() string {
	var params struct {
		Prompt []struct {
			Text string `json:"text"`
		} `json:"prompt"`
	}
	_ = json.Unmarshal(l.Params, &params)
	var b strings.Builder
	for _, block := range params.Prompt {
		b.WriteString(block.Text)
	}
	return b.String()
}
