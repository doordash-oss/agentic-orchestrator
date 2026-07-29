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
	"fmt"
	"net/http"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/workspace"
)

// providerReadinessProbeTimeout bounds a single provider readiness probe
// (typically a `<cli> auth status`-style command).
const providerReadinessProbeTimeout = 5 * time.Second

// maxReadinessTextLen bounds provider-supplied detail/remedy strings before
// they cross the API boundary.
const maxReadinessTextLen = 240

// errCodeNotReady is the structured API error code returned when a mutation
// is rejected because mandatory runtime readiness is not satisfied.
const errCodeNotReady = "not_ready"

// handleReadiness serves GET /api/v1/readiness. It reuses the cached provider
// probe results (probing lazily only on the very first request) so polling
// clients never pay a provider-CLI probe per request; POST
// /api/v1/readiness/refresh forces a re-probe.
func (h *apiHandler) handleReadiness(w http.ResponseWriter, r *http.Request) {
	snapshot := h.readinessSnapshot(r.Context(), false)
	revision := revisionForAny(snapshot)
	snapshot.Meta = h.responseMeta(revision)
	h.writeRevisionedJSON(w, r, revision, snapshot)
}

// handleReadinessRefreshRoute serves POST /api/v1/readiness/refresh: it
// re-runs the provider probes (e.g. after the user completed an external
// authentication flow), updates active model routing, publishes an
// invalidation event when readiness changed, and returns the fresh snapshot.
func (h *apiHandler) handleReadinessRefreshRoute(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if !h.requireTrustedMutation(w, r) {
		return
	}
	var req map[string]any
	if !decodeMutationJSON(w, r, &req) {
		return
	}
	snapshot := h.readinessSnapshot(r.Context(), true)
	revision := revisionForAny(snapshot)
	snapshot.Meta = h.responseMeta(revision)
	writeJSON(w, http.StatusOK, snapshot)
}

// rejectNotReadyForCreation gates feature creation on mandatory readiness.
// While no provider is usable (or the configuration is invalid) it writes a
// structured 409 not_ready error carrying the outstanding readiness issues
// and reports true. Handlers constructed without a provider registry (tests,
// embedded uses) are not gated.
func (h *apiHandler) rejectNotReadyForCreation(w http.ResponseWriter, r *http.Request) bool {
	if h.registry == nil {
		return false
	}
	snapshot := h.readinessSnapshot(r.Context(), false)
	if snapshot.Ready {
		return false
	}
	writeAPIError(w, http.StatusConflict, errCodeNotReady,
		"runtime is not ready to create features", map[string]any{"issues": snapshot.Issues})
	return true
}

// readinessSnapshot assembles the consolidated readiness response. Provider
// probe results are cached; forceProbe re-runs them. The cheap sections
// (models, configuration, workspace) are recomputed on every call.
func (h *apiHandler) readinessSnapshot(ctx context.Context, forceProbe bool) ReadinessResponse {
	providers, probedAt := h.providerReadinessStatuses(ctx, forceProbe)
	cfg := h.configOrDefault()
	resp := ReadinessResponse{
		APIVersion:    APIVersion,
		Providers:     providers,
		Models:        h.modelReadiness(),
		Configuration: configurationReadiness(cfg),
		Workspace:     workspaceReadiness(cfg),
	}
	if !probedAt.IsZero() {
		at := probedAt
		resp.ProbedAt = &at
	}
	resp.Ready = anyProviderReady(providers) && resp.Models.Available && resp.Configuration.Valid
	resp.Issues = flattenReadinessIssues(resp)
	return resp
}

// providerReadinessStatuses returns the per-provider readiness entries. The
// first call (and every forced refresh) probes provider CLIs and narrows the
// registry's active model routing to the usable providers; later calls reuse
// the cached result. A change in provider readiness is published to the SSE
// stream as a runtime invalidation event.
func (h *apiHandler) providerReadinessStatuses(ctx context.Context, force bool) ([]ProviderReadiness, time.Time) {
	if h.registry == nil {
		return []ProviderReadiness{}, time.Time{}
	}
	h.readinessMu.Lock()
	defer h.readinessMu.Unlock()
	if !force && !h.readinessProbedAt.IsZero() {
		return append([]ProviderReadiness(nil), h.providerReadiness...), h.readinessProbedAt
	}

	all := h.registry.All()
	probes := make([]ProviderReadiness, 0, len(all))
	var ready []llm.LLMProvider
	for _, p := range all {
		status := probeProviderReadiness(ctx, p)
		probes = append(probes, status)
		if status.Ready {
			ready = append(ready, p)
		}
	}
	changed := !reflect.DeepEqual(probes, h.providerReadiness)
	h.providerReadiness = probes
	h.readinessProbedAt = time.Now().UTC()
	h.registry.RestrictToProviders(ready)
	if changed && h.broker != nil {
		h.broker.publish(snapshotRequiredEventDTO(sseEventLifecycleUpdated, ResourceDTO{Type: resourceTypeRuntime}))
	}
	return append([]ProviderReadiness(nil), probes...), h.readinessProbedAt
}

// refreshProviderReadiness re-probes one named provider while preserving the
// cached status of every other provider. Callers initialize the cache through
// providerReadinessStatuses before invoking it.
func (h *apiHandler) refreshProviderReadiness(ctx context.Context, providerName string) (ProviderReadiness, bool) {
	if h.registry == nil {
		return ProviderReadiness{}, false
	}
	provider := h.registry.ByName(providerName)
	if provider == nil {
		return ProviderReadiness{}, false
	}

	h.readinessMu.Lock()
	defer h.readinessMu.Unlock()

	refreshed := probeProviderReadiness(ctx, provider)
	probes := append([]ProviderReadiness(nil), h.providerReadiness...)
	replaced := false
	for i := range probes {
		if probes[i].Name == providerName {
			probes[i] = refreshed
			replaced = true
			break
		}
	}
	if !replaced {
		probes = append(probes, refreshed)
	}

	changed := !reflect.DeepEqual(probes, h.providerReadiness)
	h.providerReadiness = probes
	h.readinessProbedAt = time.Now().UTC()

	ready := make([]llm.LLMProvider, 0, len(probes))
	for _, status := range probes {
		if status.Ready {
			if candidate := h.registry.ByName(status.Name); candidate != nil {
				ready = append(ready, candidate)
			}
		}
	}
	h.registry.RestrictToProviders(ready)
	if changed && h.broker != nil {
		h.broker.publish(snapshotRequiredEventDTO(sseEventLifecycleUpdated, ResourceDTO{Type: resourceTypeRuntime}))
	}
	return refreshed, true
}

// probeProviderReadiness classifies one provider against the readiness
// taxonomy: missing executable, unsupported version, unauthenticated, or
// ready. Detail/remedy text is bounded and never includes credentials — the
// remedy is the provider's own install hint or auth command.
func probeProviderReadiness(ctx context.Context, p llm.LLMProvider) ProviderReadiness {
	out := ProviderReadiness{Name: p.Name()}
	if !p.DetectCLI() {
		out.Issue = &ReadinessIssue{
			Code:    MissingExecutable,
			Message: p.Name() + " CLI was not found",
			Remedy:  "Install with: " + p.InstallHint(),
		}
		return out
	}
	out.Installed = true
	if version, err := p.VersionInfo(); err == nil {
		out.Version = SafeDisplayText(strings.TrimSpace(version), maxReadinessTextLen)
	}
	if enforcer, ok := p.(llm.VersionEnforcer); ok && enforcer.EnforcesMinVersion() {
		if below, version, minVer := agent.BelowMinVersion(p); below {
			out.Issue = &ReadinessIssue{
				Code: UnsupportedVersion,
				Message: fmt.Sprintf("%s CLI version %s is below the required minimum %d.%d.%d",
					p.Name(), version, minVer[0], minVer[1], minVer[2]),
				Remedy: "Upgrade with: " + p.InstallHint(),
			}
			return out
		}
	}
	checker, ok := p.(llm.ReadinessChecker)
	if !ok {
		out.Ready = true
		return out
	}
	probeCtx, cancel := context.WithTimeout(ctx, providerReadinessProbeTimeout)
	defer cancel()
	status := checker.CheckReadiness(probeCtx)
	if status.Ready {
		out.Ready = true
		return out
	}
	message := strings.TrimSpace(status.Detail)
	if message == "" {
		message = p.Name() + " provider is not authenticated"
	}
	out.Issue = &ReadinessIssue{
		Code:    Unauthenticated,
		Message: SafeDisplayText(message, maxReadinessTextLen),
		Remedy:  SafeDisplayText(strings.TrimSpace(status.Remedy), maxReadinessTextLen),
	}
	return out
}

func anyProviderReady(providers []ProviderReadiness) bool {
	for _, p := range providers {
		if p.Ready {
			return true
		}
	}
	return false
}

// modelReadiness reports whether any usable provider exposes models. The
// registry's active-provider filter (maintained by the readiness probe) keeps
// unauthenticated providers' models out of this list.
func (h *apiHandler) modelReadiness() ModelReadiness {
	var models []string
	if h.registry != nil {
		models = h.registry.AvailableModels()
	}
	if len(models) > 0 {
		return ModelReadiness{Available: true, Models: models}
	}
	return ModelReadiness{
		Issue: &ReadinessIssue{
			Code:    ModelsUnavailable,
			Message: "no models are available from a usable provider",
			Remedy:  "Install or authenticate a provider CLI, then refresh readiness",
		},
	}
}

// configurationReadiness validates the loaded runtime configuration shape.
func configurationReadiness(cfg *config.Config) ConfigurationReadiness {
	if cfg == nil {
		return ConfigurationReadiness{Issue: &ReadinessIssue{
			Code:    InvalidConfiguration,
			Message: "runtime configuration is not loaded",
			Remedy:  "Restart the server with a readable configuration file",
		}}
	}
	if cfg.Defaults.Pipeline != "" && !feature.PipelineProfile(cfg.Defaults.Pipeline).IsValid() {
		return ConfigurationReadiness{Issue: &ReadinessIssue{
			Code:    InvalidConfiguration,
			Message: "defaults.pipeline must be medium, large, or moonshot",
			Remedy:  "Fix defaults.pipeline in the runtime configuration",
		}}
	}
	return ConfigurationReadiness{Valid: true}
}

// workspaceReadiness validates each configured workspace root and each
// configured/discovered repository. Paths reported here are the user's own
// configured locations (the same surface as the runtime-config endpoint).
func workspaceReadiness(cfg *config.Config) WorkspaceReadiness {
	out := WorkspaceReadiness{
		Roots:        []WorkspaceRootReadiness{},
		Repositories: []RepositoryReadiness{},
	}
	if cfg == nil {
		return out
	}
	for _, root := range cfg.WorkspaceRoots {
		entry := WorkspaceRootReadiness{Path: root}
		if info, err := os.Stat(workspace.ExpandHome(root)); err == nil && info.IsDir() {
			entry.Valid = true
		} else {
			entry.Issue = &ReadinessIssue{
				Code:    InvalidWorkspaceRoot,
				Message: "workspace root does not resolve to a directory",
				Remedy:  "Create the directory or update workspace_roots in the runtime configuration",
			}
		}
		out.Roots = append(out.Roots, entry)
	}
	snapshot := runtimeConfigRepoSnapshot(cfg)
	allRepos := config.AllRepos(snapshot)
	names := make([]string, 0, len(allRepos))
	for name := range allRepos {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		repo := allRepos[name]
		entry := RepositoryReadiness{Name: name, Path: repo.Path}
		if workspace.IsGitRepo(workspace.ExpandHome(repo.Path)) {
			entry.Valid = true
		} else {
			entry.Issue = &ReadinessIssue{
				Code:    InvalidRepository,
				Message: "configured repository path is not a git repository",
				Remedy:  "Point the repository at a git checkout or initialize the directory as a repository",
			}
		}
		out.Repositories = append(out.Repositories, entry)
	}
	return out
}

// flattenReadinessIssues collects every outstanding issue across all
// readiness sections into one ordered list.
func flattenReadinessIssues(resp ReadinessResponse) []ReadinessIssue {
	var issues []ReadinessIssue
	for _, p := range resp.Providers {
		if p.Issue != nil {
			issues = append(issues, *p.Issue)
		}
	}
	if resp.Models.Issue != nil {
		issues = append(issues, *resp.Models.Issue)
	}
	if resp.Configuration.Issue != nil {
		issues = append(issues, *resp.Configuration.Issue)
	}
	for _, root := range resp.Workspace.Roots {
		if root.Issue != nil {
			issues = append(issues, *root.Issue)
		}
	}
	for _, repo := range resp.Workspace.Repositories {
		if repo.Issue != nil {
			issues = append(issues, *repo.Issue)
		}
	}
	return issues
}
