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
 * Main-process client for the server's supervisor conversation
 * (`/api/v1/supervisor/*`). Every call is fenced by the connected server's
 * identity and connection generation, so a reply that crosses a server
 * switch is discarded instead of being shown against the new server.
 * Server DTOs are parsed with bounded wire schemas, mapped to the camelCase
 * IPC types, and re-validated before they cross to the renderer. The send
 * idempotency key is minted here; the renderer never supplies it.
 */
import { randomUUID } from 'node:crypto';
import {
  SupervisorActionResponseSchema,
  SupervisorMessageResponseSchema,
  SupervisorResetResponseSchema,
  SupervisorStateResponseSchema,
  SupervisorTranscriptResponseSchema,
  validateWithSchema,
  type ServerSupervisorRecord,
  type ServerSupervisorState,
  type SupervisorTranscriptResponse,
} from '../shared/api/parse';
import {
  SupervisorActionResultSchema,
  SupervisorMessageRequestSchema,
  SupervisorMessageResultSchema,
  SupervisorRecordSchema,
  SupervisorResetResultSchema,
  SupervisorSettingsRequestSchema,
  SupervisorPendingChangeCancelRequestSchema,
  SupervisorStateSchema,
  SupervisorTranscriptPageSchema,
  SupervisorTranscriptRequestSchema,
  type ErrorReference,
  type SupervisorActionResult,
  type SupervisorMessageRequest,
  type SupervisorMessageResult,
  type SupervisorRecord,
  type SupervisorResetResult,
  type SupervisorSettingsRequest,
  type SupervisorPendingChangeCancelRequest,
  type SupervisorState,
  type SupervisorTranscriptPage,
  type SupervisorTranscriptRequest,
} from '../shared/ipc';
import { supervisorPendingRequest } from './attention';
import { alwaysLocal, assertNoLocalPathsRemotely, type LocalitySource } from './locality';
import { toTranscriptMessage, type ServerTransport } from './serverClient';
import { fencedServerRequest, type ServerIdentitySource } from './serverFence';

/** The server's `client_message_id` syntax; a minted key that fails it is never sent. */
const CLIENT_MESSAGE_ID_PATTERN = /^[A-Za-z0-9._-]{1,128}$/;

export interface SupervisorServiceDeps {
  transport: ServerTransport;
  /** Connected-server identity for request fencing. */
  identity?: ServerIdentitySource;
  /** Mints the per-send idempotency key; defaults to a random UUID. */
  makeClientMessageId?: () => string;
  /**
   * Gateway-owned locality of the active connection. While remote, a send
   * carrying local image or file paths is refused (E_REQUIRES_LOCAL_SERVER)
   * and staged upload references travel instead; local sends carry paths.
   */
  locality?: LocalitySource;
}

/**
 * Re-serializes an error-home reference to the server's snake_case wire
 * shape. The server rejects unknown fields and empty-string keys, so only the
 * fields the reference actually carries cross the wire.
 */
export function toWireErrorReference(reference: ErrorReference): Record<string, string> {
  return {
    scope: reference.scope,
    code: reference.code,
    ...(reference.featureId === undefined ? {} : { feature_id: reference.featureId }),
    ...(reference.repository === undefined ? {} : { repository: reference.repository }),
    ...(reference.taskKey === undefined ? {} : { task_key: reference.taskKey }),
    ...(reference.snapshotId === undefined ? {} : { snapshot_id: reference.snapshotId }),
    ...(reference.key === undefined ? {} : { key: reference.key }),
  };
}

type ServerStateError = NonNullable<ServerSupervisorState['persist_failure']>;

/** Maps a canonical error carried in the state read model. */
function toStateError(error: ServerStateError) {
  return {
    code: error.code,
    class: error.class,
    title: error.title,
    summary: error.summary,
    ...(error.remediation === undefined ? {} : { remediation: error.remediation }),
    ...(error.context === undefined ? {} : { context: error.context }),
    ...(error.diagnostics === undefined ? {} : { diagnostics: error.diagnostics }),
  };
}

/** Maps the wire state read model to the renderer shape. */
export function toSupervisorState(state: ServerSupervisorState): SupervisorState {
  return validateWithSchema(
    {
      conversationId: state.conversation_id,
      generation: state.generation,
      sessionId: state.session_id,
      lifecycle: state.lifecycle,
      ...(state.starting_step === undefined ? {} : { startingStep: state.starting_step }),
      lastTurnOutcome: state.last_turn_outcome,
      interruptedBy: state.interrupted_by,
      settings: {
        harness: state.settings.harness,
        model: state.settings.model,
        effort: state.settings.effort,
      },
      ...(state.pending_change === undefined
        ? {}
        : {
            pendingChange: {
              requestId: state.pending_change.request_id,
              kind: state.pending_change.kind,
              target: state.pending_change.target,
              requestedAt: state.pending_change.requested_at,
            },
          }),
      effectiveModel: state.effective_model,
      permissionMode: {
        requested: state.permission_mode.requested,
        effective: state.permission_mode.effective,
        restrictedByPolicy: state.permission_mode.restricted_by_policy,
      },
      ...(state.failure === undefined
        ? {}
        : {
            failure: {
              ...toStateError(state.failure),
              ...(state.failure.attempted_settings === undefined
                ? {}
                : { attemptedSettings: state.failure.attempted_settings }),
            },
          }),
      ...(state.persist_failure === undefined
        ? {}
        : { persistFailure: toStateError(state.persist_failure) }),
      pendingRequests: state.pending_requests.map(supervisorPendingRequest),
      contextUsage:
        state.context_usage === null
          ? null
          : {
              percent: state.context_usage.percent,
              usedTokens: state.context_usage.used_tokens,
              windowTokens: state.context_usage.window_tokens,
            },
      headSeq: state.head_seq,
      streamEpoch: state.stream_epoch,
    },
    SupervisorStateSchema,
  );
}

/** Maps one committed wire record to the renderer shape. */
export function toSupervisorRecord(record: ServerSupervisorRecord): SupervisorRecord {
  return validateWithSchema(
    {
      seq: record.seq,
      id: record.id,
      conversationId: record.conversation_id,
      generation: record.generation,
      turnId: record.turn_id,
      kind: record.kind,
      visibility: record.visibility,
      createdAt: record.created_at,
      ...(record.client_message_id === undefined
        ? {}
        : { clientMessageId: record.client_message_id }),
      ...(record.stream_message_id === undefined
        ? {}
        : { streamMessageId: record.stream_message_id }),
      messages: record.messages.map(toTranscriptMessage),
      ...(record.request === undefined
        ? {}
        : {
            request: {
              requestId: record.request.request_id,
              toolName: record.request.tool_name,
              stage: record.request.stage,
              outcome: record.request.outcome,
              ...(record.request.summary === undefined ? {} : { summary: record.request.summary }),
              ...(record.request.origin === undefined ? {} : { origin: record.request.origin }),
              ...(record.request.origin === 'child' && record.request.child_session_id !== undefined
                ? { childSessionId: record.request.child_session_id }
                : {}),
            },
          }),
      ...(record.marker === undefined
        ? {}
        : {
            marker: {
              marker: record.marker.marker,
              text: record.marker.text,
              ...(record.marker.code === undefined ? {} : { code: record.marker.code }),
              ...(record.marker.summary === undefined ? {} : { summary: record.marker.summary }),
              ...(record.marker.truncated === undefined
                ? {}
                : { truncated: record.marker.truncated }),
            },
          }),
      ...(record.attachments === undefined || record.attachments.length === 0
        ? {}
        : {
            attachments: record.attachments.map((attachment) => ({
              path: attachment.path,
              kind: attachment.kind,
              name: attachment.name,
              size: attachment.size,
            })),
          }),
      ...(record.checkpoint === undefined
        ? {}
        : {
            checkpoint: {
              coversThroughSeq: record.checkpoint.covers_through_seq,
              reason: record.checkpoint.reason,
              model: record.checkpoint.model,
              summary: record.checkpoint.summary,
              truncated: record.checkpoint.truncated,
              hasNativeBaseline: record.checkpoint.has_native_baseline,
            },
          }),
    },
    SupervisorRecordSchema,
  );
}

function toTranscriptPage(response: SupervisorTranscriptResponse): SupervisorTranscriptPage {
  return validateWithSchema(
    {
      conversationId: response.conversation_id,
      items: response.items.map(toSupervisorRecord),
      firstSeq: response.first_seq,
      lastSeq: response.last_seq,
      hasMoreBefore: response.has_more_before,
      hasMoreAfter: response.has_more_after,
      headSeq: response.head_seq,
    },
    SupervisorTranscriptPageSchema,
  );
}

export class SupervisorService {
  private readonly makeClientMessageId: () => string;

  constructor(private readonly deps: SupervisorServiceDeps) {
    this.makeClientMessageId = deps.makeClientMessageId ?? randomUUID;
  }

  async getState(): Promise<SupervisorState> {
    const body = await this.request('/api/v1/supervisor/state');
    return toSupervisorState(validateWithSchema(body, SupervisorStateResponseSchema).state);
  }

  async updateSettings(request: SupervisorSettingsRequest): Promise<SupervisorState> {
    const input = validateWithSchema(request, SupervisorSettingsRequestSchema);
    const body = await this.request('/api/v1/supervisor/settings', {
      method: 'PATCH',
      body: {
        harness: input.harness,
        ...(input.model === undefined ? {} : { model: input.model }),
        ...(input.effort === undefined ? {} : { effort: input.effort }),
        request_id: input.requestId,
        expected_generation: input.expectedGeneration,
      },
    });
    return toSupervisorState(validateWithSchema(body, SupervisorStateResponseSchema).state);
  }

  async cancelPendingChange(
    request: SupervisorPendingChangeCancelRequest,
  ): Promise<SupervisorState> {
    const input = validateWithSchema(request, SupervisorPendingChangeCancelRequestSchema);
    const body = await this.request(
      `/api/v1/supervisor/pending-change/${encodeURIComponent(input.requestId)}`,
      {
        method: 'DELETE',
      },
    );
    return toSupervisorState(validateWithSchema(body, SupervisorStateResponseSchema).state);
  }

  async dismissPersistFailure(): Promise<SupervisorState> {
    const body = await this.request('/api/v1/supervisor/persist-failure', { method: 'DELETE' });
    return toSupervisorState(validateWithSchema(body, SupervisorStateResponseSchema).state);
  }

  async getTranscript(request: SupervisorTranscriptRequest): Promise<SupervisorTranscriptPage> {
    const input = validateWithSchema(request, SupervisorTranscriptRequestSchema);
    const query = new URLSearchParams();
    if (input.before !== undefined) query.set('before', String(input.before));
    if (input.after !== undefined) query.set('after', String(input.after));
    if (input.limit !== undefined) query.set('limit', String(input.limit));
    const suffix = query.size === 0 ? '' : `?${query.toString()}`;
    const body = await this.request(`/api/v1/supervisor/transcript${suffix}`);
    return toTranscriptPage(validateWithSchema(body, SupervisorTranscriptResponseSchema));
  }

  async sendMessage(request: SupervisorMessageRequest): Promise<SupervisorMessageResult> {
    const input = validateWithSchema(request, SupervisorMessageRequestSchema);
    const remote = (this.deps.locality ?? alwaysLocal)() === 'remote';
    const images = input.images ?? [];
    const attachments = input.attachments ?? [];
    const imageUploads = input.imageUploads ?? [];
    const attachmentUploads = input.attachmentUploads ?? [];
    // Remote submit boundary: a stale local-path draft must fail, never leak
    // a path the server cannot read; staged references travel instead.
    assertNoLocalPathsRemotely(remote, images, attachments);
    const clientMessageId = this.makeClientMessageId();
    if (!CLIENT_MESSAGE_ID_PATTERN.test(clientMessageId)) {
      throw new Error('minted client message id violates the server syntax');
    }
    const body = await this.request('/api/v1/supervisor/messages', {
      method: 'POST',
      body: {
        text: input.text,
        ...(remote
          ? {
              ...(imageUploads.length === 0 ? {} : { image_uploads: imageUploads }),
              ...(attachmentUploads.length === 0 ? {} : { attachment_uploads: attachmentUploads }),
            }
          : {
              ...(images.length === 0 ? {} : { images }),
              ...(attachments.length === 0 ? {} : { attachments }),
            }),
        client_message_id: clientMessageId,
        ...(input.errorReference === undefined
          ? {}
          : { error_reference: toWireErrorReference(input.errorReference) }),
      },
    });
    const response = validateWithSchema(body, SupervisorMessageResponseSchema);
    return validateWithSchema(
      {
        record: toSupervisorRecord(response.record),
        launched: response.launched,
        deduplicated: response.deduplicated,
      },
      SupervisorMessageResultSchema,
    );
  }

  interrupt(): Promise<SupervisorActionResult> {
    return this.action('/api/v1/supervisor/interrupt');
  }

  end(): Promise<SupervisorActionResult> {
    return this.action('/api/v1/supervisor/end');
  }

  /** Opens a new conversation; settings and the old transcript stay on the server. */
  async reset(): Promise<SupervisorResetResult> {
    const body = await this.request('/api/v1/supervisor/reset', { method: 'POST', body: {} });
    const response = validateWithSchema(body, SupervisorResetResponseSchema);
    return validateWithSchema(
      {
        result: response.result,
        previousConversationId: response.previous_conversation_id,
        state: toSupervisorState(response.state),
      },
      SupervisorResetResultSchema,
    );
  }

  private async action(path: string): Promise<SupervisorActionResult> {
    const body = await this.request(path, { method: 'POST', body: {} });
    const response = validateWithSchema(body, SupervisorActionResponseSchema);
    return validateWithSchema(
      { result: response.result, state: toSupervisorState(response.state) },
      SupervisorActionResultSchema,
    );
  }

  private request(
    path: string,
    init?: { method: 'POST' | 'PATCH' | 'DELETE'; body?: Record<string, unknown> },
  ): Promise<unknown> {
    return fencedServerRequest(this.deps.transport, this.deps.identity, path, init);
  }
}
