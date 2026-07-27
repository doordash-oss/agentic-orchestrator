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
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/claude"
)

// FakeClaudeProvider is a minimal llm.LLMProvider that runs a shell script
// as the "claude" CLI and uses the real Claude protocol so callers can
// parse the script's JSON output. It is designed for deterministic tests of
// the automatic-review path across unit, agent-internal, and e2e packages.
type FakeClaudeProvider struct {
	Script string
}

func (p FakeClaudeProvider) Name() string                       { return "claude" }
func (p FakeClaudeProvider) DetectCLI() bool                    { return true }
func (p FakeClaudeProvider) InstallHint() string                { return "" }
func (p FakeClaudeProvider) VersionInfo() (string, error)       { return "test", nil }
func (p FakeClaudeProvider) MinVersion() [3]int                 { return [3]int{} }
func (p FakeClaudeProvider) EnvVarsToExclude() []string         { return nil }
func (p FakeClaudeProvider) CheckBareAuth() bool                { return true }
func (p FakeClaudeProvider) SupportsNativeToollessReview() bool { return true }
func (p FakeClaudeProvider) MatchesModel(model string) bool {
	return strings.EqualFold(model, "haiku") || strings.EqualFold(model, "haiku[200K]")
}
func (p FakeClaudeProvider) AvailableModels() []string { return []string{"haiku[200K]"} }
func (p FakeClaudeProvider) ModelCatalog() []llm.ModelInfo {
	return []llm.ModelInfo{{
		ID:            "haiku[200K]",
		DisplayName:   "Claude Haiku",
		Aliases:       []string{"haiku"},
		ContextWindow: 200000,
		Category:      "cheap",
	}}
}
func (p FakeClaudeProvider) BuildCommand(llm.CommandBuildOpts) ([]string, []string, error) {
	return []string{"sh", p.Script}, nil, nil
}
func (p FakeClaudeProvider) NewProtocol(opts llm.ProtocolOpts) llm.Protocol {
	return claude.NewProtocol(opts)
}

// WriteFakeClaudeScript writes a shell script body to a temp file and returns
// its path. The script is executable. Skips on Windows.
func WriteFakeClaudeScript(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake Claude script test is Unix-only")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-claude.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	return path
}

// NewFakeClaudeRegistry creates a Registry with a single FakeClaudeProvider
// running the given script.
func NewFakeClaudeRegistry(t *testing.T, script string) *llm.Registry {
	t.Helper()
	reg := llm.NewRegistry()
	reg.Register(FakeClaudeProvider{Script: script})
	return reg
}

// FakeClaudeInitLines returns the shell script prefix that drains stdin
// during the protocol handshake and emits the system/init message.
func FakeClaudeInitLines() string {
	return "head -n 2 >/dev/null\n" +
		"printf '%s\\n' '{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"s\",\"model\":\"haiku[200K]\"}'\n"
}

// FakeClaudeAllowScriptBody returns a script body that emits an ALLOW
// classification followed by a successful result.
func FakeClaudeAllowScriptBody() string {
	return FakeClaudeInitLines() +
		"printf '%s\\n' '{\"type\":\"assistant\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"ALLOW\"}]}}'\n" +
		"printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\"}'\n"
}

// FakeClaudeDeferScriptBody returns a script body that emits a DEFER
// classification followed by a successful result.
func FakeClaudeDeferScriptBody() string {
	return FakeClaudeInitLines() +
		"printf '%s\\n' '{\"type\":\"assistant\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"DEFER\"}]}}'\n" +
		"printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\"}'\n"
}

// FakeClaudeMalformedScriptBody returns a script body that emits prose
// instead of an ALLOW/DEFER token, followed by a successful result.
func FakeClaudeMalformedScriptBody() string {
	return FakeClaudeInitLines() +
		"printf '%s\\n' '{\"type\":\"assistant\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"I think it is fine\"}]}}'\n" +
		"printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\"}'\n"
}

// FakeClaudeSleepScriptBody returns a script body that drains stdin and
// then sleeps indefinitely, simulating a hung provider.
func FakeClaudeSleepScriptBody() string {
	return "head -n 2 >/dev/null\nsleep 30\n"
}

// FakeClaudeExitScriptBody returns a script body that drains stdin and
// exits immediately with a non-zero status, simulating a provider crash.
func FakeClaudeExitScriptBody() string {
	return "head -n 2 >/dev/null\nexit 1\n"
}

// FakeClaudeToolUseScriptBody returns a script body that emits a
// control_request (tool permission) followed by a successful result,
// simulating a reviewer that tries to use a tool.
func FakeClaudeToolUseScriptBody() string {
	return FakeClaudeInitLines() +
		"printf '%s\\n' '{\"type\":\"control_request\",\"request_id\":\"r1\",\"request\":{\"subtype\":\"can_use_tool\",\"tool_name\":\"Read\",\"input\":{\"file_path\":\"/etc/passwd\"}}}'\n" +
		"printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\"}'\n"
}

// FakeClaudeControlThenAllowScriptBody returns a script body that emits a
// control_request, then an ALLOW assistant message, then a successful
// result — verifying the control request is terminal and the subsequent
// ALLOW can never be accepted.
func FakeClaudeControlThenAllowScriptBody() string {
	return FakeClaudeInitLines() +
		"printf '%s\\n' '{\"type\":\"control_request\",\"request_id\":\"r1\",\"request\":{\"subtype\":\"can_use_tool\",\"tool_name\":\"Bash\",\"input\":{\"command\":\"ls\"}}}'\n" +
		"printf '%s\\n' '{\"type\":\"assistant\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"ALLOW\"}]}}'\n" +
		"printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"ALLOW\"}'\n"
}

// FakeClaudeErrorResultScriptBody returns a script body that emits an ALLOW
// assistant message followed by an error result, verifying the error result
// is terminal even when assistant text contains ALLOW.
func FakeClaudeErrorResultScriptBody() string {
	return FakeClaudeInitLines() +
		"printf '%s\\n' '{\"type\":\"assistant\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"ALLOW\"}]}}'\n" +
		"printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"error\",\"is_error\":true}'\n"
}

// FakeClaudeRefusalScriptBody returns a script body that emits an ALLOW
// assistant message followed by a success result with stop_reason "refusal",
// verifying the refusal is terminal even when assistant text contains ALLOW.
func FakeClaudeRefusalScriptBody() string {
	return FakeClaudeInitLines() +
		"printf '%s\\n' '{\"type\":\"assistant\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"ALLOW\"}]}}'\n" +
		"printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"stop_reason\":\"refusal\"}'\n"
}
