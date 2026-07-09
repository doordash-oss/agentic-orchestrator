import type {
  ArtifactRef,
  ArtifactListResponse,
  AskUserRequest,
  ConfigPayload,
  CreateFeatureRequest,
  FeatureDetail,
  FeatureSummary,
  FeaturesListResponse,
  Health,
  NeedUserInputDecisionRequest,
  RecoveryActionValue,
  RecoveryScanResponse,
  ReviewCommentsResponse,
  ReviewDecisionRequest,
  RewindRequest,
  RewindResult,
} from "./types";

const API = "/api/v1";

export class ApiError extends Error {
  status: number;
  /** Stable identifier (e.g. "rewind_failed"). Empty when the server
   *  returned a plain-text body (older handlers haven't been migrated
   *  to writeError yet). */
  code: string;
  constructor(status: number, message: string, code = "") {
    super(message);
    this.status = status;
    this.code = code;
    this.name = "ApiError";
  }
}

async function extractError(res: Response): Promise<{ message: string; code: string }> {
  const fallback = { message: res.statusText, code: "" };
  let text: string;
  try {
    text = await res.text();
  } catch {
    return fallback;
  }
  if (!text) return fallback;

  const ct = res.headers.get("Content-Type") ?? "";
  if (ct.includes("application/json")) {
    try {
      const parsed = JSON.parse(text) as {
        code?: unknown;
        message?: unknown;
        error?: { code?: unknown; message?: unknown };
      };
      const code =
        typeof parsed.error?.code === "string"
          ? parsed.error.code
          : typeof parsed.code === "string"
            ? parsed.code
            : "";
      const message =
        typeof parsed.error?.message === "string" && parsed.error.message !== ""
          ? parsed.error.message
          : typeof parsed.message === "string" && parsed.message !== ""
            ? parsed.message
            : text.trim();
      return { message, code };
    } catch {
    }
  }
  return { message: text.trim(), code: "" };
}

async function getJSON<T>(path: string, signal?: AbortSignal): Promise<T> {
  const res = await fetch(path, { signal, credentials: "same-origin" });
  if (!res.ok) {
    const { message, code } = await extractError(res);
    throw new ApiError(res.status, message, code);
  }
  return res.json() as Promise<T>;
}

async function sendJSON<T>(
  method: string,
  path: string,
  body: unknown = {},
): Promise<T> {
  const res = await fetch(path, {
    method,
    credentials: "same-origin",
    headers: {
      "Content-Type": "application/json",
      "X-Agentico-Client": "local",
    },
    body: JSON.stringify(body ?? {}),
  });
  if (!res.ok) {
    const { message, code } = await extractError(res);
    throw new ApiError(res.status, message, code);
  }
  if (res.status === 204) {
    return undefined as T;
  }
  return res.json() as Promise<T>;
}

export const api = {
  health: async (signal?: AbortSignal) => {
    const dto = await getJSON<HealthDTO>(`${API}/health`, signal);
    return mapHealth(dto);
  },
  featuresList: async (signal?: AbortSignal) => {
    const [dto, sessions] = await Promise.all([
      getJSON<FeatureListDTO>(`${API}/features`, signal),
      optional(() => getJSON<SessionListDTO>(`${API}/sessions`, signal)),
    ]);
    const details = await Promise.all(
      dto.features.map((feature) =>
        optional(() =>
          getJSON<FeatureDetailDTOResponse>(
            `${API}/features/${encodeURIComponent(feature.id)}`,
            signal,
          ),
        ),
      ),
    );
    const detailByID = new Map(
      details.flatMap((detail) =>
        detail?.feature ? [[detail.feature.id, detail.feature] as const] : [],
      ),
    );
    return {
      features: dto.features.map((feature) =>
        mapFeatureSummary(
          feature,
          sessions?.sessions ?? [],
          detailByID.get(feature.id),
        ),
      ),
    } satisfies FeaturesListResponse;
  },
  featureDetail: async (id: string, signal?: AbortSignal) => {
    const [detail, sessions, prompts, permissions] = await Promise.all([
      getJSON<FeatureDetailDTOResponse>(`${API}/features/${encodeURIComponent(id)}`, signal),
      optional(() => getJSON<SessionListDTO>(`${API}/sessions`, signal)),
      optional(() => getJSON<PromptSnapshotDTO>(`${API}/prompts`, signal)),
      optional(() => getJSON<PermissionSnapshotDTO>(`${API}/permissions`, signal)),
    ]);
    return mapFeatureDetail(
      detail.feature,
      sessions?.sessions ?? [],
      prompts,
      permissions,
    );
  },
  config: async (signal?: AbortSignal) => {
    const [runtime, models] = await Promise.all([
      getJSON<RuntimeConfigDTO>(`${API}/config/runtime`, signal),
      getJSON<ModelCatalogDTO>(`${API}/catalog/models`, signal),
    ]);
    return mapConfig(runtime, models);
  },
  createFeature: async (body: CreateFeatureRequest) => {
    const created = await sendJSON<ActionResultDTO>(
      "POST",
      `${API}/features`,
      mapCreateFeatureRequest(body),
    );
    return {
      id: created.feature_id ?? "",
      slug: created.feature_id ?? "",
      name: body.name,
      status: "Created",
      created: new Date().toISOString(),
      is_running: false,
      needs_review: false,
      repo_names: body.repos,
    } satisfies FeatureSummary;
  },
  startFeature: (id: string) =>
    sendJSON<void>(
      "POST",
      `${API}/features/${encodeURIComponent(id)}/actions/start`,
    ),
  stopFeature: (id: string) =>
    sendJSON<void>(
      "POST",
      `${API}/features/${encodeURIComponent(id)}/actions/pause-stop`,
    ),
  deleteFeature: (id: string) =>
    sendJSON<void>(
      "POST",
      `${API}/features/${encodeURIComponent(id)}/actions/delete`,
    ),
  retryFeature: (id: string) =>
    sendJSON<void>(
      "POST",
      `${API}/features/${encodeURIComponent(id)}/actions/retry`,
    ),
  recovery: async (signal?: AbortSignal) => {
    const dto = await getJSON<RecoverySnapshotDTO>(`${API}/recovery`, signal);
    return {
      snapshot_id: dto.snapshot_id,
      items: dto.items.map((item) => ({
        key: item.key,
        feature_id: item.feature_id,
        feature_name: item.feature_name,
        repo_name: item.repo_name,
        phase: item.phase,
        iteration: item.iteration,
        pid: item.pid,
        process_alive: item.process_alive,
      })),
    } satisfies RecoveryScanResponse;
  },
  executeRecovery: (snapshotID: string, actions: Record<string, RecoveryActionValue>) =>
    sendJSON<void>("POST", `${API}/recovery/actions`, { snapshot_id: snapshotID, actions }),
  logs: async (id: string, phase: string, iter?: number, signal?: AbortSignal) => {
    if (phase === "session-output") {
      return activeSessionOutput(id, signal);
    }
    const runNumber = iter && iter > 0 ? iter : await activeRunNumber(id, signal);
    const logID = phase === "observe" ? "observe" : phase === "phase" ? "phase" : "session";
    const content = await getJSON<TextContentDTO>(
      `${API}/features/${encodeURIComponent(id)}/runs/${runNumber}/logs/${logID}`,
      signal,
    );
    return content.text;
  },
  logsIndex: async (id: string, signal?: AbortSignal) => {
    const detail = await getJSON<FeatureDetailDTOResponse>(
      `${API}/features/${encodeURIComponent(id)}`,
      signal,
    );
    const runs = [detail.feature.active_run_detail, ...detail.feature.historical_runs]
      .filter((run): run is RunSummaryDTO => !!run && run.run_number > 0);
    const iterations = Array.from(new Set(runs.map((run) => run.run_number))).sort((a, b) => a - b);
    const sessions = await optional(() => getJSON<SessionListDTO>(`${API}/sessions`, signal));
    const hasActiveSession = (sessions?.sessions ?? []).some(
      (session) =>
        session.feature_id === id && isActiveSessionStatus(sessionStatusKey(session)),
    );
    const runLogEntries = await availableRunLogEntries(id, iterations, signal);
    return {
      entries: [
        ...runLogEntries,
        ...(hasActiveSession ? [{ phase: "session-output", iterations: [1] }] : []),
      ],
    };
  },
  diff: async (_id: string, _signal?: AbortSignal) => {
    throw new ApiError(404, "Diff preview is not exposed by the PR #63 server API yet", "not_available");
  },
  reviewComments: async (id: string, _signal?: AbortSignal) => {
    const detail = await getJSON<FeatureDetailDTOResponse>(
      `${API}/features/${encodeURIComponent(id)}`,
    );
    const repos = detail.feature.repo_status
      .map((repo) => repo.name)
      .filter((name) => name !== "");
    const responses = await Promise.all(
      repos.map((repo) =>
        optional(() =>
          sendJSON<ReviewCommentsFetchDTO>(
            "POST",
            `${API}/features/${encodeURIComponent(id)}/actions/review-comments/fetch`,
            { repo, mode: "auto" },
          ),
        ),
      ),
    );
    const comments = responses.flatMap((response) => response?.comments ?? []).map(mapReviewComment);
    return { comments, total: comments.length } satisfies ReviewCommentsResponse;
  },
  sessionMessage: (id: string, text: string) =>
    sendJSON<void>(
      "POST",
      `${API}/prompts/help/send`,
      { session_id: id, message: text },
    ),
  sessionStop: async (_id: string) => undefined,
  respondToControl: (
    sessionId: string,
    requestId: string,
    allow: boolean,
    reason?: string,
  ) =>
    sendJSON<void>(
      "POST",
      `${API}/permissions/answer`,
      { request_id: requestId, session_id: sessionId, decision: allow ? "allow" : "deny", reason },
    ),
  respondToAskUser: (sessionId: string, body: AskUserRequest) =>
    sendJSON<void>(
      "POST",
      `${API}/prompts/ask-user/answer`,
      { request_id: body.request_id, session_id: sessionId, answers: body.answers },
    ),
  artifactsList: async (id: string, signal?: AbortSignal) => {
    const runNumber = await activeRunNumber(id, signal);
    const dto = await getJSON<ArtifactListDTO>(
      `${API}/features/${encodeURIComponent(id)}/runs/${runNumber}/artifacts`,
      signal,
    );
    return {
      items: dto.artifacts.map(mapArtifact),
      pending_review_phase: dto.artifacts.find((artifact) => artifact.phase)?.phase,
    } satisfies ArtifactListResponse;
  },
  artifactRead: async (id: string, key: string, signal?: AbortSignal) => {
    const runNumber = await activeRunNumber(id, signal);
    const content = await getJSON<TextContentDTO>(
      `${API}/features/${encodeURIComponent(id)}/runs/${runNumber}/artifacts/${encodeURIComponent(key)}`,
      signal,
    );
    return content.text;
  },
  reviewDecision: (id: string, body: ReviewDecisionRequest) =>
    sendJSON<void>(
      "POST",
      `${API}/features/${encodeURIComponent(id)}/review-decision`,
      body,
    ),
  rewind: (id: string, body: RewindRequest) =>
    sendJSON<RewindResult>(
      "POST",
      `${API}/features/${encodeURIComponent(id)}/actions/rewind`,
      body,
    ),
  rewindProceed: (id: string, body: RewindRequest) =>
    sendJSON<void>(
      "POST",
      `${API}/features/${encodeURIComponent(id)}/actions/rewind`,
      body,
    ),
  needUserInputDecision: (id: string, body: NeedUserInputDecisionRequest) =>
    sendJSON<void>(
      "POST",
      `${API}/features/${encodeURIComponent(id)}/need-user-input`,
      body,
    ),

  publish: (id: string) =>
    sendJSON<void>(
      "POST",
      `${API}/features/${encodeURIComponent(id)}/actions/publish`,
    ),
  publishMark: async (_id: string, _prURL: string) => undefined,
  publishCommitUncommitted: (id: string) =>
    sendJSON<void>(
      "POST",
      `${API}/features/${encodeURIComponent(id)}/actions/publish/description`,
    ),
  tweakStart: (id: string) =>
    sendJSON<{ session_id: string }>(
      "POST",
      `${API}/features/${encodeURIComponent(id)}/actions/tweak`,
    ),
  tweakCommit: async (_id: string) => ({ had_changes: true }),
  tweakFinish: (id: string, hadChanges: boolean) =>
    sendJSON<void>(
      "POST",
      `${API}/features/${encodeURIComponent(id)}/actions/tweak/finish`,
      { had_changes: hadChanges },
    ),
  refactorStart: (id: string, prompt: string, repoName?: string) =>
    sendJSON<{ session_id: string }>(
      "POST",
      `${API}/features/${encodeURIComponent(id)}/actions/refactor`,
      { prompt, repo: repoName },
    ),
  rebaseStart: (id: string, repoName: string) =>
    sendJSON<void>(
      "POST",
      `${API}/features/${encodeURIComponent(id)}/actions/rebase`,
      { repo: repoName },
    ),
  rebaseForcePush: (id: string, repoName: string) =>
    sendJSON<void>(
      "POST",
      `${API}/features/${encodeURIComponent(id)}/actions/rebase`,
      { repo: repoName, force_push: true },
    ),
};

export async function sessionTranscript(sessionID: string, signal?: AbortSignal) {
  const dto = await getJSON<TranscriptDTO>(
    `${API}/sessions/${encodeURIComponent(sessionID)}/transcript?limit=200`,
    signal,
  );
  return dto.messages;
}

export async function sessionPendingControls(sessionID: string, signal?: AbortSignal) {
  const dto = await getJSON<SessionDetailDTOResponse>(
    `${API}/sessions/${encodeURIComponent(sessionID)}`,
    signal,
  );
  return dto.session.pending_controls ?? [];
}

export async function sessionOutputTail(sessionID: string, signal?: AbortSignal) {
  const first = await getJSON<SessionOutputDTO>(
    `${API}/sessions/${encodeURIComponent(sessionID)}/output?from=0&limit=1`,
    signal,
  );
  const from = Math.max(0, (first.size ?? 0) - 50_000);
  const output = await getJSON<SessionOutputDTO>(
    `${API}/sessions/${encodeURIComponent(sessionID)}/output?from=${from}&limit=50000`,
    signal,
  );
  return output.data ?? "";
}

async function activeRunNumber(id: string, signal?: AbortSignal): Promise<number> {
  const detail = await getJSON<FeatureDetailDTOResponse>(
    `${API}/features/${encodeURIComponent(id)}`,
    signal,
  );
  return detail.feature.active_run || detail.feature.run_count || 1;
}

async function availableRunLogEntries(
  id: string,
  iterations: number[],
  signal?: AbortSignal,
): Promise<{ phase: string; iterations: number[] }[]> {
  const logIDs = ["session", "phase", "observe"];
  const entries = await Promise.all(
    logIDs.map(async (logID) => {
      const available = await Promise.all(
        iterations.map(async (runNumber) => {
          const content = await optional(() =>
            getJSON<TextContentDTO>(
              `${API}/features/${encodeURIComponent(id)}/runs/${runNumber}/logs/${logID}?limit=1`,
              signal,
            ),
          );
          return content ? runNumber : null;
        }),
      );
      return {
        phase: logID,
        iterations: available.filter((n): n is number => n !== null),
      };
    }),
  );
  return entries.filter((entry) => entry.iterations.length > 0);
}

async function activeSessionOutput(id: string, signal?: AbortSignal): Promise<string> {
  const sessions = await getJSON<SessionListDTO>(`${API}/sessions`, signal);
  const session = sessions.sessions.find(
    (row) => row.feature_id === id && isActiveSessionStatus(sessionStatusKey(row)),
  );
  if (!session) return "(no active session output)";
  const output = await getJSON<SessionOutputDTO>(
    `${API}/sessions/${encodeURIComponent(session.id)}/output?from=0&limit=50000`,
    signal,
  );
  return output.data || "(empty session output)";
}

async function optional<T>(fn: () => Promise<T>): Promise<T | null> {
  try {
    return await fn();
  } catch {
    return null;
  }
}

function mapHealth(dto: HealthDTO): Health {
  const started = Date.parse(dto.started_at);
  return {
    status: dto.status,
    uptime_seconds: Number.isNaN(started) ? 0 : Math.max(0, Math.floor((Date.now() - started) / 1000)),
    assets_embedded: false,
    version: 1,
    app_version: dto.owner.version ?? "dev",
  };
}

function mapCreateFeatureRequest(body: CreateFeatureRequest): CreateFeatureRequest {
  const planReview = body.checkpoints.plan_review;
  return {
    ...body,
    checkpoints: {
      inquiry_review: body.checkpoints.inquiry_review,
      research_review: body.checkpoints.research_review,
      design_review: body.checkpoints.design_review,
      roadmap_review: body.checkpoints.roadmap_review ?? planReview,
      phase_plan_review: body.checkpoints.phase_plan_review ?? planReview,
      manual_publish: body.checkpoints.manual_publish,
    },
  };
}

function mapFeatureSummary(
  dto: FeatureSummaryDTO,
  sessions: SessionDTO[] = [],
  detail?: FeatureDetailDTO,
): FeatureSummary {
  const status = effectiveFeatureStatus(dto, sessions);
  const running = isRunningStatus(status) || hasActiveSessionForFeature(dto.id, sessions);
  return {
    id: dto.id,
    slug: dto.slug,
    name: dto.name,
    status,
    current_phase: dto.current_phase,
    repo_names: dto.repos,
    created: dto.created_at,
    is_running: running,
    needs_review: !running && isReviewGateStatus(status),
    total_cost_usd: featureTotalCost(dto.id, sessions, detail),
  };
}

function mapFeatureDetail(
  dto: FeatureDetailDTO,
  sessions: SessionDTO[],
  prompts: PromptSnapshotDTO | null,
  permissions: PermissionSnapshotDTO | null,
): FeatureDetail {
  const summary = mapFeatureSummary(dto, sessions);
  const activeRun = dto.active_run_detail;
  const featureSessions = sessions.filter((session) => session.feature_id === dto.id);
  const helpQueue = (prompts?.help_queue ?? [])
    .filter((item) => item.feature_id === dto.id)
    .map((item) => ({ question: item.question, pending: item.pending, time: item.time ?? "" }));
  const permissionQueue = (permissions?.requests ?? [])
    .filter((item) => item.feature_id === dto.id)
    .map((item) => ({
      tool: item.tool_name || item.summary || "permission",
      args: item.summary,
      pending: item.status !== "answered",
      time: "",
    }));
  return {
    ...summary,
    description: dto.description,
    failure: dto.failure,
    models: dto.models,
    checkpoints: {
      inquiry_review: dto.checkpoints.inquiry_review,
      research_review: dto.checkpoints.research_review,
      design_review: dto.checkpoints.design_review,
      plan_review: dto.checkpoints.phase_plan_review ?? dto.checkpoints.roadmap_review,
      manual_publish: dto.checkpoints.manual_publish,
    },
    pipeline: dto.pipeline,
    active_iteration: activeRun?.iteration,
    started_at: activeRun?.started_at,
    active_phase_start: activeRun?.started_at,
    phase_timings_ms: secondsMapToMillis(dto.timing.by_phase),
    phase_costs: dto.cost.by_phase,
    repos: dto.repo_status.map((repo) => ({
      name: repo.name,
      publishable: repo.publishable,
      touched: repo.touched,
      pr_url: repo.pr_url,
      has_error: !!repo.last_error,
    })),
    sessions: featureSessions.map((session) => {
      const status = sessionStatusKey(session);
      return {
        id: session.id,
        feature_id: session.feature_id,
        phase: session.phase,
        repo_name: session.repo,
        kind: session.kind,
        label: session.label,
        status: session.status,
        is_active: isActiveSessionStatus(status),
        iteration: session.iteration,
        started_at: session.started_at,
        provider: session.provider,
        model: session.model,
        context_percentage: session.context_percentage,
      };
    }),
    active_cycle: dto.cycle
      ? {
          type: dto.cycle.type ?? "",
          status: dto.cycle.status,
          count: dto.cycle.count,
          iteration: dto.cycle.iteration,
        }
      : undefined,
    repo_cycles: dto.repo_status
      .filter((repo) => repo.cycle_type || repo.cycle_status)
      .map((repo) => ({
        repo: repo.name,
        cycle: {
          type: repo.cycle_type ?? "",
          status: repo.cycle_status,
          count: dto.cycle?.count,
          iteration: dto.cycle?.iteration,
          has_error: !!repo.last_error,
        },
      })),
    help_queue: helpQueue,
    permissions_queue: permissionQueue,
    active_run: dto.active_run,
    run_count: dto.run_count,
    total_cost_usd: dto.cost.total_usd,
    pending_need_user_input: dto.need_user_input?.open
      ? [{ summary: "Feature needs input", iteration: dto.need_user_input.iteration }]
      : (prompts?.need_user_inputs ?? [])
          .filter((item) => item.feature_id === dto.id && item.open)
          .map((item) => ({ summary: "Feature needs input", iteration: item.iteration })),
  };
}

function secondsMapToMillis(values: Record<string, number>): Record<string, number> {
  const out: Record<string, number> = {};
  for (const [key, value] of Object.entries(values ?? {})) {
    out[key] = value * 1000;
  }
  return out;
}

function mapConfig(runtime: RuntimeConfigDTO, models: ModelCatalogDTO): ConfigPayload {
  return {
    repos: runtime.repos.map((repo) => ({ name: repo.name })),
    workspace_roots: runtime.workspace_roots,
    models: Object.fromEntries(
      Object.entries(models.phase_provider_models).map(([phase, providers]) => [
        phase,
        Object.entries(providers).flatMap(([provider, ids]) =>
          ids.map((id) => ({ id, provider })),
        ),
      ]),
    ) as ConfigPayload["models"],
    defaults: {
      models: runtime.feature_defaults.models,
      inquireness: runtime.feature_defaults.inquireness,
      pipeline: runtime.feature_defaults.pipeline,
      checkpoints: {
        inquiry_review: runtime.feature_defaults.checkpoints.inquiry_review,
        research_review: runtime.feature_defaults.checkpoints.research_review,
        design_review: runtime.feature_defaults.checkpoints.design_review,
        plan_review:
          runtime.feature_defaults.checkpoints.phase_plan_review ??
          runtime.feature_defaults.checkpoints.roadmap_review,
        manual_publish: runtime.feature_defaults.checkpoints.manual_publish,
      },
    },
    enums: {
      pipelines: ["medium", "large", "moonshot"],
      risks: ["low", "medium", "high"],
      inquireness: ["none", "low", "medium", "high"],
    },
  };
}

function mapArtifact(dto: ArtifactDTO): ArtifactRef {
  return {
    key: dto.id,
    phase: dto.phase,
    bytes: dto.size,
    mime: mimeForArtifact(dto),
  };
}

function mimeForArtifact(dto: ArtifactDTO): string {
  const path = dto.path ?? dto.id;
  if (path.endsWith(".md") || dto.type === "markdown") return "text/markdown";
  if (path.endsWith(".json")) return "application/json";
  if (path.endsWith(".yaml") || path.endsWith(".yml")) return "application/x-yaml";
  return "text/plain";
}

function mapReviewComment(dto: ReviewCommentDTO) {
  return {
    id: dto.id,
    path: dto.path ?? "",
    line: dto.line ?? 0,
    body: dto.body ?? "",
    user: { login: dto.user_login ?? "unknown" },
    created_at: dto.created_at ?? "",
    diff_hunk: dto.diff_hunk ?? "",
    in_reply_to_id: dto.in_reply_to_id ?? 0,
    type: dto.type ?? "",
    repo_name: dto.repo_name,
  };
}

function isRunningStatus(status: string): boolean {
  return (
    status.endsWith("ing") ||
    status === "Reviewing" ||
    status === "FinalReviewing" ||
    status === "SettingUpWorktrees"
  );
}

function effectiveFeatureStatus(
  feature: FeatureSummaryDTO,
  sessions: SessionDTO[],
): FeatureSummary["status"] {
  const declared = feature.status as FeatureSummary["status"];
  const active = sessions.find(
    (session) =>
      session.feature_id === feature.id &&
      isActiveSessionStatus(sessionStatusKey(session)),
  );
  if (active) {
    if (sessionStatusKey(active).startsWith("waiting")) {
      return "NeedUserInput";
    }
    return runningStatusForPhase(active.phase ?? feature.current_phase) ?? declared;
  }

  return declared;
}

function featureTotalCost(
  featureID: string,
  sessions: SessionDTO[],
  detail?: FeatureDetailDTO,
): number | undefined {
  if (detail && detail.cost.total_usd > 0) return detail.cost.total_usd;
  const sessionTotal = sessions
    .filter((session) => session.feature_id === featureID)
    .reduce((sum, session) => sum + (session.usage?.cost_usd ?? 0), 0);
  return sessionTotal > 0 ? sessionTotal : undefined;
}

function sessionStatusKey(session: SessionDTO): string {
  return (session.status || session.turn_state || "").toLowerCase();
}

function isActiveSessionStatus(status: string): boolean {
  return (
    status === "active" ||
    status === "running" ||
    status === "waitinghelp" ||
    status === "waitingpermission" ||
    status === "waitinginput" ||
    status === "waiting_permission" ||
    status === "waiting_question"
  );
}

function hasActiveSessionForFeature(featureID: string, sessions: SessionDTO[]): boolean {
  return sessions.some(
    (session) =>
      session.feature_id === featureID &&
      isActiveSessionStatus(sessionStatusKey(session)),
  );
}

function runningStatusForPhase(phase?: string): FeatureSummary["status"] | null {
  switch ((phase ?? "").toLowerCase()) {
    case "inquire":
    case "inquiry":
      return "Inquiring";
    case "research":
      return "Researching";
    case "design":
      return "Designing";
    case "plan":
    case "planning":
      return "Planning";
    case "implement":
    case "implementation":
      return "Implementing";
    case "review":
      return "Reviewing";
    case "final_review":
    case "final review":
    case "finalreview":
      return "FinalReviewing";
    case "knowledge_base":
    case "knowledgebase":
    case "kb":
      return "BuildingKB";
    default:
      return null;
  }
}

function isReviewGateStatus(status: string): boolean {
  return status.endsWith("NeedsReview");
}

interface HealthDTO {
  status: string;
  started_at: string;
  owner: { version?: string };
}

interface FeatureListDTO {
  features: FeatureSummaryDTO[];
}

interface FeatureDetailDTOResponse {
  feature: FeatureDetailDTO;
}

interface FeatureSummaryDTO {
  id: string;
  name: string;
  slug: string;
  status: string;
  current_phase: string;
  cycle?: CycleDTO;
  active_run: number;
  run_count: number;
  repos: string[];
  created_at: string;
  checkpoints: CheckpointsDTO;
}

interface FeatureDetailDTO extends FeatureSummaryDTO {
  description?: string;
  failure?: { type?: string; message: string };
  summary?: string;
  pipeline?: string;
  models: FeatureDetail["models"];
  active_run_detail?: RunSummaryDTO;
  historical_runs: RunSummaryDTO[];
  repo_status: RepoStatusDTO[];
  timing: { total_seconds: number; by_phase: Record<string, number> };
  cost: { total_usd: number; by_phase: Record<string, number> };
  need_user_input?: { open: boolean; iteration?: number };
}

interface CheckpointsDTO {
  inquiry_review?: boolean;
  research_review?: boolean;
  design_review?: boolean;
  roadmap_review?: boolean;
  phase_plan_review?: boolean;
  manual_publish?: boolean;
}

interface CycleDTO {
  type?: string;
  status?: string;
  count?: number;
  iteration?: number;
}

interface RunSummaryDTO {
  run_number: number;
  started_at?: string;
  current_phase?: string;
  iteration?: number;
}

interface RepoStatusDTO {
  name: string;
  touched: boolean;
  pr_url?: string;
  last_error?: string;
  publishable: boolean;
  cycle_type?: string;
  cycle_status?: string;
}

interface RuntimeConfigDTO {
  feature_defaults: {
    models: FeatureDetail["models"];
    inquireness?: string;
    pipeline?: string;
    checkpoints: CheckpointsDTO;
  };
  repos: { name: string }[];
  workspace_roots?: string[];
}

interface ModelCatalogDTO {
  phase_provider_models: Record<string, Record<string, string[]>>;
}

interface RecoverySnapshotDTO {
  snapshot_id: string;
  items: {
    key: string;
    feature_id: string;
    feature_name?: string;
    repo_name?: string;
    phase?: string;
    iteration?: number;
    pid?: number;
    process_alive: boolean;
  }[];
}

interface ArtifactListDTO {
  artifacts: ArtifactDTO[];
}

interface ArtifactDTO {
  id: string;
  type: string;
  phase?: string;
  path?: string;
  size?: number;
}

interface TextContentDTO {
  text: string;
}

interface SessionListDTO {
  sessions: SessionDTO[];
}

interface SessionDetailDTOResponse {
  session: SessionDetailDTO;
}

interface SessionDetailDTO extends SessionDTO {
  pending_controls?: ControlRequestDTO[];
}

export interface ControlRequestDTO {
  request_id: string;
  session_id?: string;
  feature_id?: string;
  phase?: string;
  tool_name: string;
  status: string;
  summary?: string;
  input?: unknown;
  questions?: unknown;
}

interface SessionDTO {
  id: string;
  feature_id: string;
  phase?: string;
  repo?: string;
  kind?: string;
  label?: string;
  provider?: string;
  model?: string;
  status: string;
  turn_state?: string;
  started_at: string;
  iteration?: number;
  context_percentage?: number;
  usage?: {
    cost_usd?: number;
  };
}

interface PromptSnapshotDTO {
  help_queue: { feature_id: string; question: string; pending: boolean; time?: string }[];
  need_user_inputs: { feature_id?: string; open: boolean; iteration?: number }[];
}

interface PermissionSnapshotDTO {
  requests: {
    request_id: string;
    session_id?: string;
    feature_id?: string;
    tool_name: string;
    status: string;
    summary?: string;
  }[];
}

interface ReviewCommentsFetchDTO {
  comments: ReviewCommentDTO[];
}

interface ReviewCommentDTO {
  id: number;
  type?: string;
  repo_name?: string;
  path?: string;
  line?: number;
  body?: string;
  user_login?: string;
  created_at?: string;
  diff_hunk?: string;
  in_reply_to_id?: number;
}

interface ActionResultDTO {
  feature_id?: string;
  result: string;
  session_id?: string;
}

export interface TranscriptDTO {
  messages: TranscriptMessageDTO[];
}

export interface TranscriptMessageDTO {
  index: number;
  role: string;
  type: string;
  text?: string;
  tool?: string;
  status?: string;
}

interface SessionOutputDTO {
  data: string;
  size?: number;
}
