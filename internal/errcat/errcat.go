// Copyright 2026 DoorDash, Inc.

// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package errcat owns the canonical error contract: a code catalog that
// authors every human-readable error string, the three-class severity enum,
// and the typed context blocks a code may attach. It is a leaf package and
// imports nothing outside the standard library so every layer can share it.
package errcat

import (
	"sort"
	"strings"
)

// Class is the severity treatment class of an error code.
type Class string

const (
	// ClassBlocking marks errors that stopped the request or run.
	ClassBlocking Class = "blocking"
	// ClassNeedsAction marks fix-then-retry preconditions: the request is
	// parked until the user acts.
	ClassNeedsAction Class = "needs_action"
	// ClassWarning marks benign no-op rejections.
	ClassWarning Class = "warning"
)

// Valid reports whether c is a known class value.
func (c Class) Valid() bool {
	switch c {
	case ClassBlocking, ClassNeedsAction, ClassWarning:
		return true
	default:
		return false
	}
}

// Code is a stable snake_case catalog code.
type Code string

// CodeRepository is the typed context block for git repositories a code may
// reference. Name is required; every other field is optional.
type CodeRepository struct {
	Name            string   `json:"name" yaml:"name"`
	Branch          string   `json:"branch,omitempty" yaml:"branch,omitempty"`
	ConflictFiles   []string `json:"conflict_files,omitempty" yaml:"conflict_files,omitempty"`
	DirtyFiles      []string `json:"dirty_files,omitempty" yaml:"dirty_files,omitempty"`
	ParentAnchorSHA string   `json:"parent_anchor_sha,omitempty" yaml:"parent_anchor_sha,omitempty"`
	ExpectedRefSHA  string   `json:"expected_ref_sha,omitempty" yaml:"expected_ref_sha,omitempty"`
	ChildHeadSHA    string   `json:"child_head_sha,omitempty" yaml:"child_head_sha,omitempty"`
	CandidateSHA    string   `json:"candidate_sha,omitempty" yaml:"candidate_sha,omitempty"`
	MergeHEAD       string   `json:"merge_head,omitempty" yaml:"merge_head,omitempty"`
	ObservedSHA     string   `json:"observed_sha,omitempty" yaml:"observed_sha,omitempty"`
}

// CodePhase is the typed context block for the phase a code references.
type CodePhase struct {
	Name      string `json:"name" yaml:"name"`
	Iteration int    `json:"iteration,omitempty" yaml:"iteration,omitempty"`
}

// CodeCommand is the typed context block for a failed command.
type CodeCommand struct {
	ExitCode int      `json:"exit_code,omitempty" yaml:"exit_code,omitempty"`
	LogPaths []string `json:"log_paths,omitempty" yaml:"log_paths,omitempty"`
}

// CodeSetupTask is the typed context block naming the setup task that owns
// a setup failure. A run-level setup-failure record carries it as its only
// context; task-level records never do (the task is itself the owner).
type CodeSetupTask struct {
	Key   string `json:"key" yaml:"key"`
	Kind  string `json:"kind" yaml:"kind"`
	Label string `json:"label" yaml:"label"`
}

// Remediation is the catalog-authored next step for an error code.
type Remediation struct {
	Hint    string   `json:"hint,omitempty"`
	Actions []string `json:"actions,omitempty"`
}

// Context carries the typed context blocks attached to a rendered error.
// Only blocks declared by the code's catalog entry survive rendering.
type Context struct {
	Repositories []CodeRepository `json:"repositories,omitempty"`
	Phase        *CodePhase       `json:"phase,omitempty"`
	Command      *CodeCommand     `json:"command,omitempty"`
	SetupTask    *CodeSetupTask   `json:"setup_task,omitempty"`
}

// Error is the rendered canonical error value. Title and Summary are always
// nonempty catalog-authored text; Diagnostics is raw, deepest-disclosure
// detail and may be empty.
type Error struct {
	Code        Code         `json:"code"`
	Class       Class        `json:"class"`
	Title       string       `json:"title"`
	Summary     string       `json:"summary"`
	Remediation *Remediation `json:"remediation,omitempty"`
	Context     *Context     `json:"context,omitempty"`
	Diagnostics string       `json:"diagnostics,omitempty"`
}

// Block names a context block kind a code may populate.
type Block string

const (
	BlockRepositories Block = "repositories"
	BlockPhase        Block = "phase"
	BlockCommand      Block = "command"
	BlockSetupTask    Block = "setup_task"
)

// Params is a marker interface implemented by the small per-code parameter
// sets a summary template interpolates. A summary template receives the
// caller's params and must fall back to its static summary when the params
// are absent, zero, or of a different code's type.
type Params interface {
	params()
}

// Entry is one catalog entry: the authored contract for a code.
type Entry struct {
	// Class is the authored severity class.
	Class Class
	// Title is the authored, always-nonempty title (no trailing period,
	// no IDs or file lists).
	Title string
	// Summary is the authored static summary used when no template params
	// apply.
	Summary string
	// Remediation is the authored remediation hint; empty means none.
	Remediation string
	// Actions are remediation action references as plain action IDs. They
	// must equal values of the OpenAPI feature-action enum.
	Actions []string
	// Blocks are the context blocks this code may populate. Blocks the
	// caller attaches that the entry does not declare are dropped.
	Blocks []Block

	summaryParams func(Params) string
}

// Lookup returns the catalog entry for code.
func Lookup(code Code) (Entry, bool) {
	entry, ok := catalog[code]
	return entry, ok
}

// Codes returns every catalog code, sorted.
func Codes() []Code {
	codes := make([]Code, 0, len(catalog))
	for code := range catalog {
		codes = append(codes, code)
	}
	sort.Slice(codes, func(i, j int) bool { return codes[i] < codes[j] })
	return codes
}

const (
	fallbackTitle   = "Internal error"
	fallbackSummary = "The server could not complete the request."
)

type config struct {
	params       Params
	diagnostics  string
	repositories []CodeRepository
	phase        *CodePhase
	command      *CodeCommand
	setupTask    *CodeSetupTask
}

// Option customizes a rendered error.
type Option func(*config)

// WithParams supplies the code's typed summary parameters.
func WithParams(params Params) Option {
	return func(c *config) { c.params = params }
}

// WithDiagnostics attaches raw, deepest-disclosure detail text.
func WithDiagnostics(text string) Option {
	return func(c *config) { c.diagnostics = strings.TrimSpace(text) }
}

// WithRepositories attaches repositories context. Blocks the code did not
// declare are dropped at render time.
func WithRepositories(repos ...CodeRepository) Option {
	return func(c *config) { c.repositories = append(c.repositories, repos...) }
}

// WithPhase attaches phase context.
func WithPhase(phase CodePhase) Option {
	return func(c *config) { c.phase = &phase }
}

// WithCommand attaches command context.
func WithCommand(command CodeCommand) Option {
	return func(c *config) { c.command = &command }
}

// WithSetupTask attaches the setup-task context block naming the task that
// owns a setup failure.
func WithSetupTask(task CodeSetupTask) Option {
	return func(c *config) { c.setupTask = &task }
}

// New renders the canonical error for code. An unknown code resolves to the
// fallback internal-error code. Title and summary are never empty, and only
// context blocks the code declared survive.
func New(code Code, opts ...Option) Error {
	cfg := config{}
	for _, opt := range opts {
		opt(&cfg)
	}
	entry, ok := catalog[code]
	if !ok {
		code = InternalError
		entry = catalog[code]
	}
	title := strings.TrimSpace(entry.Title)
	if title == "" {
		title = fallbackTitle
	}
	summary := strings.TrimSpace(entry.Summary)
	if entry.summaryParams != nil {
		if rendered := strings.TrimSpace(entry.summaryParams(cfg.params)); rendered != "" {
			summary = rendered
		}
	}
	if summary == "" {
		summary = fallbackSummary
	}
	rendered := Error{
		Code:        code,
		Class:       entry.Class,
		Title:       title,
		Summary:     summary,
		Diagnostics: cfg.diagnostics,
	}
	if entry.Remediation != "" || len(entry.Actions) > 0 {
		rendered.Remediation = &Remediation{Hint: entry.Remediation, Actions: entry.Actions}
	}
	declared := make(map[Block]bool, len(entry.Blocks))
	for _, block := range entry.Blocks {
		declared[block] = true
	}
	var ctx Context
	if declared[BlockRepositories] && len(cfg.repositories) > 0 {
		ctx.Repositories = cfg.repositories
	}
	if declared[BlockPhase] && cfg.phase != nil {
		ctx.Phase = cfg.phase
	}
	if declared[BlockCommand] && cfg.command != nil {
		ctx.Command = cfg.command
	}
	if declared[BlockSetupTask] && cfg.setupTask != nil {
		ctx.SetupTask = cfg.setupTask
	}
	if ctx.Repositories != nil || ctx.Phase != nil || ctx.Command != nil || ctx.SetupTask != nil {
		rendered.Context = &ctx
	}
	return rendered
}
