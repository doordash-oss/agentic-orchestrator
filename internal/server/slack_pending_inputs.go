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

package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"path/filepath"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// PendingSlackInputs projects the same live controls and durable queues used by
// the prompts and permissions endpoints into the notifier's narrow port.
func (h *apiHandler) PendingSlackInputs(featureID string) ([]ports.SlackPendingInput, error) {
	var pending []ports.SlackPendingInput
	if h.sessions != nil {
		for _, sess := range h.sessions.ActiveSessions() {
			if sess == nil || sess.FeatureID() != featureID || sess.Kind() == ports.KindChat {
				continue
			}
			for _, req := range sess.PendingControlRequests() {
				if req == nil {
					continue
				}
				if req.Request.ToolName == toolNameAskUserQuestion {
					questions := slackAskUserQuestions(sess, req)
					for i, question := range questions {
						item := ports.SlackPendingInput{
							Kind:          ports.SlackPendingQuestion,
							FeatureID:     featureID,
							RequestID:     req.RequestID,
							QuestionIndex: i,
							QuestionCount: len(questions),
							Header:        question.Header,
							Question:      question.Question,
							MultiSelect:   question.MultiSelect,
						}
						for _, option := range question.Options {
							projected := ports.SlackPendingInputOption{
								Label:       option.Label,
								Description: option.Description,
							}
							if option.Confidence != nil {
								projected.Confidence = *option.Confidence
								projected.HasConfidence = true
							}
							item.Options = append(item.Options, projected)
						}
						pending = append(pending, item)
					}
					continue
				}
				pending = append(pending, ports.SlackPendingInput{
					Kind:            ports.SlackPendingPermission,
					FeatureID:       featureID,
					RequestID:       req.RequestID,
					ToolName:        req.Request.ToolName,
					Input:           slackControlInput(req),
					Phase:           sess.Phase().String(),
					RepoName:        sess.RepoName(),
					RememberPattern: safeRememberPattern(req),
					WaitingSince:    req.WaitingSince,
				})
			}
		}
	}

	if h.store == nil {
		return pending, nil
	}
	f, err := h.store.Load(featureID)
	if err != nil {
		return nil, fmt.Errorf("loading feature pending inputs: %w", err)
	}
	if f == nil {
		return pending, nil
	}
	for _, help := range f.HelpQueue {
		if help.Pending {
			pending = append(pending, ports.SlackPendingInput{
				Kind:         ports.SlackPendingHelp,
				FeatureID:    featureID,
				HelpQuestion: help.Question,
				WaitingSince: help.Time,
			})
		}
	}
	if f.Status.IsNeedsReview() {
		ctx, resolveErr := resolveReviewSessionContext(h.store, f)
		if resolveErr != nil {
			log.Printf(
				"slack pending review resolution failed for feature %q: %s",
				featureID,
				slackReviewResolutionFailureClass(resolveErr),
			)
		} else {
			pending = append(pending, ports.SlackPendingInput{
				Kind:                      ports.SlackPendingReview,
				FeatureID:                 featureID,
				ReviewID:                  ctx.reviewID,
				ReviewMode:                ctx.reviewMode,
				TargetPhase:               ctx.targetPhase.DirName(),
				ArtifactID:                ctx.artifactID,
				ArtifactPath:              ctx.sourcePath,
				ArtifactFilename:          filepath.Base(ctx.sourcePath),
				ArtifactBytes:             append([]byte(nil), ctx.source...),
				ArtifactSize:              ctx.artifactSize,
				ArtifactUnavailableReason: ctx.unavailableReason,
				RunNumber:                 ctx.run.RunNumber,
				SourceRevision:            ctx.sourceRevision,
				CanIterate:                ctx.canIterate,
				Roadmap:                   ctx.roadmap,
				PhasePlan:                 ctx.phasePlan,
				RoadmapPhase:              ctx.roadmapPhase,
				TotalRoadmapPhases:        ctx.roadmapTotal,
			})
		}
	}
	if f.Status == feature.StatusNeedUserInput && f.PendingNeedUserInputPath != "" {
		gate := rawNeedUserInputGateDTO(
			f.ID, entityFeature, "", f.CurrentIteration, f.InputNotifications, f.PendingNeedUserInputPath,
		)
		item := ports.SlackPendingInput{
			Kind:          ports.SlackPendingGate,
			FeatureID:     featureID,
			GatePath:      f.PendingNeedUserInputPath,
			Iteration:     gate.Iteration,
			WaitingSince:  gate.WaitingSince,
			GateSummary:   gate.Summary,
			GateQuestions: make([]string, 0, len(gate.Questions)),
		}
		for _, question := range gate.Questions {
			item.GateQuestions = append(item.GateQuestions, question.Prompt)
		}
		if gate.Verification != nil {
			for _, blocker := range gate.Verification.Blockers {
				item.GateBlockers = append(item.GateBlockers, ports.SlackPendingInputBlocker{
					Name:        blocker.Name,
					RepoName:    blocker.RepoName,
					Command:     blocker.Command,
					Reason:      blocker.Reason,
					Remediation: blocker.Remediation,
				})
			}
		}
		pending = append(pending, item)
	}
	return pending, nil
}

func slackReviewResolutionFailureClass(err error) string {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "artifact_missing"
	case errors.Is(err, fs.ErrPermission):
		return "artifact_unreadable"
	case strings.Contains(err.Error(), "invalid review artifact target"):
		return "artifact_invalid"
	case strings.Contains(err.Error(), "review artifact") &&
		strings.Contains(err.Error(), "not found"):
		return "artifact_missing"
	case strings.Contains(err.Error(), "unavailable"):
		return "artifact_unavailable"
	default:
		return "resolution_failed"
	}
}

func slackControlInput(req *llm.ControlRequestMessage) map[string]any {
	if req == nil || len(req.Request.Input) == 0 {
		return nil
	}
	var input map[string]any
	if err := json.Unmarshal(req.Request.Input, &input); err != nil {
		return nil
	}
	return input
}

func slackAskUserQuestions(sess ports.SessionView, req *llm.ControlRequestMessage) []AskUserQuestion {
	if req == nil || req.Request.ToolName != toolNameAskUserQuestion {
		return nil
	}
	questions := slackAskUserQuestionsFromInput(req.Request.Input)
	if !askUserQuestionDTOsNeedConfidence(questions) || sess == nil || sess.MessageLog() == nil {
		return questions
	}
	blocks := sess.MessageLog().ToolUseBlocks()
	for i := len(blocks) - 1; i >= 0; i-- {
		block := blocks[i]
		if block.Name != toolNameAskUserQuestion || len(block.Input) == 0 {
			continue
		}
		source := slackAskUserQuestionsFromInput(block.Input)
		if askUserQuestionDTOBundlesMatch(questions, source) {
			return copyAskUserQuestionDTOConfidence(questions, source)
		}
	}
	return questions
}

func slackAskUserQuestionsFromInput(input json.RawMessage) []AskUserQuestion {
	if len(input) == 0 {
		return nil
	}
	var envelope struct {
		Questions []struct {
			Question         string `json:"question"`
			Header           string `json:"header"`
			MultiSelect      bool   `json:"multiSelect"`
			MultiSelectSnake bool   `json:"multi_select"`
			Options          []struct {
				Label       string   `json:"label"`
				Description string   `json:"description"`
				Confidence  *float64 `json:"confidence"`
			} `json:"options"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(input, &envelope); err != nil || len(envelope.Questions) == 0 {
		return nil
	}
	questions := make([]AskUserQuestion, 0, len(envelope.Questions))
	for _, rawQuestion := range envelope.Questions {
		question := AskUserQuestion{
			Question:    rawQuestion.Question,
			Header:      rawQuestion.Header,
			MultiSelect: rawQuestion.MultiSelect || rawQuestion.MultiSelectSnake,
		}
		for _, rawOption := range rawQuestion.Options {
			option := AskUserOption{
				Label:       rawOption.Label,
				Description: rawOption.Description,
				Confidence:  rawOption.Confidence,
			}
			if option.Label == "" && option.Description == "" && option.Confidence == nil {
				continue
			}
			question.Options = append(question.Options, option)
		}
		if question.Question == "" && question.Header == "" && len(question.Options) == 0 {
			continue
		}
		questions = append(questions, question)
	}
	return questions
}
