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
  SupervisorSettingsRequestSchema,
  SupervisorStateSchema,
  SupervisorTranscriptPageSchema,
  SupervisorTranscriptRequestSchema,
  type ErrorReference,
  type SupervisorActionResult,
  type SupervisorMessageRequest,
  type SupervisorMessageResult,
  type SupervisorRecord,
  type SupervisorSettingsRequest,
  type SupervisorState,
  type SupervisorTranscriptPage,
  type SupervisorTranscriptRequest,
} from '../shared/ipc';
import { supervisorPendingRequest } from './attention';
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
      settings: {
        harness: state.settings.harness,
        model: state.settings.model,
        effort: state.settings.effort,
      },
      effectiveModel: state.effective_model,
      pendingRequests: state.pending_requests.map(supervisorPendingRequest),
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
        model: input.model,
        ...(input.effort === undefined ? {} : { effort: input.effort }),
      },
    });
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
    const clientMessageId = this.makeClientMessageId();
    if (!CLIENT_MESSAGE_ID_PATTERN.test(clientMessageId)) {
      throw new Error('minted client message id violates the server syntax');
    }
    const body = await this.request('/api/v1/supervisor/messages', {
      method: 'POST',
      body: {
        text: input.text,
        client_message_id: clientMessageId,
        ...(input.errorReference === undefined
          ? {}
          : { error_reference: toWireErrorReference(input.errorReference) }),
      },
    });
    const response = validateWithSchema(body, SupervisorMessageResponseSchema);
    return validateWithSchema(
      { record: toSupervisorRecord(response.record), launched: response.launched },
      SupervisorMessageResultSchema,
    );
  }

  interrupt(): Promise<SupervisorActionResult> {
    return this.action('/api/v1/supervisor/interrupt');
  }

  end(): Promise<SupervisorActionResult> {
    return this.action('/api/v1/supervisor/end');
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
    init?: { method: 'POST' | 'PATCH'; body: Record<string, unknown> },
  ): Promise<unknown> {
    return fencedServerRequest(this.deps.transport, this.deps.identity, path, init);
  }
}
