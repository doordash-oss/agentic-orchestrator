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

package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
)

type attentionKind int

const (
	attentionNone attentionKind = iota
	attentionPermission
	attentionAskUser
	attentionNeedUserInput
	attentionReview
	attentionWatch
)

const ctaLabelAnswer = "Answer"

// toolNameAskUserQuestion is the ToolName reported on control requests and
// tool-use blocks for the built-in AskUserQuestion tool.
const toolNameAskUserQuestion = "AskUserQuestion"

// toolNameBash is the ToolName reported for shell command execution.
const toolNameBash = "Bash"

// toolNameAgent is the ToolName reported for sub-agent delegation.
const toolNameAgent = "Agent"

// toolNameRead is the ToolName reported for file-read operations.
const toolNameRead = "Read"

// toolNameEdit is the ToolName reported for file-edit operations.
const toolNameEdit = "Edit"

// toolNameWrite is the ToolName reported for file-write operations.
const toolNameWrite = "Write"

// toolNameGlob is the ToolName reported for filesystem glob lookups.
const toolNameGlob = "Glob"

// toolNameGrep is the ToolName reported for content search operations.
const toolNameGrep = "Grep"

// toolNameMultiEdit is the ToolName reported for multi-region file edits.
const toolNameMultiEdit = "MultiEdit"

// toolNameTaskCreate is the ToolName reported for sub-task creation.
const toolNameTaskCreate = "TaskCreate"

// roleAssistant is the message Role/Type reported for assistant-authored
// SDK messages.
const roleAssistant = "assistant"

// msgTypeControlRequest is the SDKMessage.Type reported for control-request
// (permission/AskUser/hook) envelopes.
const msgTypeControlRequest = "control_request"

const attentionTypeLabelPermission = "Permission Request"
const attentionTypeLabelQuestion = "Question"

type featureAttention struct {
	Kind       attentionKind
	CTALabel   string
	TypeLabel  string
	Summary    string
	RepoName   string
	GatePath   string
	CycleType  feature.RepoCycleType
	ReviewMode string
}

func computeFeatureAttention(f *feature.Feature, sess session.SessionView) featureAttention {
	if f == nil {
		return featureAttention{Kind: attentionNone}
	}
	if f.Status.IsNeedsReview() {
		return featureAttention{
			Kind:       attentionReview,
			CTALabel:   "Review",
			TypeLabel:  "Review Required",
			Summary:    reviewAttentionSummary(f),
			ReviewMode: reviewAttentionMode(f),
		}
	}
	if featureCanSurfacePromptQueues(f) {
		if summary, ok := pendingPermissionSummary(f, sess); ok {
			return featureAttention{
				Kind:      attentionPermission,
				CTALabel:  "Approve",
				TypeLabel: attentionTypeLabelPermission,
				Summary:   summary,
			}
		}
		if summary, ok := pendingAskUserSummary(f, sess); ok {
			return featureAttention{
				Kind:      attentionAskUser,
				CTALabel:  ctaLabelAnswer,
				TypeLabel: attentionTypeLabelQuestion,
				Summary:   summary,
			}
		}
	}
	if f.Status == feature.StatusNeedUserInput {
		return featureAttention{
			Kind:      attentionNeedUserInput,
			CTALabel:  ctaLabelAnswer,
			TypeLabel: "Input Required",
			Summary:   "Feature-level input gate",
			GatePath:  f.PendingNeedUserInputPath,
		}
	}
	if cycles := f.PendingUserInputCycles(); len(cycles) > 0 {
		cycle := cycles[0]
		return featureAttention{
			Kind:      attentionNeedUserInput,
			CTALabel:  ctaLabelAnswer,
			TypeLabel: "Input Required",
			Summary:   fmt.Sprintf("%s input gate for %s", cycle.CycleType, cycle.RepoName),
			RepoName:  cycle.RepoName,
			GatePath:  cycle.GatePath,
			CycleType: cycle.CycleType,
		}
	}
	if isWatchAttentionEligible(f) {
		return featureAttention{
			Kind:      attentionWatch,
			CTALabel:  "Watch",
			TypeLabel: "Live Preview",
			Summary:   "Watch active work",
		}
	}
	return featureAttention{Kind: attentionNone}
}

func featureCanSurfacePromptQueues(f *feature.Feature) bool {
	if f == nil {
		return false
	}
	return f.Status != feature.StatusInterrupted
}

func (a featureAttention) HasCTA() bool {
	return a.Kind != attentionNone
}

func (a featureAttention) RequiresUser() bool {
	switch a.Kind {
	case attentionPermission, attentionAskUser, attentionNeedUserInput, attentionReview:
		return true
	default:
		return false
	}
}

func (a featureAttention) FooterHint() string {
	if !a.HasCTA() || a.CTALabel == "" {
		return ""
	}
	return "[a] " + a.CTALabel
}

// InBoxCTA returns the prominent in-box call-to-action phrase, e.g.
// "press [a] to Answer". Footer hints stay terse via FooterHint(); this
// verbose form is reserved for the Live Preview attention box, where there
// is room for an inviting phrasing.
func (a featureAttention) InBoxCTA() string {
	if !a.HasCTA() || a.CTALabel == "" {
		return ""
	}
	return "press [a] to " + a.CTALabel
}

// IsQuestionTone reports whether the attention represents a user-input
// request — a question from the agent or an input gate. These get an
// info-blue, friendlier presentation rather than the warning-yellow
// reserved for permissions and reviews.
func (a featureAttention) IsQuestionTone() bool {
	return a.Kind == attentionAskUser || a.Kind == attentionNeedUserInput
}

func (a featureAttention) ActivityLine() string {
	switch a.Kind {
	case attentionPermission:
		return "Waiting for approval..."
	case attentionAskUser, attentionNeedUserInput:
		return "Waiting for an answer..."
	case attentionReview:
		return "Waiting for review..."
	default:
		return ""
	}
}

func contextualAActionHint(f *feature.Feature) (hint string, lead bool) {
	return contextualAActionHintFor(f, nil)
}

func contextualAActionHintFor(f *feature.Feature, sess session.SessionView) (hint string, lead bool) {
	att := computeFeatureAttention(f, sess)
	return att.FooterHint(), att.RequiresUser()
}

func featureNeedsUserAttention(f *feature.Feature) bool {
	return computeFeatureAttention(f, nil).RequiresUser()
}

func awaitingUserGlyph() string {
	return WarningStyle.Render("!")
}

func isWatchAttentionEligible(f *feature.Feature) bool {
	if f == nil {
		return false
	}
	if f.Status == feature.StatusCreated {
		return true
	}
	if f.Status == feature.StatusSettingUpWorktrees {
		return false
	}
	if f.Status.IsRunning() {
		return true
	}
	if f.Status == feature.StatusPublished || f.Status == feature.StatusCodeReady {
		return featureHasRunningCycle(f)
	}
	return false
}

func pendingPermissionSummary(f *feature.Feature, sess session.SessionView) (string, bool) {
	if cr := firstPendingPermissionControlRequest(sess); cr != nil {
		return permissionControlRequestSummary(cr), true
	}
	if sess != nil && sess.Status() == session.SessionWaitingPermission {
		return "Permission request", true
	}
	if f != nil {
		for _, p := range f.PermissionsQueue {
			if !p.Pending {
				continue
			}
			return permissionRequestSummary(p), true
		}
	}
	return "", false
}

func pendingAskUserSummary(f *feature.Feature, sess session.SessionView) (string, bool) {
	if cr := firstPendingAskUserControlRequest(sess); cr != nil {
		return askUserControlRequestSummary(cr), true
	}
	if sess != nil && (sess.Status() == session.SessionWaitingHelp || sess.HasPendingAskUserQuestion()) {
		return "Agent has a question", true
	}
	if f != nil {
		for _, h := range f.HelpQueue {
			if h.Pending {
				return singleLine(normalizeManagedHelpQuestion(h.Question)), true
			}
		}
	}
	return "", false
}

func firstPendingPermissionControlRequest(sess session.SessionView) *llm.ControlRequestMessage {
	return firstPendingControlRequest(sess, func(cr *llm.ControlRequestMessage) bool {
		return cr.Request.ToolName != "" && cr.Request.ToolName != toolNameAskUserQuestion
	})
}

func firstPendingAskUserControlRequest(sess session.SessionView) *llm.ControlRequestMessage {
	return firstPendingControlRequest(sess, func(cr *llm.ControlRequestMessage) bool {
		return cr.Request.ToolName == toolNameAskUserQuestion
	})
}

func firstPendingControlRequest(sess session.SessionView, match func(*llm.ControlRequestMessage) bool) *llm.ControlRequestMessage {
	if sess == nil {
		return nil
	}
	for _, cr := range sess.PendingControlRequests() {
		if cr != nil && match(cr) {
			return cr
		}
	}
	if cr := sess.LastControlRequest(); cr != nil && match(cr) {
		return cr
	}
	return nil
}

func permissionControlRequestSummary(cr *llm.ControlRequestMessage) string {
	if cr == nil {
		return "Permission request"
	}
	tool := cr.Request.ToolName
	if tool == "" {
		tool = "Tool"
	}
	if detail := livePreviewToolInputSummary(tool, cr.Request.Input); detail != "" {
		return tool + ": " + detail
	}
	return tool
}

func askUserControlRequestSummary(cr *llm.ControlRequestMessage) string {
	if cr == nil {
		return "Agent has a question"
	}
	if summary := livePreviewAskUserSummary(cr.Request.Input); summary != "" {
		return summary
	}
	return "Agent has a question"
}

func permissionRequestSummary(p feature.PermissionRequest) string {
	tool := p.Tool
	if tool == "" {
		tool = "Tool"
	}
	args := strings.TrimSpace(p.Args)
	if args == "" {
		return tool
	}
	if json.Valid([]byte(args)) {
		if detail := livePreviewToolInputSummary(tool, json.RawMessage(args)); detail != "" {
			return tool + ": " + detail
		}
	}
	return tool + ": " + singleLine(args)
}

func reviewAttentionSummary(f *feature.Feature) string {
	return reviewArtifactLabel(f) + " needs review"
}

func reviewArtifactLabel(f *feature.Feature) string {
	if f == nil {
		return "Artifact"
	}
	if f.IsRewind && f.PendingReviewPhase != nil {
		return fmt.Sprintf("Rewind to %s", f.PendingReviewPhase.String())
	}
	switch f.Status {
	case feature.StatusPlanNeedsReview:
		return planReviewArtifactLabel(f)
	case feature.StatusPromptNeedsReview:
		return "Prompt"
	case feature.StatusInquiryNeedsReview:
		return "Inquiry"
	case feature.StatusResearchNeedsReview:
		return feature.PhaseResearch.String()
	case feature.StatusDesignNeedsReview:
		return feature.PhaseDesign.String()
	default:
		return "Artifact"
	}
}

func planReviewArtifactLabel(f *feature.Feature) string {
	if f == nil {
		return "Plan"
	}
	if f.CurrentRoadmapPhase == 0 && f.TotalRoadmapPhases > 0 {
		return "Roadmap"
	}
	if f.CurrentRoadmapPhase > 0 && f.TotalRoadmapPhases > 1 {
		return fmt.Sprintf("Phase %d plan", f.CurrentRoadmapPhase)
	}
	return "Plan"
}

func reviewAttentionMode(f *feature.Feature) string {
	if f == nil {
		return ""
	}
	if f.PendingReviewPhase != nil {
		if f.IsRewind {
			return "rewind"
		}
		return "gate"
	}
	if f.Status == feature.StatusPlanNeedsReview {
		return "plan"
	}
	return "artifact"
}
