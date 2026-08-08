import {
  FeatureListResponseSchema,
  PermissionSnapshotResponseSchema,
  PromptSnapshotResponseSchema,
  SessionListResponseSchema,
  validateWithSchema,
  type ServerSessionSummary,
} from '../shared/api/parse';
import {
  AskUserAnswerRequestSchema,
  AttentionSnapshotSchema,
  GateDraftRequestSchema,
  GateResumeRequestSchema,
  HelpAnswerRequestSchema,
  PermissionDecisionRequestSchema,
  VerificationGateActionSchema,
  ATTENTION_ALREADY_RESOLVED_NOTICE,
  CHAT_SESSION_ID,
  isPendingReviewStatus,
  reviewKindLabel,
  type AttentionActionResult,
  type AttentionItem,
  type AttentionSnapshot,
  type AskUserAnswerRequest,
  type GateDraftRequest,
  type GateResumeRequest,
  type HelpAnswerRequest,
  type PermissionDecisionRequest,
  type VerificationGateAction,
} from '../shared/ipc';
import { SafeErrorException } from '../shared/errors';
import type { ApiRequestInit } from './gateway/runtimeGateway';
import { serverRequest, type ServerTransport } from './serverClient';

const REMEDIES = {
  remedyByCode: {
    bad_request: 'Refresh the item and correct the response.',
    conflict: ATTENTION_ALREADY_RESOLVED_NOTICE,
    not_found: ATTENTION_ALREADY_RESOLVED_NOTICE,
  },
};
const fallbackTime = '1970-01-01T00:00:00.000Z';

// Fallback for servers that predate HelpQueue.kind: mirrors the synthetic
// help text (internal/server/read_model.go, agentQuestionPrompt).
const syntheticHelpPrompt = 'Agent has a question';

function helpWaitingKind(help: {
  kind?: string;
  question: string;
  feature_id: string;
}): 'question' | 'input' | 'coordinating' {
  if (help.kind === 'input' || help.kind === 'question' || help.kind === 'coordinating') {
    return help.kind;
  }
  // Legacy servers send only the placeholder prose. A chat session is always
  // awaiting the user; anything else in that state is phase coordination.
  if (help.question.trim() === '' || help.question === syntheticHelpPrompt) {
    return help.feature_id === CHAT_SESSION_ID ? 'input' : 'coordinating';
  }
  return 'question';
}

const WAITING_TURN_STATES = new Set(['waiting_input', 'waiting_question']);

function waitingSessionFor(
  sessions: readonly ServerSessionSummary[],
  help: { feature_id: string; session_id?: string },
): ServerSessionSummary | undefined {
  return (
    sessions.find((session) => session.id === help.session_id) ??
    sessions.find(
      (session) =>
        session.feature_id === help.feature_id &&
        session.turn_state !== undefined &&
        WAITING_TURN_STATES.has(session.turn_state),
    )
  );
}

function runningTaskDescriptions(session: ServerSessionSummary | undefined): string[] {
  return (session?.task_activities ?? [])
    .filter((task) => task.state === 'running')
    .map((task) => task.description ?? '')
    .filter((description) => description !== '');
}

function supportedVerificationActions(actions: string[]): VerificationGateAction[] {
  const supported = new Set<VerificationGateAction>();
  for (const action of actions) {
    const parsed = VerificationGateActionSchema.safeParse(action.trim().toUpperCase());
    if (parsed.success) supported.add(parsed.data);
  }
  return [...supported];
}

/** Server-owned blocking prompts, translated once in the main process. */
export class AttentionService {
  constructor(private readonly transport: ServerTransport) {}

  async getSnapshot(): Promise<AttentionSnapshot> {
    const [promptsRaw, permissionsRaw, featuresRaw, sessionsRaw] = await Promise.all([
      this.get('/api/v1/prompts', PromptSnapshotResponseSchema),
      this.get('/api/v1/permissions', PermissionSnapshotResponseSchema),
      this.get('/api/v1/features', FeatureListResponseSchema),
      // Session provenance is best-effort: the inbox must survive without it.
      this.get('/api/v1/sessions', SessionListResponseSchema).catch(() => null),
    ]);
    const sessions = sessionsRaw?.sessions ?? [];
    const featureIDs = new Set(featuresRaw.features.map((feature) => feature.id));
    // Refactor passes never appear as top-level features, but their sessions
    // raise prompts under the child's feature id. Route them to the parent
    // tab instead of dropping them as orphans.
    const parentByChild = new Map(
      featuresRaw.features.flatMap((feature) =>
        feature.active_child === undefined ? [] : [[feature.active_child.id, feature.id] as const],
      ),
    );
    const hasListedFeature = (featureID: string | undefined): boolean =>
      featureID === undefined || featureIDs.has(featureID) || parentByChild.has(featureID);
    const hasRequiredListedFeature = (featureID: string | undefined): featureID is string =>
      featureID !== undefined && (featureIDs.has(featureID) || parentByChild.has(featureID));
    const parentOf = (featureID: string | undefined): { parentFeatureId: string } | object => {
      const parent = featureID === undefined ? undefined : parentByChild.get(featureID);
      return parent === undefined ? {} : { parentFeatureId: parent };
    };
    const items: AttentionItem[] = [
      ...permissionsRaw.requests
        .filter((request) => request.status === 'pending' && hasListedFeature(request.feature_id))
        .map((request) => ({
          kind: 'permission' as const,
          id: request.request_id,
          ...(request.feature_id === undefined ? {} : { featureId: request.feature_id }),
          ...parentOf(request.feature_id),
          ...(request.session_id === undefined ? {} : { sessionId: request.session_id }),
          ...(request.phase === undefined ? {} : { phase: request.phase }),
          toolName: request.tool_name,
          ...(request.summary === undefined ? {} : { summary: request.summary }),
          ...(request.input === undefined ? {} : { input: request.input }),
          waitingSince: request.waiting_since ?? fallbackTime,
          ...(request.remember === undefined
            ? {}
            : {
                remember: {
                  pattern: request.remember.pattern,
                  scope: request.remember.scope,
                  scopeDisplay: request.remember.scope_display,
                },
              }),
        })),
      ...promptsRaw.ask_user_questions
        .filter(
          (request) =>
            request.status === 'pending' &&
            (request.questions?.length ?? 0) > 0 &&
            hasListedFeature(request.feature_id),
        )
        .map((request) => ({
          kind: 'questions' as const,
          id: request.request_id,
          ...(request.feature_id === undefined ? {} : { featureId: request.feature_id }),
          ...parentOf(request.feature_id),
          ...(request.session_id === undefined ? {} : { sessionId: request.session_id }),
          ...(request.phase === undefined ? {} : { phase: request.phase }),
          waitingSince: request.waiting_since ?? fallbackTime,
          questions: request.questions!.map((question, index) => ({
            key: question.question ?? question.header ?? `Question ${index + 1}`,
            header: question.header ?? question.question ?? `Question ${index + 1}`,
            multiSelect: question.multi_select === true,
            options: (question.options ?? []).flatMap((option) =>
              option.label === undefined
                ? []
                : [
                    {
                      label: option.label,
                      ...(option.description === undefined
                        ? {}
                        : { description: option.description }),
                      ...(option.confidence === undefined ? {} : { confidence: option.confidence }),
                    },
                  ],
            ),
          })),
        })),
      ...promptsRaw.help_queue
        .filter(
          (help) =>
            help.pending &&
            (help.feature_id === CHAT_SESSION_ID || hasRequiredListedFeature(help.feature_id)),
        )
        .map((help) => {
          const chat = help.feature_id === CHAT_SESSION_ID;
          const session = chat ? undefined : waitingSessionFor(sessions, help);
          const sessionId = chat ? CHAT_SESSION_ID : (help.session_id ?? session?.id);
          const runningTasks = runningTaskDescriptions(session);
          return {
            kind: 'help' as const,
            id: `${help.feature_id}:${help.session_id ?? ''}`,
            ...(chat ? {} : { featureId: help.feature_id }),
            ...(chat ? {} : parentOf(help.feature_id)),
            ...(sessionId === undefined ? {} : { sessionId }),
            ...(session?.phase === undefined ? {} : { phase: session.phase }),
            waitingSince: help.time ?? fallbackTime,
            prompt: help.question,
            waitingKind: helpWaitingKind(help),
            ...(runningTasks.length === 0 ? {} : { runningTasks }),
          };
        }),
      ...promptsRaw.need_user_inputs
        .filter((gate) => gate.open && hasRequiredListedFeature(gate.feature_id))
        .map((gate) => ({
          kind: 'gate' as const,
          id: `${gate.feature_id}:${gate.repo_name ?? ''}`,
          featureId: gate.feature_id!,
          ...parentOf(gate.feature_id),
          waitingSince: gate.waiting_since ?? fallbackTime,
          ...(gate.scope === undefined ? {} : { scope: gate.scope }),
          ...(gate.repo_name === undefined ? {} : { repoName: gate.repo_name }),
          ...(gate.iteration === undefined ? {} : { iteration: gate.iteration }),
          ...(gate.summary === undefined ? {} : { summary: gate.summary }),
          ...(gate.verification === undefined
            ? {}
            : {
                verification: {
                  blockers: gate.verification.blockers.map((blocker) => ({
                    itemId: blocker.item_id,
                    name: blocker.name,
                    ...(blocker.repo_name === undefined ? {} : { repoName: blocker.repo_name }),
                    command: blocker.command,
                    reason: blocker.reason,
                    capabilities: blocker.capabilities,
                    remediation: blocker.remediation,
                  })),
                  allowedActions: supportedVerificationActions(gate.verification.allowed_actions),
                },
              }),
          questions: (gate.questions ?? []).map((question, index) => ({
            index: question.index !== undefined && question.index > 0 ? question.index : index + 1,
            prompt: question.prompt ?? `Question ${index + 1}`,
            answer: question.answer ?? '',
          })),
        })),
      ...featuresRaw.features
        .filter((feature) => isPendingReviewStatus(feature.status))
        .map((feature) => ({
          kind: 'review' as const,
          // This identity changes only when the server opens a new review or advances it.
          id: `review:${feature.id}:${feature.active_run}:${feature.current_phase}:${feature.status}`,
          featureId: feature.id,
          waitingSince: feature.created_at ?? fallbackTime,
          reviewKind: reviewKindLabel(feature.status),
          phase: feature.current_phase,
        })),
    ];
    const unique = new Map(items.map((item) => [item.id, item]));
    const classRank: Record<AttentionItem['kind'], number> = {
      recovery: 0,
      permission: 1,
      questions: 2,
      gate: 3,
      review: 4,
      help: 5,
    };
    return validateWithSchema(
      {
        items: [...unique.values()].sort(
          (a, b) =>
            a.waitingSince.localeCompare(b.waitingSince) ||
            classRank[a.kind] - classRank[b.kind] ||
            a.id.localeCompare(b.id),
        ),
      },
      AttentionSnapshotSchema,
    );
  }

  async answerPermission(request: PermissionDecisionRequest): Promise<AttentionActionResult> {
    const input = validateWithSchema(request, PermissionDecisionRequestSchema);
    return this.mutate('/api/v1/permissions/answer', {
      request_id: input.requestId,
      ...(input.sessionId === undefined ? {} : { session_id: input.sessionId }),
      decision: input.decision,
      ...(input.decision === 'allow_remember'
        ? { remember_pattern: input.rememberPattern, remember_scope: input.rememberScope }
        : {}),
    });
  }
  async answerQuestions(request: AskUserAnswerRequest): Promise<AttentionActionResult> {
    const input = validateWithSchema(request, AskUserAnswerRequestSchema);
    return this.mutate('/api/v1/prompts/ask-user/answer', {
      request_id: input.requestId,
      ...(input.sessionId === undefined ? {} : { session_id: input.sessionId }),
      answers: input.answers,
    });
  }
  async sendHelp(request: HelpAnswerRequest): Promise<AttentionActionResult> {
    const input = validateWithSchema(request, HelpAnswerRequestSchema);
    return this.mutate('/api/v1/prompts/help/send', {
      ...(input.featureId === undefined ? {} : { feature_id: input.featureId }),
      ...(input.sessionId === undefined ? {} : { session_id: input.sessionId }),
      message: input.message,
    });
  }
  async saveGateDraft(request: GateDraftRequest): Promise<AttentionActionResult> {
    const input = validateWithSchema(request, GateDraftRequestSchema);
    return this.mutate(`/api/v1/features/${input.featureId}/actions/need-user-input-draft`, {
      ...(input.repoName === undefined ? {} : { repo_name: input.repoName }),
      answers: input.answers,
    });
  }
  async resolveGate(request: GateResumeRequest): Promise<AttentionActionResult> {
    const input = validateWithSchema(request, GateResumeRequestSchema);
    return this.mutate(`/api/v1/features/${input.featureId}/actions/need-user-input`, {
      ...(input.repoName === undefined ? {} : { repo_name: input.repoName }),
    });
  }

  private async get<T extends { api_version: string }>(
    path: string,
    schema: { parse: (value: unknown) => T },
  ): Promise<T> {
    const body = await serverRequest(this.transport, path, undefined, REMEDIES);
    return schema.parse(body);
  }
  private async mutate(
    path: string,
    body: Record<string, unknown>,
  ): Promise<AttentionActionResult> {
    try {
      const response = await serverRequest(
        this.transport,
        path,
        { method: 'POST', body } as ApiRequestInit,
        REMEDIES,
      );
      const value = response as {
        result?: unknown;
        permission_answer_response?: { audit_warning?: unknown; already_existed?: unknown };
      };
      const permission = value.permission_answer_response;
      return {
        result: typeof value.result === 'string' ? value.result : 'Submitted.',
        ...(permission?.already_existed === true
          ? { notice: 'A matching remembered rule already exists.' }
          : {}),
        ...(typeof permission?.audit_warning === 'string'
          ? { notice: permission.audit_warning }
          : {}),
      };
    } catch (error) {
      if (
        error instanceof SafeErrorException &&
        (error.safe.code === 'conflict' ||
          error.safe.code === 'not_found' ||
          (error.safe.code === 'bad_request' &&
            /^pending request \S+ not found$/i.test(error.safe.message)))
      )
        return {
          result: 'Already resolved.',
          alreadyResolved: true,
          notice: ATTENTION_ALREADY_RESOLVED_NOTICE,
        };
      throw error;
    }
  }
}
