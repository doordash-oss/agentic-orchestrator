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
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent/prompts"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/guidelinedef"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/skilldef"
	"github.com/doordash-oss/agentic-orchestrator/internal/utilskill"
)

// ContextReadTracker holds the directories and observer needed to track
// reads of KB, skill, and guideline files during agent sessions.
type ContextReadTracker struct {
	KBBaseDir     string
	SkillsDir     string
	GuidelinesDir string
	Observer      *observe.Observer
}

// Install sets an onToolAllowed callback on the session that emits
// context.file_read events when the agent reads KB, skill, or guideline files.
func (t *ContextReadTracker) Install(sess interface {
	SetOnToolAllowed(func(string, json.RawMessage))
	SetOnFileRead(func(llm.FileReadEvent))
}, sc observe.SpanContext, phase, sessionID string) {
	if t == nil || t.Observer == nil {
		return
	}
	emitRead := func(read llm.FileReadEvent) {
		if read.FilePath == "" {
			return
		}
		category, ok := ClassifyContextRead(read.FilePath, t.KBBaseDir, t.SkillsDir, t.GuidelinesDir)
		if !ok {
			return
		}
		meta := observe.ContextFileReadMeta{
			Source:         read.Source,
			ProviderItemID: read.ProviderItemID,
			ExitCode:       read.ExitCode,
		}
		t.Observer.ContextFileRead(sc, phase, sessionID, category, read.FilePath, meta)
	}
	sess.SetOnToolAllowed(func(toolName string, input json.RawMessage) {
		if toolName != "Read" {
			return
		}
		var readInput struct {
			FilePath string `json:"file_path"`
		}
		if err := json.Unmarshal(input, &readInput); err != nil || readInput.FilePath == "" {
			return
		}
		emitRead(llm.FileReadEvent{FilePath: readInput.FilePath, Source: "tool.read"})
	})
	sess.SetOnFileRead(emitRead)
}

// installContextReadTracker is a convenience method on PhaseRunner that creates
// a ContextReadTracker and installs it on the session.
func (pr *PhaseRunner) installContextReadTracker(sess interface {
	SetOnToolAllowed(func(string, json.RawMessage))
	SetOnFileRead(func(llm.FileReadEvent))
}, sc observe.SpanContext, phase, sessionID, stateDir string) {
	tracker := &ContextReadTracker{
		KBBaseDir:     filepath.Join(filepath.Dir(stateDir), "knowledge-base"),
		SkillsDir:     pr.SkillsDir,
		GuidelinesDir: pr.GuidelinesDir,
		Observer:      pr.Observer,
	}
	tracker.Install(sess, sc, phase, sessionID)
}

// SubagentProgressTracker forwards subagent (Task tool) progress and
// terminal messages from a session to the Observer so they land in
// events.jsonl. Without this, events.jsonl goes silent while the main
// agent is blocked inside a long Task() call and the feature appears stuck.
type SubagentProgressTracker struct {
	Observer  *observe.Observer
	SC        observe.SpanContext
	Phase     string
	SessionID string
}

// Install registers an onSubagentEvent callback on the session that maps
// incoming SDKMessage task_progress / task_notification envelopes to
// Observer.AgentTaskProgress / AgentTaskEnded. No-op on a nil tracker or
// tracker with a nil Observer.
func (t *SubagentProgressTracker) Install(sess interface {
	SetOnSubagentEvent(func(llm.SDKMessage))
}) {
	if t == nil || t.Observer == nil {
		return
	}
	sess.SetOnSubagentEvent(func(msg llm.SDKMessage) {
		if ts := msg.TaskStarted; ts != nil {
			t.Observer.AgentTaskStarted(t.SC, t.Phase, t.SessionID,
				ts.TaskID, ts.ToolUseID, ts.Description, ts.TaskType)
			return
		}
		if tp := msg.TaskProgress; tp != nil {
			totalTokens, toolUses, durationMs := taskUsageFields(tp.Usage)
			t.Observer.AgentTaskProgress(t.SC, t.Phase, t.SessionID,
				tp.TaskID, tp.ToolUseID, tp.Description, tp.LastToolName,
				totalTokens, toolUses, durationMs)
			return
		}
		if tn := msg.TaskNotification; tn != nil {
			totalTokens, toolUses, durationMs := taskUsageFields(tn.Usage)
			t.Observer.AgentTaskEnded(t.SC, t.Phase, t.SessionID,
				tn.TaskID, tn.ToolUseID, tn.Status, tn.Summary,
				totalTokens, toolUses, durationMs)
		}
	})
}

// taskUsageFields unpacks a *llm.TaskUsage into its three numeric fields,
// returning zeros when the payload is nil.
func taskUsageFields(u *llm.TaskUsage) (totalTokens, toolUses int, durationMs int64) {
	if u == nil {
		return 0, 0, 0
	}
	return u.TotalTokens, u.ToolUses, u.DurationMs
}

// installSubagentProgressTracker is a convenience method on PhaseRunner that
// creates a SubagentProgressTracker and installs it on the session alongside
// the context-read tracker.
func (pr *PhaseRunner) installSubagentProgressTracker(sess interface {
	SetOnSubagentEvent(func(llm.SDKMessage))
}, sc observe.SpanContext, phase, sessionID string) {
	tracker := &SubagentProgressTracker{
		Observer:  pr.Observer,
		SC:        sc,
		Phase:     phase,
		SessionID: sessionID,
	}
	tracker.Install(sess)
}

// ClassifyContextRead determines if a file path is a KB, skill, or guideline
// file. Returns the category ("kb", "skill", "guideline") and true, or "" and
// false if the path doesn't match any context directory.
func ClassifyContextRead(filePath, kbBaseDir, skillsDir, guidelinesDir string) (category string, ok bool) {
	if kbBaseDir != "" && strings.HasPrefix(filePath, kbBaseDir) {
		return "kb", true
	}
	if skillsDir != "" && strings.HasPrefix(filePath, skillsDir) {
		return "skill", true
	}
	if guidelinesDir != "" && strings.HasPrefix(filePath, guidelinesDir) {
		return "guideline", true
	}
	return "", false
}

// buildPreflightInput projects KBInfo / phase / skillsDir / guidelinesDir
// into the typed PreflightInput consumed by the RoleSpec system prompt.
// Centralized so all phase-specific prompt builders share one source of
// truth for which orientation surfaces fire and how Skills / Guidelines
// are resolved.
func buildPreflightInput(phase feature.Phase, skillsDir string, kbInfos []KBInfo, guidelinesDir string, requiredSkillNames ...string) prompts.PreflightInput {
	kbViews := make([]prompts.KBView, 0, len(kbInfos))
	for _, kb := range kbInfos {
		kbViews = append(kbViews, prompts.KBView{Name: kb.Name, IndexPath: kb.IndexPath, RootDir: kb.RootDir})
	}
	skillViews := resolveAdditionalSkills(phase, skillsDir, requiredSkillNames...)
	guidelineViews := resolveGuidelineViews(guidelinesDir)
	return prompts.PreflightInput{
		KBInfos:       kbViews,
		Guidelines:    guidelineViews,
		Skills:        skillViews,
		HasKB:         len(kbViews) > 0,
		HasGuidelines: len(guidelineViews) > 0,
		HasSkills:     len(skillViews) > 0,
	}
}

// resolveGuidelineViews returns the GuidelineView rows that populate the
// "Guidelines" subsection of the system prompt. Returns nil when guidelinesDir
// is empty or no embedded guidelines are available, in which case the
// subsection is suppressed entirely.
//
// Listing every embedded language (rather than a single root path) means
// the agent does not have to guess directory names — each language's
// top-level index.md is presented as an absolute path the agent can Read
// directly when the language is relevant to the target repository.
func resolveGuidelineViews(guidelinesDir string) []prompts.GuidelineView {
	if guidelinesDir == "" {
		return nil
	}
	defs, err := guidelinedef.ParseEmbedded()
	if err != nil || len(defs) == 0 {
		return nil
	}
	names := make([]string, 0, len(defs))
	for name := range defs {
		names = append(names, name)
	}
	sort.Strings(names)
	views := make([]prompts.GuidelineView, 0, len(names))
	for _, name := range names {
		def := defs[name]
		views = append(views, prompts.GuidelineView{
			Language:  def.Language,
			IndexPath: filepath.Join(guidelinesDir, name, "index.md"),
		})
	}
	return views
}

// resolveAdditionalSkills returns the SkillView rows that should populate
// the RoleSpec system prompt's "Additional Skills" table for this phase,
// excluding skills that the caller promotes to mandatory guidance.
// Returns nil when skillsDir is empty (no skill paths to advertise) or
// when the phase has no registered utility skills.
func resolveAdditionalSkills(phase feature.Phase, skillsDir string, excludedNames ...string) []prompts.SkillView {
	if skillsDir == "" {
		return nil
	}
	names := utilskill.ForPhase(phase)
	if len(excludedNames) > 0 {
		excluded := make(map[string]struct{}, len(excludedNames))
		for _, name := range excludedNames {
			excluded[name] = struct{}{}
		}
		filtered := names[:0]
		for _, name := range names {
			if _, skip := excluded[name]; !skip {
				filtered = append(filtered, name)
			}
		}
		names = filtered
	}
	return resolveSkillViews(names, skillsDir)
}

// resolveSkillViews resolves named embedded skills into prompt rows.
func resolveSkillViews(names []string, skillsDir string) []prompts.SkillView {
	if skillsDir == "" || len(names) == 0 {
		return nil
	}
	defs, err := skilldef.ParseEmbedded()
	if err != nil {
		return nil
	}
	views := make([]prompts.SkillView, 0, len(names))
	for _, name := range names {
		def, ok := defs[name]
		if !ok {
			continue
		}
		topics := def.Topics
		if topics == "" {
			topics = "—"
		}
		views = append(views, prompts.SkillView{
			Name:        def.Name,
			Description: def.Description,
			Topics:      topics,
			Path:        filepath.Join(skillsDir, name, "SKILL.md"),
		})
	}
	if len(views) == 0 {
		return nil
	}
	return views
}
