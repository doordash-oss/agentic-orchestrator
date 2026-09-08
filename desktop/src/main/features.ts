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
 * Main-process feature operations. Everything talks to the authoritative
 * server through the runtime gateway's bearer transport and returns strict
 * renderer-facing views; nothing here caches server-domain data, reads
 * runtime files, or lets the renderer compose REST paths. Creating a feature
 * queues durable setup but does NOT dispatch it — callers must dispatch the
 * `setup` action afterwards. Runtime lifecycle and completion mutations remain
 * separate, explicitly allowlisted operations dispatched through
 * `dispatchAction`.
 */
import {
  CanonicalErrorException,
  isRequestTimeout,
  redactText,
  redactedCanonicalError,
  requiresLocalServerError,
} from '../shared/errors';
import {
  FeatureActionResponseSchema,
  ServerFeatureOperationalActionResponseSchema,
  FeatureDetailResponseSchema,
  FeatureListResponseSchema,
  PublishDescriptionResponseSchema,
  RuntimeConfigCreationSchema,
  RebaseFeatureResponseSchema,
  RefactorFeatureResponseSchema,
  DiscardChildResponseSchema,
  DeleteFeatureResponseSchema,
  ReviewFeedbackFetchResponseSchema,
  ReviewFeedbackSelectionResponseSchema,
  ReviewFeedbackFeatureResponseSchema,
  validateWithSchema,
  type ServerFeatureDetail,
  type ServerOwnedError,
  type ServerRelationshipChild,
  type ServerReviewFeedbackComment,
  type ServerReviewFeedbackDraftComment,
  type ServerSetup,
} from '../shared/api/parse';
import {
  CreateFeatureInputSchema,
  FeatureActionRequestSchema,
  FeatureIdSchema,
  FeatureSetupStatusSchema,
  LaunchRebaseChildRequestSchema,
  LaunchRefactorChildRequestSchema,
  DiscardRefactorChildRequestSchema,
  DeleteFeatureCascadeRequestSchema,
  FetchReviewFeedbackRequestSchema,
  UpdateReviewFeedbackSelectionRequestSchema,
  LaunchReviewFeedbackChildRequestSchema,
  type CreateFeatureInput,
  type CreateFeatureResult,
  type CreationDefaults,
  type EffortLevel,
  type FeatureSetupView,
  type FeatureSnapshot,
  type FeaturesListResult,
  type FeatureActionRequest,
  type FeatureActionResult,
  type OwnedError,
  type PublishDescriptionResult,
  type ReadinessSnapshot,
  type RepositoryFileRef,
  type LaunchRebaseChildRequest,
  type LaunchRebaseChildResult,
  type LaunchRefactorChildRequest,
  type LaunchRefactorChildResult,
  type DiscardRefactorChildRequest,
  type DiscardRefactorChildResult,
  type DeleteFeatureCascadeRequest,
  type DeleteFeatureCascadeResult,
  type FetchReviewFeedbackRequest,
  type FetchReviewFeedbackResult,
  type UpdateReviewFeedbackSelectionRequest,
  type UpdateReviewFeedbackSelectionResult,
  type LaunchReviewFeedbackChildRequest,
  type LaunchReviewFeedbackChildResult,
  type ReviewFeedbackCommentView,
  type ReviewFeedbackDraftCommentView,
  type ReviewFeedbackRepoGroup,
  type SetupDispatchResult,
  type SetupTaskView,
} from '../shared/ipc';
import type { ApiRequestInit } from './gateway/runtimeGateway';
import { alwaysLocal, type LocalitySource } from './locality';
import { serverRequest, type ServerTransport } from './serverClient';

/** The authenticated transport surface the gateway provides. */
export type FeatureTransport = ServerTransport;

export interface FeatureServiceDeps {
  transport: FeatureTransport;
  /** Fresh authoritative readiness (repository eligibility for creation). */
  readReadiness(): Promise<ReadinessSnapshot>;
  resolveRepositoryFiles(refs: readonly RepositoryFileRef[]): Promise<string[]>;
  /**
   * Gateway-owned locality of the active connection. While remote, submit
   * boundaries refuse any locally staged image/attachment/repository-file
   * path with E_REQUIRES_LOCAL_SERVER (a stale renderer draft must fail,
   * never leak a local path) and forward staged upload references instead.
   */
  locality?: LocalitySource;
}

const PHASE_MODEL_LABELS: ReadonlyArray<readonly [string, string]> = [
  ['inquiry', 'Inquiry'],
  ['research', 'Research'],
  ['planning', 'Planning'],
  ['implementation', 'Implementation'],
  ['review', 'Review'],
  ['utilities', 'Utilities'],
  ['kb_build', 'Knowledge base'],
];

const EFFORT_LEVELS = new Set<EffortLevel>([
  'auto',
  'low',
  'medium',
  'high',
  'xhigh',
  'max',
  'ultra',
]);

/**
 * Remote submit guard: while remotely connected, a locally shaped path
 * payload (images/attachments/repository-file refs) fails with the locality
 * error rather than leaking a path the server cannot read. Local payloads
 * and staged upload references pass through untouched.
 */
function assertNoLocalPathsRemotely(remote: boolean, ...groups: readonly string[][]): void {
  if (!remote) return;
  for (const group of groups) {
    if (group.length > 0) {
      throw new CanonicalErrorException(requiresLocalServerError());
    }
  }
}

// Description generation is a synchronous utility LLM session. Its session
// idle bounds are five minutes, so leave transport cleanup time beyond that
// without weakening the 30-second default for ordinary API calls.
const PUBLISH_DESCRIPTION_TIMEOUT_MS = 6 * 60_000;

// Publish and merge run non-idempotent multi-repository git and forge work
// (commit, push, then pull-request create or update per repository), which
// legitimately takes minutes. The 30-second default would abort a request whose
// server-side work is still progressing, so these carry their own bound.
const LONG_MUTATION_TIMEOUT_MS = 10 * 60_000;
const LONG_MUTATION_ACTIONS: ReadonlySet<string> = new Set(['publish', 'merge']);

export class FeatureService {
  private readonly actionFlights = new Map<string, Promise<FeatureActionResult>>();
  private readonly locality: LocalitySource;

  constructor(private readonly deps: FeatureServiceDeps) {
    this.locality = deps.locality ?? alwaysLocal;
  }

  /**
   * Fresh creation context in one main-process composition: repository
   * eligibility from readiness discovery plus the server-side defaults the
   * creation contract applies.
   */
  async creationDefaults(): Promise<CreationDefaults> {
    const [configBody, readiness] = await Promise.all([
      this.api('/api/v1/config/runtime'),
      this.deps.readReadiness(),
    ]);
    const config = validateWithSchema(configBody, RuntimeConfigCreationSchema);
    const models = PHASE_MODEL_LABELS.flatMap(([key, label]) => {
      const model =
        config.feature_defaults.models[key as keyof typeof config.feature_defaults.models];
      return model === undefined || model === '' ? [] : [{ phase: label, model }];
    });
    const effort = PHASE_MODEL_LABELS.flatMap(([key, label]) => {
      const value =
        config.feature_defaults.effort?.[key as keyof typeof config.feature_defaults.effort];
      return value === undefined || !EFFORT_LEVELS.has(value as EffortLevel)
        ? []
        : [{ phase: label, effort: value as EffortLevel }];
    });
    return {
      repositories: readiness.repositories,
      defaults: {
        ...(config.feature_defaults.pipeline === undefined ||
        config.feature_defaults.pipeline === ''
          ? {}
          : { pipeline: config.feature_defaults.pipeline }),
        ...(config.feature_defaults.inquireness === undefined ||
        config.feature_defaults.inquireness === ''
          ? {}
          : { inquireness: config.feature_defaults.inquireness }),
        models,
        effort,
        // The creation contract's server default: a new feature branch.
        useCurrentBranch: false,
      },
    };
  }

  /**
   * Creates exactly one durable feature. The server queues durable setup;
   * dispatching it is a separate, explicit action (dispatchSetup).
   */
  async createFeature(input: CreateFeatureInput): Promise<CreateFeatureResult> {
    // Defense in depth: the IPC layer already validated this shape.
    const validated = validateWithSchema(input, CreateFeatureInputSchema);
    const remote = this.locality() === 'remote';
    // Remote submit boundary: refuse locally staged paths outright (a
    // pre-switch draft must fail, not leak a path the server cannot read);
    // staged upload references travel as image_uploads/attachment_uploads.
    assertNoLocalPathsRemotely(
      remote,
      validated.images,
      validated.attachments,
      // Repository-file references resolve to local paths; refuse remotely.
      validated.repositoryFiles.map((ref) => `${ref.repoKey}:${ref.path}`),
    );
    const repositoryAttachments = remote
      ? []
      : await this.deps.resolveRepositoryFiles(validated.repositoryFiles);
    const body = await this.api('/api/v1/features', {
      method: 'POST',
      body: {
        name: validated.name.trim(),
        ...(validated.description.trim() === '' ? {} : { description: validated.description }),
        repos: validated.repoKeys,
        ...(validated.useCurrentBranch ? { use_current_branch: true } : {}),
        images: validated.images,
        attachments: [...validated.attachments, ...repositoryAttachments],
        ...(remote
          ? {
              image_uploads: validated.imageUploads,
              attachment_uploads: validated.attachmentUploads,
            }
          : {}),
        pipeline: validated.pipeline,
        risk_level: validated.riskLevel,
        inquireness: validated.inquireness,
        ...(validated.exitCriteria.trim() === ''
          ? {}
          : { exit_criteria: validated.exitCriteria.trim() }),
        models: validated.models,
        effort: validated.effort,
        checkpoints: {
          inquiry_review: validated.checkpoints.inquiryReview,
          research_review: validated.checkpoints.researchReview,
          design_review: validated.checkpoints.designReview,
          roadmap_review: validated.checkpoints.roadmapReview,
          phase_plan_review: validated.checkpoints.phasePlanReview,
          manual_publish: validated.checkpoints.manualPublish,
          draft_publish: validated.checkpoints.draftPublish,
        },
        idempotency_key: validated.idempotencyKey,
      },
    });
    const response = validateWithSchema(body, FeatureActionResponseSchema);
    return { featureId: validateWithSchema(response.feature_id, FeatureIdSchema) };
  }

  /**
   * Dispatches durable setup for a created feature, or retries only the
   * unfinished tasks of a failed one. Never starts orchestration.
   */
  async dispatchSetup(featureId: string): Promise<SetupDispatchResult> {
    const id = validateWithSchema(featureId, FeatureIdSchema);
    const body = await this.api(`/api/v1/features/${id}/actions/setup`, {
      method: 'POST',
      body: {},
    });
    const response = validateWithSchema(body, FeatureActionResponseSchema);
    return { result: response.result };
  }

  async generatePublishDescription(
    featureId: string,
    repos: string[] = [],
  ): Promise<PublishDescriptionResult> {
    const id = validateWithSchema(featureId, FeatureIdSchema);
    const body = await this.api(`/api/v1/features/${id}/actions/publish/description`, {
      method: 'POST',
      body: repos.length === 0 ? {} : { repos },
      timeoutMs: PUBLISH_DESCRIPTION_TIMEOUT_MS,
    });
    const response = validateWithSchema(body, PublishDescriptionResponseSchema);
    return {
      featureId: validateWithSchema(response.feature_id, FeatureIdSchema),
      title: redactText(response.title).slice(0, 200),
      body: redactText(response.body).slice(0, 4000),
    };
  }

  async listFeatures(): Promise<FeaturesListResult> {
    const body = await this.api('/api/v1/features');
    const response = validateWithSchema(body, FeatureListResponseSchema);
    return {
      features: response.features.map((feature) => ({
        id: validateWithSchema(feature.id, FeatureIdSchema),
        name: feature.name,
        status: feature.status,
        currentPhase: feature.current_phase,
        repos: feature.repos,
        createdAt: feature.created_at,
        activeRun: feature.active_run,
        runCount: feature.run_count,
        ...(feature.progress.current_phase_status === undefined
          ? {}
          : { phaseStatus: feature.progress.current_phase_status }),
        // Canonical warning objects cross IPC intact except diagnostics,
        // which pass through the same redaction as every other raw text.
        warnings: (feature.warnings ?? []).map(redactedCanonicalError),
        errors: (feature.errors ?? []).map(toOwnedErrorView),
        ...(feature.active_child === undefined
          ? {}
          : { activeChild: toRelationshipChildView(feature.active_child) }),
        ...(feature.child_history === undefined
          ? {}
          : { childHistory: feature.child_history.map(toRelationshipChildView) }),
        ...(feature.child_history_total === undefined
          ? {}
          : { childHistoryTotal: feature.child_history_total }),
        ...(feature.child_history_truncated === undefined
          ? {}
          : { childHistoryTruncated: feature.child_history_truncated }),
      })),
      warnings: (response.warnings ?? []).map(redactedCanonicalError),
    };
  }

  /** Dispatches only allowlisted server-catalogue actions, single-flight per input. */
  async dispatchAction(request: FeatureActionRequest): Promise<FeatureActionResult> {
    const input = validateWithSchema(request, FeatureActionRequestSchema);
    const key = `${input.featureId}:${input.action}:${JSON.stringify('body' in input ? input.body : {})}`;
    const existing = this.actionFlights.get(key);
    if (existing !== undefined) return existing;
    const release = (): void => {
      if (this.actionFlights.get(key) === flight) this.actionFlights.delete(key);
    };
    const flight = this.runOperationalAction(input).then(
      (result) => {
        release();
        return result;
      },
      (error: unknown) => {
        // A timed-out mutation is still running server-side: keep the flight so
        // a second dispatch cannot launch a duplicate of it.
        if (!isRequestTimeout(error)) release();
        throw error;
      },
    );
    this.actionFlights.set(key, flight);
    return flight;
  }

  async getFeature(featureId: string): Promise<FeatureSnapshot> {
    const id = validateWithSchema(featureId, FeatureIdSchema);
    const body = await this.api(`/api/v1/features/${id}`);
    const response = validateWithSchema(body, FeatureDetailResponseSchema);
    return toSnapshot(response.feature);
  }

  async launchRebaseChild(request: LaunchRebaseChildRequest): Promise<LaunchRebaseChildResult> {
    const input = validateWithSchema(request, LaunchRebaseChildRequestSchema);
    // Zero-input child launch: the server accepts and ignores any body, so
    // nothing is sent beyond the feature id in the path. The response carries
    // the new child id, the parent id, and the action result — the same shape
    // the refactor and review-feedback launches return.
    const body = await this.api(`/api/v1/features/${input.featureId}/actions/rebase`, {
      method: 'POST',
      body: {},
    });
    const response = validateWithSchema(body, RebaseFeatureResponseSchema);
    return {
      childId: validateWithSchema(response.feature_id, FeatureIdSchema),
      parentId: validateWithSchema(response.parent_id, FeatureIdSchema),
      result: response.result,
    };
  }

  async launchRefactorChild(
    request: LaunchRefactorChildRequest,
  ): Promise<LaunchRefactorChildResult> {
    const input = validateWithSchema(request, LaunchRefactorChildRequestSchema);
    const remote = this.locality() === 'remote';
    // Remote submit boundary: refuse locally staged paths outright; staged
    // upload references travel as image_uploads/attachment_uploads.
    assertNoLocalPathsRemotely(
      remote,
      input.images ?? [],
      input.attachments ?? [],
      (input.repositoryFiles ?? []).map((ref) => `${ref.repoKey}:${ref.path}`),
    );
    // Referenced repository files travel as attachments, as in creation.
    const repositoryAttachments = remote
      ? []
      : await this.deps.resolveRepositoryFiles(input.repositoryFiles ?? []);
    const attachments = remote ? [] : [...(input.attachments ?? []), ...repositoryAttachments];
    const body = await this.api(`/api/v1/features/${input.parentId}/actions/refactor`, {
      method: 'POST',
      body: {
        name: input.name,
        ...(input.description === undefined ? {} : { description: input.description }),
        ...(remote || input.images === undefined ? {} : { images: input.images }),
        ...(attachments.length === 0 ? {} : { attachments }),
        ...(remote
          ? {
              ...(input.imageUploads === undefined ? {} : { image_uploads: input.imageUploads }),
              ...(input.attachmentUploads === undefined
                ? {}
                : { attachment_uploads: input.attachmentUploads }),
            }
          : {}),
        ...(input.pipeline === undefined ? {} : { pipeline: input.pipeline }),
        ...(input.checkpoints === undefined
          ? {}
          : {
              checkpoints: {
                inquiry_review: input.checkpoints.inquiryReview,
                research_review: input.checkpoints.researchReview,
                design_review: input.checkpoints.designReview,
                roadmap_review: input.checkpoints.roadmapReview,
                phase_plan_review: input.checkpoints.phasePlanReview,
                manual_publish: input.checkpoints.manualPublish,
                draft_publish: input.checkpoints.draftPublish,
              },
            }),
        ...(input.effort === undefined ? {} : { effort: input.effort }),
        ...(input.models === undefined ? {} : { models: input.models }),
        ...(input.riskLevel === undefined ? {} : { risk_level: input.riskLevel }),
        ...(input.exitCriteria === undefined ? {} : { exit_criteria: input.exitCriteria }),
        ...(input.inquireness === undefined ? {} : { inquireness: input.inquireness }),
      },
    });
    const response = validateWithSchema(body, RefactorFeatureResponseSchema);
    return {
      childId: validateWithSchema(response.feature_id, FeatureIdSchema),
      parentId: validateWithSchema(response.parent_id, FeatureIdSchema),
      result: response.result,
    };
  }

  async discardRefactorChild(
    request: DiscardRefactorChildRequest,
  ): Promise<DiscardRefactorChildResult> {
    const input = validateWithSchema(request, DiscardRefactorChildRequestSchema);
    const body = await this.api(`/api/v1/features/${input.childId}/actions/discard`, {
      method: 'POST',
      body: {},
    });
    const response = validateWithSchema(body, DiscardChildResponseSchema);
    const normalized = response.result.toLowerCase();
    const status = normalized.includes('drain')
      ? 'draining'
      : normalized.includes('attention')
        ? 'attention'
        : normalized.includes('retry')
          ? 'retry'
          : 'completed';
    return {
      childId: validateWithSchema(response.feature_id, FeatureIdSchema),
      result: response.result,
      status,
    };
  }

  async fetchReviewFeedback(
    request: FetchReviewFeedbackRequest,
  ): Promise<FetchReviewFeedbackResult> {
    const input = validateWithSchema(request, FetchReviewFeedbackRequestSchema);
    const body = await this.api(
      `/api/v1/features/${input.featureId}/actions/review-feedback/fetch`,
      {
        method: 'POST',
        body: {},
      },
    );
    const response = validateWithSchema(body, ReviewFeedbackFetchResponseSchema);
    return {
      featureId: input.featureId,
      revision: response.revision,
      snapshotId: response.snapshot_id,
      repos: response.repos.map(toReviewFeedbackRepoGroupView),
    };
  }

  async updateReviewFeedbackSelection(
    request: UpdateReviewFeedbackSelectionRequest,
  ): Promise<UpdateReviewFeedbackSelectionResult> {
    const input = validateWithSchema(request, UpdateReviewFeedbackSelectionRequestSchema);
    const body = await this.api(
      `/api/v1/features/${input.featureId}/actions/review-feedback/selection`,
      {
        method: 'POST',
        body: {
          expected_revision: input.expectedRevision,
          updates: input.updates.map((update) => ({
            stable_ref: update.stableRef,
            selected: update.selected,
          })),
        },
      },
    );
    const response = validateWithSchema(body, ReviewFeedbackSelectionResponseSchema);
    return {
      featureId: input.featureId,
      revision: response.revision,
      repos: response.repos.map(toReviewFeedbackRepoGroupView),
    };
  }

  async launchReviewFeedbackChild(
    request: LaunchReviewFeedbackChildRequest,
  ): Promise<LaunchReviewFeedbackChildResult> {
    const input = validateWithSchema(request, LaunchReviewFeedbackChildRequestSchema);
    const body = await this.api(`/api/v1/features/${input.parentId}/actions/review-feedback`, {
      method: 'POST',
      body: {
        expected_revision: input.expectedRevision,
        ...(input.gate === undefined ? {} : { gate: input.gate }),
      },
    });
    const response = validateWithSchema(body, ReviewFeedbackFeatureResponseSchema);
    return {
      childId: validateWithSchema(response.child_id ?? response.feature_id, FeatureIdSchema),
      parentId: validateWithSchema(response.parent_id, FeatureIdSchema),
      result: response.result,
      ...(response.changed === undefined ? {} : { changed: response.changed }),
      ...(response.omitted === undefined ? {} : { omitted: response.omitted }),
      ...(response.deferred === undefined ? {} : { deferred: response.deferred }),
    };
  }

  async deleteFeatureCascade(
    request: DeleteFeatureCascadeRequest,
  ): Promise<DeleteFeatureCascadeResult> {
    const input = validateWithSchema(request, DeleteFeatureCascadeRequestSchema);
    const body = await this.api(`/api/v1/features/${input.featureId}/actions/delete`, {
      method: 'POST',
      body: {},
    });
    const response = validateWithSchema(body, DeleteFeatureResponseSchema);
    return {
      featureId: validateWithSchema(response.feature_id, FeatureIdSchema),
      operationId: response.operation_id,
      status: response.status,
      diagnostics: (response.diagnostics ?? []).map((diagnostic) => ({
        ...(diagnostic.code === undefined ? {} : { code: diagnostic.code }),
        ...(diagnostic.message === undefined ? {} : { message: redactText(diagnostic.message) }),
      })),
    };
  }

  // --- transport helpers -----------------------------------------------------

  /**
   * One authenticated request through the shared server client. Canonical
   * server rejections (including 409 `not_ready` with its readiness titles)
   * cross unchanged; the catalog owns their text.
   */
  private api(path: string, init?: ApiRequestInit): Promise<unknown> {
    return serverRequest(this.deps.transport, path, init);
  }

  private async runOperationalAction(input: FeatureActionRequest): Promise<FeatureActionResult> {
    try {
      const body = await this.api(`/api/v1/features/${input.featureId}/actions/${input.action}`, {
        method: 'POST',
        body: 'body' in input ? input.body : {},
        ...(LONG_MUTATION_ACTIONS.has(input.action) ? { timeoutMs: LONG_MUTATION_TIMEOUT_MS } : {}),
      });
      const response = validateWithSchema(body, ServerFeatureOperationalActionResponseSchema);
      return {
        featureId: validateWithSchema(response.feature_id, FeatureIdSchema),
        action: input.action,
        result: response.result,
        ...(response.phase === undefined || response.phase === '' ? {} : { phase: response.phase }),
        sessionIds: response.session_ids ?? [],
      };
    } catch (error) {
      // Re-read eligibility after a structured rejection. Keep the original
      // actionable mutation error even when this best-effort refresh fails.
      try {
        await this.getFeature(input.featureId);
      } catch {
        // The renderer will next converge through its invalidation/resync path.
      }
      throw error;
    }
  }
}

/** Spreads `{ [key]: value }` only when the value is present and non-empty. */
function spreadDefined<K extends string, V extends string | number>(
  key: K,
  value: V | undefined,
): Partial<Record<K, V>> {
  return value === undefined || value === '' ? {} : ({ [key]: value } as Record<K, V>);
}

/** Maps a server-side comment (snake_case) to the renderer-facing view (camelCase), redacting free-text fields. */
function toReviewFeedbackCommentView(
  comment: ServerReviewFeedbackComment,
): ReviewFeedbackCommentView {
  return {
    repo: comment.repo,
    id: comment.id,
    type: comment.type,
    ...spreadDefined('path', comment.path),
    ...spreadDefined('line', comment.line),
    ...spreadDefined('author', comment.author),
    ...(comment.body === undefined ? {} : { body: redactText(comment.body) }),
    ...(comment.diff_hunk === undefined ? {} : { diffHunk: redactText(comment.diff_hunk) }),
    ...spreadDefined('inReplyToId', comment.in_reply_to_id),
    ...spreadDefined('createdAt', comment.created_at),
  };
}

/** Maps a pending-draft comment to its renderer-facing view, redacting free text. */
function toReviewFeedbackDraftCommentView(
  comment: ServerReviewFeedbackDraftComment,
): ReviewFeedbackDraftCommentView {
  return {
    stableRef: comment.stable_ref,
    selected: comment.selected,
    ...toReviewFeedbackCommentView(comment),
  };
}

/** Maps a server repo group (snake_case) to the renderer-facing view (camelCase). */
function toReviewFeedbackRepoGroupView(group: {
  repo: string;
  pr_url: string;
  comments: ServerReviewFeedbackDraftComment[];
}): ReviewFeedbackRepoGroup {
  return {
    repo: group.repo,
    prUrl: group.pr_url,
    comments: group.comments.map(toReviewFeedbackDraftCommentView),
  };
}

/** Maps the validated server detail to the strict renderer-facing snapshot. */
function toSnapshot(feature: ServerFeatureDetail): FeatureSnapshot {
  const setup = toSetupView(feature.active_run_detail?.setup);
  return {
    id: validateWithSchema(feature.id, FeatureIdSchema),
    name: feature.name,
    slug: feature.slug,
    status: feature.status,
    currentPhase: feature.current_phase,
    ...(feature.pipeline === undefined || feature.pipeline === ''
      ? {}
      : { pipeline: feature.pipeline }),
    ...(feature.description === undefined || feature.description === ''
      ? {}
      : { description: feature.description }),
    ...(feature.risk_level === undefined || feature.risk_level === ''
      ? {}
      : { riskLevel: feature.risk_level }),
    ...(feature.exit_criteria === undefined || feature.exit_criteria === ''
      ? {}
      : { exitCriteria: feature.exit_criteria }),
    ...(feature.wait_reason === undefined || feature.wait_reason === ''
      ? {}
      : { waitReason: redactText(feature.wait_reason) }),
    repos: feature.repos,
    createdAt: feature.created_at,
    activeRun: feature.active_run,
    ...spreadDefined(
      'currentRoadmapPhase',
      feature.active_run_detail?.roadmap_phase ?? feature.progress.current_roadmap_phase,
    ),
    ...spreadDefined(
      'totalRoadmapPhases',
      feature.active_run_detail?.roadmap_total ?? feature.progress.total_roadmap_phases,
    ),
    ...spreadDefined(
      'currentIteration',
      feature.active_run_detail?.iteration ?? feature.progress.current_iteration,
    ),
    ...spreadDefined(
      'phaseStatus',
      feature.active_run_detail?.phase_status ?? feature.progress.current_phase_status,
    ),
    ...(setup === null ? {} : { setup }),
    // Canonical warning objects cross IPC intact except diagnostics, which
    // pass through the same redaction as every other raw text.
    warnings: (feature.warnings ?? []).map(redactedCanonicalError),
    errors: (feature.errors ?? []).map(toOwnedErrorView),
    automaticReview: {
      mode: feature.automatic_review.mode,
      enabled: feature.automatic_review.enabled,
      source: feature.automatic_review.source,
    },
    actions: (feature.actions ?? []).map((action) => ({
      id: action.id,
      enabled: action.enabled,
      disabledReasons: (action.disabled_reasons ?? []).map((reason) => ({
        code: reason.code,
        message: redactText(reason.message),
      })),
      ...(action.required_inputs === undefined
        ? {}
        : {
            inputs: action.required_inputs.map((input) => ({
              name: input.name,
              ...(input.options === undefined ? {} : { options: input.options }),
            })),
          }),
      ...(action.impact_preview === undefined
        ? {}
        : {
            impactPreview: {
              kind: action.impact_preview.kind,
              subject: {
                id: validateWithSchema(action.impact_preview.subject.id, FeatureIdSchema),
                name: action.impact_preview.subject.name,
              },
              categories: action.impact_preview.categories.map((category) => ({
                key: category.key,
                label: category.label,
                items: category.items,
              })),
              retained: action.impact_preview.retained,
            },
          }),
    })),
    ...(feature.active_child === undefined
      ? {}
      : { activeChild: toRelationshipChildView(feature.active_child) }),
    ...(feature.child_history === undefined
      ? {}
      : { childHistory: feature.child_history.map(toRelationshipChildView) }),
    ...(feature.child_history_total === undefined
      ? {}
      : { childHistoryTotal: feature.child_history_total }),
    ...(feature.child_history_truncated === undefined
      ? {}
      : { childHistoryTruncated: feature.child_history_truncated }),
    ...spreadDefined('parentId', feature.parent_id),
    ...spreadDefined('parentKind', feature.parent_kind),
    ...(feature.active === undefined ? {} : { active: feature.active }),
    ...(feature.setup_complete === undefined ? {} : { setupComplete: feature.setup_complete }),
    ...spreadDefined('closeOutcome', feature.close_outcome),
    ...spreadDefined('closedAt', feature.closed_at),
    ...(feature.relationship === undefined
      ? {}
      : { relationship: toRelationshipChildView(feature.relationship) }),
    ...(feature.review_feedback === undefined || feature.review_feedback.length === 0
      ? {}
      : {
          reviewFeedback: feature.review_feedback.map(toReviewFeedbackCommentView),
        }),
    ...(feature.transaction === undefined
      ? {}
      : {
          transaction: {
            ...spreadDefined('phase', feature.transaction.phase),
            ...(feature.transaction.attention === undefined
              ? {}
              : {
                  // The canonical object crosses IPC intact except
                  // diagnostics, which pass through the same redaction as
                  // every other raw text the renderer receives.
                  attention: {
                    ...feature.transaction.attention,
                    diagnostics:
                      feature.transaction.attention.diagnostics === undefined
                        ? undefined
                        : redactText(feature.transaction.attention.diagnostics),
                  },
                }),
            ...(feature.transaction.entries === undefined
              ? {}
              : {
                  entries: feature.transaction.entries.map((entry) => ({
                    ...spreadDefined('repo', entry.repo),
                    ...spreadDefined('prepState', entry.prep_state),
                    ...spreadDefined('applyState', entry.apply_state),
                    ...(entry.pending_sync === undefined
                      ? {}
                      : { pendingSync: entry.pending_sync }),
                  })),
                }),
          },
        }),
    ...(feature.repo_status === undefined || feature.repo_status.length === 0
      ? {}
      : {
          repoStatus: feature.repo_status.map((repo) => ({
            name: repo.name,
            publishable: repo.publishable,
            ...(repo.touched === undefined ? {} : { touched: repo.touched }),
            ...(repo.pr_url === undefined || repo.pr_url === '' ? {} : { prUrl: repo.pr_url }),
            ...(repo.freshness === undefined || repo.freshness === ''
              ? {}
              : { freshness: repo.freshness }),
            // The canonical object crosses IPC intact except diagnostics,
            // which pass through the same redaction as every other raw
            // text the renderer receives.
            ...(repo.error === undefined
              ? {}
              : {
                  error: {
                    ...repo.error,
                    diagnostics:
                      repo.error.diagnostics === undefined
                        ? undefined
                        : redactText(repo.error.diagnostics),
                  },
                }),
            ...(repo.rebase_status === undefined || repo.rebase_status === ''
              ? {}
              : { rebaseStatus: repo.rebase_status }),
            ...(repo.rebase_target === undefined || repo.rebase_target === ''
              ? {}
              : { rebaseTarget: repo.rebase_target }),
            ...(repo.conflict_files === undefined || repo.conflict_files.length === 0
              ? {}
              : { conflictFiles: repo.conflict_files }),
          })),
        }),
    reviewGate: {
      reviewingGate: feature.review_gate.reviewing_gate,
      reviewFixing: feature.review_gate.review_fixing,
      validatingPlan: feature.review_gate.validating_plan,
      validatorStatuses: { ...(feature.review_gate.validator_statuses ?? {}) },
    },
    ...(feature.verification_items === undefined
      ? {}
      : {
          verificationItems: feature.verification_items.map((item) => ({
            name: item.name,
            state: item.state,
          })),
        }),
    ...(feature.timing === undefined
      ? {}
      : { timing: { totalSeconds: feature.timing.total_seconds } }),
    ...(feature.failure === undefined
      ? {}
      : {
          // The canonical object crosses IPC intact except diagnostics,
          // which pass through the same redaction as every other raw text
          // the renderer receives.
          failure: {
            ...feature.failure,
            diagnostics:
              feature.failure.diagnostics === undefined
                ? undefined
                : redactText(feature.failure.diagnostics),
          },
        }),
  };
}

/**
 * Maps a validated server owned-error entry (snake_case) to the
 * renderer-facing view (camelCase). Entries never carry diagnostics, so no
 * redaction applies; the class and reference discipline were validated at
 * the parse boundary.
 */
function toOwnedErrorView(entry: ServerOwnedError): OwnedError {
  return {
    ref: {
      scope: entry.ref.scope,
      code: entry.ref.code,
      ...(entry.ref.feature_id === undefined ? {} : { featureId: entry.ref.feature_id }),
      ...(entry.ref.repository === undefined ? {} : { repository: entry.ref.repository }),
      ...(entry.ref.task_key === undefined ? {} : { taskKey: entry.ref.task_key }),
      ...(entry.ref.snapshot_id === undefined ? {} : { snapshotId: entry.ref.snapshot_id }),
      ...(entry.ref.key === undefined ? {} : { key: entry.ref.key }),
    },
    error: entry.error,
  };
}

function toRelationshipChildView(child: ServerRelationshipChild) {
  const hasDiffSummary = diffSummaryPresence(child);
  return {
    id: validateWithSchema(child.id, FeatureIdSchema),
    name: child.name,
    kind: child.kind,
    displayToken: child.display_token,
    displayState: child.display_state,
    pipeline: child.pipeline,
    status: child.status,
    ...spreadDefined('setupStatus', child.setup_status),
    ...spreadDefined('relationshipState', child.relationship_state),
    ...(child.outcome === undefined ? {} : { outcome: child.outcome }),
    startedAt: child.started_at,
    ...spreadDefined('closedAt', child.closed_at),
    cost: { totalUsd: child.cost.total_usd, byPhase: child.cost.by_phase },
    integrationState: child.integration_state,
    ...(child.attention === undefined
      ? {}
      : {
          // The canonical object crosses IPC intact except diagnostics,
          // which pass through the same redaction as every other raw text
          // the renderer receives.
          attention: {
            ...child.attention,
            diagnostics:
              child.attention.diagnostics === undefined
                ? undefined
                : redactText(child.attention.diagnostics),
          },
        }),
    // Canonical warning objects cross IPC intact except diagnostics, which
    // pass through the same redaction as every other raw text.
    warnings: child.warnings.map(redactedCanonicalError),
    ...spreadDefined('diffSummary', child.diff_summary),
    ...(hasDiffSummary === undefined ? {} : { hasDiffSummary }),
  };
}

/**
 * A detail-route child carries the body and no flag; deriving the flag from it
 * keeps every consumer on one predicate for "a preserved diff exists".
 */
function diffSummaryPresence(child: ServerRelationshipChild): boolean | undefined {
  if (child.has_diff_summary !== undefined) return child.has_diff_summary;
  return child.diff_summary === undefined ? undefined : child.diff_summary !== '';
}

/** Orders tasks by the server-owned task_order; unknown keys keep a stable tail. */
function toSetupView(setup: ServerSetup | undefined): FeatureSetupView | null {
  if (setup === undefined) {
    return null;
  }
  const status = FeatureSetupStatusSchema.safeParse(setup.status);
  if (!status.success) {
    // Unknown lifecycle value from a newer server: fail closed on the setup
    // section rather than mislabeling progress.
    return null;
  }
  const tasks = setup.tasks ?? {};
  const order = setup.task_order ?? [];
  const orderedKeys = [
    ...order.filter((key) => key in tasks),
    ...Object.keys(tasks)
      .filter((key) => !order.includes(key))
      .sort(),
  ];
  const taskViews: SetupTaskView[] = [];
  for (const key of orderedKeys) {
    const task = tasks[key];
    if (task === undefined) {
      continue;
    }
    const taskStatus = FeatureSetupStatusSchema.safeParse(task.status);
    if (!taskStatus.success) {
      continue;
    }
    taskViews.push({
      key: task.key === '' ? key : task.key,
      kind: task.kind,
      label: task.label === undefined || task.label === '' ? key : task.label,
      ...(task.repo === undefined || task.repo === '' ? {} : { repo: task.repo }),
      status: taskStatus.data,
      ...(task.branch === undefined || task.branch === '' ? {} : { branch: task.branch }),
      attempt: task.attempt ?? 0,
      ...(task.error === undefined
        ? {}
        : {
            // The task's canonical failure object crosses IPC intact except
            // diagnostics, which pass through the same redaction as every
            // other raw text the renderer receives.
            error: {
              ...task.error,
              diagnostics:
                task.error.diagnostics === undefined
                  ? undefined
                  : redactText(task.error.diagnostics),
            },
          }),
    });
  }
  return {
    status: status.data,
    attempt: setup.attempt ?? 0,
    tasks: taskViews,
  };
}
