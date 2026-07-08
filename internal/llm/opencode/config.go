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

package opencode

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// OpenCode permission/tool keys verified against the installed CLI. Unlisted
// keys default to allow, so every surface Agentico mediates is listed explicitly.
const (
	permKeyBash       = "bash"               // shell execution
	permKeyEdit       = "edit"               // file edits
	permKeyWrite      = "write"              // file writes
	permKeyApplyPatch = "apply_patch"        // patch application
	permKeyRead       = "read"               // file reads
	permKeyWebfetch   = "webfetch"           // web fetch
	permKeyWebsearch  = "websearch"          // web search
	permKeyTask       = "task"               // subagent / task tool
	permKeySkill      = "skill"              // skill invocation
	permKeyQuestion   = "question"           // user-facing questions
	permKeyExternal   = "external_directory" // access to paths outside the session cwd
)

// fileEditPermKeys are the file-mutating surfaces bounded to writable roots.
var fileEditPermKeys = []string{permKeyEdit, permKeyWrite, permKeyApplyPatch}

// mediatedToolPermKeys are the non-file tool surfaces routed through Agentico's
// permission flow (shell, web, subagent, skill). Each is set to the mode-derived
// decision ("ask" in normal mode, "allow" in dangerous-skip mode).
var mediatedToolPermKeys = []string{permKeyBash, permKeyWebfetch, permKeyWebsearch, permKeyTask, permKeySkill}

// catchAllPattern is the glob applied when no root pattern matches. OpenCode
// resolves a surface by last-matching rule (not most-specific; default "ask"),
// and Go marshals map keys sorted, so "*" precedes the "<root>/**" globs and a
// matching root glob — evaluated later — overrides it. The catch-all wins only
// when no root glob matches: an exact-file edit root never matches OpenCode's
// worktree-relative request path and collapses here (see delegateEdits). Its
// value differs by surface: "deny" for file edits (nothing writable outside
// writable roots) and "ask" for reads (external reads pause through Agentico).
const catchAllPattern = "*"

// permissionConfig returns the session-scoped OpenCode permission map keyed by
// real OpenCode tool surfaces.
//
//   - Mediated tool surfaces (shell, web fetch/search, subagent, skill) are set to
//     "ask" in normal mode so Agentico pauses for the user, or "allow" in
//     dangerous-skip mode for the same noninteractive surfaces other providers
//     allow.
//   - User questions always stay "ask" so an AskUserQuestion is never silently
//     auto-approved, even in dangerous-skip mode.
//   - Reads are bounded to mounted read roots (see readPermission): allowed
//     inside the roots Agentico mounted, asked outside them in normal mode so an
//     external-directory read pauses through Agentico, and noninteractive in
//     dangerous-skip mode. OpenCode is not OS-sandboxed, so this mediates rather
//     than enforces a boundary.
//   - External-directory access (see externalDirectoryPermission) is bounded to
//     the same mounted roots so paths outside the session cwd that Agentico
//     mounted stay reachable past OpenCode's separate external_directory gate.
//   - File-editing surfaces (edit/write/apply_patch) are bounded to writable
//     roots: when roots are known, each becomes a path-pattern map allowing the
//     mode decision inside "<root>/**" and explicitly denying everything else;
//     with no known roots they fall back to the mode decision as a plain value.
func permissionConfig(dangerouslySkipPerms bool, workDir string, writableRoots, readRoots []string) map[string]any {
	toolDecision := "ask"
	if dangerouslySkipPerms {
		toolDecision = "allow"
	}

	perm := make(map[string]any, len(mediatedToolPermKeys)+len(fileEditPermKeys)+2)
	for _, key := range mediatedToolPermKeys {
		perm[key] = toolDecision
	}
	perm[permKeyQuestion] = "ask"
	perm[permKeyRead] = readPermission(dangerouslySkipPerms, workDir, readRoots)
	perm[permKeyExternal] = externalDirectoryPermission(dangerouslySkipPerms, workDir, writableRoots, readRoots)

	editDecision := editPermission(toolDecision, workDir, writableRoots)
	for _, key := range fileEditPermKeys {
		perm[key] = editDecision
	}
	return perm
}

// readPermission returns the permission value for the read surface.
//
//   - In dangerous-skip mode reads are noninteractive everywhere ("allow"), the
//     same noninteractive surface other providers allow.
//   - In normal mode with known read roots it returns a path-pattern map allowing
//     reads inside each "<root>/**" and asking for everything else, so a read
//     outside the mounted roots pauses through Agentico while mounted roots
//     (state, work dir, skills, guidelines, worktrees, attachments) stay
//     readable.
//   - In normal mode with no known read roots it asks rather than silently
//     allowing arbitrary reads.
func readPermission(dangerouslySkipPerms bool, workDir string, readRoots []string) any {
	if dangerouslySkipPerms {
		return "allow"
	}
	roots := normalizeRoots(readRoots)
	if len(roots) == 0 {
		return "ask"
	}
	patterns := make(map[string]string, len(roots)+1)
	patterns[catchAllPattern] = "ask"
	for _, root := range roots {
		for _, glob := range rootGlobs(workDir, root) {
			patterns[glob] = "allow"
		}
	}
	return patterns
}

// externalDirectoryPermission returns the permission value for OpenCode's
// external_directory surface, the separate gate OpenCode applies to any path
// outside the session's working directory. OpenCode's built-in default asks for
// every external path (allowing only the cwd and tmp), so the read pattern map
// alone does not keep mounted roots readable: a root Agentico mounts outside the
// cwd — feature state, skills, guidelines, sibling worktrees — still trips this
// gate. Left unconfigured, that ask cannot be answered in a subagent session
// (OpenCode does not forward subagent permission requests over ACP) and stalls
// the run. Mirroring readPermission, every mounted root (read and writable) is
// allowed and everything else asks in normal mode, or is allowed in
// dangerous-skip mode.
func externalDirectoryPermission(dangerouslySkipPerms bool, workDir string, writableRoots, readRoots []string) any {
	if dangerouslySkipPerms {
		return "allow"
	}
	roots := normalizeRoots(append(append([]string(nil), readRoots...), writableRoots...))
	if len(roots) == 0 {
		return "ask"
	}
	patterns := make(map[string]string, len(roots)+1)
	patterns[catchAllPattern] = "ask"
	for _, root := range roots {
		for _, glob := range rootGlobs(workDir, root) {
			patterns[glob] = "allow"
		}
	}
	return patterns
}

// subagentPermissionConfig returns the non-interactive permission profile for a
// managed subagent session. OpenCode's ACP bridge silently drops permission
// requests originating from internally-spawned child sessions (its handler bails
// when the session is absent from the ACP client's session registry, which
// task-spawned children never join), so a subagent that reaches an "ask"
// decision blocks forever. This tracks OpenCode upstream issue #32388
// (https://github.com/anomalyco/opencode/issues/32388), still open; this
// deterministic profile is the workaround and can be revisited once OpenCode
// forwards child-session permission prompts to the ACP client. This profile
// therefore resolves every surface
// deterministically: surfaces Agentico already auto-approves (reads,
// external-directory access, web, and edits bounded to writable roots) are
// allowed; user questions are denied because a subagent cannot obtain a human
// answer; and human-gated surfaces (shell, skill invocation) are denied in normal
// mode so the subagent fails fast rather than hanging, or allowed under
// dangerous-skip mode where the whole run is already noninteractive.
//
// Subagent task-spawning is denied unconditionally — in every mode — to enforce
// depth-1 delegation: only the primary session may spawn subagents. This is a
// structural recursion limit, not a human-gating choice, so it holds even under
// dangerous-skip. Without it, a subagent can spawn subagents that spawn more,
// producing multiplicative fan-out: a glm-5p2 research session let
// web-search-researcher children recurse into thousands of grandchildren and
// exhausted memory.
func subagentPermissionConfig(dangerouslySkipPerms bool, workDir string, writableRoots []string) map[string]any {
	humanGated := "deny"
	if dangerouslySkipPerms {
		humanGated = "allow"
	}
	perm := map[string]any{
		permKeyRead:      "allow",
		permKeyExternal:  "allow",
		permKeyWebfetch:  "allow",
		permKeyWebsearch: "allow",
		permKeyTask:      "deny",
		permKeyQuestion:  "deny",
		permKeyBash:      humanGated,
		permKeySkill:     humanGated,
	}
	editDecision := editPermission("allow", workDir, writableRoots)
	for _, key := range fileEditPermKeys {
		perm[key] = editDecision
	}
	return perm
}

// editPermission returns the permission value for a file-mutating surface. With
// writable roots it returns a path-pattern map that allows the decision for
// each root itself and inside each "<root>/**", then denies everywhere else;
// with no roots it returns the bare decision so behavior is unchanged from a
// non-bounded session.
func editPermission(decision string, workDir string, writableRoots []string) any {
	roots := normalizeRoots(writableRoots)
	if len(roots) == 0 {
		return decision
	}
	patterns := make(map[string]string, len(roots)+1)
	patterns[catchAllPattern] = "deny"
	for _, root := range roots {
		for _, glob := range rootGlobs(workDir, root) {
			patterns[glob] = decision
		}
	}
	return patterns
}

// rootGlob turns a mounted root into the recursive child glob OpenCode matches
// tool paths (reads, edits) against.
func rootGlob(root string) string {
	root = rootPattern(root)
	if root == "/" {
		return "/**"
	}
	return root + "/**"
}

// rootPattern returns the exact path pattern for a mounted root. It preserves
// "/" while trimming trailing slashes from ordinary roots so exact file roots
// and their recursive child globs share the same canonical prefix.
func rootPattern(root string) string {
	root = strings.TrimRight(root, "/")
	if root == "" {
		return "/"
	}
	return root
}

// rootGlobs returns every tool-path pattern that should match a mounted root.
// OpenCode evaluates an edit against an inconsistent path form: cwd-relative
// ("../../../knowledge-base/<repo>/**") when it can relativize, but a
// leading-slash-STRIPPED absolute path ("Users/x/.../repo/file") when its
// git-worktree detection resolves to "/" — observed for both the implementation
// worktree and out-of-cwd helper artifacts. An absolute-only ("/Users/...") glob
// matches neither stripped nor relative forms, so the edit falls through to the
// catch-all deny. Emitting the absolute, the slash-stripped absolute, and (when
// workDir is known) the cwd-relative forms keeps a root matchable on every
// surface. The exact (non-/**) pattern matters for bounded helpers that pass file
// roots such as validation feedback artifacts and phase_complete; without it
// OpenCode only allows children of those files and denies the actual artifact
// write. An empty or non-relativizable workDir yields the absolute forms alone.
func rootGlobs(workDir, root string) []string {
	abs := rootPattern(root)
	globs := []string{abs, rootGlob(root)}
	if stripped := strings.TrimPrefix(abs, "/"); stripped != abs && stripped != "" {
		globs = append(globs, stripped, stripped+"/**")
	}
	if workDir == "" {
		return globs
	}
	rel, err := filepath.Rel(workDir, root)
	if err != nil || rel == "" || rel == "." {
		return globs
	}
	return append(globs, rootPattern(rel), rootGlob(rel))
}

// normalizeRoots trims, drops empties, and de-duplicates writable roots while
// preserving a deterministic (sorted) order for stable config output.
func normalizeRoots(roots []string) []string {
	seen := make(map[string]struct{}, len(roots))
	var out []string
	for _, r := range roots {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if _, ok := seen[r]; ok {
			continue
		}
		seen[r] = struct{}{}
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// agentJSONShape is the embedded-agent definition shape produced by
// agentdef.JSONForNames (agent name -> definition).
type agentJSONShape struct {
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
	Model       string `json:"model"`
}

// convertAgents converts Agentico's embedded-agent JSON into OpenCode managed
// agent definitions. Each agent becomes a subagent carrying its description and
// prompt; an agent's model is included only when it is already a valid OpenCode
// backend id, so an Agentico-internal model name (e.g. a category alias) is
// omitted rather than passed through as a bad value. Empty input yields no
// agents; malformed input is an error whose message never echoes the input.
//
// Every converted subagent carries a non-interactive permission override (see
// subagentPermissionConfig) because OpenCode's ACP bridge cannot forward a
// child session's permission request, so a subagent must never reach an "ask".
func convertAgents(agentsJSON string, dangerouslySkipPerms bool, workDir string, writableRoots []string) (map[string]managedAgent, error) {
	subPerm := subagentPermissionConfig(dangerouslySkipPerms, workDir, writableRoots)
	out := make(map[string]managedAgent)

	if s := strings.TrimSpace(agentsJSON); s != "" {
		var raw map[string]agentJSONShape
		if err := json.Unmarshal([]byte(s), &raw); err != nil {
			return nil, fmt.Errorf("invalid embedded-agent JSON")
		}
		for name, def := range raw {
			agent := managedAgent{
				Description: def.Description,
				Prompt:      def.Prompt,
				Mode:        "subagent",
				Permission:  subPerm,
			}
			// Include the model only when it is already a valid OpenCode backend id
			// in slash form; an Agentico-internal alias (e.g. "sonnet") is omitted so
			// the subagent inherits the session model rather than receiving a bad value.
			if _, _, ok := splitBackend(def.Model); ok && validateBackendModel(def.Model) == nil {
				agent.Model = def.Model
			}
			out[name] = agent
		}
	}

	// OpenCode's built-in subagents (general, explore) are spawnable via the task
	// tool but are absent from Agentico's converted set, so without an override
	// they inherit the session's top-level permission (bash=ask). A subagent that
	// reaches "ask" blocks forever — OpenCode's ACP bridge cannot forward a child
	// session's permission request (upstream issue #32388, open:
	// https://github.com/anomalyco/opencode/issues/32388) — so each built-in is
	// pinned to the same deterministic, non-interactive subagent profile
	// (bash/skill deny in normal mode) to fail fast instead of hanging. The
	// override carries only mode and permission, leaving the built-in's own
	// description/prompt intact through OpenCode's config merge. This workaround
	// becomes unnecessary once OpenCode resolves that issue.
	for _, name := range openCodeBuiltinSubagents {
		if _, ok := out[name]; ok {
			continue
		}
		out[name] = managedAgent{Mode: "subagent", Permission: subPerm}
	}

	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// openCodeBuiltinSubagents are OpenCode's built-in agents the model can spawn as
// child sessions via the task tool. They are not part of Agentico's converted
// agent set, so convertAgents pins them to the deterministic subagent permission
// profile; otherwise they inherit the top-level "ask" and hang, since a child
// session's permission request cannot be answered over ACP.
var openCodeBuiltinSubagents = []string{"general", "explore"}

// effortSupportedProviders names the OpenCode backend providers that expose a
// stable, documented reasoning-effort control ("reasoningEffort"). Effort is
// mapped only for these; any other backend leaves no effort key behind rather
// than guessing an unsupported control.
var effortSupportedProviders = map[string]bool{"openai": true}

// reasoningEffortFor maps an Agentico effort level to OpenCode's documented
// reasoningEffort value for backends that support it. The second result is false
// when the level is empty or the backend has no stable reasoning control, in
// which case no effort config is emitted.
func reasoningEffortFor(backend string, level llm.EffortLevel) (string, bool) {
	provider, _, ok := splitBackend(backend)
	if !ok || !effortSupportedProviders[provider] {
		return "", false
	}
	switch level {
	case llm.EffortLow:
		return "low", true
	case llm.EffortMedium:
		return "medium", true
	case llm.EffortHigh:
		return "high", true
	case llm.EffortMax:
		// OpenAI reasoning effort tops out at "high"; map the highest Agentico
		// level to it rather than inventing an unsupported value.
		return "high", true
	default:
		return "", false
	}
}

// applyEffort sets the model-scoped reasoningEffort option for the selected
// backend when (and only when) OpenCode exposes a stable reasoning control for
// it. The option is added under the specific provider/model so it merges
// additively over the user's provider config without clobbering credentials.
func applyEffort(cfg *managedConfig, backend string, level llm.EffortLevel) {
	value, ok := reasoningEffortFor(backend, level)
	if !ok {
		return
	}
	provider, model, hasModel := splitBackend(backend)
	if !hasModel {
		return
	}
	if cfg.Provider == nil {
		cfg.Provider = make(map[string]any, 1)
	}
	cfg.Provider[provider] = map[string]any{
		"models": map[string]any{
			model: map[string]any{
				"options": map[string]any{
					"reasoningEffort": value,
				},
			},
		},
	}
}

// splitBackend splits a "provider/model" backend id into its provider and model
// parts. ok is false when the id is not in slash form.
func splitBackend(backend string) (provider, model string, ok bool) {
	idx := strings.Index(backend, "/")
	if idx <= 0 || idx == len(backend)-1 {
		return "", "", false
	}
	return backend[:idx], backend[idx+1:], true
}

// isolationFeature is an inherited-surface isolation environment flag together
// with the OpenCode version that introduced support for it and a short feature
// name used in fallback diagnostics.
type isolationFeature struct {
	env   string
	name  string
	since [3]int
}

// isolationFeatures lists the environment flags that scrub user-global OpenCode
// tools, plugins, Claude-compatibility surfaces, global skills, and
// noninteractive runtime behavior so they cannot bypass the managed session
// contract. Each is gated on the OpenCode version that introduced it; all are
// available at or below the provider's enforced MinVersion, so a launched session
// (which is guaranteed to meet MinVersion) always receives the full set. The
// version gate exists so a future flag introduced above MinVersion is omitted —
// and reported — on an older-but-still-supported CLI rather than emitted blindly.
var isolationFeatures = []isolationFeature{
	{"OPENCODE_PURE=1", "external-plugin isolation (--pure)", [3]int{1, 0, 0}},
	{"OPENCODE_DISABLE_DEFAULT_PLUGINS=1", "default-plugin isolation", [3]int{1, 0, 0}},
	{"OPENCODE_DISABLE_PROJECT_CONFIG=1", "project-config isolation", [3]int{1, 0, 0}},
	{"OPENCODE_DISABLE_CLAUDE_CODE=1", "Claude-compatibility isolation", [3]int{1, 0, 0}},
	{"OPENCODE_DISABLE_CLAUDE_CODE_PROMPT=1", "Claude-compatibility prompt isolation", [3]int{1, 0, 0}},
	{"OPENCODE_DISABLE_CLAUDE_CODE_SKILLS=1", "Claude-compatibility skills isolation", [3]int{1, 0, 0}},
	{"OPENCODE_DISABLE_EXTERNAL_SKILLS=1", "global/external-skills isolation", [3]int{1, 0, 0}},
	{"OPENCODE_DISABLE_AUTOUPDATE=1", "autoupdate suppression", [3]int{1, 0, 0}},
	{"OPENCODE_DISABLE_SHARE=1", "transcript-share suppression", [3]int{1, 0, 0}},
}

// buildIsolationEnv returns the isolation environment flags supported by the
// installed OpenCode version plus the names of any requested isolation features
// that the installed version does not support (so callers can report the gap and
// fall back to permission mediation rather than pretend isolation occurred).
func buildIsolationEnv(installed [3]int) (env, unavailable []string) {
	return isolationEnvFrom(isolationFeatures, installed)
}

// isolationEnvFrom is the version-gating core, parameterized over the feature
// list so it can be exercised with synthetic features in tests.
func isolationEnvFrom(features []isolationFeature, installed [3]int) (env, unavailable []string) {
	for _, f := range features {
		if versionAtLeast(installed, f.since) {
			env = append(env, f.env)
		} else {
			unavailable = append(unavailable, f.name)
		}
	}
	return env, unavailable
}

// isolationFallbackDiagnostic renders a redacted, actionable message naming the
// isolation features unavailable on the installed OpenCode and stating that the
// session falls back to permission mediation. It carries no config or secret
// content.
func isolationFallbackDiagnostic(unavailable []string) string {
	if len(unavailable) == 0 {
		return ""
	}
	return sanitizeDiagnostic(fmt.Sprintf(
		"OpenCode isolation features unavailable on the installed CLI: %s; "+
			"falling back to permission mediation for these surfaces",
		strings.Join(unavailable, ", "),
	))
}

// minSupportedVersion is the enforced MinVersion floor every launched OpenCode
// session is guaranteed to meet (the provider is filtered out of routing below
// it), used as the version basis for isolation-feature gating.
func minSupportedVersion() [3]int {
	return (&Provider{}).MinVersion()
}

// versionAtLeast reports whether got is greater than or equal to min, comparing
// major, then minor, then patch.
func versionAtLeast(got, min [3]int) bool {
	for i := 0; i < 3; i++ {
		if got[i] != min[i] {
			return got[i] > min[i]
		}
	}
	return true
}
