/**
 * Shared narrow client for the authoritative server's authenticated REST
 * surface. Main-process services route every request through here so the
 * success check, structured-error parsing, redaction, and fail-closed
 * fallback stay identical across services instead of drifting per call site.
 * Services stay in charge of their own domain vocabulary: each supplies its
 * remedy-by-code table (and whether 409 `not_ready` target issues fold into
 * the remediation).
 */
import { randomUUID } from 'node:crypto';
import { redactText, SafeErrorException, safeError, toSafeError } from '../shared/errors';
import {
  ServerErrorResponseSchema,
  ServerErrorWithIssuesSchema,
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
} from '../shared/ipc';
import { assertCompatibleApiVersion } from '../shared/apiVersion';
import { assertNoPrototypePollution, assertWithinByteSize } from '../shared/sanitize';
import { SseBlockAssembler, type SseBlock, type SseStream } from './gateway/events';
import type { ApiRequestInit, HttpResult } from './gateway/runtimeGateway';

/** The authenticated transport surface the runtime gateway provides. */
export interface ServerTransport {
  apiRequest(path: string, init?: ApiRequestInit): Promise<HttpResult>;
  openSessionOutputStream?(sessionId: string, options?: { from?: number }): Promise<SseStream>;
}

/** How a service maps structured server rejections to safe errors. */
export interface ServerErrorMapping {
  /** Concrete, safe next steps per structured server error code. */
  remedyByCode: Readonly<Record<string, string>>;
  /**
   * When true, error bodies are parsed with the `target.issues` extension
   * (409 `not_ready` rejections) and the redacted issue messages are folded
   * into the remediation so the caller can show why. When false, `target`
   * payloads are ignored entirely.
   */
  foldTargetIssues?: boolean;
}

/**
 * Performs one authenticated request and returns the raw success body for
 * the caller to schema-validate. Non-2xx responses throw the mapped
 * SafeErrorException; nothing from the wire crosses unredacted.
 */
export async function serverRequest(
  transport: ServerTransport,
  path: string,
  init: ApiRequestInit | undefined,
  mapping: ServerErrorMapping,
): Promise<unknown> {
  const result = await transport.apiRequest(path, init);
  if (result.status >= 200 && result.status < 300) {
    assertSafeParsedPayload(result.body);
    return result.body;
  }
  throw mapServerError(result, mapping);
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

const SESSION_REMEDIES: Readonly<Record<string, string>> = {
  not_found: 'The session no longer exists. Refresh the feature to find its current session.',
  conflict: 'Refresh the feature and session snapshots, then retry.',
};

export class SessionService {
  private readonly subscriptions = new Map<
    string,
    { cancelled: boolean; stream: SseStream | null; closed: boolean }
  >();

  constructor(
    private readonly transport: ServerTransport,
    private readonly makeSubscriptionId: () => string = randomUUID,
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
            : parsed.type === 'done'
              ? {
                  subscriptionId,
                  type: 'done',
                  sessionId: parsed.sessionId,
                  nextIndex: parsed.nextIndex,
                }
              : {
                  subscriptionId,
                  type: 'error',
                  sessionId: input.sessionId,
                  error: parsed.error,
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
    return serverRequest(this.transport, path, undefined, { remedyByCode: SESSION_REMEDIES });
  }
}

export type ParsedSessionOutput =
  | { type: 'record'; sessionId: string; index: number; message: TranscriptMessage }
  | { type: 'done'; sessionId: string; nextIndex: number }
  | { type: 'error'; error: { code: string; message: string; remediation?: string } };

/** Parses one bounded, versioned session-output SSE block. */
export function parseSessionOutputBlock(block: SseBlock): ParsedSessionOutput {
  if (!['session.output', 'session.output.done', 'session.output.error'].includes(block.event)) {
    throw new SafeErrorException(safeError('E_STREAM_PROTOCOL', 'Unknown session output event.'));
  }
  const chunk = parseServerJson(block.data, SessionOutputChunkSchema, 2 * 1024 * 1024);
  if (block.event === 'session.output.error') {
    return {
      type: 'error',
      error: safeError(
        'E_SESSION_OUTPUT',
        'The runtime reported a session output error.',
        'Refresh the session transcript, then reconnect the stream.',
      ),
    };
  }
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

function toTranscriptMessage(message: ServerTranscriptMessage): TranscriptMessage {
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

/** Maps a structured server error body into a SafeError, failing closed. */
export function mapServerError(
  result: HttpResult,
  mapping: ServerErrorMapping,
): SafeErrorException {
  if (mapping.foldTargetIssues === true) {
    const parsed = ServerErrorWithIssuesSchema.safeParse(result.body);
    if (parsed.success) {
      const { code, message, target } = parsed.data.error;
      const issues = target?.issues ?? [];
      const issueText = issues.map((issue) => redactText(issue.message)).join(' ');
      const remedy =
        issueText !== ''
          ? `${issueText} ${mapping.remedyByCode[code] ?? ''}`.trim()
          : mapping.remedyByCode[code];
      return new SafeErrorException(safeError(code, redactText(message), remedy));
    }
  } else {
    const parsed = ServerErrorResponseSchema.safeParse(result.body);
    if (parsed.success) {
      const { code, message } = parsed.data.error;
      return new SafeErrorException(
        safeError(code, redactText(message), mapping.remedyByCode[code]),
      );
    }
  }
  return new SafeErrorException(
    safeError(
      `E_HTTP_${result.status}`,
      'The runtime rejected the request.',
      'Retry; if this persists, restart the runtime and check its log.',
    ),
  );
}
