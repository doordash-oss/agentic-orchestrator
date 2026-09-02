// Copyright 2026 DoorDash, Inc.
//
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

package server

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// Chat context scopes: the durable homes an explain-in-chat reference can
// point at. The zero value means the request carries no reference.
const (
	chatContextScopeRun         = ChatContextScope("run")
	chatContextScopeTransaction = ChatContextScope("transaction")
	chatContextScopeRepository  = ChatContextScope("repository")
	chatContextScopeSetup       = ChatContextScope("setup")
	chatContextScopeRecovery    = ChatContextScope("recovery")
)

// chatContextAbsent reports whether a decoded request context reference
// carries nothing at all: the request behaves exactly as one without
// `context`.
func chatContextAbsent(ref ChatContextReference) bool {
	return ref == ChatContextReference{}
}

// validateChatContextReference checks a non-absent reference's shape and
// returns the name of the offending field, or "" when the reference is
// well-formed for its scope. Every scope requires the code; each scope then
// requires its own keys and rejects keys foreign to it, so a reference can
// never address two homes at once.
func validateChatContextReference(ref ChatContextReference) string {
	if chatContextAbsent(ref) {
		return ""
	}
	if ref.Code == "" {
		return "code"
	}
	switch ref.Scope {
	case chatContextScopeRun:
		if ref.FeatureID == "" {
			return "feature_id"
		}
		if ref.Repository != "" {
			return "repository"
		}
		if ref.TaskKey != "" {
			return "task_key"
		}
		if ref.SnapshotID != "" || ref.Key != "" {
			return "snapshot_id"
		}
	case chatContextScopeTransaction:
		if ref.FeatureID == "" {
			return "feature_id"
		}
		if ref.TaskKey != "" {
			return "task_key"
		}
		if ref.SnapshotID != "" || ref.Key != "" {
			return "snapshot_id"
		}
	case chatContextScopeRepository:
		if ref.FeatureID == "" {
			return "feature_id"
		}
		if ref.Repository == "" {
			return "repository"
		}
		if ref.TaskKey != "" {
			return "task_key"
		}
		if ref.SnapshotID != "" || ref.Key != "" {
			return "snapshot_id"
		}
	case chatContextScopeSetup:
		if ref.FeatureID == "" {
			return "feature_id"
		}
		if ref.TaskKey == "" {
			return "task_key"
		}
		if ref.Repository != "" {
			return "repository"
		}
		if ref.SnapshotID != "" || ref.Key != "" {
			return "snapshot_id"
		}
	case chatContextScopeRecovery:
		if ref.SnapshotID == "" {
			return "snapshot_id"
		}
		if ref.Key == "" {
			return "key"
		}
		if ref.FeatureID != "" {
			return "feature_id"
		}
		if ref.Repository != "" || ref.TaskKey != "" {
			return "repository"
		}
	default:
		return "scope"
	}
	return ""
}

// writeChatContextInvalid rejects a malformed chat context reference with
// the cataloged blocking envelope, naming the offending field in
// diagnostics. Called before uploads are consumed, so nothing is staged.
func writeChatContextInvalid(w http.ResponseWriter, ref ChatContextReference, field string) {
	writeAPIError(
		w,
		http.StatusBadRequest,
		errcat.ChatContextInvalid,
		errcat.WithParams(errcat.ChatContextParams{Scope: string(ref.Scope), Code: ref.Code}),
		errcat.WithDiagnostics(fmt.Sprintf("chat context reference field %q is not valid for scope %q", field, ref.Scope)),
	)
}

// chatContextRejection is one typed resolver failure: the HTTP status, the
// catalog code, and the diagnostics that name what could not be resolved.
type chatContextRejection struct {
	status      int
	code        errcat.Code
	diagnostics string
}

func (r *chatContextRejection) write(w http.ResponseWriter, ref ChatContextReference) {
	writeAPIError(
		w,
		r.status,
		r.code,
		errcat.WithParams(errcat.ChatContextParams{Scope: string(ref.Scope), Code: ref.Code}),
		errcat.WithDiagnostics(r.diagnostics),
	)
}

func chatContextFeatureInvalid(featureID string) *chatContextRejection {
	return &chatContextRejection{
		status:      http.StatusBadRequest,
		code:        errcat.ChatContextInvalid,
		diagnostics: fmt.Sprintf("chat context feature %q is not known", featureID),
	}
}

func chatContextHomeNotFound(home string) *chatContextRejection {
	return &chatContextRejection{
		status:      http.StatusNotFound,
		code:        errcat.ChatContextNotFound,
		diagnostics: fmt.Sprintf("chat context home %s holds no matching error", home),
	}
}

// resolveChatContext turns a validated reference into the hidden context
// bundle for a chat turn, or a typed rejection. Resolution mirrors the
// read-model lookups exactly: run reads the run's failure record;
// transaction reads the child's journal attention record, or the named
// entry's cleanup or tail warning whose code matches; repository reads the
// repository state's error; setup reads the named task's error; recovery
// looks up the stored snapshot and item and classifies it exactly as the
// recovery projection does. The bundle never enters an HTTP response body.
func (h *apiHandler) resolveChatContext(ref ChatContextReference) (string, *chatContextRejection) {
	switch ref.Scope {
	case chatContextScopeRun:
		return h.resolveChatContextRun(ref)
	case chatContextScopeTransaction:
		return h.resolveChatContextTransaction(ref)
	case chatContextScopeRepository:
		return h.resolveChatContextRepository(ref)
	case chatContextScopeSetup:
		return h.resolveChatContextSetup(ref)
	case chatContextScopeRecovery:
		return h.resolveChatContextRecovery(ref)
	default:
		return "", chatContextFeatureInvalid(ref.FeatureID)
	}
}

func (h *apiHandler) resolveChatContextRun(ref ChatContextReference) (string, *chatContextRejection) {
	f, rejection := h.chatContextFeature(ref.FeatureID)
	if rejection != nil {
		return "", rejection
	}
	rec := f.FailureRecord()
	if rec == nil || rec.Code != errcat.Code(ref.Code) {
		return "", chatContextHomeNotFound("run")
	}
	return buildChatContextBundle(ref, f.ID, f.Name, errcat.RenderRecord(*rec), chatContextRecordLogPaths(rec)), nil
}

func (h *apiHandler) resolveChatContextTransaction(ref ChatContextReference) (string, *chatContextRejection) {
	f, rejection := h.chatContextFeature(ref.FeatureID)
	if rejection != nil {
		return "", rejection
	}
	if repo := ref.Repository; repo != "" {
		if f.Parent == nil || f.Parent.Transaction == nil {
			return "", chatContextHomeNotFound("transaction entry " + repo)
		}
		entry := f.Parent.Transaction.EntryByRepo(repo)
		if entry == nil {
			return "", chatContextHomeNotFound("transaction entry " + repo)
		}
		for _, rec := range []*errcat.FailureRecord{entry.Cleanup, entry.Tail} {
			if rec != nil && rec.Code == errcat.Code(ref.Code) {
				return buildChatContextBundle(ref, f.ID, f.Name, errcat.RenderRecord(*rec), chatContextRecordLogPaths(rec)), nil
			}
		}
		return "", chatContextHomeNotFound("transaction entry " + repo)
	}
	rec := f.IntegrationAttentionRecord()
	if rec == nil || rec.Code != errcat.Code(ref.Code) {
		return "", chatContextHomeNotFound("transaction")
	}
	return buildChatContextBundle(ref, f.ID, f.Name, errcat.RenderRecord(*rec), chatContextRecordLogPaths(rec)), nil
}

func (h *apiHandler) resolveChatContextRepository(ref ChatContextReference) (string, *chatContextRejection) {
	f, rejection := h.chatContextFeature(ref.FeatureID)
	if rejection != nil {
		return "", rejection
	}
	state := f.Run().RepoStates[ref.Repository]
	if state == nil || state.Error == nil || state.Error.Code != errcat.Code(ref.Code) {
		return "", chatContextHomeNotFound("repository " + ref.Repository)
	}
	rec := *state.Error
	return buildChatContextBundle(ref, f.ID, f.Name, errcat.RenderRecord(rec), chatContextRecordLogPaths(&rec)), nil
}

func (h *apiHandler) resolveChatContextSetup(ref ChatContextReference) (string, *chatContextRejection) {
	f, rejection := h.chatContextFeature(ref.FeatureID)
	if rejection != nil {
		return "", rejection
	}
	setup := f.Run().Setup
	if setup == nil {
		return "", chatContextHomeNotFound("setup task " + ref.TaskKey)
	}
	task, ok := setup.Tasks[ref.TaskKey]
	if !ok || task.Error == nil || task.Error.Code != errcat.Code(ref.Code) {
		return "", chatContextHomeNotFound("setup task " + ref.TaskKey)
	}
	logPaths := chatContextRecordLogPaths(task.Error)
	if path := strings.TrimSpace(setup.LatestLogPath); path != "" {
		logPaths = appendChatContextLogPath(logPaths, path)
	}
	return buildChatContextBundle(ref, f.ID, f.Name, errcat.RenderRecord(*task.Error), logPaths), nil
}

func (h *apiHandler) resolveChatContextRecovery(ref ChatContextReference) (string, *chatContextRejection) {
	items, ok := h.lookupRecoverySnapshot(ref.SnapshotID)
	if !ok {
		return "", chatContextHomeNotFound("recovery snapshot")
	}
	var item ports.RecoveryItem
	found := false
	for _, candidate := range items {
		if ports.RecoveryActionKey(candidate.PIDFile.FeatureID, candidate.RepoName) == ref.Key {
			item, found = candidate, true
			break
		}
	}
	if !found {
		return "", chatContextHomeNotFound("recovery item " + ref.Key)
	}
	rendered := orphanSessionRenderedError(item)
	if rendered.Code != errcat.Code(ref.Code) {
		return "", chatContextHomeNotFound("recovery item " + ref.Key)
	}
	featureID, featureName := item.PIDFile.FeatureID, ""
	if item.Feature != nil {
		featureName = item.Feature.Name
	}
	var logPaths []string
	if path := strings.TrimSpace(item.PIDFile.LogPath); path != "" {
		logPaths = appendChatContextLogPath(logPaths, path)
	}
	return buildChatContextBundle(ref, featureID, featureName, rendered, logPaths), nil
}

// chatContextFeature loads the referenced feature, mapping an unknown or
// unloadable feature to the invalid-reference rejection.
func (h *apiHandler) chatContextFeature(featureID string) (*feature.Feature, *chatContextRejection) {
	if h.store == nil {
		return nil, chatContextFeatureInvalid(featureID)
	}
	f, err := h.store.Load(featureID)
	if err != nil || f == nil {
		return nil, chatContextFeatureInvalid(featureID)
	}
	return f, nil
}

// chatContextRecordLogPaths collects the absolute log paths a stored
// record's command block knows.
func chatContextRecordLogPaths(rec *errcat.FailureRecord) []string {
	var paths []string
	if rec == nil || rec.Context == nil || rec.Context.Command == nil {
		return nil
	}
	for _, path := range rec.Context.Command.LogPaths {
		if path = strings.TrimSpace(path); path != "" {
			paths = appendChatContextLogPath(paths, path)
		}
	}
	return paths
}

// appendChatContextLogPath appends a log path, keeping first-seen order and
// dropping duplicates.
func appendChatContextLogPath(paths []string, path string) []string {
	for _, existing := range paths {
		if existing == path {
			return paths
		}
	}
	return append(paths, path)
}

// buildChatContextBundle renders the hidden context bundle as text: a
// heading naming the scope and the feature (name and ID), the catalog
// plain-text rendering of the stored record — heading, summary, hint,
// context lines, and the full stored diagnostics — and the absolute log
// locations known for the home, omitted when empty. No log content is
// inlined.
func buildChatContextBundle(ref ChatContextReference, featureID, featureName string, rendered errcat.Error, logPaths []string) string {
	var b strings.Builder
	if featureName = strings.TrimSpace(featureName); featureName != "" {
		fmt.Fprintf(&b, "Chat context — %s error on feature %q (%s)\n\n", ref.Scope, featureName, featureID)
	} else {
		fmt.Fprintf(&b, "Chat context — %s error on feature %s\n\n", ref.Scope, featureID)
	}
	if err := errcat.Fprint(&b, rendered); err != nil {
		// Fprint to a strings.Builder cannot fail; keep the bundle anyway.
		fmt.Fprintf(&b, "error[%s]: %s\n  %s\n", rendered.Code, rendered.Title, rendered.Summary)
	}
	if len(logPaths) > 0 {
		b.WriteString("\nLog locations:\n")
		for _, path := range logPaths {
			fmt.Fprintf(&b, "- %s\n", path)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
