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

import { describe, expect, it } from 'vitest';
import { SupervisorEventSchema, type SupervisorEvent } from '../../shared/ipc';
import { SseBlockAssembler, type SseStream } from '../gateway/events';
import { parseSupervisorStreamEvent, SupervisorStreamRunner } from '../supervisorStream';

// --- fixtures ----------------------------------------------------------------

const EPOCH = 'a1b2c3d4';

function wireState(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    conversation_id: 'conv-1',
    generation: 1,
    session_id: '__supervisor__.conv-1.1',
    lifecycle: 'idle',
    last_turn_outcome: 'completed',
    interrupted_by: 'none',
    settings: { harness: 'claude', model: 'claude-sonnet-4-5', effort: '' },
    effective_model: 'claude-sonnet-4-5',
    permission_mode: { requested: 'default', effective: 'default', restricted_by_policy: false },
    pending_requests: [],
    head_seq: 3,
    stream_epoch: EPOCH,
    ...overrides,
  };
}

function wireRecord(seq: number, overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    seq,
    id: `rec-${String(seq)}`,
    conversation_id: 'conv-1',
    generation: 1,
    turn_id: 'turn-1',
    kind: 'assistant',
    visibility: 'content',
    created_at: '2026-10-06T10:00:00Z',
    messages: [{ index: seq, role: 'assistant', type: 'text', text: `reply ${String(seq)}` }],
    ...overrides,
  };
}

function envelope(kind: string, extra: Record<string, unknown> = {}): Record<string, unknown> {
  return { kind, conversation_id: 'conv-1', generation: 1, stream_epoch: EPOCH, ...extra };
}

/** One SSE frame exactly as the server writes it: only records carry an id. */
function frame(event: Record<string, unknown>): string[] {
  const kind = String(event['kind']);
  const record = event['record'] as { seq?: number } | undefined;
  return [
    ...(kind === 'record' && record?.seq !== undefined ? [`id: ${String(record.seq)}`] : []),
    `event: ${kind}`,
    `data: ${JSON.stringify(event)}`,
    '',
  ];
}

function frames(events: Array<Record<string, unknown>>): string[] {
  return events.flatMap(frame);
}

const recordEvent = (seq: number, extra: Record<string, unknown> = {}) =>
  envelope('record', { seq, record: wireRecord(seq), ...extra });

// --- harness -------------------------------------------------------------------

interface ScriptedConnection {
  status?: number;
  lines: string[];
  /** Keeps the stream open (pending until closed) after the scripted lines. */
  stayOpen?: boolean;
}

function makeHarness(script: Array<ScriptedConnection | Error>) {
  const pushes: SupervisorEvent[] = [];
  const logs: string[] = [];
  const sleeps: number[] = [];
  const openCalls: Array<{ afterSeq?: number; epoch?: string }> = [];
  let openCount = 0;

  const runner = new SupervisorStreamRunner({
    source: {
      openSupervisorEventStream: (options) => {
        openCalls.push(options);
        const step = script[Math.min(openCount, script.length - 1)] ?? new Error('script empty');
        openCount += 1;
        if (step instanceof Error) return Promise.reject(step);
        let closed = false;
        let release: (() => void) | null = null;
        const lines = (async function* () {
          for (const line of step.lines) {
            if (closed) return;
            yield line;
          }
          if (step.stayOpen === true && !closed) {
            await new Promise<void>((resolve) => {
              release = resolve;
            });
          }
        })();
        const connection: SseStream = {
          status: step.status ?? 200,
          lines,
          close: () => {
            closed = true;
            release?.();
          },
        };
        return Promise.resolve(connection);
      },
    },
    sleep: (ms) => {
      sleeps.push(ms);
      return new Promise((resolve) => setTimeout(resolve, 0));
    },
    log: (line) => logs.push(line),
    onPush: (event) => pushes.push(event),
    backoff: { initialMs: 10, maxMs: 40 },
  });

  const settle = async (rounds = 1): Promise<void> => {
    for (let i = 0; i < rounds; i += 1) {
      await new Promise((resolve) => setTimeout(resolve, 0));
    }
  };
  return { runner, pushes, logs, sleeps, openCalls, settle };
}

const types = (pushes: SupervisorEvent[]) => pushes.map((push) => push.type);

// --- parsing -------------------------------------------------------------------

describe('parseSupervisorStreamEvent', () => {
  it('parses each server kind with its required body', () => {
    for (const event of [
      recordEvent(4),
      envelope('record', {
        seq: 5,
        record: wireRecord(5, {
          kind: 'marker',
          visibility: 'display_only',
          messages: [],
          marker: { marker: 'interrupted', text: 'Interrupted before restart' },
        }),
      }),
      envelope('state', {
        state: wireState({
          lifecycle: 'starting',
          starting_step: 'rebuilding',
          last_turn_outcome: 'interrupted',
          interrupted_by: 'shutdown',
        }),
      }),
      envelope('delta', {
        delta: { turn_id: 't', stream_message_id: 'm', chunk_index: 0, text: 'He' },
      }),
      envelope('state', { state: wireState() }),
      envelope('request', {
        request: { request_id: 'p1', tool_name: 'Bash', status: 'pending' },
      }),
      envelope('heartbeat'),
      envelope('stream.reset', { snapshot_required: true }),
    ]) {
      const assembler = new SseBlockAssembler();
      const block = frame(event)
        .map((line) => assembler.push(line))
        .find((value) => value !== null)!;
      expect(parseSupervisorStreamEvent(block), String(event['kind'])).not.toBeNull();
    }
  });

  it('drops malformed, polluted, oversized, bodiless, or mis-framed payloads', () => {
    const block = (data: string, extra: Partial<{ id: string; event: string }> = {}) => ({
      id: '',
      event: '',
      data,
      ...extra,
    });
    expect(parseSupervisorStreamEvent(block('{not json'))).toBeNull();
    expect(
      parseSupervisorStreamEvent(block('{"kind":"heartbeat","__proto__":{"polluted":true}}')),
    ).toBeNull();
    expect(
      parseSupervisorStreamEvent(
        block(JSON.stringify(envelope('heartbeat', { pad: 'x'.repeat(1024 * 1024) }))),
      ),
    ).toBeNull();
    expect(parseSupervisorStreamEvent(block(JSON.stringify(envelope('record'))))).toBeNull();
    expect(parseSupervisorStreamEvent(block(JSON.stringify(envelope('state'))))).toBeNull();
    expect(parseSupervisorStreamEvent(block(JSON.stringify(envelope('exploded'))))).toBeNull();
    expect(
      parseSupervisorStreamEvent(block(JSON.stringify(recordEvent(4)), { id: '5' })),
    ).toBeNull();
    expect(
      parseSupervisorStreamEvent(block(JSON.stringify(recordEvent(4)), { event: 'delta' })),
    ).toBeNull();
    expect(
      parseSupervisorStreamEvent(
        block(JSON.stringify({ ...envelope('heartbeat'), generation: -1 })),
      ),
    ).toBeNull();
    // A request with an origin outside root/child, or an unbounded child id.
    const request = { request_id: 'p1', tool_name: 'Bash', status: 'pending' };
    expect(
      parseSupervisorStreamEvent(
        block(JSON.stringify(envelope('request', { request: { ...request, origin: 'parent' } }))),
      ),
    ).toBeNull();
    expect(
      parseSupervisorStreamEvent(
        block(
          JSON.stringify(
            envelope('request', {
              request: { ...request, origin: 'child', child_session_id: 'x'.repeat(201) },
            }),
          ),
        ),
      ),
    ).toBeNull();
  });
});

// --- runner --------------------------------------------------------------------

describe('SupervisorStreamRunner', () => {
  it('pushes live status, validated events, then stale when the stream ends', async () => {
    const harness = makeHarness([
      {
        lines: frames([
          envelope('state', { seq: 3, state: wireState() }),
          recordEvent(4),
          envelope('delta', {
            delta: { turn_id: 'turn-2', stream_message_id: 'msg-1', chunk_index: 0, text: 'Hel' },
          }),
          envelope('request', {
            request: {
              request_id: 'perm-1',
              session_id: '__supervisor__.conv-1.1',
              feature_id: '__supervisor__',
              tool_name: 'Bash',
              status: 'pending',
              waiting_since: '2026-10-06T10:00:00Z',
            },
          }),
          envelope('heartbeat'),
        ]),
      },
      { lines: [], stayOpen: true },
    ]);
    harness.runner.start();
    await harness.settle(3);
    harness.runner.stop();

    expect(types(harness.pushes)).toEqual([
      'stream-status',
      'state',
      'record',
      'delta',
      'request',
      'stream-status',
      'stream-status',
    ]);
    expect(harness.pushes[0]).toEqual({ type: 'stream-status', status: 'live' });
    expect(harness.pushes[5]).toEqual({ type: 'stream-status', status: 'stale' });
    expect(harness.pushes[2]).toMatchObject({
      type: 'record',
      conversationId: 'conv-1',
      generation: 1,
      streamEpoch: EPOCH,
      record: { seq: 4, turnId: 'turn-1', messages: [{ index: 4, text: 'reply 4' }] },
    });
    expect(harness.pushes[4]).toMatchObject({
      type: 'request',
      request: {
        kind: 'permission',
        id: 'perm-1',
        target: 'supervisor',
        sessionId: '__supervisor__.conv-1.1',
        toolName: 'Bash',
      },
    });
    // The supervisor request never carries the reserved feature id.
    expect(harness.pushes[4]).not.toHaveProperty('request.featureId');
    for (const push of harness.pushes) {
      expect(SupervisorEventSchema.safeParse(push).success).toBe(true);
    }
  });

  it('pushes a sub-agent request with its origin and child session id', async () => {
    const harness = makeHarness([
      {
        lines: frames([
          envelope('request', {
            request: {
              request_id: 'perm-child',
              session_id: '__supervisor__.conv-1.1',
              feature_id: '__supervisor__',
              tool_name: 'Bash',
              status: 'pending',
              waiting_since: '2026-10-06T10:00:00Z',
              origin: 'child',
              child_session_id: 'agent_sub_1',
            },
          }),
          envelope('request', {
            request: {
              request_id: 'perm-root',
              session_id: '__supervisor__.conv-1.1',
              tool_name: 'Bash',
              status: 'pending',
              origin: 'root',
            },
          }),
        ]),
      },
      { lines: [], stayOpen: true },
    ]);
    harness.runner.start();
    await harness.settle(3);
    harness.runner.stop();

    const requests = harness.pushes.filter((push) => push.type === 'request');
    expect(requests).toHaveLength(2);
    expect(requests[0]).toMatchObject({
      request: {
        kind: 'permission',
        id: 'perm-child',
        target: 'supervisor',
        origin: 'child',
        childSessionId: 'agent_sub_1',
      },
    });
    expect(requests[1]).toMatchObject({ request: { id: 'perm-root', origin: 'root' } });
    expect(requests[1]).not.toHaveProperty('request.childSessionId');
    for (const push of harness.pushes) {
      expect(SupervisorEventSchema.safeParse(push).success).toBe(true);
    }
  });

  it('resumes from the last record seq and epoch on reconnect, dropping replayed duplicates', async () => {
    const harness = makeHarness([
      { lines: frames([envelope('state', { state: wireState() }), recordEvent(4)]) },
      new Error('refused'),
      {
        // The server replays committed records after the cursor; a duplicate
        // at or below it must not reach the renderer twice.
        lines: frames([recordEvent(4), recordEvent(5)]),
        stayOpen: true,
      },
    ]);
    harness.runner.start();
    await harness.settle(6);
    harness.runner.stop();

    expect(harness.openCalls[0]).toEqual({});
    expect(harness.openCalls[1]).toEqual({ afterSeq: 4, epoch: EPOCH });
    expect(harness.openCalls[2]).toEqual({ afterSeq: 4, epoch: EPOCH });
    const recordSeqs = harness.pushes.flatMap((push) =>
      push.type === 'record' ? [push.record.seq] : [],
    );
    expect(recordSeqs).toEqual([4, 5]);
    expect(harness.runner.getCursor()).toEqual({ seq: 5, epoch: EPOCH });
    // The global stream's capped backoff applies.
    expect(harness.sleeps.slice(0, 2)).toEqual([10, 20]);
  });

  it('non-record events never advance the resume cursor', async () => {
    const harness = makeHarness([
      {
        lines: frames([envelope('state', { seq: 30, state: wireState({ head_seq: 30 }) })]),
      },
      { lines: [], stayOpen: true },
    ]);
    harness.runner.start();
    await harness.settle(3);
    harness.runner.stop();

    expect(harness.openCalls[1]).toEqual({});
    expect(harness.runner.getCursor()).toEqual({ seq: 0, epoch: EPOCH });
  });

  it('a stream.reset clears the cursor and tells the renderer to re-snapshot', async () => {
    const harness = makeHarness([
      { lines: frames([recordEvent(8)]) },
      {
        lines: frames([
          envelope('stream.reset', { stream_epoch: 'new-epoch', snapshot_required: true }),
        ]),
      },
      { lines: [], stayOpen: true },
    ]);
    harness.runner.start();
    await harness.settle(8);
    harness.runner.stop();

    expect(harness.openCalls[1]).toEqual({ afterSeq: 8, epoch: EPOCH });
    // After the reset the next subscription resumes with no cursor at all.
    expect(harness.openCalls[2]).toEqual({});
    expect(harness.pushes).toContainEqual({ type: 'reset' });
    expect(harness.runner.getCursor().seq).toBe(0);
  });

  it('an observed epoch change drops the old cursor before adopting the new epoch', async () => {
    const harness = makeHarness([
      {
        lines: frames([recordEvent(8), recordEvent(2, { stream_epoch: 'epoch-b' })]),
        stayOpen: true,
      },
    ]);
    harness.runner.start();
    await harness.settle(3);
    harness.runner.stop();

    expect(types(harness.pushes)).toEqual(['stream-status', 'record', 'reset', 'record']);
    expect(harness.runner.getCursor()).toEqual({ seq: 2, epoch: 'epoch-b' });
  });

  it('resetCursor (server identity change) restarts from no cursor and pushes a reset', async () => {
    const harness = makeHarness([
      { lines: frames([recordEvent(12)]), stayOpen: true },
      { lines: [], stayOpen: true },
    ]);
    harness.runner.start();
    await harness.settle(2);
    harness.runner.stop();
    harness.runner.resetCursor();
    harness.runner.start();
    await harness.settle(2);
    harness.runner.stop();

    expect(harness.openCalls).toEqual([{}, {}]);
    expect(harness.pushes).toContainEqual({ type: 'reset' });
  });

  it('restarting after stop on the same server resumes from the tracked cursor', async () => {
    const harness = makeHarness([
      { lines: frames([recordEvent(12)]), stayOpen: true },
      { lines: [], stayOpen: true },
    ]);
    harness.runner.start();
    await harness.settle(2);
    harness.runner.stop();
    harness.runner.start();
    await harness.settle(2);
    harness.runner.stop();

    expect(harness.openCalls[1]).toEqual({ afterSeq: 12, epoch: EPOCH });
  });

  it('treats a non-200 answer as a failed attempt with stale status and capped backoff', async () => {
    const harness = makeHarness([
      { status: 401, lines: [] },
      new Error('refused'),
      new Error('refused'),
      new Error('refused'),
      { lines: [], stayOpen: true },
    ]);
    harness.runner.start();
    await harness.settle(8);
    harness.runner.stop();

    expect(harness.pushes.slice(0, 2)).toEqual([
      { type: 'stream-status', status: 'stale' },
      { type: 'stream-status', status: 'stale' },
    ]);
    expect(harness.sleeps.slice(0, 4)).toEqual([10, 20, 40, 40]);
    expect(harness.pushes.at(-1)).toEqual({ type: 'stream-status', status: 'live' });
  });

  it('drops malformed events without breaking the stream or advancing the cursor', async () => {
    const harness = makeHarness([
      {
        lines: [
          'event: record',
          'data: {not json',
          '',
          ...frame(envelope('record', { record: wireRecord(3, { kind: 'exploded' }) })),
          ...frame(envelope('state', { state: wireState({ lifecycle: 'dancing' }) })),
          ...frame(
            envelope('delta', {
              delta: { turn_id: 't', stream_message_id: 'm', chunk_index: -1, text: 'x' },
            }),
          ),
          ...frame({ ...recordEvent(6), stream_epoch: 'bad epoch with spaces' }),
          'event: heartbeat',
          'data: {"kind":"heartbeat","__proto__":{"polluted":true}}',
          '',
          ...frame(recordEvent(7)),
        ],
        stayOpen: true,
      },
    ]);
    harness.runner.start();
    await harness.settle(3);
    harness.runner.stop();

    expect(types(harness.pushes)).toEqual(['stream-status', 'record']);
    expect(harness.pushes[1]).toMatchObject({ type: 'record', record: { seq: 7 } });
    expect(harness.logs.length).toBeGreaterThanOrEqual(5);
    expect(harness.runner.getCursor()).toEqual({ seq: 7, epoch: EPOCH });
  });

  it('stop closes the connection and suppresses further pushes; start is idempotent', async () => {
    const harness = makeHarness([{ lines: [], stayOpen: true }]);
    harness.runner.start();
    harness.runner.start();
    await harness.settle();
    expect(harness.openCalls).toHaveLength(1);
    harness.runner.stop();
    await harness.settle();
    const count = harness.pushes.length;
    await harness.settle(2);
    expect(harness.pushes).toHaveLength(count);
  });

  it('never leaks token-shaped material into pushes or logs', async () => {
    const harness = makeHarness([
      {
        lines: frames([
          envelope('heartbeat', { token: 'Bearer tok-secret-xyz' }),
          envelope('state', { state: wireState(), authorization: 'Bearer tok-secret-xyz' }),
        ]),
      },
      new Error('refused: Bearer tok-secret-xyz'),
      { lines: [], stayOpen: true },
    ]);
    harness.runner.start();
    await harness.settle(4);
    harness.runner.stop();

    const emitted = JSON.stringify(harness.pushes) + JSON.stringify(harness.logs);
    expect(emitted).not.toContain('tok-secret-xyz');
  });
});
