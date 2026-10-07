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

import { describe, expect, it, vi } from 'vitest';
import { CanonicalErrorException } from '../../shared/errors';
import type { ApiRequestInit, HttpResult } from '../gateway/runtimeGateway';
import type { ServerTransport } from '../serverClient';
import { SupervisorService } from '../supervisor';

function wireState(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    conversation_id: 'conv-1',
    generation: 2,
    session_id: '__supervisor__.conv-1.2',
    lifecycle: 'waiting_permission',
    last_turn_outcome: 'none',
    interrupted_by: 'none',
    settings: { harness: 'claude', model: 'claude-sonnet-4-5', effort: 'high' },
    effective_model: 'claude-sonnet-4-5',
    permission_mode: { requested: 'default', effective: 'default', restricted_by_policy: false },
    pending_requests: [
      {
        request_id: 'perm-1',
        session_id: '__supervisor__.conv-1.2',
        feature_id: '__supervisor__',
        tool_name: 'Bash',
        status: 'pending',
        waiting_since: '2026-10-06T10:00:00Z',
        input: { command: 'ls' },
      },
      {
        request_id: 'ask-1',
        session_id: '__supervisor__.conv-1.2',
        tool_name: 'AskUserQuestion',
        status: 'pending',
        waiting_since: '2026-10-06T10:01:00Z',
        questions: [{ question: 'Which feature?', options: [{ label: 'A' }] }],
      },
    ],
    head_seq: 9,
    stream_epoch: 'a1b2c3',
    ...overrides,
  };
}

function wireRecord(seq: number): Record<string, unknown> {
  return {
    seq,
    id: `rec-${String(seq)}`,
    conversation_id: 'conv-1',
    generation: 2,
    turn_id: 'turn-1',
    kind: 'user',
    visibility: 'content',
    created_at: '2026-10-06T10:00:00Z',
    client_message_id: 'minted-1',
    messages: [{ index: seq, role: 'user', type: 'text', text: 'hello', block_index: 0 }],
  };
}

function transport(
  respond: (path: string, init?: ApiRequestInit) => HttpResult,
): ServerTransport & { apiRequest: ReturnType<typeof vi.fn> } {
  return {
    apiRequest: vi.fn((path: string, init?: ApiRequestInit) =>
      Promise.resolve(respond(path, init)),
    ),
  };
}

describe('SupervisorService', () => {
  it('maps the state read model, routing pending requests to supervisor attention items', async () => {
    const api = transport(() => ({ status: 200, body: { api_version: 'v1', state: wireState() } }));
    const service = new SupervisorService({ transport: api });

    const state = await service.getState();

    expect(api.apiRequest).toHaveBeenCalledWith('/api/v1/supervisor/state', undefined);
    expect(state).toMatchObject({
      conversationId: 'conv-1',
      generation: 2,
      sessionId: '__supervisor__.conv-1.2',
      lifecycle: 'waiting_permission',
      lastTurnOutcome: 'none',
      interruptedBy: 'none',
      settings: { harness: 'claude', model: 'claude-sonnet-4-5', effort: 'high' },
      effectiveModel: 'claude-sonnet-4-5',
      permissionMode: { requested: 'default', effective: 'default', restrictedByPolicy: false },
      headSeq: 9,
      streamEpoch: 'a1b2c3',
    });
    expect(state.pendingRequests).toEqual([
      {
        kind: 'permission',
        id: 'perm-1',
        target: 'supervisor',
        sessionId: '__supervisor__.conv-1.2',
        toolName: 'Bash',
        input: { command: 'ls' },
        waitingSince: '2026-10-06T10:00:00Z',
      },
      {
        kind: 'questions',
        id: 'ask-1',
        target: 'supervisor',
        sessionId: '__supervisor__.conv-1.2',
        waitingSince: '2026-10-06T10:01:00Z',
        questions: [
          {
            key: 'Which feature?',
            header: 'Which feature?',
            multiSelect: false,
            options: [{ label: 'A' }],
          },
        ],
      },
    ]);
  });

  it('patches settings with only the declared fields', async () => {
    const api = transport(() => ({ status: 200, body: { api_version: 'v1', state: wireState() } }));
    const service = new SupervisorService({ transport: api });

    await service.updateSettings({ harness: 'codex', model: 'gpt-5' });

    expect(api.apiRequest).toHaveBeenCalledWith('/api/v1/supervisor/settings', {
      method: 'PATCH',
      body: { harness: 'codex', model: 'gpt-5' },
    });
  });

  it('passes the server canonical settings rejection through unchanged', async () => {
    const api = transport(() => ({
      status: 409,
      body: {
        api_version: 'v1',
        error: {
          code: 'supervisor_settings_locked',
          class: 'needs_action',
          title: 'Supervisor settings locked',
          summary: 'Settings cannot change while the supervisor is running.',
        },
      },
    }));
    const service = new SupervisorService({ transport: api });

    const err = await service
      .updateSettings({ harness: 'claude', model: 'm' })
      .catch((e: unknown) => e);
    expect(err).toBeInstanceOf(CanonicalErrorException);
    expect((err as CanonicalErrorException).canonical.code).toBe('supervisor_settings_locked');
  });

  it('builds the transcript query from the validated cursor and maps the page', async () => {
    const api = transport(() => ({
      status: 200,
      body: {
        api_version: 'v1',
        conversation_id: 'conv-1',
        items: [wireRecord(7), wireRecord(8)],
        first_seq: 7,
        last_seq: 8,
        has_more_before: true,
        has_more_after: false,
        head_seq: 8,
      },
    }));
    const service = new SupervisorService({ transport: api });

    const page = await service.getTranscript({ before: 9, limit: 2 });
    await service.getTranscript({});

    expect(api.apiRequest.mock.calls.map(([path]) => path)).toEqual([
      '/api/v1/supervisor/transcript?before=9&limit=2',
      '/api/v1/supervisor/transcript',
    ]);
    expect(page).toMatchObject({
      conversationId: 'conv-1',
      firstSeq: 7,
      lastSeq: 8,
      hasMoreBefore: true,
      hasMoreAfter: false,
      headSeq: 8,
    });
    expect(page.items[0]).toEqual({
      seq: 7,
      id: 'rec-7',
      conversationId: 'conv-1',
      generation: 2,
      turnId: 'turn-1',
      kind: 'user',
      visibility: 'content',
      createdAt: '2026-10-06T10:00:00Z',
      clientMessageId: 'minted-1',
      messages: [{ index: 7, blockIndex: 0, role: 'user', type: 'text', text: 'hello' }],
    });
    await expect(service.getTranscript({ before: 3, after: 1 })).rejects.toThrow();
    expect(api.apiRequest).toHaveBeenCalledTimes(2);
  });

  it('mints the client message id in main and sends only text plus that id', async () => {
    const api = transport(() => ({
      status: 200,
      body: { api_version: 'v1', record: wireRecord(10), launched: true },
    }));
    const service = new SupervisorService({
      transport: api,
      makeClientMessageId: () => 'minted-1',
    });

    const result = await service.sendMessage({ text: 'hello' });

    expect(api.apiRequest).toHaveBeenCalledWith('/api/v1/supervisor/messages', {
      method: 'POST',
      body: { text: 'hello', client_message_id: 'minted-1' },
    });
    expect(result.launched).toBe(true);
    expect(result.record.seq).toBe(10);
  });

  it('sends an attached error reference in snake_case with only the fields it carries', async () => {
    const api = transport(() => ({
      status: 200,
      body: { api_version: 'v1', record: wireRecord(10), launched: false },
    }));
    const service = new SupervisorService({
      transport: api,
      makeClientMessageId: () => 'minted-1',
    });

    await service.sendMessage({
      text: 'explain',
      errorReference: { scope: 'setup', code: 'setup_failed', featureId: 'f-1', taskKey: 'repo' },
    });
    await service.sendMessage({
      text: 'explain',
      errorReference: { scope: 'recovery', code: 'orphan', snapshotId: 's-1', key: 'k-1' },
    });

    expect(api.apiRequest.mock.calls.map(([, init]) => (init as ApiRequestInit).body)).toEqual([
      {
        text: 'explain',
        client_message_id: 'minted-1',
        error_reference: {
          scope: 'setup',
          code: 'setup_failed',
          feature_id: 'f-1',
          task_key: 'repo',
        },
      },
      {
        text: 'explain',
        client_message_id: 'minted-1',
        error_reference: { scope: 'recovery', code: 'orphan', snapshot_id: 's-1', key: 'k-1' },
      },
    ]);
  });

  it('refuses a malformed error reference without sending', async () => {
    const api = transport(() => ({ status: 200, body: {} }));
    const service = new SupervisorService({ transport: api });
    await expect(
      service.sendMessage({
        text: 'hi',
        errorReference: { scope: 'run', code: 'x' } as never,
      }),
    ).rejects.toThrow();
    expect(api.apiRequest).not.toHaveBeenCalled();
  });

  it('defaults to a UUID idempotency key that satisfies the server syntax', async () => {
    const api = transport(() => ({
      status: 200,
      body: { api_version: 'v1', record: wireRecord(10), launched: false },
    }));
    await new SupervisorService({ transport: api }).sendMessage({ text: 'hi' });
    const body = (api.apiRequest.mock.calls[0]?.[1] as { body: { client_message_id: string } })
      .body;
    expect(body.client_message_id).toMatch(/^[A-Za-z0-9._-]{1,128}$/);
  });

  it('refuses a minted id that violates the server syntax without sending', async () => {
    const api = transport(() => ({ status: 200, body: {} }));
    const service = new SupervisorService({ transport: api, makeClientMessageId: () => 'a b' });
    await expect(service.sendMessage({ text: 'hi' })).rejects.toThrow();
    expect(api.apiRequest).not.toHaveBeenCalled();
  });

  it('interrupts and ends with an empty JSON body and maps the action result', async () => {
    const api = transport((path) => ({
      status: 200,
      body: {
        api_version: 'v1',
        result: path.endsWith('/interrupt') ? 'accepted' : 'ended',
        state: wireState({ lifecycle: 'stopped', pending_requests: [] }),
      },
    }));
    const service = new SupervisorService({ transport: api });

    await expect(service.interrupt()).resolves.toMatchObject({
      result: 'accepted',
      state: { lifecycle: 'stopped' },
    });
    await expect(service.end()).resolves.toMatchObject({ result: 'ended' });
    expect(api.apiRequest.mock.calls).toEqual([
      ['/api/v1/supervisor/interrupt', { method: 'POST', body: {} }],
      ['/api/v1/supervisor/end', { method: 'POST', body: {} }],
    ]);
  });

  it('discards a reply that crossed a server switch', async () => {
    let identity = { serverKey: 'server-a' as string | null, generation: 1 };
    const api = transport(() => {
      identity = { serverKey: 'server-b', generation: 2 };
      return { status: 200, body: { api_version: 'v1', state: wireState() } };
    });
    const service = new SupervisorService({ transport: api, identity: () => identity });

    const err = await service.getState().catch((e: unknown) => e);
    expect((err as CanonicalErrorException).canonical.code).toBe('E_SERVER_SWITCHED');
  });

  it('maps the restart, rebuild and launch-failure fields of the state read model', async () => {
    const failure = {
      code: 'supervisor_launch_failed',
      class: 'blocking',
      title: 'Supervisor failed to start',
      summary: 'The harness exited before the handshake.',
      diagnostics: 'exit status 3',
    };
    const api = transport(() => ({
      status: 200,
      body: {
        api_version: 'v1',
        state: wireState({
          lifecycle: 'failed',
          pending_requests: [],
          last_turn_outcome: 'interrupted',
          interrupted_by: 'shutdown',
          permission_mode: { requested: 'default', effective: 'plan', restricted_by_policy: true },
          failure,
        }),
      },
    }));
    const state = await new SupervisorService({ transport: api }).getState();
    expect(state).toMatchObject({
      lifecycle: 'failed',
      lastTurnOutcome: 'interrupted',
      interruptedBy: 'shutdown',
      permissionMode: { requested: 'default', effective: 'plan', restrictedByPolicy: true },
      failure,
    });

    const rebuilding = transport(() => ({
      status: 200,
      body: {
        api_version: 'v1',
        state: wireState({ lifecycle: 'starting', starting_step: 'rebuilding' }),
      },
    }));
    await expect(
      new SupervisorService({ transport: rebuilding }).getState(),
    ).resolves.toMatchObject({ lifecycle: 'starting', startingStep: 'rebuilding' });
  });

  it('maps marker records and the interrupted request outcome', async () => {
    const api = transport(() => ({
      status: 200,
      body: {
        api_version: 'v1',
        conversation_id: 'conv-1',
        items: [
          {
            ...wireRecord(7),
            kind: 'marker',
            visibility: 'display_only',
            client_message_id: undefined,
            messages: [],
            marker: {
              marker: 'error',
              text: 'The harness exited before the handshake.',
              code: 'supervisor_launch_failed',
            },
          },
          {
            ...wireRecord(8),
            kind: 'permission',
            visibility: 'display_only',
            client_message_id: undefined,
            messages: [],
            request: {
              request_id: 'perm-1',
              tool_name: 'Bash',
              stage: 'resolved',
              outcome: 'interrupted',
            },
          },
        ],
        first_seq: 7,
        last_seq: 8,
        has_more_before: false,
        has_more_after: false,
        head_seq: 8,
      },
    }));
    const page = await new SupervisorService({ transport: api }).getTranscript({});
    expect(page.items[0]).toMatchObject({
      kind: 'marker',
      visibility: 'display_only',
      messages: [],
      marker: {
        marker: 'error',
        text: 'The harness exited before the handshake.',
        code: 'supervisor_launch_failed',
      },
    });
    expect(page.items[0]).not.toHaveProperty('request');
    expect(page.items[1]?.request).toMatchObject({ outcome: 'interrupted', toolName: 'Bash' });
  });

  it('fails closed on malformed server payloads', async () => {
    const api = transport(() => ({
      status: 200,
      body: { api_version: 'v1', state: wireState({ lifecycle: 'dancing' }) },
    }));
    await expect(new SupervisorService({ transport: api }).getState()).rejects.toBeInstanceOf(
      CanonicalErrorException,
    );
  });
});
