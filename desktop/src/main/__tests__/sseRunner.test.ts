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
import { EventStreamSupervisor, type SseStream } from '../gateway/events';
import { SseRunner } from '../gateway/sseRunner';
import { SupervisorStreamRunner } from '../supervisorStream';

function deferred() {
  let resolve!: () => void;
  const promise = new Promise<void>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

function heldStream(): { stream: SseStream; closed: () => number } {
  let closes = 0;
  const wake = deferred();
  return {
    stream: {
      status: 200,
      lines: {
        async *[Symbol.asyncIterator]() {
          await wake.promise;
        },
      },
      close() {
        closes += 1;
        wake.resolve();
      },
    },
    closed: () => closes,
  };
}

describe('shared SSE runner', () => {
  it('does not close a replacement stream when an older open resolves after stop', async () => {
    const oldOpen = deferred();
    const old = heldStream();
    const replacement = heldStream();
    let opens = 0;
    const runner = new EventStreamSupervisor({
      source: {
        openEventStream: async () => {
          if (++opens === 1) {
            await oldOpen.promise;
            return old.stream;
          }
          return replacement.stream;
        },
      },
      sleep: async () => {},
      log: () => {},
      onPush: () => {},
    });
    runner.start();
    runner.stop();
    runner.start();
    await Promise.resolve();
    oldOpen.resolve();
    await Promise.resolve();
    await Promise.resolve();
    expect(old.closed()).toBe(1);
    expect(replacement.closed()).toBe(0);
    runner.stop();
  });

  it('leaves the newer event stream open when a stopped loop finishes late', async () => {
    const oldExit = deferred();
    const oldEnteredExit = deferred();
    const oldWake = deferred();
    let oldCloses = 0;
    const old: SseStream = {
      status: 200,
      lines: {
        async *[Symbol.asyncIterator]() {
          try {
            await oldWake.promise;
            yield ': old stream';
          } finally {
            oldEnteredExit.resolve();
            await oldExit.promise;
          }
        },
      },
      close() {
        oldCloses += 1;
        oldWake.resolve();
      },
    };
    const newer = heldStream();
    let opens = 0;
    const supervisor = new EventStreamSupervisor({
      source: { openEventStream: async () => (++opens === 1 ? old : newer.stream) },
      sleep: async () => {},
      log: () => {},
      onPush: () => {},
    });
    supervisor.start();
    await Promise.resolve();
    supervisor.stop();
    supervisor.start();
    await oldEnteredExit.promise;
    oldExit.resolve();
    await Promise.resolve();
    await Promise.resolve();
    expect(oldCloses).toBeGreaterThan(0);
    expect(newer.closed()).toBe(0);
    supervisor.stop();
  });

  it('shares non-200 retry and capped backoff behavior', async () => {
    const sleeps: number[] = [];
    const statuses: string[] = [];
    let attempts = 0;
    const runner = new SseRunner<never, string>({
      open: async () => {
        attempts += 1;
        return { status: 503, lines: { async *[Symbol.asyncIterator]() {} }, close() {} };
      },
      parse: () => null,
      ingest: (_event, cursor) => ({ cursor, pushes: [] }),
      status: (state) => state,
      emit: (status) => {
        statuses.push(status);
      },
      sleep: async (ms) => {
        sleeps.push(ms);
        if (sleeps.length === 3) runner.stop();
      },
      log: () => {},
      label: 'test stream',
      dropped: 'dropped',
      backoff: { initialMs: 10, maxMs: 20 },
    });
    runner.start();
    while (sleeps.length < 3) await Promise.resolve();
    expect(attempts).toBe(3);
    expect(sleeps).toEqual([10, 20, 20]);
    expect(statuses).toEqual(['connecting', 'stale', 'stale', 'stale']);
  });

  it('starts both adapters without changing their status shapes', async () => {
    const events = heldStream();
    const supervisorStream = heldStream();
    const eventPushes: unknown[] = [];
    const supervisorPushes: unknown[] = [];
    const eventRunner = new EventStreamSupervisor({
      source: { openEventStream: async () => events.stream },
      sleep: async () => {},
      log: () => {},
      onPush: (push) => eventPushes.push(push),
    });
    const supervisorRunner = new SupervisorStreamRunner({
      source: { openSupervisorEventStream: async () => supervisorStream.stream },
      sleep: async () => {},
      log: () => {},
      onPush: (push) => supervisorPushes.push(push),
    });
    eventRunner.start();
    supervisorRunner.start();
    await Promise.resolve();
    expect(eventPushes).toEqual([
      { type: 'status', stream: 'connecting' },
      { type: 'status', stream: 'live' },
    ]);
    expect(supervisorPushes).toEqual([{ type: 'stream-status', status: 'live' }]);
    eventRunner.stop();
    supervisorRunner.stop();
  });
});
