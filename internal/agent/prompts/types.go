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

package prompts

// RepoView is the per-repository projection a template needs to render the
// "Target Repositories" list. Path is the resolved path (worktree path when
// available, falling back to the repo's checkout path).
type RepoView struct {
	Name string
	Path string
}

// KBView is the per-knowledge-base projection used by the system prompt's
// Useful Resources section.
// Name is the repo this KB belongs to; rendered as the bullet label so the
// agent can tell multi-repo KBs apart at a glance.
type KBView struct {
	Name      string
	IndexPath string
	RootDir   string
}

// SkillView is the per-skill projection used by the "Additional Skills" row
// in the system prompt. Name / Description / Topics come from the
// SKILL.md frontmatter; Path is the absolute path the agent should Read
// when its topics match the current task.
type SkillView struct {
	Name        string
	Description string
	Topics      string
	Path        string
}

// GuidelineView is the per-language projection used by the "Guidelines"
// subsection of the system prompt. Language is the display label
// (e.g. "Go", "Python"); IndexPath is the absolute path to the
// language's top-level index.md on disk.
type GuidelineView struct {
	Language  string
	IndexPath string
}

// OutputRootView is one named output directory rendered by the generic
// RoleSpec-backed system prompt.
type OutputRootView struct {
	Name        string
	Path        string
	Description string
}

// RoleSystemInput is the data passed to system.tmpl for RoleSpec-backed
// phase sessions.
//
// ReadOnlyOutsideRoots, when true, makes the rendered prompt assert that
// the OutputRoots are the ONLY locations the agent may write to — and that
// this role never writes code. It is the per-role plumbing for the
// RoleSpec field of the same name. Leave false for roles that legitimately
// modify source code (e.g. Implement).
type RoleSystemInput struct {
	OutputRoots          []OutputRootView
	MarkerPath           string
	SkillPath            string
	Preflight            PreflightInput
	ReadOnlyOutsideRoots bool

	AskingClause string
}

// PreflightInput is the data passed to the RoleSpec system template's
// "Useful Resources" section. It lists every orientation surface
// (knowledge bases, language guidelines, additional skills) the agent has
// access to for this assignment.
//
// HasKB / HasGuidelines / HasSkills gate their respective subsections; the
// section as a whole is suppressed when none of the three is enabled.
// Guidelines, when non-empty, expands into per-language bullets listing
// each language's index.md absolute path so the agent can browse the
// catalog without guessing where guidelines live.
type PreflightInput struct {
	KBInfos    []KBView
	Guidelines []GuidelineView
	Skills     []SkillView

	HasKB         bool
	HasGuidelines bool
	HasSkills     bool
}

// GrillMeInquirenessInput is the data passed to the grillme_inquireness
// partial. Level is retained for call-site compatibility, but the rendered
// prompt text is intentionally invariant across "none", "medium", and "high".
type GrillMeInquirenessInput struct {
	Level string
}

// AutonomousInquirenessInput is the data passed to the autonomous_inquireness
// partial. It is intentionally empty: revision loops run without a user in
// the loop, so there is no inquireness level to consult. The struct exists
// solely so the partial has a typed input that distinguishes its call sites
// from grill-me at the type level.
type AutonomousInquirenessInput struct{}

// VisualReferencesInput is the data passed to the visual_references partial.
// Label is the phase-specific verb woven into the imperative (e.g.
// "implementing this iteration", "producing the roadmap"). Returns "" when
// Images is empty.
type VisualReferencesInput struct {
	Images []string
	Label  string
}

// QAFilesInput is the data passed to the qa_files partial. Lead is the
// per-phase trailing sentence (e.g. "Read these Q&A files for important
// context..."). TrailingBlank controls whether a final blank line is
// appended (some legacy phases emit it, others don't).
type QAFilesInput struct {
	Paths         []string
	Lead          string
	TrailingBlank bool
}

// AxisApprovalView is the per-axis projection consumed by the
// prior_axis_approvals partial.
type AxisApprovalView struct {
	Axis           string
	FrozenSections []string
}

// PriorAxisApprovalsInput is the data passed to the prior_axis_approvals
// partial. ArtifactName is "roadmap" or "phase plan".
type PriorAxisApprovalsInput struct {
	ArtifactName string
	Approvals    []AxisApprovalView
}

// DeferralView is the per-deferral projection consumed by the deferrals_due
// partial. RedeferralCount is the precomputed count from feature.Deferral.RedeferralCount().
type DeferralView struct {
	ID              string
	Description     string
	CreatedInPhase  int
	CreatedInKind   string
	DueByPhase      int
	Reason          string
	RedeferralCount int
}

// DeferralsDueInput is the data passed to the deferrals_due partial. Kind
// is one of "plan", "implement", "review" — anything else suppresses the
// per-kind preface (the entries themselves still render).
type DeferralsDueInput struct {
	Phase   int
	Kind    string
	Entries []DeferralView
}

// SummaryUserInput is the data passed to summary.user.tmpl.
type SummaryUserInput struct {
	Name        string
	Description string
}

// PRDescriptionUserInput is the data passed to pr_description.user.tmpl.
// Empty fields suppress their corresponding sections so the model is not
// asked to reason about absent context.
type PRDescriptionUserInput struct {
	FeatureName        string
	FeatureDescription string
	Roadmap            string
	CommitBodies       string
	DiffStat           string
}

// ScoutUserInput is the data passed to scout.user.tmpl. Files carries
// pre-read contents (the scout consumes them inline rather than spawning
// exploration sub-agents).
type ScoutUserInput struct {
	Query string
	Files []ScoutFile
}

// ScoutFile is a per-file entry for the scout prompt.
type ScoutFile struct {
	Path    string
	Purpose string
	Content string
}
