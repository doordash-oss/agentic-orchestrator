import {
  FeatureListResponseSchema,
  PermissionSnapshotResponseSchema,
  PromptSnapshotResponseSchema,
  validateWithSchema,
} from '../shared/api/parse';
import {
  AskUserAnswerRequestSchema,
  AttentionSnapshotSchema,
  GateDraftRequestSchema,
  GateResolutionRequestSchema,
  HelpAnswerRequestSchema,
  PermissionDecisionRequestSchema,
  ATTENTION_ALREADY_RESOLVED_NOTICE,
  isPendingReviewStatus,
  reviewKindLabel,
  type AttentionActionResult,
  type AttentionItem,
  type AttentionSnapshot,
  type AskUserAnswerRequest,
  type GateDraftRequest,
  type GateResolutionRequest,
  type HelpAnswerRequest,
  type PermissionDecisionRequest,
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

/** Server-owned blocking prompts, translated once in the main process. */
export class AttentionService {
  constructor(private readonly transport: ServerTransport) {}

  async getSnapshot(): Promise<AttentionSnapshot> {
    const [promptsRaw, permissionsRaw, featuresRaw] = await Promise.all([
      this.get('/api/v1/prompts', PromptSnapshotResponseSchema),
      this.get('/api/v1/permissions', PermissionSnapshotResponseSchema),
      this.get('/api/v1/features', FeatureListResponseSchema),
    ]);
    const items: AttentionItem[] = [
      ...permissionsRaw.requests
        .filter((request) => request.status === 'pending')
        .map((request) => ({
          kind: 'permission' as const,
          id: request.request_id,
          ...(request.feature_id === undefined ? {} : { featureId: request.feature_id }),
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
        .filter((request) => request.status === 'pending' && (request.questions?.length ?? 0) > 0)
        .map((request) => ({
          kind: 'questions' as const,
          id: request.request_id,
          ...(request.feature_id === undefined ? {} : { featureId: request.feature_id }),
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
        .filter((help) => help.pending)
        .map((help) => ({
          kind: 'help' as const,
          id: `${help.feature_id}:${help.session_id ?? ''}`,
          featureId: help.feature_id,
          ...(help.session_id === undefined ? {} : { sessionId: help.session_id }),
          waitingSince: help.time ?? fallbackTime,
          prompt: help.question,
        })),
      ...promptsRaw.need_user_inputs
        .filter((gate) => gate.open && gate.feature_id !== undefined)
        .map((gate) => ({
          kind: 'gate' as const,
          id: `${gate.feature_id}:${gate.repo_name ?? ''}:${gate.cycle_type ?? ''}`,
          featureId: gate.feature_id!,
          waitingSince: gate.waiting_since ?? fallbackTime,
          ...(gate.scope === undefined ? {} : { scope: gate.scope }),
          ...(gate.repo_name === undefined ? {} : { repoName: gate.repo_name }),
          ...(gate.cycle_type === undefined ? {} : { cycleType: gate.cycle_type }),
          ...(gate.iteration === undefined ? {} : { iteration: gate.iteration }),
          ...(gate.summary === undefined ? {} : { summary: gate.summary }),
          questions: (gate.questions ?? []).map((question, index) => ({
            index: question.index ?? index,
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
      feature_id: input.featureId,
      ...(input.sessionId === undefined ? {} : { session_id: input.sessionId }),
      message: input.message,
    });
  }
  async saveGateDraft(request: GateDraftRequest): Promise<AttentionActionResult> {
    const input = validateWithSchema(request, GateDraftRequestSchema);
    return this.mutate(`/api/v1/features/${input.featureId}/actions/need-user-input-draft`, {
      ...(input.repoName === undefined ? {} : { repo_name: input.repoName }),
      ...(input.cycleType === undefined ? {} : { cycle_type: input.cycleType }),
      answers: input.answers,
    });
  }
  async resolveGate(request: GateResolutionRequest): Promise<AttentionActionResult> {
    const input = validateWithSchema(request, GateResolutionRequestSchema);
    return this.mutate(`/api/v1/features/${input.featureId}/actions/need-user-input`, {
      decision: input.decision,
      ...(input.repoName === undefined ? {} : { repo_name: input.repoName }),
      ...(input.cycleType === undefined ? {} : { cycle_type: input.cycleType }),
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
        (error.safe.code === 'conflict' || error.safe.code === 'not_found')
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
