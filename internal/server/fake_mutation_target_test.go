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
	"context"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

type fakeMutationTarget struct{}

func (f *fakeMutationTarget) CreateFeature(CreateFeatureRequest) (CreateFeatureResponse, error) {
	return CreateFeatureResponse{FeatureID: "created-feature", Result: "created"}, nil
}

func (f *fakeMutationTarget) StartFeature(featureID string) (FeatureStartResponse, error) {
	return FeatureStartResponse{FeatureID: featureID, Result: "started"}, nil
}

func (f *fakeMutationTarget) StopFeature(featureID string) (FeatureStopResponse, error) {
	return FeatureStopResponse{FeatureID: featureID, Result: "stopped"}, nil
}

func (f *fakeMutationTarget) RestartFeature(featureID string, req RestartFeatureRequest) (FeatureRestartResponse, error) {
	return FeatureRestartResponse{FeatureID: featureID, Result: "restarted"}, nil
}

func (f *fakeMutationTarget) ReviewDecision(featureID string, req ReviewDecisionRequest) (ReviewDecisionResponse, error) {
	return ReviewDecisionResponse{FeatureID: featureID, Decision: req.Decision, Result: "submitted"}, nil
}

func (f *fakeMutationTarget) UpdateFeatureConfig(featureID string, req FeatureConfigMutationRequest) (FeatureConfigUpdateResponse, error) {
	return FeatureConfigUpdateResponse{FeatureID: featureID, Result: "updated"}, nil
}

func (f *fakeMutationTarget) NeedUserInputDecision(featureID string, req NeedUserInputDecisionRequest) (NeedUserInputDecisionResponse, error) {
	return NeedUserInputDecisionResponse{FeatureID: featureID, Decision: req.Decision, Result: "decided"}, nil
}

func (f *fakeMutationTarget) DraftNeedUserInputAnswers(featureID string, req NeedUserInputDraftRequest) (NeedUserInputDraftResponse, error) {
	return NeedUserInputDraftResponse{FeatureID: featureID, Result: "drafted"}, nil
}

func (f *fakeMutationTarget) ToggleInputNotifications(featureID string) (InputNotificationsToggleResponse, error) {
	return InputNotificationsToggleResponse{FeatureID: featureID, Result: "toggled"}, nil
}

func (f *fakeMutationTarget) AnswerPermission(req PermissionAnswerRequest) (PermissionAnswerResponse, error) {
	return PermissionAnswerResponse{SessionID: req.SessionID, RequestID: req.RequestID, Decision: req.Decision, Result: "answered"}, nil
}

func (f *fakeMutationTarget) AnswerAskUser(req AskUserAnswerRequest) (AskUserAnswerResponse, error) {
	return AskUserAnswerResponse{SessionID: req.SessionID, RequestID: req.RequestID, Result: "answered"}, nil
}

func (f *fakeMutationTarget) SendHelp(req HelpAnswerRequest) (HelpSendResponse, error) {
	return HelpSendResponse{FeatureID: req.FeatureID, SessionID: req.SessionID, Result: "sent"}, nil
}

func (f *fakeMutationTarget) StartChat(ChatStartRequest) (ChatStartResponse, error) {
	return ChatStartResponse{SessionID: "chat-1", Result: "started"}, nil
}

func (f *fakeMutationTarget) RuntimeConfig(RuntimeConfigMutationRequest) (RuntimeConfigUpdateResponse, error) {
	return RuntimeConfigUpdateResponse{Result: "updated"}, nil
}

func (f *fakeMutationTarget) GeneratePublishDescription(featureID string, req PublishDescriptionRequest) (PublishDescriptionResponse, error) {
	return PublishDescriptionResponse{FeatureID: featureID, Title: "Generated title", Body: "Generated body", Result: "generated"}, nil
}

func (f *fakeMutationTarget) PublishFeature(featureID string, req PublishFeatureRequest) (PublishFeatureResponse, error) {
	return PublishFeatureResponse{FeatureID: featureID, Result: "published"}, nil
}

func (f *fakeMutationTarget) MergeFeature(featureID string) (MergeFeatureResponse, error) {
	return MergeFeatureResponse{FeatureID: featureID, Result: "merged"}, nil
}

func (f *fakeMutationTarget) RewindFeature(featureID string, req RewindFeatureRequest) (RewindFeatureResponse, error) {
	return RewindFeatureResponse{FeatureID: featureID, Result: "rewound", TargetPhase: req.TargetPhase, RoadmapPhase: req.RoadmapPhase}, nil
}

func (f *fakeMutationTarget) RetryFeature(featureID string) (RetryFeatureResponse, error) {
	return RetryFeatureResponse{FeatureID: featureID, Result: "retried"}, nil
}

func (f *fakeMutationTarget) StartRebase(featureID string, req RebaseActionRequest) (RebaseStartResponse, error) {
	return RebaseStartResponse{FeatureID: featureID, Result: "started", Repo: req.Repo, CycleType: "rebase", RebaseTarget: req.RebaseTarget}, nil
}

func (f *fakeMutationTarget) FetchReviewComments(featureID string, req ReviewCommentsFetchRequest) (ReviewCommentsFetchResponse, error) {
	return ReviewCommentsFetchResponse{FeatureID: featureID, Repo: req.Repo, Comments: []ReviewCommentDTO{}}, nil
}

func (f *fakeMutationTarget) StartReviewComments(featureID string, req ReviewCommentsActionRequest) (ReviewCommentsStartResponse, error) {
	return ReviewCommentsStartResponse{FeatureID: featureID, Result: "started", Repo: req.Repo, Mode: req.Mode, CycleType: "review_comments"}, nil
}

func (f *fakeMutationTarget) StartTweak(featureID string, req TweakActionRequest) (TweakStartResponse, error) {
	return TweakStartResponse{FeatureID: featureID, Result: "started", CycleType: "tweak"}, nil
}

func (f *fakeMutationTarget) FinishTweak(featureID string, req TweakFinishRequest) (TweakFinishResponse, error) {
	return TweakFinishResponse{FeatureID: featureID, Result: "finished", Decision: req.Decision, HadChanges: req.HadChanges}, nil
}

func (f *fakeMutationTarget) StartRefactor(featureID string, req RefactorActionRequest) (RefactorStartResponse, error) {
	return RefactorStartResponse{FeatureID: featureID, Result: "started", Repo: req.Repo, CycleType: "refactor", Pipeline: string(req.Pipeline)}, nil
}

func (f *fakeMutationTarget) RestartRefactor(featureID string, req RefactorActionRequest) (RefactorRestartResponse, error) {
	return RefactorRestartResponse{FeatureID: featureID, Result: "restarted", Repo: req.Repo, CycleType: "refactor", Pipeline: string(req.Pipeline)}, nil
}

func (f *fakeMutationTarget) MarkDone(featureID string) (MarkDoneResponse, error) {
	return MarkDoneResponse{FeatureID: featureID, Result: "marked_done"}, nil
}

func (f *fakeMutationTarget) CleanupFeature(featureID string, req CleanupActionRequest) (CleanupFeatureResponse, error) {
	return CleanupFeatureResponse{FeatureID: featureID, Result: "cleaned", Target: req.Target}, nil
}

func (f *fakeMutationTarget) DeleteFeature(featureID string) (DeleteFeatureResponse, error) {
	return DeleteFeatureResponse{FeatureID: featureID, Result: "deleted"}, nil
}

func (f *fakeMutationTarget) ScanRecovery(context.Context) ([]ports.RecoveryItem, error) {
	return nil, nil
}

func (f *fakeMutationTarget) ExecuteRecovery(context.Context, []ports.RecoveryItem, map[string]ports.RecoveryAction) (RecoveryActionResponse, error) {
	return RecoveryActionResponse{Result: "executed"}, nil
}
