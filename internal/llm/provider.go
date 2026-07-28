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
	"strings"
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

// ReviewModelRanker is implemented by providers that define provider-local
// preference bands for automatic-review models. Lower bands are preferred.
// Returning false excludes the model from automatic selection.
type ReviewModelRanker interface {
	ReviewPreferenceBand(model ModelInfo) (int, bool)
}

// NativeToollessReviewer is implemented only by providers whose automatic-
// review launch and protocol have been audited to expose no native tool,
// question, child-session, customization, or persistence surface. General
// provider support is deliberately insufficient: automatic review requires an
// explicit positive attestation of the complete isolation contract.
type NativeToollessReviewer interface {
	SupportsNativeToollessReview() bool
}

// VersionEnforcer is implemented by providers whose installed CLI must meet
// MinVersion() to be usable at startup. When EnforcesMinVersion reports true and
// the installed CLI is older than MinVersion(), the provider is excluded from
// the ready set just like a failed readiness probe.
//
// Providers that do not implement this interface (or that return false) keep the
// warn-only version policy: a below-minimum CLI logs a startup warning but the
// provider stays selectable.
type VersionEnforcer interface {
	EnforcesMinVersion() bool
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

// SessionCostAdjustment is provider-specific cost and usage that is not carried
// by the primary session result. For example, OpenCode stores Task subagents as
// child sessions outside the parent ACP usage_update total.
type SessionCostAdjustment struct {
	TotalCostUSD float64
	Usage        Usage
	SessionIDs   []string
}

// SessionCostAugmenter is an optional Protocol capability for providers that
// need to account for provider-managed child sessions in addition to the parent
// session's terminal result.
type SessionCostAugmenter interface {
	AdditionalSessionCost(ctx context.Context) (SessionCostAdjustment, error)
}

// CatalogProvider exposes the model catalog populated by discovery.
// Providers implementing this interface allow the Registry to perform
// category-based model selection from the live catalog.
type CatalogProvider interface {
	ModelCatalog() []ModelInfo
}

// CatalogDiscoverer is implemented by providers that can refresh their model
// catalog from the local provider CLI. Discovery is best-effort: callers should
// fall back to CatalogProvider/default catalogs when it fails.
type CatalogDiscoverer interface {
	DiscoverModelCatalog(ctx context.Context) ([]ModelInfo, error)
}

// ModelDiscoveryReporter receives models as soon as a provider discovers them.
// It is best-effort and should be safe to call from provider discovery
// goroutines.
type ModelDiscoveryReporter func(ModelInfo)

// CatalogProgressDiscoverer is implemented by providers that can stream
// model discovery progress before returning the final catalog.
type CatalogProgressDiscoverer interface {
	DiscoverModelCatalogWithProgress(ctx context.Context, report ModelDiscoveryReporter) ([]ModelInfo, error)
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
	// WritableRoots lists the only locations a provider may edit/patch in normal
	// operation, mirroring ProtocolOpts.WritableRoots. Empty means the provider
	// applies its default writable scope; providers that do not consume it leave
	// their behavior unchanged.
	WritableRoots []string
	// ReadRoots lists the directories a provider may read without per-call
	// mediation, mirroring the read mounts Agentico granted (feature state, work
	// dir, and additional read roots such as skills, guidelines, worktrees,
	// knowledge base, images, and attachments). Empty means the provider applies
	// its default read scope; providers that do not consume it leave their
	// behavior unchanged.
	ReadRoots []string
	// WorkDir is the session's working directory (cwd). Empty leaves a provider's
	// path matching unchanged.
	WorkDir string
	// PermissionMode pins the provider's session-level permission mode. Empty
	// means inherit the user's default (e.g. ~/.claude/settings.json). Set this
	// for phases whose prompts require behavior that the user's defaults could
	// silently override — most notably, grilling phases must avoid Claude
	// Code's "auto" mode because it injects a "work without stopping for
	// clarifying questions" system-reminder that overrides [grill-me].
	PermissionMode string
	// ZeroTools, when true, asks the provider to expose zero tools (e.g.
	// Claude's --tools ""). This is stronger than DisallowedTools because it
	// removes the entire native and MCP tool surface rather than denylisting
	// known tool names. Providers that do not support it leave their behavior
	// unchanged.
	ZeroTools bool
	// NoSessionPersistence, when true, asks the provider to disable native
	// session persistence (e.g. Claude's --no-session-persistence) so no
	// provider transcript or session identity is created. Providers that do
	// not support it leave their behavior unchanged.
	NoSessionPersistence bool
	// NoCustomization, when true, asks the provider to skip all project/user
	// customization — CLAUDE.md auto-discovery, hooks, LSP, plugin sync,
	// settings, rules, skills, and local configuration (e.g. Claude's
	// --safe-mode). This ensures the hidden reviewer's context contains only the
	// static review policy and the declared execution-context fields, with
	// no project-injected instructions that could alter the classification.
	NoCustomization bool
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
	// NativeToollessReview selects the provider's audited one-turn reviewer
	// protocol boundary. Providers that attest NativeToollessReviewer use this
	// to omit every tool, question, child-session, continuation, and persistent
	// session surface while leaving ordinary sessions unchanged.
	NativeToollessReview bool
	// ResumeSessionID, when non-empty, asks the protocol to resume a prior
	// provider session identity rather than start a fresh one. Empty means a
	// normal new session, so providers that do not resume leave their behavior
	// unchanged.
	ResumeSessionID string
	// Interactive marks a session where a human answers every turn in real time
	// (e.g. AMA chat), as opposed to an unattended autonomous phase whose
	// pending questions may be auto-picked by confidence/"(Recommended)"
	// markers. Providers whose AskUserQuestion support is text-parsed rather
	// than native (OpenCode, Codex) use this to skip that entire pipeline: it
	// exists only to imitate Claude's native AskUserQuestion tool-call UX for a
	// provider that can otherwise only express a question as plain text, and a
	// human already reading every reply gets no benefit from that imitation —
	// they can just read the model's question (in whatever shape it naturally
	// comes) and answer with an ordinary chat message. This also avoids
	// steering the model into a rigid confidence/exact-3-options template it
	// would otherwise keep reusing as its default style for the rest of the
	// conversation.
	Interactive bool
}

// EffortLevel is a provider-agnostic effort/reasoning level that each provider
// maps to its own CLI-specific naming. The canonical ordered set is
// low < medium < high < xhigh < max. Not every provider exposes all five;
// each provider's ModelInfo.EffortCapabilities advertises only the levels it
// supports. Utility sessions can request Low directly; pipeline profiles
// determine phase effort as Medium → Medium and Large/Moonshot → High.
type EffortLevel string

const (
	EffortLow    EffortLevel = "low"
	EffortMedium EffortLevel = "medium"
	EffortHigh   EffortLevel = "high"
	EffortXHigh  EffortLevel = "xhigh"
	EffortMax    EffortLevel = "max"
)

// EffortAuto is the configuration value that means "derive the effective effort
// from the active pipeline profile." It is never a member of
// EffortCapabilities on a ModelInfo; it is only used in persisted configuration
// and in the EffortSource enum to signal that the effective level was
// auto-derived rather than explicitly chosen.
const EffortAuto EffortLevel = "auto"

// EffortSource indicates whether the effective effort level was derived from
// the pipeline (Auto) or came from an explicit user configuration (Explicit).
type EffortSource string

const (
	EffortSourceAuto     EffortSource = "auto"
	EffortSourceExplicit EffortSource = "explicit"
)

// AllEffortLevels is the canonical ordered set of explicit effort levels from
// lowest to highest, excluding Auto. Providers use this to build
// EffortCapabilities slices and the resolver uses it for validation.
var AllEffortLevels = []EffortLevel{EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax}

// IsValidExplicitEffort reports whether level is one of the closed set
// auto|low|medium|high|xhigh|max. It accepts EffortAuto in addition to the five
// explicit levels.
func IsValidExplicitEffort(level EffortLevel) bool {
	switch level {
	case EffortAuto, EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax:
		return true
	}
	return false
}

// EffortCapabilitiesForModel returns the ordered effort capabilities for a
// model in the given provider's catalog. If the provider is nil or the model
// is not found, it returns nil (Auto-only).
func EffortCapabilitiesForModel(provider LLMProvider, model string) []EffortLevel {
	if provider == nil {
		return nil
	}
	cp, ok := provider.(CatalogProvider)
	if !ok {
		return nil
	}
	for _, entry := range cp.ModelCatalog() {
		if strings.EqualFold(entry.ID, model) {
			return entry.EffortCapabilities
		}
		for _, alias := range entry.Aliases {
			if strings.EqualFold(alias, model) {
				return entry.EffortCapabilities
			}
		}
	}
	return nil
}

// EffortCapabilitySupported reports whether level is in capabilities. EffortAuto
// is never in capabilities (it is always implicitly supported).
func EffortCapabilitySupported(capabilities []EffortLevel, level EffortLevel) bool {
	for _, cap := range capabilities {
		if cap == level {
			return true
		}
	}
	return false
}

// MapStandardEffortLevel maps a provider-agnostic EffortLevel to the CLI
// --effort/model_reasoning_effort value shared by Claude and Codex: low,
// medium, high, xhigh, max, defaulting to high for unrecognized levels.
func MapStandardEffortLevel(level EffortLevel) string {
	switch level {
	case EffortLow:
		return "low"
	case EffortMedium:
		return "medium"
	case EffortHigh:
		return "high"
	case EffortXHigh:
		return "xhigh"
	case EffortMax:
		return "max"
	default:
		return "high" // safe default
	}
}

// ErrNotSupported is returned when a provider doesn't support an operation.
var ErrNotSupported = fmt.Errorf("operation not supported by this provider")

// PhaseRole identifies which config phase a model default applies to.
type PhaseRole string

const (
	PhaseInquiry         PhaseRole = "inquiry"
	PhaseResearch        PhaseRole = "research"
	PhasePlanning        PhaseRole = "planning"
	PhaseImplementation  PhaseRole = "implementation"
	PhaseReview          PhaseRole = "review"
	PhaseChat            PhaseRole = "chat"
	PhaseKBBuild         PhaseRole = "kb_build"
	PhaseAutomaticReview PhaseRole = "automatic_review"
)

// ConfigFieldForRole maps a PhaseRole to the corresponding field name on
// config.ModelConfig and config.EffortConfig. PhaseChat maps to "Utilities"
// because the "chat" role uses the "utilities" model/effort field. Returns ""
// for unrecognized roles.
func ConfigFieldForRole(role PhaseRole) string {
	switch role {
	case PhaseInquiry:
		return "Inquiry"
	case PhaseResearch:
		return "Research"
	case PhasePlanning:
		return "Planning"
	case PhaseImplementation:
		return "Implementation"
	case PhaseReview:
		return "Review"
	case PhaseChat:
		return "Utilities"
	case PhaseKBBuild:
		return "KBBuild"
	}
	return ""
}
