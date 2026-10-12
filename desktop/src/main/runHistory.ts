/*
Copyright 2026 DoorDash, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

/**
 * Main-process run-history and rewind operations. Everything talks to the
 * authoritative server through the runtime gateway's bearer transport and
 * returns strict renderer-facing views; nothing here caches server-domain
 * data, reads runtime files, or lets the renderer compose REST paths.
 */
import { redactText, redactedCanonicalError } from '../shared/errors';
import {
  ArtifactListResponseSchema,
  RunLogListResponseSchema,
  RunDetailResponseSchema,
  RunListResponseSchema,
  RunSessionListResponseSchema,
  RewindPreviewResponseSchema,
  RewindActionResponseSchema,
  TextContentResponseSchema,
  LivePreviewResponseSchema,
  validateWithSchema,
  type ServerRunSummary,
  type ServerRunDetail,
  type ServerArtifact,
} from '../shared/api/parse';
import {
  FeatureIdSchema,
  RunListRequestSchema,
  RunGetRequestSchema,
  RunArtifactsListRequestSchema,
  RunArtifactContentRequestSchema,
  RunLogContentRequestSchema,
  RewindPreviewRequestSchema,
  RewindExecuteRequestSchema,
  type FeatureActionResult,
  type RunListResult,
  type RunDetailView,
  type RunSessionsListResult,
  type RunArtifactsListResult,
  type RunLogsListResult,
  type LivePreviewView,
  type RunTextContent,
  type RewindPreviewView,
  type RunSummaryView,
} from '../shared/ipc';
import type { ApiRequestInit } from './gateway/runtimeGateway';
import {
  serverRequest,
  toSessionSummary,
  toTranscriptMessage,
  type ServerTransport,
} from './serverClient';
import { defined } from '../shared/defined';

type RunHistoryTransport = ServerTransport;

export class RunHistoryService {
  constructor(private readonly transport: RunHistoryTransport) {}

  async listRuns(request: unknown): Promise<RunListResult> {
    const input = validateWithSchema(request, RunListRequestSchema);
    const id = validateWithSchema(input.featureId, FeatureIdSchema);
    const params = new URLSearchParams();
    params.set('page', String(input.page ?? 1));
    params.set('page_size', String(input.pageSize ?? 20));
    const body = await this.api(`/api/v1/features/${id}/runs?${params}`);
    const response = validateWithSchema(body, RunListResponseSchema);
    return {
      runs: response.runs.map(toRunSummaryView),
      page: response.page,
      pageSize: response.page_size,
      total: response.total,
      totalPages: response.total_pages,
    };
  }

  async getRun(request: unknown): Promise<RunDetailView> {
    const input = validateWithSchema(request, RunGetRequestSchema);
    const id = validateWithSchema(input.featureId, FeatureIdSchema);
    const body = await this.api(`/api/v1/features/${id}/runs/${input.runNumber}`);
    const response = validateWithSchema(body, RunDetailResponseSchema);
    return toRunDetailView(response.run);
  }

  async listRunSessions(request: unknown): Promise<RunSessionsListResult> {
    const input = validateWithSchema(request, RunGetRequestSchema);
    const id = validateWithSchema(input.featureId, FeatureIdSchema);
    const body = await this.api(`/api/v1/features/${id}/runs/${input.runNumber}/sessions`);
    const response = validateWithSchema(body, RunSessionListResponseSchema);
    return {
      runNumber: response.run_number,
      sessions: response.sessions.map(toSessionSummary),
    };
  }

  async getLivePreview(featureId: unknown): Promise<LivePreviewView> {
    const id = validateWithSchema(featureId, FeatureIdSchema);
    const body = await this.api(`/api/v1/features/${id}/live-preview`);
    const response = validateWithSchema(body, LivePreviewResponseSchema);
    return {
      featureId: validateWithSchema(response.feature.id, FeatureIdSchema),
      activity: redactText(response.activity),
      ...defined(
        'session',
        response.session === undefined ? undefined : toSessionSummary(response.session),
      ),
      contextPercentage: response.context.percentage,
      totalSeconds: response.timing.total_seconds,
      totalUsd: response.cost.total_usd,
      transcript: response.transcript.map(toTranscriptMessage),
    };
  }

  async listRunArtifacts(request: unknown): Promise<RunArtifactsListResult> {
    const input = validateWithSchema(request, RunArtifactsListRequestSchema);
    const id = validateWithSchema(input.featureId, FeatureIdSchema);
    const body = await this.api(`/api/v1/features/${id}/runs/${input.runNumber}/artifacts`);
    const response = validateWithSchema(body, ArtifactListResponseSchema);
    return {
      artifacts: response.artifacts.map(toArtifactView),
    };
  }

  async listRunLogs(request: unknown): Promise<RunLogsListResult> {
    const input = validateWithSchema(request, RunArtifactsListRequestSchema);
    const id = validateWithSchema(input.featureId, FeatureIdSchema);
    const body = await this.api(`/api/v1/features/${id}/runs/${input.runNumber}/logs`);
    const response = validateWithSchema(body, RunLogListResponseSchema);
    return {
      logs: response.logs.map((log) => ({
        id: log.id,
        path: redactText(log.path),
        size: log.size,
        modifiedAt: log.modified_at,
      })),
    };
  }

  async getRunArtifactContent(request: unknown): Promise<RunTextContent> {
    const input = validateWithSchema(request, RunArtifactContentRequestSchema);
    return this.getBoundedTextContent(input, 'artifacts', input.artifactId);
  }

  async getRunLogContent(request: unknown): Promise<RunTextContent> {
    const input = validateWithSchema(request, RunLogContentRequestSchema);
    return this.getBoundedTextContent(input, 'logs', input.logId);
  }

  private async getBoundedTextContent(
    input: { featureId: string; runNumber: number; offset?: number; limit?: number },
    kind: 'artifacts' | 'logs',
    contentID: string,
  ): Promise<RunTextContent> {
    const id = validateWithSchema(input.featureId, FeatureIdSchema);
    const params = new URLSearchParams();
    if (input.offset !== undefined) params.set('offset', String(input.offset));
    if (input.limit !== undefined) params.set('limit', String(input.limit));
    const suffix = params.toString() ? `?${params}` : '';
    const body = await this.api(
      `/api/v1/features/${id}/runs/${input.runNumber}/${kind}/${encodeURIComponent(contentID)}${suffix}`,
    );
    const response = validateWithSchema(body, TextContentResponseSchema);
    return {
      id: response.id,
      offset: response.offset,
      limit: response.limit,
      size: response.size,
      text: redactText(response.text),
      truncated: response.truncated,
    };
  }

  async getRewindPreview(request: unknown): Promise<RewindPreviewView> {
    const input = validateWithSchema(request, RewindPreviewRequestSchema);
    const id = validateWithSchema(input.featureId, FeatureIdSchema);
    const params = new URLSearchParams();
    params.set('target_phase', input.targetPhase);
    if (input.roadmapPhase !== undefined) params.set('roadmap_phase', String(input.roadmapPhase));
    if (input.upgradePipeline !== undefined) params.set('upgrade_pipeline', input.upgradePipeline);
    const body = await this.api(`/api/v1/features/${id}/rewind/preview?${params}`);
    const response = validateWithSchema(body, RewindPreviewResponseSchema);
    return {
      eligible: response.eligible,
      sourceRunNumber: response.source_run_number,
      sourceRevision: response.source_revision,
      targetPhase: response.target_phase,
      effectivePhase: response.effective_phase,
      ...defined('roadmapPhase', response.roadmap_phase),
      ...(response.upgrade_pipeline !== undefined && response.upgrade_pipeline !== ''
        ? { upgradePipeline: response.upgrade_pipeline }
        : {}),
      ...(response.valid_phases !== undefined
        ? {
            validPhases: response.valid_phases.map((c) => ({
              phase: c.phase,
              ...(c.escalates_to !== undefined && c.escalates_to !== ''
                ? { escalatesTo: c.escalates_to }
                : {}),
              ...(c.override_phase !== undefined && c.override_phase !== ''
                ? { overridePhase: c.override_phase }
                : {}),
            })),
          }
        : {}),
      ...defined('validRoadmapPhases', response.valid_roadmap_phases),
      ...defined('upgradePipelineOptions', response.upgrade_pipeline_options),
      ...defined('carriedPhases', response.carried_phases),
      ...defined('carriedFromRun', response.carried_from_run),
      ...defined(
        'prConsequences',
        response.pr_consequences === undefined
          ? undefined
          : response.pr_consequences.map((p) => ({
              repo: p.repo,
              prUrl: p.pr_url,
            })),
      ),
      ...defined(
        'worktreeConsequences',
        response.worktree_consequences === undefined
          ? undefined
          : response.worktree_consequences.map((w) => ({
              repo: w.repo,
              resetKind: w.reset_kind,
            })),
      ),
      ...defined('backupBranchRepos', response.backup_branch_repos),
      ...defined('validationFindings', response.validation_findings),
    };
  }

  async executeRewind(request: unknown): Promise<FeatureActionResult> {
    const input = validateWithSchema(request, RewindExecuteRequestSchema);
    const id = validateWithSchema(input.featureId, FeatureIdSchema);
    const body: Record<string, unknown> = {
      target_phase: input.targetPhase,
    };
    if (input.roadmapPhase !== undefined) body.roadmap_phase = input.roadmapPhase;
    if (input.upgradePipeline !== undefined) body.upgrade_pipeline = input.upgradePipeline;
    if (input.sourceRunNumber !== undefined) body.source_run_number = input.sourceRunNumber;
    if (input.sourceRevision !== undefined) body.source_revision = input.sourceRevision;
    const response = await this.api(`/api/v1/features/${id}/actions/rewind`, {
      method: 'POST',
      body,
    });
    const result = validateWithSchema(response, RewindActionResponseSchema);
    return {
      featureId: validateWithSchema(result.feature_id, FeatureIdSchema),
      action: 'rewind',
      result: result.result,
      ...defined('sourceRunNumber', result.source_run_number),
      ...defined('newRunNumber', result.new_run_number),
      // Canonical warning objects cross IPC intact except diagnostics,
      // which pass through the same redaction as every other raw text.
      ...defined(
        'warnings',
        result.warnings === undefined ? undefined : result.warnings.map(redactedCanonicalError),
      ),
    };
  }

  private api(path: string, init?: ApiRequestInit): Promise<unknown> {
    return serverRequest(this.transport, path, init);
  }
}

function toRunSummaryView(run: ServerRunSummary): RunSummaryView {
  return {
    runNumber: run.run_number,
    ...defined('startedAt', run.started_at),
    ...defined('sealedAt', run.sealed_at),
    ...defined('sealReason', run.seal_reason),
    ...defined('currentPhase', run.current_phase),
    ...defined('phaseStatus', run.phase_status),
    ...defined('iteration', run.iteration),
    ...defined('roadmapPhase', run.roadmap_phase),
    ...defined('roadmapTotal', run.roadmap_total),
    ...defined('pendingReviewPhase', run.pending_review_phase),
    ...defined('isRewind', run.is_rewind),
    artifactCount: run.artifact_count,
    ...defined('hasNeedUserGate', run.has_need_user_gate),
  };
}

function toRunDetailView(run: ServerRunDetail): RunDetailView {
  const base = toRunSummaryView(run);
  return {
    ...base,
    ...defined('rewindTarget', run.rewind_target),
    ...defined('rewindRoadmapPhase', run.rewind_roadmap_phase),
    ...defined('carriedFromRun', run.carried_from_run),
    ...defined('carriedPhases', run.carried_phases),
    ...defined('backupBranchRepos', run.backup_branch_repos),
    ...defined('committing', run.committing),
    ...defined(
      'timing',
      run.timing === undefined
        ? undefined
        : { totalSeconds: run.timing.total_seconds, byPhase: run.timing.by_phase },
    ),
    ...defined(
      'cost',
      run.cost === undefined
        ? undefined
        : { totalUsd: run.cost.total_usd, byPhase: run.cost.by_phase },
    ),
  };
}

function toArtifactView(artifact: ServerArtifact): RunArtifactsListResult['artifacts'][number] {
  return {
    id: artifact.id,
    ...defined('type', artifact.type),
    ...defined('category', artifact.category),
    runNumber: artifact.run_number,
    ...defined('phase', artifact.phase),
    ...defined('size', artifact.size),
    ...defined('modifiedAt', artifact.modified_at),
    ...defined('contentAvailable', artifact.content_available),
  };
}
