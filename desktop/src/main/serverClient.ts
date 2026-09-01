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
 * Shared narrow client for the authoritative server's authenticated REST
 * surface. Main-process services route every request through here so the
 * success check, structured-error parsing, redaction, and fail-closed
 * fallback stay identical across services instead of drifting per call site.
 * A canonical server error crosses unchanged (the catalog owns its human
 * text); anything else degrades to the main-process transport safe error.
 */
import { randomUUID } from 'node:crypto';
import {
  CanonicalErrorException,
  redactText,
  requiresLocalServerError,
  SafeErrorException,
  safeError,
  toSafeError,
} from '../shared/errors';
import {
  CanonicalErrorResponseSchema,
  SessionDetailResponseSchema,
  SessionListResponseSchema,
  SessionOutputChunkSchema,
  TranscriptResponseSchema,
  parseServerJson,
  validateWithSchema,
  type ServerSessionDetail,
  type ServerSessionSummary,
  type ServerTranscriptMessage,
} from '../shared/api/parse';
import {
  ChatActionResultSchema,
  ChatStartRequestSchema,
  SessionDetailSchema,
  SessionIdSchema,
  SessionOutputEventSchema,
  SessionOutputOpenRequestSchema,
  SessionSummarySchema,
  SessionTranscriptRequestSchema,
  SessionTranscriptSchema,
  type SessionDetail,
  type SessionOutputEvent,
  type SessionOutputOpenRequest,
  type SessionSummary,
  type SessionTranscript,
  type SessionTranscriptRequest,
  type TranscriptMessage,
  type ChatActionResult,
  type ChatStartRequest,
} from '../shared/ipc';
import { assertCompatibleApiVersion } from '../shared/apiVersion';
import { assertNoPrototypePollution, assertWithinByteSize } from '../shared/sanitize';
import { SseBlockAssembler, type SseBlock, type SseStream } from './gateway/events';
import type { ApiRequestInit, HttpResult } from './gateway/runtimeGateway';
import { alwaysLocal, type LocalitySource } from './locality';

/** The authenticated transport surface the runtime gateway provides. */
export interface ServerTransport {
  apiRequest(path: string, init?: ApiRequestInit): Promise<HttpResult>;
  openSessionOutputStream?(sessionId: string, options?: { from?: number }): Promise<SseStream>;
}

/**
 * Performs one authenticated request and returns the raw success body for
 * the caller to schema-validate. Non-2xx responses throw either a
 * CanonicalErrorException (the server's catalog-rendered error, passed
 * through unchanged) or the main-process transport SafeErrorException;
 * nothing from the wire crosses unredacted.
 */
export async function serverRequest(
  transport: ServerTransport,
  path: string,
  init: ApiRequestInit | undefined,
): Promise<unknown> {
  const result = await transport.apiRequest(path, init);
  if (result.status >= 200 && result.status < 300) {
    assertSafeParsedPayload(result.body);
    return result.body;
  }
  throw mapServerError(result);
}

/** Applies byte, pollution, and API-version gates to already-decoded JSON. */
function assertSafeParsedPayload(body: unknown): void {
  assertNoPrototypePollution(body);
  const serialized = JSON.stringify(body);
  assertWithinByteSize(serialized ?? '');
  if (typeof body === 'object' && body !== null && 'api_version' in body) {
    const version = (body as { api_version?: unknown }).api_version;
    assertCompatibleApiVersion(typeof version === 'string' ? version : '');
  }
}

export class SessionService {
  private readonly subscriptions = new Map<
    string,
    { cancelled: boolean; stream: SseStream | null; closed: boolean }
  >();

  constructor(
    private readonly transport: ServerTransport,
    private readonly makeSubscriptionId: () => string = randomUUID,
    /**
     * Gateway-owned locality of the active connection. While remote, chat
     * start refuses any local image path outright (a stale draft must fail,
     * never leak one) and forwards staged upload references instead.
     */
    private readonly locality: LocalitySource = alwaysLocal,
  ) {}

  async list(): Promise<SessionSummary[]> {
    const response = validateWithSchema(
      await this.api('/api/v1/sessions'),
      SessionListResponseSchema,
    );
    return response.sessions.map(toSessionSummary);
  }

  async get(sessionId: string): Promise<SessionDetail> {
    const id = validateWithSchema(sessionId, SessionIdSchema);
    const response = validateWithSchema(
      await this.api(`/api/v1/sessions/${id}`),
      SessionDetailResponseSchema,
    );
    return toSessionDetail(response.session);
  }

  async transcript(request: SessionTranscriptRequest): Promise<SessionTranscript> {
    const input = validateWithSchema(request, SessionTranscriptRequestSchema);
    const query = new URLSearchParams();
    if (input.offset !== undefined) query.set('offset', String(input.offset));
    if (input.limit !== undefined) query.set('limit', String(input.limit));
    const suffix = query.size === 0 ? '' : `?${query.toString()}`;
    const response = validateWithSchema(
      await this.api(`/api/v1/sessions/${input.sessionId}/transcript${suffix}`),
      TranscriptResponseSchema,
    );
    return validateWithSchema(
      {
        sessionId: input.sessionId,
        cursor: response.cursor,
        messages: response.messages.map(toTranscriptMessage),
      },
      SessionTranscriptSchema,
    );
  }

  async startChat(request: ChatStartRequest): Promise<ChatActionResult> {
    const input = validateWithSchema(request, ChatStartRequestSchema);
    const remote = this.locality() === 'remote';
    if (remote && (input.images?.length ?? 0) > 0) {
      // A locally shaped path remotely is a stale draft: fail, never leak.
      throw new SafeErrorException(requiresLocalServerError());
    }
    const response = await serverRequest(this.transport, '/api/v1/prompts/chat/start', {
      method: 'POST',
      body: {
        message: input.message,
        images: input.images ?? [],
        ...(remote && (input.imageUploads?.length ?? 0) > 0
          ? { image_uploads: input.imageUploads }
          : {}),
      },
    } as ApiRequestInit);
    const raw = response as { session_id?: unknown; result?: unknown };
    return validateWithSchema(
      {
        sessionId: raw.session_id,
        result: raw.result,
      },
      ChatActionResultSchema,
    );
  }

  async endChat(): Promise<ChatActionResult> {
    const response = await serverRequest(this.transport, '/api/v1/prompts/chat/end', {
      method: 'POST',
      body: {},
    } as ApiRequestInit);
    const raw = response as { session_id?: unknown; result?: unknown };
    return validateWithSchema(
      {
        sessionId: raw.session_id,
        result: raw.result,
      },
      ChatActionResultSchema,
    );
  }

  subscribe(request: SessionOutputOpenRequest, emit: (event: SessionOutputEvent) => void): string {
    const input = validateWithSchema(request, SessionOutputOpenRequestSchema);
    const subscriptionId = this.makeSubscriptionId();
    const state = { cancelled: false, stream: null as SseStream | null, closed: false };
    this.subscriptions.set(subscriptionId, state);
    void this.consume(subscriptionId, input, state, emit);
    return subscriptionId;
  }

  cancel(subscriptionId: string): boolean {
    const state = this.subscriptions.get(subscriptionId);
    if (state === undefined) return false;
    state.cancelled = true;
    this.close(state);
    this.subscriptions.delete(subscriptionId);
    return true;
  }

  cancelAll(): void {
    for (const subscriptionId of [...this.subscriptions.keys()]) this.cancel(subscriptionId);
  }

  activeSubscriptionCount(): number {
    return this.subscriptions.size;
  }

  private async consume(
    subscriptionId: string,
    input: SessionOutputOpenRequest,
    state: { cancelled: boolean; stream: SseStream | null; closed: boolean },
    emit: (event: SessionOutputEvent) => void,
  ): Promise<void> {
    try {
      const openStream = this.transport.openSessionOutputStream;
      if (openStream === undefined) {
        throw new SafeErrorException(
          safeError('E_SSE_UNAVAILABLE', 'This build has no session output transport wired.'),
        );
      }
      const stream = await openStream.call(this.transport, input.sessionId, {
        ...(input.from === undefined ? {} : { from: input.from }),
      });
      state.stream = stream;
      if (state.cancelled) return;
      if (stream.status !== 200) {
        throw new SafeErrorException(
          safeError(`E_HTTP_${stream.status}`, 'The runtime rejected the session output stream.'),
        );
      }
      const assembler = new SseBlockAssembler();
      for await (const line of stream.lines) {
        if (state.cancelled) return;
        const block = assembler.push(line);
        if (block === null) continue;
        const parsed = parseSessionOutputBlock(block);
        const event: SessionOutputEvent =
          parsed.type === 'record'
            ? {
                subscriptionId,
                type: 'record',
                sessionId: parsed.sessionId,
                index: parsed.index,
                message: parsed.message,
              }
            : {
                subscriptionId,
                type: 'done',
                sessionId: parsed.sessionId,
                nextIndex: parsed.nextIndex,
              };
        emit(validateWithSchema(event, SessionOutputEventSchema));
        if (parsed.type === 'done') return;
      }
    } catch (error) {
      if (!state.cancelled) {
        try {
          emit(
            validateWithSchema(
              {
                subscriptionId,
                type: 'error',
                sessionId: input.sessionId,
                error: toSafeError(error, 'E_SESSION_STREAM'),
              },
              SessionOutputEventSchema,
            ),
          );
        } catch {
          // The renderer may disappear between stream failure and error delivery.
        }
      }
    } finally {
      this.close(state);
      if (this.subscriptions.get(subscriptionId) === state) {
        this.subscriptions.delete(subscriptionId);
      }
    }
  }

  private close(state: { stream: SseStream | null; closed: boolean }): void {
    if (state.closed || state.stream === null) return;
    state.closed = true;
    state.stream.close();
  }

  private api(path: string): Promise<unknown> {
    return serverRequest(this.transport, path, undefined);
  }
}

export type ParsedSessionOutput =
  | { type: 'record'; sessionId: string; index: number; message: TranscriptMessage }
  | { type: 'done'; sessionId: string; nextIndex: number };

/** Parses one bounded, versioned session-output SSE block. */
export function parseSessionOutputBlock(block: SseBlock): ParsedSessionOutput {
  if (!['session.output', 'session.output.done'].includes(block.event)) {
    throw new SafeErrorException(safeError('E_STREAM_PROTOCOL', 'Unknown session output event.'));
  }
  const chunk = parseServerJson(block.data, SessionOutputChunkSchema, 2 * 1024 * 1024);
  if (chunk.session_id === undefined || chunk.session_id === '') {
    throw new SafeErrorException(
      safeError('E_STREAM_PROTOCOL', 'Session output omitted its session ID.'),
    );
  }
  if (block.event === 'session.output.done' || chunk.done === true) {
    return { type: 'done', sessionId: chunk.session_id, nextIndex: chunk.index };
  }
  if (chunk.message === undefined || chunk.message.index !== chunk.index) {
    throw new SafeErrorException(
      safeError('E_STREAM_PROTOCOL', 'Session output row cursor did not match its message.'),
    );
  }
  return {
    type: 'record',
    sessionId: chunk.session_id,
    index: chunk.index,
    message: toTranscriptMessage(chunk.message),
  };
}

export function toSessionSummary(session: ServerSessionSummary): SessionSummary {
  return validateWithSchema(
    {
      id: session.id,
      featureId: session.feature_id,
      runNumber: session.run_number,
      phase: session.phase,
      ...(session.repo === undefined ? {} : { repo: session.repo }),
      kind: session.kind,
      ...(session.label === undefined ? {} : { label: session.label }),
      ...(session.provider === undefined ? {} : { provider: session.provider }),
      ...(session.model === undefined ? {} : { model: session.model }),
      status: session.status,
      ...(session.turn_state === undefined ? {} : { turnState: session.turn_state }),
      startedAt: session.started_at,
      ...(session.iteration === undefined ? {} : { iteration: session.iteration }),
      ...(session.context_percentage === undefined
        ? {}
        : { contextPercentage: session.context_percentage }),
      taskActivities: session.task_activities.map((task) => ({
        taskId: task.task_id,
        ...(task.tool_use_id === undefined ? {} : { toolUseId: task.tool_use_id }),
        ...(task.child_session_id === undefined ? {} : { childSessionId: task.child_session_id }),
        ...(task.description === undefined ? {} : { description: task.description }),
        state: task.state,
        ...(task.last_tool_name === undefined ? {} : { lastToolName: task.last_tool_name }),
        ...(task.last_path === undefined ? {} : { lastPath: task.last_path }),
        ...(task.status === undefined ? {} : { status: task.status }),
        ...(task.summary === undefined ? {} : { summary: task.summary }),
        ...(task.output_file === undefined ? {} : { outputFile: task.output_file }),
        ...(task.usage?.total_tokens === undefined ? {} : { totalTokens: task.usage.total_tokens }),
        ...(task.usage?.tool_uses === undefined ? {} : { toolUses: task.usage.tool_uses }),
        ...(task.usage?.duration_ms === undefined ? {} : { durationMs: task.usage.duration_ms }),
        startedAt: task.started_at,
        updatedAt: task.updated_at,
        ...(task.finished_at === undefined ? {} : { finishedAt: task.finished_at }),
      })),
      runningTaskCount: session.running_task_count,
      usage: {
        ...(session.usage.input_tokens === undefined
          ? {}
          : { inputTokens: session.usage.input_tokens }),
        ...(session.usage.output_tokens === undefined
          ? {}
          : { outputTokens: session.usage.output_tokens }),
        ...(session.usage.cost_usd === undefined ? {} : { costUsd: session.usage.cost_usd }),
      },
    },
    SessionSummarySchema,
  );
}

function toSessionDetail(session: ServerSessionDetail): SessionDetail {
  return validateWithSchema(
    {
      ...toSessionSummary(session),
      transcriptCursor: session.transcript_cursor,
      pendingControlCount: session.pending_controls.length,
      ...(session.initial_prompt === undefined ? {} : { initialPrompt: session.initial_prompt }),
      canAttach: session.can_attach,
      logAvailable: session.log_available,
      ...(session.safe_error === undefined ? {} : { safeError: redactText(session.safe_error) }),
    },
    SessionDetailSchema,
  );
}

export function toTranscriptMessage(message: ServerTranscriptMessage): TranscriptMessage {
  return {
    index: message.index,
    ...(message.block_index === undefined ? {} : { blockIndex: message.block_index }),
    role: message.role,
    type: message.type,
    ...(message.text === undefined ? {} : { text: message.text }),
    ...(message.tool === undefined ? {} : { tool: message.tool }),
    ...(message.status === undefined ? {} : { status: message.status }),
    ...(message.redacted === undefined ? {} : { redacted: message.redacted }),
    ...(message.locally_appended === undefined
      ? {}
      : { locallyAppended: message.locally_appended }),
    ...(message.auto_picked === undefined ? {} : { autoPicked: message.auto_picked }),
    ...(message.auto_pick_question === undefined
      ? {}
      : { autoPickQuestion: message.auto_pick_question }),
    ...(message.auto_pick_confidence === undefined
      ? {}
      : { autoPickConfidence: message.auto_pick_confidence }),
    ...(message.file_change === undefined
      ? {}
      : {
          fileChange: {
            ...(message.file_change.path === undefined ? {} : { path: message.file_change.path }),
            ...(message.file_change.old_path === undefined
              ? {}
              : { oldPath: message.file_change.old_path }),
            ...(message.file_change.operation === undefined
              ? {}
              : { operation: message.file_change.operation }),
            ...(message.file_change.detail === undefined
              ? {}
              : { detail: message.file_change.detail }),
            ...(message.file_change.added_lines === undefined
              ? {}
              : { addedLines: message.file_change.added_lines }),
            ...(message.file_change.removed_lines === undefined
              ? {}
              : { removedLines: message.file_change.removed_lines }),
            ...(message.file_change.has_diff_patch === undefined
              ? {}
              : { hasDiffPatch: message.file_change.has_diff_patch }),
          },
        }),
    ...(message.tool_call === undefined
      ? {}
      : {
          toolCall: {
            ...(message.tool_call.summary === undefined
              ? {}
              : { summary: message.tool_call.summary }),
            ...(message.tool_call.prompt === undefined ? {} : { prompt: message.tool_call.prompt }),
          },
        }),
    ...(message.task === undefined
      ? {}
      : {
          task: {
            ...(message.task.id === undefined ? {} : { id: message.task.id }),
            ...(message.task.tool_use_id === undefined
              ? {}
              : { toolUseId: message.task.tool_use_id }),
            ...(message.task.description === undefined
              ? {}
              : { description: message.task.description }),
            ...(message.task.task_type === undefined ? {} : { taskType: message.task.task_type }),
            ...(message.task.prompt === undefined ? {} : { prompt: message.task.prompt }),
            ...(message.task.last_tool_name === undefined
              ? {}
              : { lastToolName: message.task.last_tool_name }),
            ...(message.task.status === undefined ? {} : { status: message.task.status }),
            ...(message.task.summary === undefined ? {} : { summary: message.task.summary }),
            ...(message.task.output_file === undefined
              ? {}
              : { outputFile: message.task.output_file }),
          },
        }),
  };
}

/**
 * Maps a non-2xx server response. A body that parses as the canonical error
 * envelope crosses unchanged as a CanonicalErrorException — the catalog owns
 * its title, summary, remediation, and typed context. Anything else (or a
 * body that fails canonical parsing) fails closed to the main-process
 * transport SafeError.
 */
export function mapServerError(result: HttpResult): SafeErrorException | CanonicalErrorException {
  const parsed = CanonicalErrorResponseSchema.safeParse(result.body);
  if (parsed.success) {
    // The catalog owns every authored field, so the object crosses intact;
    // only the raw diagnostics text is free-form server output, and it alone
    // is redacted before crossing the IPC boundary.
    const error = parsed.data.error;
    const canonical =
      error.diagnostics === undefined
        ? error
        : { ...error, diagnostics: redactText(error.diagnostics) };
    return new CanonicalErrorException(canonical);
  }
  return new SafeErrorException(
    safeError(
      `E_HTTP_${result.status}`,
      'The runtime rejected the request.',
      'Retry; if this persists, restart the runtime and check its log.',
    ),
  );
}
