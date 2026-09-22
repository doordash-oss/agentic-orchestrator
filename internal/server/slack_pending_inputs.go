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
	"fmt"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
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
				dto := controlRequestDTO(sess, req)
				if dto.ToolName == toolNameAskUserQuestion {
					for i, question := range dto.Questions {
						item := ports.SlackPendingInput{
							Kind:          ports.SlackPendingQuestion,
							FeatureID:     featureID,
							RequestID:     dto.RequestID,
							QuestionIndex: i,
							QuestionCount: len(dto.Questions),
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
					RequestID:       dto.RequestID,
					ToolName:        dto.ToolName,
					Input:           dto.Input,
					Phase:           dto.Phase,
					RepoName:        sess.RepoName(),
					RememberPattern: rememberPattern(dto.Remember),
					WaitingSince:    dto.WaitingSince,
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
	if f.Status == feature.StatusNeedUserInput && f.PendingNeedUserInputPath != "" {
		gate := needUserInputGateDTO(
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

func rememberPattern(preview *PermissionRememberPreview) string {
	if preview == nil {
		return ""
	}
	return preview.Pattern
}
