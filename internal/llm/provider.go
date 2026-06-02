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

package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
)

// LLMProvider defines the interface that each coding tool (Claude Code, Codex,
// etc.) must implement to integrate with the orchestrator. One instance exists
// per provider type and is registered in the global Registry.
type LLMProvider interface {
	// Name returns the provider identifier (e.g. "claude", "codex").
	Name() string

	// MatchesModel reports whether this provider handles the given model string.
	MatchesModel(model string) bool

	// DetectCLI reports whether the provider's CLI binary is available in PATH.
	DetectCLI() bool

	// AvailableModels returns the model strings this provider supports.
	AvailableModels() []string

	// BuildCommand returns the CLI command args and environment variables
	// needed to start an interactive session with this provider.
	BuildCommand(opts CommandBuildOpts) (args []string, env []string, err error)

	// NewProtocol creates a per-session Protocol instance that handles the
	// wire-level communication with the provider's CLI process.
	NewProtocol(opts ProtocolOpts) Protocol

	// InstallHint returns instructions for installing this provider's CLI.
	InstallHint() string

	// VersionInfo runs the provider's CLI version command and returns the
	// raw version string (e.g. "1.2.3"). Returns an error if the CLI is not
	// installed or the version command fails.
	VersionInfo() (string, error)

	// MinVersion returns the minimum CLI version required for this provider.
	// CheckProviderVersions uses this to validate each provider independently.
	MinVersion() [3]int

	// EnvVarsToExclude returns environment variable prefixes that should be
	// stripped when spawning child processes for this provider.
	EnvVarsToExclude() []string
}

// ProviderReadiness reports whether an installed provider CLI is actually
// usable for Agentic startup. This is deliberately separate from DetectCLI:
// a binary can be present in PATH but still be unusable because the user has
// not completed the provider's authentication flow.
type ProviderReadiness struct {
	Ready  bool
	Detail string
	Remedy string
}

// ReadinessChecker is implemented by providers that can run a provider-specific
// non-interactive readiness probe.
type ReadinessChecker interface {
	CheckReadiness(ctx context.Context) ProviderReadiness
}

// PromptAdapter provides provider-specific prompt content.
type PromptAdapter interface {
	AskingQuestionsClause() string
}

// RecommendationConfidenceClause is the provider-agnostic prose that teaches
// agents to self-rate their confidence for every answer option they present.
// The recommended option's score is included so downstream readers can tell
// which option the agent judged strongest without depending on provider-
// specific payload details.
// Both Claude and Codex AskingQuestionsClause implementations embed this
// verbatim so the contract is identical across providers regardless of how the
// question payload itself is wire-formatted.
const RecommendationConfidenceClause = `For every multiple-choice question you construct, assign every answer option a confidence score as a decimal between 0.00 and 1.00. This score is a property of the answer itself: how sure you are that the option is the right answer given the context you have. Surface these confidence scores to the user in the question payload you emit. Mark exactly one recommended option, and that recommended option MUST be the single highest-confidence option. If your intended recommendation is not the highest-confidence option, revise either the recommendation label or the confidence scores before sending. When no option is recommended (e.g. free-text questions or genuinely undecidable choices), you do not need to produce confidence values.`

// CostCalculator computes cost and context window for a provider's models.
type CostCalculator interface {
	ComputeCost(model string, inputTokens, outputTokens int64) float64
	ContextWindowForModel(model string) int
}

// CatalogProvider exposes the model catalog populated by discovery.
// Providers implementing this interface allow the Registry to perform
// category-based model selection from the live catalog.
type CatalogProvider interface {
	ModelCatalog() []ModelInfo
}

// Protocol handles the wire-level communication with a provider's CLI process.
// One instance is created per session. Implementations hold provider-specific
// state (e.g. Codex thread IDs, handshake channels).
type Protocol interface {
	// SetStdin sets the stdin writer for the CLI process. Called after the
	// process starts but before Handshake.
	SetStdin(w io.Writer)

	// Handshake performs the provider-specific initialization sequence.
	// For Claude: sends the initialize request.
	// For Codex: performs the 3-step initialize → thread/start → turn/start.
	// Returns when the session is ready to accept user messages.
	Handshake(ctx context.Context) error

	// ParseLine translates a line from the CLI's stdout into zero or more
	// SDKMessages. Returns nil slice for lines that produce no messages
	// (e.g. Codex token usage updates).
	ParseLine(line []byte) ([]SDKMessage, error)

	// SendUserMessage sends a user message to the CLI process.
	SendUserMessage(text string) error

	// RespondToControl responds to a tool permission request.
	// originalInput is the original tool input JSON (used by Claude to echo back).
	RespondToControl(requestID string, allow bool, originalInput json.RawMessage, reason string) error

	// RespondToHook responds to a PreToolUse hook callback (Claude-specific).
	// Providers that don't use hooks should no-op.
	RespondToHook(requestID string) error

	// RespondToAskUser responds to an AskUserQuestion request.
	RespondToAskUser(requestID string, questions json.RawMessage, answers map[string]string, annotations map[string]AskUserAnnotation) error

	// Interrupt asks the CLI to cancel the current turn while keeping the
	// session alive for subsequent messages. Returns ErrNotSupported when
	// the provider has no protocol-level interrupt.
	Interrupt() error

	// SessionID returns the provider's session identifier, if available.
	// Claude returns the session_id from the init message; Codex returns "".
	SessionID() string

	// TranscriptPath returns the path to the provider's transcript file, if available.
	// Claude returns the ~/.claude/projects/... JSONL path; Codex returns "".
	TranscriptPath() string

	// Close performs any cleanup needed when the session ends.
	Close() error
}

// CommandBuildOpts contains all parameters needed to build a CLI command.
type CommandBuildOpts struct {
	Model                string
	Prompt               string
	SystemPrompt         string
	DangerouslySkipPerms bool
	DisallowedTools      []string
	AllowedTools         []string
	ResumeSessionID      string
	IncludePartial       bool
	AdditionalDirs       []string
	AgentNames           []string
	AgentsJSON           string
	StateDir             string
	PermissionPromptTool string
	EffortLevel          EffortLevel // pipeline-driven effort level; each provider maps to its own naming
	// PermissionMode pins the provider's session-level permission mode. Empty
	// means inherit the user's default (e.g. ~/.claude/settings.json). Set this
	// for phases whose prompts require behavior that the user's defaults could
	// silently override — most notably, grilling phases must avoid Claude
	// Code's "auto" mode because it injects a "work without stopping for
	// clarifying questions" system-reminder that overrides [grill-me].
	PermissionMode string
}

// ProtocolOpts contains all parameters needed to create a Protocol instance.
type ProtocolOpts struct {
	Model          string
	ContextWindow  int
	WorkDir        string
	SystemPrompt   string
	InitialPrompt  string
	ApprovalPolicy string
	WritableRoots  []string
	DSP            bool
	StateDir       string
	MarkerPath     string
}

// EffortLevel is a provider-agnostic effort/reasoning level that each provider
// maps to its own CLI-specific naming. The pipeline profile determines the
// effort level: Medium → Medium, Large → High, Moonshot → Max.
type EffortLevel string

const (
	EffortMedium EffortLevel = "medium"
	EffortHigh   EffortLevel = "high"
	EffortMax    EffortLevel = "max"
)

// ErrNotSupported is returned when a provider doesn't support an operation.
var ErrNotSupported = fmt.Errorf("operation not supported by this provider")

// PhaseRole identifies which config phase a model default applies to.
type PhaseRole string

const (
	PhaseResearch       PhaseRole = "research"
	PhasePlanning       PhaseRole = "planning"
	PhaseImplementation PhaseRole = "implementation"
	PhaseReview         PhaseRole = "review"
	PhaseChat           PhaseRole = "chat"
	PhaseKBBuild        PhaseRole = "kb_build"
)
