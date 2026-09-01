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

package errcat

import (
	"fmt"
	"strings"
)

// Generic codes shared by every endpoint family.
const (
	BadRequest           Code = "bad_request"
	NotFound             Code = "not_found"
	InternalError        Code = "internal_error"
	Unauthorized         Code = "unauthorized"
	Forbidden            Code = "forbidden"
	MethodNotAllowed     Code = "method_not_allowed"
	UnsupportedMediaType Code = "unsupported_media_type"
	RequestTooLarge      Code = "request_too_large"
	Unavailable          Code = "unavailable"
)

// Mutation and action-conflict codes.
const (
	Conflict              Code = "conflict"
	PublishRemoteDiverged Code = "publish_remote_diverged"
	PublishRemoteChanged  Code = "publish_remote_changed"
	PipelineMismatch      Code = "pipeline_mismatch"
	NeedUserInputOpen     Code = "need_user_input_open"
	PhaseFinalizing       Code = "phase_finalizing"
	InvalidTransition     Code = "invalid_transition"
	InvalidWorkspaceRoot  Code = "invalid_workspace_root"
)

// Relationship-guard codes.
const (
	RelationshipClosed        Code = "relationship_closed"
	ParentMutationLocked      Code = "parent_mutation_locked"
	ChildMutationRestricted   Code = "child_mutation_restricted"
	CascadeDeleteNotAvailable Code = "cascade_delete_not_available"
)

// Child-launch codes.
const (
	ParentNotFound               Code = "parent_not_found"
	ParentIsChild                Code = "parent_is_child"
	ParentStatusIneligible       Code = "parent_status_ineligible"
	ActiveChildExists            Code = "active_child_exists"
	ParentWorktreesDirty         Code = "parent_worktrees_dirty"
	ChildExecutionBlocked        Code = "child_execution_blocked"
	RebaseTargetResolutionFailed Code = "rebase_target_resolution_failed"
	RebaseFetchFailed            Code = "rebase_fetch_failed"
	RebaseAlreadyUpToDate        Code = "rebase_already_up_to_date"
)

// Review-feedback codes.
const (
	ReviewFeedbackEmptySelection         Code = "review_feedback_empty_selection"
	ReviewFeedbackUnsupportedCommentType Code = "review_feedback_unsupported_comment_type"
	ReviewFeedbackUnknownRepo            Code = "review_feedback_unknown_repo"
	ReviewFeedbackRepoHasNoPR            Code = "review_feedback_repo_has_no_pull_request"
	ReviewFeedbackDraftNotFound          Code = "review_feedback_draft_not_found"
	ReviewFeedbackUnknownReference       Code = "review_feedback_unknown_reference"
	ReviewFeedbackRevisionConflict       Code = "review_feedback_revision_conflict"
	ReviewFeedbackZeroLaunchable         Code = "review_feedback_zero_launchable_selection"
	ReviewFeedbackFetchFailed            Code = "review_feedback_fetch_failed"
	ReviewFeedbackMalformedReference     Code = "review_feedback_malformed_reference"
	ReviewFeedbackSelectionTooLarge      Code = "review_feedback_selection_update_too_large"
	FeatureReadFailed                    Code = "feature_read_failed"
)

// Workspace-init codes.
const (
	ConsentRequired          Code = "consent_required"
	InvalidRepositoryPath    Code = "invalid_repository_path"
	PathOutsideWorkspaceRoot Code = "path_outside_workspace_root"
	AlreadyRepository        Code = "already_repository"
	DirectoryNotEmpty        Code = "directory_not_empty"
)

// Readiness and provider codes.
const (
	NotReady                        Code = "not_ready"
	ProviderNotFound                Code = "provider_not_found"
	ProviderModelRefreshUnsupported Code = "provider_model_refresh_unsupported"
	ProviderModelRefreshFailed      Code = "provider_model_refresh_failed"
	PromptSnapshotTooLarge          Code = "prompt_snapshot_too_large"
)

// Read-failure codes.
const (
	RelationshipReadFailed Code = "relationship_read_failed"
)

// Readiness issue codes. They travel inside readiness payloads today; the
// catalog owns their authored text so every layer shares one vocabulary.
const (
	InvalidConfiguration Code = "invalid_configuration"
	InvalidRepository    Code = "invalid_repository"
	MissingExecutable    Code = "missing_executable"
	ModelsUnavailable    Code = "models_unavailable"
	Unauthenticated      Code = "unauthenticated"
	UnsupportedVersion   Code = "unsupported_version"
)

// WorkspaceRootParams carries the rejected workspace-root paths for
// InvalidWorkspaceRoot.
type WorkspaceRootParams struct {
	Paths []InvalidPath
}

// InvalidPath is one rejected path and why.
type InvalidPath struct {
	Path   string
	Reason string
}

func (WorkspaceRootParams) params() {}

// ReadinessParams carries the readiness issue titles for NotReady.
type ReadinessParams struct {
	Titles []string
}

func (ReadinessParams) params() {}

// RelatedFeatureParams names related features in a summary.
type RelatedFeatureParams struct {
	ParentID string
	ChildID  string
}

func (RelatedFeatureParams) params() {}

// PublishRepoParams carries the repository and branch a publish conflict
// refers to.
type PublishRepoParams struct {
	Repo              string
	Branch            string
	RemoteOnlyCommits int
}

func (PublishRepoParams) params() {}

// SubjectParams carries the missing subject for NotFound.
type SubjectParams struct {
	// Subject names the kind of resource, e.g. "Feature".
	Subject string
	// Name optionally identifies the specific resource instance.
	Name string
}

func (SubjectParams) params() {}

// PathParams carries the offending path for path-validation codes.
type PathParams struct {
	Path string
}

func (PathParams) params() {}

func joinQuoted(values []string) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = fmt.Sprintf("%q", value)
	}
	return strings.Join(quoted, ", ")
}

func commitWord(count int) string {
	if count == 1 {
		return "commit"
	}
	return "commits"
}

var catalog = map[Code]Entry{
	// --- Generic codes -----------------------------------------------------
	BadRequest: {
		Class:       ClassBlocking,
		Title:       "Bad request",
		Summary:     "The request was not valid.",
		Remediation: "Check the request details and try again.",
	},
	NotFound: {
		Class:   ClassBlocking,
		Title:   "Not found",
		Summary: "The requested resource was not found.",
		summaryParams: func(p Params) string {
			subject, ok := p.(SubjectParams)
			if !ok || strings.TrimSpace(subject.Subject) == "" {
				return ""
			}
			if subject.Name != "" {
				return fmt.Sprintf("%s %q was not found.", subject.Subject, subject.Name)
			}
			return fmt.Sprintf("%s was not found.", subject.Subject)
		},
		Remediation: "Refresh the view to see the current state, then try again.",
	},
	InternalError: {
		Class:       ClassBlocking,
		Title:       "Internal error",
		Summary:     fallbackSummary,
		Remediation: "Retry; if it keeps happening, restart the runtime and check its log.",
	},
	Unauthorized: {
		Class:       ClassBlocking,
		Title:       "Unauthorized",
		Summary:     "The request is missing a valid bearer token.",
		Remediation: "Reconnect to the server in Settings, then retry.",
	},
	Forbidden: {
		Class:       ClassBlocking,
		Title:       "Forbidden",
		Summary:     "The server's security policy refused the request.",
		Remediation: "Retry from the desktop app; if it persists, restart the runtime.",
	},
	MethodNotAllowed: {
		Class:   ClassBlocking,
		Title:   "Method not allowed",
		Summary: "The HTTP method is not supported by this endpoint.",
	},
	UnsupportedMediaType: {
		Class:   ClassBlocking,
		Title:   "Unsupported media type",
		Summary: "The request body format is not supported.",
	},
	RequestTooLarge: {
		Class:       ClassBlocking,
		Title:       "Request too large",
		Summary:     "The request body exceeds the size limit.",
		Remediation: "Choose a smaller payload and retry.",
	},
	Unavailable: {
		Class:       ClassBlocking,
		Title:       "Service unavailable",
		Summary:     "The service is not available right now.",
		Remediation: "Wait a moment, then retry.",
	},

	// --- Mutation and action-conflict codes --------------------------------
	Conflict: {
		Class:       ClassBlocking,
		Title:       "Conflict",
		Blocks:      []Block{BlockRepositories},
		Summary:     "The request conflicts with the current state of the feature.",
		Remediation: "Refresh the feature and retry.",
	},
	PublishRemoteDiverged: {
		Class:  ClassBlocking,
		Title:  "Pull-request branch diverged",
		Blocks: []Block{BlockRepositories},
		summaryParams: func(p Params) string {
			params, ok := p.(PublishRepoParams)
			if !ok || params.Repo == "" {
				return ""
			}
			return fmt.Sprintf(
				"The pull-request branch for %q contains %d remote %s that are not in this workspace.",
				params.Repo, params.RemoteOnlyCommits, commitWord(params.RemoteOnlyCommits),
			)
		},
		Summary:     "The pull-request branch contains remote work that is not in this workspace.",
		Remediation: "Review and reconcile the pull-request branch on the remote, then refresh and retry.",
	},
	PublishRemoteChanged: {
		Class:  ClassBlocking,
		Title:  "Pull-request branch changed",
		Blocks: []Block{BlockRepositories},
		summaryParams: func(p Params) string {
			params, ok := p.(PublishRepoParams)
			if !ok || params.Repo == "" {
				return ""
			}
			return fmt.Sprintf("The pull-request branch for %q changed while Agentico was publishing.", params.Repo)
		},
		Summary:     "The pull-request branch changed while Agentico was publishing.",
		Remediation: "Refresh the publish state and retry; nothing was overwritten.",
	},
	PipelineMismatch: {
		Class:       ClassBlocking,
		Title:       "Pipeline mismatch",
		Summary:     "The submitted pipeline does not match the feature's pipeline.",
		Remediation: "Refresh the feature and retry with its current pipeline.",
	},
	NeedUserInputOpen: {
		Class:       ClassBlocking,
		Title:       "Waiting on user input",
		Summary:     "The feature is waiting on an open user input request.",
		Remediation: "Answer the open input request to continue.",
	},
	PhaseFinalizing: {
		Class:       ClassBlocking,
		Title:       "Phase finalizing",
		Summary:     "The feature is finalizing the current phase.",
		Remediation: "Wait for the phase to finish, then retry.",
	},
	InvalidTransition: {
		Class:       ClassBlocking,
		Title:       "Invalid transition",
		Summary:     "The action is not valid in the feature's current state.",
		Remediation: "Refresh the feature and retry.",
	},
	InvalidWorkspaceRoot: {
		Class:   ClassBlocking,
		Title:   "Invalid workspace root",
		Summary: "A workspace root does not resolve to an existing directory.",
		summaryParams: func(p Params) string {
			params, ok := p.(WorkspaceRootParams)
			var paths []string
			for _, invalid := range params.Paths {
				if invalid.Path != "" {
					paths = append(paths, invalid.Path)
				}
			}
			if !ok || len(paths) == 0 {
				return ""
			}
			return "Some workspace roots do not resolve to existing directories: " + strings.Join(paths, "; ") + "."
		},
		Remediation: "Check the workspace roots in settings and try again.",
	},

	// --- Relationship-guard codes -------------------------------------------
	RelationshipClosed: {
		Class:       ClassBlocking,
		Title:       "Child relationship closed",
		Summary:     "The child pass's relationship is closed and cannot be mutated.",
		Remediation: "Refresh the parent feature to see the pass's final state.",
	},
	ParentMutationLocked: {
		Class:       ClassBlocking,
		Title:       "Parent locked during child pass",
		Summary:     "The parent feature is locked while a child pass is active.",
		Remediation: "Wait for the child pass to finish, then retry.",
	},
	ChildMutationRestricted: {
		Class:       ClassBlocking,
		Title:       "Child mutation restricted",
		Summary:     "Child passes only accept pass-specific actions.",
		Remediation: "Use the pass workspace to act on this child.",
	},
	CascadeDeleteNotAvailable: {
		Class:       ClassBlocking,
		Title:       "Cascade delete unavailable",
		Summary:     "Cascade delete is not available while a child pass is active.",
		Remediation: "Finish or discard the child pass first, then delete.",
	},

	// --- Child-launch codes --------------------------------------------------
	ParentNotFound: {
		Class:   ClassBlocking,
		Title:   "Parent feature not found",
		Summary: "The parent feature was not found.",
		summaryParams: func(p Params) string {
			params, ok := p.(RelatedFeatureParams)
			if !ok || params.ParentID == "" {
				return ""
			}
			return fmt.Sprintf("The parent feature %q was not found.", params.ParentID)
		},
		Remediation: "Refresh the feature list and choose an existing parent.",
	},
	ParentIsChild: {
		Class:   ClassBlocking,
		Title:   "Parent is a child pass",
		Summary: "A child pass cannot be the parent of another pass.",
	},
	ParentStatusIneligible: {
		Class:       ClassBlocking,
		Title:       "Parent status not eligible",
		Summary:     "The parent feature's status does not allow launching a pass right now.",
		Remediation: "Refresh the parent feature and check its state.",
	},
	ActiveChildExists: {
		Class:   ClassNeedsAction,
		Title:   "Active child pass exists",
		Summary: "The parent feature already has an active child pass.",
		summaryParams: func(p Params) string {
			params, ok := p.(RelatedFeatureParams)
			if !ok || params.ParentID == "" {
				return ""
			}
			if params.ChildID == "" {
				return fmt.Sprintf("The parent feature %q already has an active child pass.", params.ParentID)
			}
			return fmt.Sprintf("The parent feature %q already has an active child pass %q.", params.ParentID, params.ChildID)
		},
		Remediation: "Finish or discard the active child pass, then launch a new one.",
	},
	ParentWorktreesDirty: {
		Class:       ClassNeedsAction,
		Title:       "Parent worktrees are dirty",
		Summary:     "The parent feature's worktrees have uncommitted changes.",
		Blocks:      []Block{BlockRepositories},
		Remediation: "Commit or stash the listed changes in each repository, then retry.",
	},
	ChildExecutionBlocked: {
		Class:   ClassBlocking,
		Title:   "Child passes are not runnable",
		Summary: "Child passes are driven by their parent; they cannot be started directly.",
	},
	RebaseTargetResolutionFailed: {
		Class:       ClassBlocking,
		Title:       "Rebase target unresolved",
		Summary:     "The rebase target branch could not be resolved.",
		Blocks:      []Block{BlockRepositories},
		Remediation: "Check that the target branch exists on the remote, then retry.",
	},
	RebaseFetchFailed: {
		Class:       ClassBlocking,
		Title:       "Fetch failed",
		Summary:     "Fetching the latest state from the remote failed.",
		Blocks:      []Block{BlockRepositories},
		Remediation: "Check the repository's remote access, then retry.",
	},
	RebaseAlreadyUpToDate: {
		Class:   ClassWarning,
		Title:   "Already up to date",
		Summary: "The feature is already up to date with its rebase targets.",
		Blocks:  []Block{BlockRepositories},
	},

	// --- Review-feedback codes ------------------------------------------------
	ReviewFeedbackEmptySelection: {
		Class:       ClassBlocking,
		Title:       "Empty selection",
		Summary:     "The review feedback selection is empty.",
		Remediation: "Select at least one comment to address.",
	},
	ReviewFeedbackUnsupportedCommentType: {
		Class:   ClassBlocking,
		Title:   "Unsupported comment type",
		Summary: "The review feedback contains a comment type that cannot be addressed.",
	},
	ReviewFeedbackUnknownRepo: {
		Class:       ClassBlocking,
		Title:       "Unknown repository",
		Summary:     "The selected repository does not belong to the parent feature.",
		Remediation: "Refresh the review feedback and retry.",
	},
	ReviewFeedbackRepoHasNoPR: {
		Class:   ClassBlocking,
		Title:   "No pull request",
		Summary: "The repository has no pull request to fetch feedback from.",
	},
	ReviewFeedbackDraftNotFound: {
		Class:       ClassBlocking,
		Title:       "Draft not found",
		Summary:     "No pending review feedback draft exists for this feature.",
		Remediation: "Fetch the review feedback again to create a fresh draft.",
	},
	ReviewFeedbackUnknownReference: {
		Class:       ClassBlocking,
		Title:       "Unknown reference",
		Summary:     "The reference does not belong to a repository of this feature.",
		Remediation: "Refresh the review feedback and retry.",
	},
	ReviewFeedbackRevisionConflict: {
		Class:       ClassBlocking,
		Title:       "Draft revision conflict",
		Summary:     "The review feedback draft changed since it was loaded.",
		Remediation: "The draft reloads with its current revision; review it and retry.",
	},
	ReviewFeedbackZeroLaunchable: {
		Class:   ClassBlocking,
		Title:   "Nothing left to address",
		Summary: "Every selected comment has already been addressed or excluded.",
	},
	ReviewFeedbackFetchFailed: {
		Class:       ClassBlocking,
		Title:       "Fetch failed",
		Blocks:      []Block{BlockRepositories},
		Summary:     "Fetching review feedback from the remote failed.",
		Remediation: "Check remote access and retry the fetch.",
	},
	ReviewFeedbackMalformedReference: {
		Class:       ClassBlocking,
		Title:       "Malformed reference",
		Summary:     "A review feedback reference could not be parsed.",
		Remediation: "Refresh the review feedback and retry.",
	},
	ReviewFeedbackSelectionTooLarge: {
		Class:       ClassBlocking,
		Title:       "Selection update too large",
		Summary:     "The selection update exceeds the allowed number of references.",
		Remediation: "Update the selection in smaller batches.",
	},
	FeatureReadFailed: {
		Class:       ClassBlocking,
		Title:       "Feature read failed",
		Summary:     "The feature could not be read from the store.",
		Remediation: "Refresh the feature and retry.",
	},

	// --- Workspace-init codes ---------------------------------------------------
	ConsentRequired: {
		Class:       ClassNeedsAction,
		Title:       "Consent required",
		Summary:     "Explicit consent is required to initialize a repository.",
		Remediation: "Confirm the initialization consent, then retry.",
	},
	InvalidRepositoryPath: {
		Class:   ClassBlocking,
		Title:   "Invalid repository path",
		Summary: "The repository path is not valid.",
		summaryParams: func(p Params) string {
			params, ok := p.(PathParams)
			if !ok || params.Path == "" {
				return ""
			}
			return fmt.Sprintf("The repository path %q is not valid.", params.Path)
		},
		Remediation: "Choose an absolute folder inside a configured workspace root.",
	},
	PathOutsideWorkspaceRoot: {
		Class:   ClassBlocking,
		Title:   "Path outside workspace root",
		Summary: "The path must be inside a configured workspace root.",
		summaryParams: func(p Params) string {
			params, ok := p.(PathParams)
			if !ok || params.Path == "" {
				return ""
			}
			return fmt.Sprintf("The path %q is not strictly inside a configured workspace root.", params.Path)
		},
		Remediation: "Choose a folder inside a workspace root, or add its parent as a root first.",
	},
	AlreadyRepository: {
		Class:       ClassBlocking,
		Title:       "Already a repository",
		Summary:     "The selected path is already a git repository.",
		Remediation: "Select the existing repository instead of initializing it.",
	},
	DirectoryNotEmpty: {
		Class:       ClassBlocking,
		Title:       "Directory not empty",
		Summary:     "The directory is not empty and is not a git repository.",
		Remediation: "Choose an empty folder or an existing repository.",
	},

	// --- Readiness and provider codes ---------------------------------------------
	NotReady: {
		Class:   ClassNeedsAction,
		Title:   "Runtime not ready",
		Summary: "The runtime is not ready to create features.",
		summaryParams: func(p Params) string {
			params, ok := p.(ReadinessParams)
			if !ok || len(params.Titles) == 0 {
				return ""
			}
			return "The runtime is not ready to create features: " + strings.Join(params.Titles, "; ") + "."
		},
		Remediation: "Complete the outstanding setup steps, then try again.",
	},
	ProviderNotFound: {
		Class:       ClassBlocking,
		Title:       "Provider not found",
		Summary:     "The requested provider is not registered.",
		Remediation: "Choose a configured provider.",
	},
	ProviderModelRefreshUnsupported: {
		Class:   ClassBlocking,
		Title:   "Model refresh unsupported",
		Summary: "The provider does not support refreshing its model list.",
	},
	ProviderModelRefreshFailed: {
		Class:       ClassBlocking,
		Title:       "Model refresh failed",
		Summary:     "Refreshing the provider's model list failed.",
		Remediation: "Check the provider's authentication, then retry.",
	},
	PromptSnapshotTooLarge: {
		Class:   ClassBlocking,
		Title:   "Prompt snapshot too large",
		Summary: "The pending prompt snapshot exceeds the safe response limit.",
	},

	// --- Read-failure codes ------------------------------------------------------
	RelationshipReadFailed: {
		Class:       ClassBlocking,
		Title:       "Relationship history read failed",
		Summary:     "The relationship history could not be read.",
		Remediation: "Refresh the feature and retry.",
	},

	// --- Readiness issue codes -----------------------------------------------------
	InvalidConfiguration: {
		Class:       ClassBlocking,
		Title:       "Invalid configuration",
		Summary:     "The runtime configuration is unusable.",
		Remediation: "Fix the configuration and restart the runtime.",
	},
	InvalidRepository: {
		Class:       ClassBlocking,
		Title:       "Invalid repository",
		Summary:     "A configured repository path is not a git repository.",
		Remediation: "Check the repository paths in settings.",
	},
	MissingExecutable: {
		Class:       ClassBlocking,
		Title:       "Missing executable",
		Summary:     "A provider CLI is not installed.",
		Remediation: "Install the missing provider CLI, then retry.",
	},
	ModelsUnavailable: {
		Class:       ClassBlocking,
		Title:       "Models unavailable",
		Summary:     "No usable provider exposes any model.",
		Remediation: "Check the provider configuration and authentication.",
	},
	Unauthenticated: {
		Class:       ClassBlocking,
		Title:       "Unauthenticated",
		Summary:     "A provider CLI is installed but its authentication flow has not been completed.",
		Remediation: "Complete the provider's authentication, then retry.",
	},
	UnsupportedVersion: {
		Class:       ClassBlocking,
		Title:       "Unsupported version",
		Summary:     "An installed provider CLI is below the enforced minimum version.",
		Remediation: "Update the provider CLI, then retry.",
	},
}
