import { describe, expect, it } from 'vitest';
import { AppEventSchema, type AppEvent } from '../../shared/ipc';
import {
  EventCursorTracker,
  EventStreamSupervisor,
  SseBlockAssembler,
  parseSseEvent,
  type SseStream,
} from '../gateway/events';

// --- helpers ---------------------------------------------------------------

function envelope(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    api_version: 'v1',
    id: '7',
    seq: 7,
    epoch: 'epoch-a',
    kind: 'feature.updated',
    at: '2026-07-14T10:00:00Z',
    resource: { type: 'feature', id: 'feat1234', feature_id: 'feat1234' },
    snapshot_required: true,
    ...overrides,
  };
}

function parsed(overrides: Record<string, unknown> = {}) {
  const value = parseSseEvent(JSON.stringify(envelope(overrides)), { id: '', event: '' });
  if (value === null) {
    throw new Error('expected a parseable event');
  }
  return value;
}

// --- SSE block assembly ------------------------------------------------------

describe('SseBlockAssembler', () => {
  it('accumulates id/event/data lines and emits a block on the blank separator', () => {
    const assembler = new SseBlockAssembler();
    expect(assembler.push('id: 12')).toBeNull();
    expect(assembler.push('event: feature.updated')).toBeNull();
    expect(assembler.push('data: {"seq":12}')).toBeNull();
    const block = assembler.push('');
    expect(block).toEqual({ id: '12', event: 'feature.updated', data: '{"seq":12}' });
  });

  it('joins multi-line data with newlines and resets between blocks', () => {
    const assembler = new SseBlockAssembler();
    assembler.push('data: {"a":');
    assembler.push('data: 1}');
    expect(assembler.push('')).toEqual({ id: '', event: '', data: '{"a":\n1}' });
    assembler.push('data: {"b":2}');
    expect(assembler.push('')).toEqual({ id: '', event: '', data: '{"b":2}' });
  });

  it('ignores comment/blank blocks and unknown fields', () => {
    const assembler = new SseBlockAssembler();
    expect(assembler.push(': keep-alive comment')).toBeNull();
    expect(assembler.push('')).toBeNull(); // no data accumulated -> no block
    assembler.push('retry: 2000');
    expect(assembler.push('')).toBeNull();
  });
});

// --- envelope parsing --------------------------------------------------------

describe('parseSseEvent', () => {
  it('parses a well-formed envelope', () => {
    const evt = parsed();
    expect(evt.seq).toBe(7);
    expect(evt.epoch).toBe('epoch-a');
    expect(evt.kind).toBe('feature.updated');
    expect(evt.resource?.feature_id).toBe('feat1234');
    expect(evt.snapshot_required).toBe(true);
  });

  it('falls back to the block event/id when the payload omits kind/seq', () => {
    const evt = parseSseEvent(
      JSON.stringify({ api_version: 'v1', at: 'x', resource: { type: 'runtime' } }),
      { id: '42', event: 'heartbeat' },
    );
    expect(evt).not.toBeNull();
    expect(evt?.kind).toBe('heartbeat');
    expect(evt?.seq).toBe(42);
  });

  it('drops malformed JSON, oversized payloads, and polluted payloads', () => {
    expect(parseSseEvent('{not json', { id: '', event: '' })).toBeNull();
    expect(
      parseSseEvent(`{"kind":"x","summary":"${'a'.repeat(70 * 1024)}"}`, { id: '', event: '' }),
    ).toBeNull();
    expect(
      parseSseEvent('{"kind":"x","__proto__":{"polluted":true}}', { id: '', event: '' }),
    ).toBeNull();
  });

  it('drops events with no kind at all', () => {
    expect(parseSseEvent('{"seq":3}', { id: '3', event: '' })).toBeNull();
  });
});

// --- cursor tracking / convergence -------------------------------------------

describe('EventCursorTracker', () => {
  it('invalidates and advances the cursor on a fresh snapshot-required event', () => {
    const tracker = new EventCursorTracker();
    const decision = tracker.ingest(parsed());
    expect(decision).toEqual({
      action: 'invalidate',
      kind: 'feature.updated',
      resourceType: 'feature',
      resourceId: 'feat1234',
      featureId: 'feat1234',
    });
    expect(tracker.getCursor()).toEqual({ seq: 7, epoch: 'epoch-a' });
  });

  it('ignores duplicate events by sequence', () => {
    const tracker = new EventCursorTracker();
    tracker.ingest(parsed({ seq: 7 }));
    expect(tracker.ingest(parsed({ seq: 7 }))).toEqual({ action: 'ignore' });
    expect(tracker.getCursor().seq).toBe(7);
  });

  it('ignores out-of-order older events without regressing the cursor', () => {
    const tracker = new EventCursorTracker();
    tracker.ingest(parsed({ seq: 9 }));
    expect(tracker.ingest(parsed({ seq: 8 }))).toEqual({ action: 'ignore' });
    expect(tracker.getCursor().seq).toBe(9);
  });

  it('accepts forward sequence gaps (the broker coalesces per resource)', () => {
    const tracker = new EventCursorTracker();
    tracker.ingest(parsed({ seq: 2 }));
    const decision = tracker.ingest(parsed({ seq: 40 }));
    expect(decision.action).toBe('invalidate');
    expect(tracker.getCursor().seq).toBe(40);
  });

  it('requests a resync when the epoch changes and adopts the new cursor', () => {
    const tracker = new EventCursorTracker();
    tracker.ingest(parsed({ seq: 7, epoch: 'epoch-a' }));
    const decision = tracker.ingest(parsed({ seq: 3, epoch: 'epoch-b' }));
    expect(decision).toEqual({ action: 'resync' });
    expect(tracker.getCursor()).toEqual({ seq: 3, epoch: 'epoch-b' });
  });

  it('requests a resync on connected and stream.reset events', () => {
    const tracker = new EventCursorTracker();
    expect(tracker.ingest(parsed({ kind: 'connected', seq: 5 }))).toEqual({ action: 'resync' });
    expect(tracker.getCursor().seq).toBe(5);
    expect(tracker.ingest(parsed({ kind: 'stream.reset', seq: 6 }))).toEqual({ action: 'resync' });
    expect(tracker.getCursor().seq).toBe(6);
  });

  it('advances the cursor on heartbeats without invalidating', () => {
    const tracker = new EventCursorTracker();
    const decision = tracker.ingest(
      parsed({ kind: 'heartbeat', seq: 11, snapshot_required: false }),
    );
    expect(decision).toEqual({ action: 'ignore' });
    expect(tracker.getCursor().seq).toBe(11);
  });

  it('treats a snapshot-required heartbeat as a forced resync', () => {
    const tracker = new EventCursorTracker();
    expect(tracker.ingest(parsed({ kind: 'heartbeat', seq: 4, snapshot_required: true }))).toEqual({
      action: 'resync',
    });
  });

  it('ignores activity events that require no snapshot', () => {
    const tracker = new EventCursorTracker();
    const decision = tracker.ingest(
      parsed({ kind: 'session.output.activity', snapshot_required: false }),
    );
    expect(decision).toEqual({ action: 'ignore' });
  });

  it('converges after a reconnect replay of already-seen events', () => {
    const tracker = new EventCursorTracker();
    tracker.ingest(parsed({ seq: 5 }));
    tracker.ingest(parsed({ seq: 6 }));
    // Reconnect replays 5..7 from the ring buffer: only 7 invalidates.
    expect(tracker.ingest(parsed({ seq: 5 })).action).toBe('ignore');
    expect(tracker.ingest(parsed({ seq: 6 })).action).toBe('ignore');
    expect(tracker.ingest(parsed({ seq: 7 })).action).toBe('invalidate');
    expect(tracker.getCursor().seq).toBe(7);
  });
});

// --- supervisor ---------------------------------------------------------------

interface ScriptedConnection {
  status?: number;
  lines: string[];
  /** Keeps the stream open (pending forever) after the scripted lines. */
  stayOpen?: boolean;
}

function sseLines(events: Array<Record<string, unknown>>): string[] {
  return events.flatMap((evt) => [`data: ${JSON.stringify(evt)}`, '']);
}

function makeSupervisorHarness(script: Array<ScriptedConnection | Error>) {
  const pushes: AppEvent[] = [];
  const logs: string[] = [];
  const sleeps: number[] = [];
  const openCalls: Array<{ afterSeq?: number; epoch?: string }> = [];
  let openCount = 0;
  let release: (() => void) | null = null;

  const supervisor = new EventStreamSupervisor({
    source: {
      openEventStream: (options) => {
        openCalls.push(options);
        const step = script[Math.min(openCount, script.length - 1)] ?? new Error('script empty');
        openCount += 1;
        if (step instanceof Error) {
          return Promise.reject(step);
        }
        let closed = false;
        const lines = (async function* () {
          for (const line of step.lines) {
            if (closed) {
              return;
            }
            yield line;
          }
          if (step.stayOpen === true) {
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
      // Yield to the macrotask queue so tests can interleave with the loop.
      return new Promise((resolve) => setTimeout(resolve, 0));
    },
    log: (line) => logs.push(line),
    onPush: (event) => pushes.push(event),
    backoff: { initialMs: 10, maxMs: 40 },
  });

  return {
    supervisor,
    pushes,
    logs,
    sleeps,
    openCalls,
    settle: () => new Promise((resolve) => setTimeout(resolve, 0)),
  };
}

describe('EventStreamSupervisor', () => {
  it('emits live status, invalidations, then stale on stream end', async () => {
    const harness = makeSupervisorHarness([
      {
        lines: sseLines([
          envelope({ kind: 'connected', seq: 3, resource: { type: 'runtime' } }),
          envelope({ seq: 4 }),
        ]),
      },
      new Error('connection refused'),
    ]);
    harness.supervisor.start();
    await harness.settle();
    harness.supervisor.stop();
    await harness.settle();

    expect(harness.pushes).toContainEqual({ type: 'status', stream: 'live' });
    expect(harness.pushes).toContainEqual({ type: 'invalidated', kind: 'resync' });
    expect(harness.pushes).toContainEqual({
      type: 'invalidated',
      kind: 'feature.updated',
      resourceType: 'feature',
      resourceId: 'feat1234',
      featureId: 'feat1234',
    });
    expect(harness.pushes).toContainEqual({ type: 'status', stream: 'stale' });
  });

  it('every emitted push validates against the strict renderer event schema', async () => {
    const harness = makeSupervisorHarness([
      {
        lines: sseLines([
          envelope({ kind: 'connected', seq: 1, resource: { type: 'runtime' } }),
          envelope({ seq: 2 }),
          envelope({ seq: 3, kind: 'config.updated', resource: { type: 'runtime' } }),
        ]),
      },
      new Error('closed'),
    ]);
    harness.supervisor.start();
    await harness.settle();
    harness.supervisor.stop();

    expect(harness.pushes.length).toBeGreaterThan(0);
    for (const push of harness.pushes) {
      expect(AppEventSchema.safeParse(push).success).toBe(true);
    }
  });

  it('drops server-controlled fields that would violate the push schema', async () => {
    const harness = makeSupervisorHarness([
      {
        lines: sseLines([envelope({ seq: 2, kind: 'bad kind with spaces and tokens Bearer abc' })]),
      },
      new Error('closed'),
    ]);
    harness.supervisor.start();
    await harness.settle();
    harness.supervisor.stop();

    expect(harness.pushes.filter((push) => push.type === 'invalidated').length).toBe(0);
  });

  it('reconnects with the last cursor and backs off with a cap', async () => {
    const harness = makeSupervisorHarness([
      { lines: sseLines([envelope({ seq: 9, epoch: 'epoch-a' })]) },
      new Error('refused'),
      new Error('refused'),
      new Error('refused'),
      { lines: [], stayOpen: true },
    ]);
    harness.supervisor.start();
    await harness.settle();
    await harness.settle();
    await harness.settle();
    await harness.settle();
    harness.supervisor.stop();
    await harness.settle();

    expect(harness.openCalls[0]).toEqual({});
    expect(harness.openCalls[1]).toEqual({ afterSeq: 9, epoch: 'epoch-a' });
    // Backoff grows and caps: first delay after a successful connection resets.
    expect(harness.sleeps[0]).toBe(10);
    expect(harness.sleeps[1]).toBe(20);
    expect(harness.sleeps[2]).toBe(40);
    expect(harness.sleeps[3]).toBe(40);
  });

  it('resets the backoff after a successful connection', async () => {
    const harness = makeSupervisorHarness([
      new Error('refused'),
      new Error('refused'),
      { lines: sseLines([envelope({ seq: 1 })]) },
      new Error('refused'),
      { lines: [], stayOpen: true },
    ]);
    harness.supervisor.start();
    for (let i = 0; i < 6; i += 1) {
      await harness.settle();
    }
    harness.supervisor.stop();
    await harness.settle();

    expect(harness.sleeps.slice(0, 4)).toEqual([10, 20, 10, 20]);
  });

  it('treats a non-200 response as a failed attempt', async () => {
    const harness = makeSupervisorHarness([
      { status: 401, lines: [] },
      { lines: [], stayOpen: true },
    ]);
    harness.supervisor.start();
    await harness.settle();
    await harness.settle();
    harness.supervisor.stop();
    await harness.settle();

    expect(harness.pushes).toContainEqual({ type: 'status', stream: 'stale' });
    expect(harness.openCalls.length).toBeGreaterThanOrEqual(2);
  });

  it('stop closes the connection and suppresses further pushes; start is idempotent', async () => {
    const harness = makeSupervisorHarness([{ lines: [], stayOpen: true }]);
    harness.supervisor.start();
    harness.supervisor.start();
    await harness.settle();
    expect(harness.openCalls.length).toBe(1);

    harness.supervisor.stop();
    await harness.settle();
    const count = harness.pushes.length;
    await harness.settle();
    expect(harness.pushes.length).toBe(count);
  });

  it('restarting after stop resumes from the tracked cursor', async () => {
    const harness = makeSupervisorHarness([
      { lines: sseLines([envelope({ seq: 12, epoch: 'epoch-a' })]), stayOpen: true },
      { lines: [], stayOpen: true },
    ]);
    harness.supervisor.start();
    await harness.settle();
    harness.supervisor.stop();
    await harness.settle();
    harness.supervisor.start();
    await harness.settle();
    harness.supervisor.stop();
    await harness.settle();

    expect(harness.openCalls[1]).toEqual({ afterSeq: 12, epoch: 'epoch-a' });
  });

  it('never leaks token-shaped material into pushes or logs', async () => {
    const harness = makeSupervisorHarness([
      {
        lines: [
          'data: {"kind":"feature.updated","seq":2,"snapshot_required":true,' +
            '"summary":"Bearer tok-secret-xyz","resource":{"type":"feature","id":"f1"}}',
          '',
        ],
      },
      new Error('refused: Bearer tok-secret-xyz'),
    ]);
    harness.supervisor.start();
    await harness.settle();
    await harness.settle();
    harness.supervisor.stop();
    await harness.settle();

    const emitted = JSON.stringify(harness.pushes) + JSON.stringify(harness.logs);
    expect(emitted).not.toContain('tok-secret-xyz');
  });
});
