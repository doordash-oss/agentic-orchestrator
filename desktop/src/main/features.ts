/**
 * Main-process feature operations. Everything talks to the authoritative
 * server through the runtime gateway's bearer transport and returns strict
 * renderer-facing views; nothing here caches server-domain data, reads
 * runtime files, or lets the renderer compose REST paths. Creating a feature
 * queues durable setup but does NOT dispatch it — callers must dispatch the
 * `setup` action afterwards. Start and stop remain separate, explicitly
 * allowlisted operations dispatched through `dispatchAction`.
 */
import { redactText } from '../shared/errors';
import {
  FeatureActionResponseSchema,
  ServerFeatureOperationalActionResponseSchema,
  FeatureDetailResponseSchema,
  FeatureListResponseSchema,
  RuntimeConfigCreationSchema,
  validateWithSchema,
  type ServerFeatureDetail,
  type ServerSetup,
} from '../shared/api/parse';
import {
  CreateFeatureInputSchema,
  FeatureActionRequestSchema,
  FeatureIdSchema,
  FeatureSetupStatusSchema,
  type CreateFeatureInput,
  type CreateFeatureResult,
  type CreationDefaults,
  type FeatureSetupView,
  type FeatureSnapshot,
  type FeatureActionRequest,
  type FeatureActionResult,
  type FeatureSummaryView,
  type ReadinessSnapshot,
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
    const body = await this.api('/api/v1/features', {
      method: 'POST',
      body: {
        name: validated.name.trim(),
        ...(validated.description.trim() === '' ? {} : { description: validated.description }),
        repos: validated.repoKeys,
        ...(validated.useCurrentBranch ? { use_current_branch: true } : {}),
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

  /** Dispatches only allowlisted start/stop actions, single-flight per feature/action. */
  async dispatchAction(request: FeatureActionRequest): Promise<FeatureActionResult> {
    const input = validateWithSchema(request, FeatureActionRequestSchema);
    const key = `${input.featureId}:${input.action}`;
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
        body: {},
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
    repos: feature.repos,
    createdAt: feature.created_at,
    activeRun: feature.active_run,
    ...(setup === null ? {} : { setup }),
    actions: (feature.actions ?? []).map((action) => ({
      id: action.id,
      enabled: action.enabled,
      disabledReasons: (action.disabled_reasons ?? []).map((reason) => ({
        code: reason.code,
        message: redactText(reason.message),
      })),
    })),
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
