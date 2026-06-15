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
	"net/http"
	"net/url"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type noMCPArgs struct{}

type featureIDMCPArgs struct {
	FeatureID string `json:"feature_id"`
}

type sessionIDMCPArgs struct {
	SessionID string `json:"session_id"`
}

type transcriptMCPArgs struct {
	SessionID string `json:"session_id"`
	Cursor    int    `json:"cursor,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type runContentMCPArgs struct {
	FeatureID string `json:"feature_id"`
	RunNumber int    `json:"run_number"`
}

type artifactContentMCPArgs struct {
	FeatureID  string `json:"feature_id"`
	RunNumber  int    `json:"run_number"`
	ArtifactID string `json:"artifact_id"`
	Offset     int64  `json:"offset,omitempty"`
	Limit      int64  `json:"limit,omitempty"`
}

type logContentMCPArgs struct {
	FeatureID string `json:"feature_id"`
	RunNumber int    `json:"run_number"`
	LogID     string `json:"log_id"`
	Offset    int64  `json:"offset,omitempty"`
	Limit     int64  `json:"limit,omitempty"`
}

type featureRestartMCPArgs struct {
	FeatureID string `json:"feature_id"`
	RestartFeatureRequest
}

type reviewDecisionMCPArgs struct {
	FeatureID string `json:"feature_id"`
	ReviewDecisionRequest
}

type featureConfigMutationMCPArgs struct {
	FeatureID string `json:"feature_id"`
	FeatureConfigMutationRequest
}

type needUserInputDecisionMCPArgs struct {
	FeatureID string `json:"feature_id"`
	NeedUserInputDecisionRequest
}

type needUserInputDraftMCPArgs struct {
	FeatureID string `json:"feature_id"`
	NeedUserInputDraftRequest
}

type publishFeatureMCPArgs struct {
	FeatureID string `json:"feature_id"`
	PublishFeatureRequest
}

type rewindFeatureMCPArgs struct {
	FeatureID string `json:"feature_id"`
	RewindFeatureRequest
}

type rebaseMCPArgs struct {
	FeatureID string `json:"feature_id"`
	RebaseActionRequest
}

type reviewCommentsFetchMCPArgs struct {
	FeatureID string `json:"feature_id"`
	ReviewCommentsFetchRequest
}

type reviewCommentsActionMCPArgs struct {
	FeatureID string `json:"feature_id"`
	ReviewCommentsActionRequest
}

type tweakFinishMCPArgs struct {
	FeatureID string `json:"feature_id"`
	TweakFinishRequest
}

type refactorMCPArgs struct {
	FeatureID string `json:"feature_id"`
	RefactorActionRequest
}

type cleanupMCPArgs struct {
	FeatureID string `json:"feature_id"`
	CleanupActionRequest
}

func (h *apiHandler) registerMCPTools(server *mcp.Server) {
	addRESTTool[noMCPArgs, HealthResponse](server, h, "runtime_health_get", "Get runtime health and launch metadata.", http.MethodGet, func(noMCPArgs) string {
		return "/api/v1/health"
	}, nil, nil, false)
	addRESTTool[noMCPArgs, FeatureListResponse](server, h, "feature_list", "List feature summaries.", http.MethodGet, func(noMCPArgs) string {
		return "/api/v1/features"
	}, nil, nil, false)
	addRESTTool[featureIDMCPArgs, FeatureDetailResponse](server, h, "feature_get", "Get one feature detail snapshot.", http.MethodGet, func(in featureIDMCPArgs) string {
		return "/api/v1/features/" + pathSegment(in.FeatureID)
	}, nil, nil, false)
	addRESTTool[noMCPArgs, RuntimeConfigResponse](server, h, "config_runtime_get", "Get runtime configuration.", http.MethodGet, func(noMCPArgs) string {
		return "/api/v1/config/runtime"
	}, nil, nil, false)
	addRESTTool[featureIDMCPArgs, FeatureConfigResponse](server, h, "feature_config_get", "Get feature configuration.", http.MethodGet, func(in featureIDMCPArgs) string {
		return "/api/v1/features/" + pathSegment(in.FeatureID) + "/config"
	}, nil, nil, false)
	addRESTTool[noMCPArgs, ModelCatalogResponse](server, h, "model_catalog_get", "Get model catalog metadata.", http.MethodGet, func(noMCPArgs) string {
		return "/api/v1/catalog/models"
	}, nil, nil, false)
	addRESTTool[noMCPArgs, PromptSnapshotResponse](server, h, "prompt_snapshot_get", "Get prompt and ask-user queues.", http.MethodGet, func(noMCPArgs) string {
		return "/api/v1/prompts"
	}, nil, nil, false)
	addRESTTool[noMCPArgs, PermissionSnapshotResponse](server, h, "permission_snapshot_get", "Get pending permission requests.", http.MethodGet, func(noMCPArgs) string {
		return "/api/v1/permissions"
	}, nil, nil, false)
	addRESTTool[noMCPArgs, SessionListResponse](server, h, "session_list", "List active and recent sessions.", http.MethodGet, func(noMCPArgs) string {
		return "/api/v1/sessions"
	}, nil, nil, false)
	addRESTTool[sessionIDMCPArgs, SessionDetailResponse](server, h, "session_get", "Get one session detail snapshot.", http.MethodGet, func(in sessionIDMCPArgs) string {
		return "/api/v1/sessions/" + pathSegment(in.SessionID)
	}, nil, nil, false)
	addRESTTool[transcriptMCPArgs, TranscriptResponse](server, h, "session_transcript_get", "Get a session transcript window.", http.MethodGet, func(in transcriptMCPArgs) string {
		return "/api/v1/sessions/" + pathSegment(in.SessionID) + "/transcript"
	}, func(in transcriptMCPArgs) url.Values {
		return transcriptValues(CursorQuery{Cursor: in.Cursor, Limit: in.Limit})
	}, nil, false)
	addRESTTool[runContentMCPArgs, ArtifactListResponse](server, h, "artifact_list", "List run artifacts.", http.MethodGet, func(in runContentMCPArgs) string {
		return runContentPath(in.FeatureID, in.RunNumber, "artifacts")
	}, nil, nil, false)
	addRESTTool[artifactContentMCPArgs, TextContentResponse](server, h, "artifact_content_get", "Get bounded artifact text content.", http.MethodGet, func(in artifactContentMCPArgs) string {
		return runContentPath(in.FeatureID, in.RunNumber, "artifacts") + "/" + pathSegment(in.ArtifactID)
	}, func(in artifactContentMCPArgs) url.Values {
		return textValues(TextQuery{Offset: in.Offset, Limit: in.Limit})
	}, nil, false)
	addRESTTool[logContentMCPArgs, TextContentResponse](server, h, "log_content_get", "Get bounded log text content.", http.MethodGet, func(in logContentMCPArgs) string {
		return runContentPath(in.FeatureID, in.RunNumber, "logs") + "/" + pathSegment(in.LogID)
	}, func(in logContentMCPArgs) url.Values {
		return textValues(TextQuery{Offset: in.Offset, Limit: in.Limit})
	}, nil, false)
	addRESTTool[featureIDMCPArgs, LivePreviewResponse](server, h, "live_preview_get", "Get feature live preview.", http.MethodGet, func(in featureIDMCPArgs) string {
		return "/api/v1/features/" + pathSegment(in.FeatureID) + "/live-preview"
	}, nil, nil, false)
	addRESTTool[OperationQuery, OperationSnapshotResponse](server, h, "operation_list", "List operation records.", http.MethodGet, func(OperationQuery) string {
		return "/api/v1/operations"
	}, operationValues, nil, false)
	addRESTTool[noMCPArgs, RecoverySnapshotResponse](server, h, "recovery_snapshot_get", "Scan recoverable sessions.", http.MethodGet, func(noMCPArgs) string {
		return "/api/v1/recovery"
	}, nil, nil, false)

	h.registerMCPMutationTools(server)
}

func (h *apiHandler) registerMCPMutationTools(server *mcp.Server) {
	addRESTTool[CreateFeatureRequest, OperationAcceptedResponse](server, h, "feature_create", "Create a feature.", http.MethodPost, func(CreateFeatureRequest) string {
		return "/api/v1/features"
	}, nil, func(in CreateFeatureRequest) any { return in }, true)
	addFeatureEmptyMutation(server, h, "feature_start", "Start a feature.", "start")
	addFeatureEmptyMutation(server, h, "feature_resume", "Resume a feature.", "resume")
	addFeatureEmptyMutation(server, h, "feature_stop", "Stop or pause a feature.", "pause-stop")
	addFeatureEmptyMutation(server, h, "feature_interrupt", "Interrupt a feature.", "pause-stop")
	addRESTTool[featureRestartMCPArgs, OperationAcceptedResponse](server, h, "feature_restart", "Restart a feature.", http.MethodPost, func(in featureRestartMCPArgs) string {
		return featureActionPath(in.FeatureID, "restart")
	}, nil, func(in featureRestartMCPArgs) any {
		return in.RestartFeatureRequest
	}, true)
	addRESTTool[reviewDecisionMCPArgs, OperationAcceptedResponse](server, h, "review_decision_submit", "Submit a review gate decision.", http.MethodPost, func(in reviewDecisionMCPArgs) string {
		return "/api/v1/features/" + pathSegment(in.FeatureID) + "/review-decision"
	}, nil, func(in reviewDecisionMCPArgs) any { return in.ReviewDecisionRequest }, true)
	addRESTTool[featureConfigMutationMCPArgs, OperationAcceptedResponse](server, h, "feature_config_update", "Update feature configuration.", http.MethodPost, func(in featureConfigMutationMCPArgs) string {
		return "/api/v1/features/" + pathSegment(in.FeatureID) + "/config"
	}, nil, func(in featureConfigMutationMCPArgs) any { return in.FeatureConfigMutationRequest }, true)
	addRESTTool[needUserInputDecisionMCPArgs, OperationAcceptedResponse](server, h, "need_user_input_decide", "Resume or abort a need-user-input gate.", http.MethodPost, func(in needUserInputDecisionMCPArgs) string {
		return "/api/v1/features/" + pathSegment(in.FeatureID) + "/need-user-input"
	}, nil, func(in needUserInputDecisionMCPArgs) any { return in.NeedUserInputDecisionRequest }, true)
	addRESTTool[needUserInputDraftMCPArgs, OperationAcceptedResponse](server, h, "need_user_input_draft", "Draft need-user-input answers.", http.MethodPost, func(in needUserInputDraftMCPArgs) string {
		return "/api/v1/features/" + pathSegment(in.FeatureID) + "/need-user-input-draft"
	}, nil, func(in needUserInputDraftMCPArgs) any { return in.NeedUserInputDraftRequest }, true)
	addRESTTool[PermissionAnswerRequest, OperationAcceptedResponse](server, h, "permission_answer", "Answer a permission request.", http.MethodPost, func(PermissionAnswerRequest) string {
		return "/api/v1/permissions/answer"
	}, nil, func(in PermissionAnswerRequest) any { return in }, true)
	addRESTTool[AskUserAnswerRequest, OperationAcceptedResponse](server, h, "ask_user_answer", "Answer an ask-user prompt.", http.MethodPost, func(AskUserAnswerRequest) string {
		return "/api/v1/prompts/ask-user/answer"
	}, nil, func(in AskUserAnswerRequest) any { return in }, true)
	addRESTTool[HelpAnswerRequest, OperationAcceptedResponse](server, h, "help_send", "Send help text to a waiting request.", http.MethodPost, func(HelpAnswerRequest) string {
		return "/api/v1/prompts/help/send"
	}, nil, func(in HelpAnswerRequest) any { return in }, true)
	addRESTTool[RuntimeConfigMutationRequest, OperationAcceptedResponse](server, h, "config_runtime_update", "Update runtime configuration.", http.MethodPatch, func(RuntimeConfigMutationRequest) string {
		return "/api/v1/config/runtime"
	}, nil, func(in RuntimeConfigMutationRequest) any { return in }, true)
	addRESTTool[noMCPArgs, OperationAcceptedResponse](server, h, "runtime_shutdown", "Shut down the runtime server.", http.MethodPost, func(noMCPArgs) string {
		return "/api/v1/shutdown"
	}, nil, func(noMCPArgs) any { return emptyBody() }, true)
	addRESTTool[publishFeatureMCPArgs, OperationAcceptedResponse](server, h, "feature_publish", "Publish a feature.", http.MethodPost, func(in publishFeatureMCPArgs) string {
		return featureActionPath(in.FeatureID, "publish")
	}, nil, func(in publishFeatureMCPArgs) any {
		return in.PublishFeatureRequest
	}, true)
	addFeatureEmptyMutation(server, h, "feature_merge", "Merge a local-only feature.", "merge")
	addRESTTool[rewindFeatureMCPArgs, OperationAcceptedResponse](server, h, "feature_rewind", "Rewind a feature.", http.MethodPost, func(in rewindFeatureMCPArgs) string {
		return featureActionPath(in.FeatureID, "rewind")
	}, nil, func(in rewindFeatureMCPArgs) any {
		return in.RewindFeatureRequest
	}, true)
	addFeatureEmptyMutation(server, h, "feature_retry", "Retry a failed feature.", "retry")
	addRESTTool[rebaseMCPArgs, OperationAcceptedResponse](server, h, "rebase_start", "Start a rebase cycle.", http.MethodPost, func(in rebaseMCPArgs) string {
		return featureActionPath(in.FeatureID, "rebase")
	}, nil, func(in rebaseMCPArgs) any {
		return in.RebaseActionRequest
	}, true)
	addRESTTool[reviewCommentsFetchMCPArgs, ReviewCommentsFetchResponse](server, h, "review_comments_fetch", "Fetch review comments.", http.MethodPost, func(in reviewCommentsFetchMCPArgs) string {
		return featureActionPath(in.FeatureID, "review-comments") + "/fetch"
	}, nil, func(in reviewCommentsFetchMCPArgs) any { return in.ReviewCommentsFetchRequest }, true)
	addRESTTool[reviewCommentsActionMCPArgs, OperationAcceptedResponse](server, h, "review_comments_start", "Start a review-comments cycle.", http.MethodPost, func(in reviewCommentsActionMCPArgs) string {
		return featureActionPath(in.FeatureID, "review-comments")
	}, nil, func(in reviewCommentsActionMCPArgs) any {
		return in.ReviewCommentsActionRequest
	}, true)
	addFeatureEmptyMutation(server, h, "tweak_start", "Start a tweak cycle.", "tweak")
	addRESTTool[tweakFinishMCPArgs, OperationAcceptedResponse](server, h, "tweak_finish", "Finish a tweak cycle.", http.MethodPost, func(in tweakFinishMCPArgs) string {
		return featureActionPath(in.FeatureID, "tweak") + "/finish"
	}, nil, func(in tweakFinishMCPArgs) any { return in.TweakFinishRequest }, true)
	addRESTTool[refactorMCPArgs, OperationAcceptedResponse](server, h, "refactor_start", "Start a refactor cycle.", http.MethodPost, func(in refactorMCPArgs) string {
		return featureActionPath(in.FeatureID, "refactor")
	}, nil, func(in refactorMCPArgs) any {
		return in.RefactorActionRequest
	}, true)
	addRESTTool[refactorMCPArgs, OperationAcceptedResponse](server, h, "refactor_restart", "Restart a refactor cycle.", http.MethodPost, func(in refactorMCPArgs) string {
		return featureActionPath(in.FeatureID, "refactor") + "/restart"
	}, nil, func(in refactorMCPArgs) any { return in.RefactorActionRequest }, true)
	addFeatureEmptyMutation(server, h, "feature_mark_done", "Mark a feature done.", "mark-done")
	addRESTTool[cleanupMCPArgs, OperationAcceptedResponse](server, h, "feature_cleanup", "Clean up feature runtime artifacts.", http.MethodPost, func(in cleanupMCPArgs) string {
		return featureActionPath(in.FeatureID, "cleanup")
	}, nil, func(in cleanupMCPArgs) any {
		return in.CleanupActionRequest
	}, true)
	addFeatureEmptyMutation(server, h, "feature_delete", "Delete a feature.", "delete")
	addRESTTool[RecoveryActionRequest, OperationAcceptedResponse](server, h, "recovery_execute", "Execute recovery actions.", http.MethodPost, func(RecoveryActionRequest) string {
		return "/api/v1/recovery/actions"
	}, nil, func(in RecoveryActionRequest) any { return in }, true)
}

func addFeatureEmptyMutation(server *mcp.Server, h *apiHandler, name, description, action string) {
	addRESTTool[featureIDMCPArgs, OperationAcceptedResponse](server, h, name, description, http.MethodPost, featureActionPathFor(action), nil, func(featureIDMCPArgs) any {
		return emptyBody()
	}, true)
}

func addRESTTool[In, Out any](server *mcp.Server, h *apiHandler, name, description, method string, path func(In) string, query func(In) url.Values, body func(In) any, trusted bool) {
	mcp.AddTool(server, &mcp.Tool{Name: name, Description: description}, func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		var values url.Values
		if query != nil {
			values = query(in)
		}
		var requestBody any
		if body != nil {
			requestBody = body(in)
		}
		return callRESTTool[Out](ctx, h, req, method, path(in), values, requestBody, trusted)
	})
}

func featureActionPathFor(action string) func(featureIDMCPArgs) string {
	return func(in featureIDMCPArgs) string {
		return featureActionPath(in.FeatureID, action)
	}
}
