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
import { redactText } from '../shared/errors';
import {
  FeatureActionResponseSchema,
  ServerFeatureOperationalActionResponseSchema,
  FeatureDetailResponseSchema,
  FeatureListResponseSchema,
  PublishDescriptionResponseSchema,
  RuntimeConfigCreationSchema,
  RebaseStartResponseSchema,
  RebasePreflightResponseSchema,
  ReviewCommentsFetchResponseSchema,
  ReviewCommentsStartResponseSchema,
  RefactorStartResponseSchema,
  RefactorPreflightResponseSchema,
  validateWithSchema,
  type ServerFeatureDetail,
  type ServerSetup,
} from '../shared/api/parse';
import {
  CreateFeatureInputSchema,
  FeatureActionRequestSchema,
  FeatureIdSchema,
  FeatureSetupStatusSchema,
  RebaseRequestSchema,
  RebasePreflightRequestSchema,
  ReviewCommentsFetchRequestSchema,
  ReviewCommentsStartRequestSchema,
  RefactorRequestSchema,
  RefactorPreflightRequestSchema,
  type CreateFeatureInput,
  type CreateFeatureResult,
  type CreationDefaults,
  type EffortLevel,
  type FeatureSetupView,
  type FeatureSnapshot,
  type FeatureActionRequest,
  type FeatureActionResult,
  type PublishDescriptionResult,
  type FeatureSummaryView,
  type ReadinessSnapshot,
  type RepositoryFileRef,
  type RebaseRequest,
  type RebasePreflightRequest,
  type RebasePreflightResult,
  type RebaseResult,
  type RefactorRequest,
  type RefactorPreflightRequest,
  type RefactorPreflightResult,
  type RefactorResult,
  type ReviewCommentsFetchRequest,
  type ReviewCommentsFetchResult,
  type ReviewCommentsStartRequest,
  type ReviewCommentsStartResult,
  type SetupDispatchResult,
  type SetupTaskView,
} from '../shared/ipc';
import type { ApiRequestInit } from './gateway/runtimeGateway';
import { serverRequest, type ServerTransport } from './serverClient';

/** The authenticated transport surface the gateway provides. */
export type FeatureTransport = ServerTransport;

export interface FeatureServiceDeps {
  transport: FeatureTransport;
  /** Fresh authoritative readiness (repository eligibility for creation). */
  readReadiness(): Promise<ReadinessSnapshot>;
  resolveRepositoryFiles(refs: readonly RepositoryFileRef[]): Promise<string[]>;
}

/** Concrete, safe next steps per structured server error code. */
const REMEDY_BY_CODE: Record<string, string> = {
  not_ready: 'Complete the outstanding runtime setup steps, then try again.',
  bad_request: 'Correct the highlighted input, then try again.',
  not_found: 'The feature no longer exists on the server. Close its tab.',
  conflict: 'The server rejected the action in its current state. Refresh and retry.',
};

const PHASE_MODEL_LABELS: ReadonlyArray<readonly [string, string]> = [
  ['inquiry', 'Inquiry'],
  ['research', 'Research'],
  ['planning', 'Planning'],
  ['implementation', 'Implementation'],
  ['review', 'Review'],
  ['utilities', 'Utilities'],
  ['kb_build', 'Knowledge base'],
];

const EFFORT_LEVELS = new Set<EffortLevel>(['auto', 'low', 'medium', 'high', 'xhigh', 'max']);

// Description generation is a synchronous utility LLM session. Its session
// idle bounds are five minutes, so leave transport cleanup time beyond that
// without weakening the 30-second default for ordinary API calls.
const PUBLISH_DESCRIPTION_TIMEOUT_MS = 6 * 60_000;

export class FeatureService {
  private readonly actionFlights = new Map<string, Promise<FeatureActionResult>>();

  constructor(private readonly deps: FeatureServiceDeps) {}

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
    const repositoryAttachments = await this.deps.resolveRepositoryFiles(validated.repositoryFiles);
    const body = await this.api('/api/v1/features', {
      method: 'POST',
      body: {
        name: validated.name.trim(),
        ...(validated.description.trim() === '' ? {} : { description: validated.description }),
        repos: validated.repoKeys,
        ...(validated.useCurrentBranch ? { use_current_branch: true } : {}),
        images: validated.images,
        attachments: [...validated.attachments, ...repositoryAttachments],
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

  async listFeatures(): Promise<FeatureSummaryView[]> {
    const body = await this.api('/api/v1/features');
    const response = validateWithSchema(body, FeatureListResponseSchema);
    return response.features.map((feature) => ({
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
      warnings: (feature.warnings ?? []).map((warning) => ({
        code: warning.code,
        message: redactText(warning.message),
      })),
    }));
  }

  /** Dispatches only allowlisted server-catalogue actions, single-flight per input. */
  async dispatchAction(request: FeatureActionRequest): Promise<FeatureActionResult> {
    const input = validateWithSchema(request, FeatureActionRequestSchema);
    const key = `${input.featureId}:${input.action}:${JSON.stringify('body' in input ? input.body : {})}`;
    const existing = this.actionFlights.get(key);
    if (existing !== undefined) return existing;
    const flight = this.runOperationalAction(input).finally(() => {
      if (this.actionFlights.get(key) === flight) this.actionFlights.delete(key);
    });
    this.actionFlights.set(key, flight);
    return flight;
  }

  async getFeature(featureId: string): Promise<FeatureSnapshot> {
    const id = validateWithSchema(featureId, FeatureIdSchema);
    const body = await this.api(`/api/v1/features/${id}`);
    const response = validateWithSchema(body, FeatureDetailResponseSchema);
    return toSnapshot(response.feature);
  }

  async startRebase(request: RebaseRequest): Promise<RebaseResult> {
    const input = validateWithSchema(request, RebaseRequestSchema);
    const body = await this.api(`/api/v1/features/${input.featureId}/actions/rebase`, {
      method: 'POST',
      body: {
        ...(input.sourceRevision === undefined || input.sourceRevision === ''
          ? {}
          : { source_revision: input.sourceRevision }),
      },
    });
    const response = validateWithSchema(body, RebaseStartResponseSchema);
    return {
      featureId: validateWithSchema(response.feature_id, FeatureIdSchema),
      cycleType: response.cycle_type,
      result: response.result,
    };
  }

  async preflightRebase(request: RebasePreflightRequest): Promise<RebasePreflightResult> {
    const input = validateWithSchema(request, RebasePreflightRequestSchema);
    const body = await this.api(`/api/v1/features/${input.featureId}/rebase/preflight`);
    const response = validateWithSchema(body, RebasePreflightResponseSchema);
    return {
      featureId: validateWithSchema(response.feature_id, FeatureIdSchema),
      sourceRevision: response.source_revision,
      repos: (response.repos ?? []).map((repo) => ({
        repo: repo.repo,
        target: repo.target,
        publishable: repo.publishable,
        freshness: repo.freshness,
        behind: repo.behind,
        ...(repo.blocker === undefined || repo.blocker === '' ? {} : { blocker: repo.blocker }),
        ...(repo.conflict_files === undefined || repo.conflict_files.length === 0
          ? {}
          : { conflictFiles: repo.conflict_files }),
      })),
    };
  }

  async fetchReviewComments(
    request: ReviewCommentsFetchRequest,
  ): Promise<ReviewCommentsFetchResult> {
    const input = validateWithSchema(request, ReviewCommentsFetchRequestSchema);
    const body = await this.api(
      `/api/v1/features/${input.featureId}/actions/review-comments/fetch`,
      { method: 'POST', body: { repo: input.repo } },
    );
    const response = validateWithSchema(body, ReviewCommentsFetchResponseSchema);
    return {
      featureId: validateWithSchema(response.feature_id, FeatureIdSchema),
      repo: response.repo,
      comments: (response.comments ?? []).map((comment) => ({
        id: comment.id,
        ...(comment.file === undefined && comment.path === undefined
          ? {}
          : { file: comment.file ?? comment.path }),
        ...(comment.line === undefined ? {} : { line: comment.line }),
        ...(comment.body === undefined ? {} : { body: redactText(comment.body) }),
        ...(comment.author === undefined && comment.user_login === undefined
          ? {}
          : { author: comment.author ?? comment.user_login }),
        ...(comment.type === undefined || comment.type === '' ? {} : { type: comment.type }),
        ...(comment.thread_id === undefined || comment.thread_id === ''
          ? {}
          : { threadId: comment.thread_id }),
      })),
      ...(response.revision === undefined || response.revision === ''
        ? {}
        : { revision: response.revision }),
      ...(response.modes === undefined ? {} : { modes: response.modes }),
    };
  }

  async startReviewComments(
    request: ReviewCommentsStartRequest,
  ): Promise<ReviewCommentsStartResult> {
    const input = validateWithSchema(request, ReviewCommentsStartRequestSchema);
    const body = await this.api(`/api/v1/features/${input.featureId}/actions/review-comments`, {
      method: 'POST',
      body: { repo: input.repo, mode: input.mode },
    });
    const response = validateWithSchema(body, ReviewCommentsStartResponseSchema);
    return {
      featureId: validateWithSchema(response.feature_id, FeatureIdSchema),
      cycleType: response.cycle_type,
      result: response.result,
      ...(response.session_id === undefined || response.session_id === ''
        ? {}
        : { sessionId: response.session_id }),
    };
  }

  async startRefactor(request: RefactorRequest): Promise<RefactorResult> {
    const input = validateWithSchema(request, RefactorRequestSchema);
    const body = await this.api(`/api/v1/features/${input.featureId}/actions/refactor`, {
      method: 'POST',
      body: {
        prompt: input.prompt,
        ...(input.repo === undefined || input.repo === '' ? {} : { repo: input.repo }),
        ...(input.pipeline === undefined || input.pipeline === ''
          ? {}
          : { pipeline: input.pipeline }),
        ...(input.sourceRevision === undefined || input.sourceRevision === ''
          ? {}
          : { source_revision: input.sourceRevision }),
      },
    });
    const response = validateWithSchema(body, RefactorStartResponseSchema);
    return {
      featureId: validateWithSchema(response.feature_id, FeatureIdSchema),
      cycleType: response.cycle_type,
      result: response.result,
      ...(response.repo === undefined || response.repo === '' ? {} : { repo: response.repo }),
      ...(response.pipeline === undefined || response.pipeline === ''
        ? {}
        : { pipeline: response.pipeline }),
      ...(response.session_id === undefined || response.session_id === ''
        ? {}
        : { sessionId: response.session_id }),
    };
  }

  async preflightRefactor(request: RefactorPreflightRequest): Promise<RefactorPreflightResult> {
    const input = validateWithSchema(request, RefactorPreflightRequestSchema);
    const body = await this.api(`/api/v1/features/${input.featureId}/refactor/preflight`, {
      method: 'POST',
      body: {
        prompt: input.prompt,
        ...(input.repo === undefined || input.repo === '' ? {} : { repo: input.repo }),
        ...(input.pipeline === undefined || input.pipeline === ''
          ? {}
          : { pipeline: input.pipeline }),
      },
    });
    const response = validateWithSchema(body, RefactorPreflightResponseSchema);
    return {
      featureId: validateWithSchema(response.feature_id, FeatureIdSchema),
      sourceRevision: response.source_revision,
      scope: response.scope,
      repos: response.repos ?? [],
      prompt: response.prompt,
      ...(response.pipeline === undefined || response.pipeline === ''
        ? {}
        : { pipeline: response.pipeline }),
      ...(response.blockers === undefined || response.blockers.length === 0
        ? {}
        : { blockers: response.blockers }),
    };
  }

  // --- transport helpers -----------------------------------------------------

  /**
   * One authenticated request through the shared server client. The 409
   * `not_ready` rejection carries its outstanding readiness issues; their
   * safe messages are folded into the remediation so the form can show why.
   */
  private api(path: string, init?: ApiRequestInit): Promise<unknown> {
    return serverRequest(this.deps.transport, path, init, {
      remedyByCode: REMEDY_BY_CODE,
      foldTargetIssues: true,
    });
  }

  private async runOperationalAction(input: FeatureActionRequest): Promise<FeatureActionResult> {
    try {
      const body = await this.api(`/api/v1/features/${input.featureId}/actions/${input.action}`, {
        method: 'POST',
        body: 'body' in input ? input.body : {},
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
    })),
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
            ...(repo.last_error === undefined || repo.last_error === ''
              ? {}
              : { lastError: redactText(repo.last_error) }),
            ...(repo.cycle_type === undefined || repo.cycle_type === ''
              ? {}
              : { cycleType: repo.cycle_type }),
            ...(repo.cycle_status === undefined || repo.cycle_status === ''
              ? {}
              : { cycleStatus: repo.cycle_status }),
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
    ...(feature.cycle === undefined
      ? {}
      : {
          cycle: {
            type: feature.cycle.type,
            status: feature.cycle.status,
            ...(feature.cycle.count === undefined ? {} : { count: feature.cycle.count }),
            ...(feature.cycle.iteration === undefined
              ? {}
              : { iteration: feature.cycle.iteration }),
            ...(feature.cycle.phase === undefined ? {} : { phase: feature.cycle.phase }),
            ...(feature.cycle.last_error === undefined || feature.cycle.last_error === ''
              ? {}
              : { lastError: redactText(feature.cycle.last_error) }),
            ...(feature.cycle.started_at === undefined
              ? {}
              : { startedAt: feature.cycle.started_at }),
          },
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
          failure: {
            ...(feature.failure.type === undefined ? {} : { type: feature.failure.type }),
            ...(feature.failure.message === undefined
              ? {}
              : { message: redactText(feature.failure.message) }),
          },
        }),
  };
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
      ...(task.last_error === undefined || task.last_error === ''
        ? {}
        : { error: redactText(task.last_error) }),
    });
  }
  return {
    status: status.data,
    attempt: setup.attempt ?? 0,
    tasks: taskViews,
    ...(setup.last_error === undefined || setup.last_error === ''
      ? {}
      : { lastError: redactText(setup.last_error) }),
  };
}
