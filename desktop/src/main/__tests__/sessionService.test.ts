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
import type { SseStream } from '../gateway/events';
import { SessionService, parseSessionOutputBlock, type ServerTransport } from '../serverClient';

function stream(lines: readonly string[]): SseStream {
  return {
    status: 200,
    lines: (async function* () {
      for (const line of lines) yield line;
    })(),
    close: vi.fn(),
  };
}

function transport(overrides: Partial<ServerTransport> = {}): ServerTransport {
  return {
    apiRequest: vi.fn(() => Promise.resolve({ status: 200, body: {} })),
    openSessionOutputStream: vi.fn(() => Promise.resolve(stream([]))),
    ...overrides,
  };
}

const summary = {
  id: 'session-1',
  feature_id: 'abcd1234',
  run_number: 2,
  phase: 'implement',
  kind: 'agent',
  status: 'running',
  started_at: '2026-07-15T00:00:00Z',
  usage: {},
  task_activities: [],
  running_task_count: 0,
};

describe('SessionService snapshots', () => {
  it('maps runtime-validated list, detail, and bounded transcript responses', async () => {
    const apiRequest = vi
      .fn()
      .mockResolvedValueOnce({ status: 200, body: { api_version: 'v1', sessions: [summary] } })
      .mockResolvedValueOnce({
        status: 200,
        body: {
          api_version: 'v1',
          session: {
            ...summary,
            transcript_cursor: { total: 8, start: 0, end: 8 },
            pending_controls: [],
            can_attach: true,
            log_available: true,
          },
        },
      })
      .mockResolvedValueOnce({
        status: 200,
        body: {
          api_version: 'v1',
          cursor: { total: 8, start: 6, end: 8 },
          messages: [
            { index: 6, role: 'assistant', type: 'text', text: 'first' },
            { index: 7, role: 'tool', type: 'progress', status: 'running' },
          ],
        },
      });
    const service = new SessionService({ ...transport(), apiRequest });

    await expect(service.list()).resolves.toStrictEqual([
      {
        id: 'session-1',
        featureId: 'abcd1234',
        runNumber: 2,
        phase: 'implement',
        kind: 'agent',
        status: 'running',
        startedAt: '2026-07-15T00:00:00Z',
        usage: {},
        taskActivities: [],
        runningTaskCount: 0,
      },
    ]);
    await expect(service.get('session-1')).resolves.toMatchObject({
      id: 'session-1',
      transcriptCursor: { total: 8, start: 0, end: 8 },
      canAttach: true,
    });
    await expect(
      service.transcript({ sessionId: 'session-1', offset: 6, limit: 2 }),
    ).resolves.toMatchObject({
      cursor: { total: 8, start: 6, end: 8 },
      messages: [{ index: 6 }, { index: 7 }],
    });
    expect(apiRequest).toHaveBeenNthCalledWith(
      3,
      '/api/v1/sessions/session-1/transcript?offset=6&limit=2',
      undefined,
    );
  });

  it('rejects incompatible, oversized, polluted, and malformed snapshots', async () => {
    const cases: unknown[] = [
      { api_version: 'v2', sessions: [] },
      { api_version: 'v1', sessions: [{ ...summary, extra: 'x'.repeat(6 * 1024 * 1024) }] },
      JSON.parse('{"api_version":"v1","sessions":[],"__proto__":{}}'),
      { api_version: 'v1', sessions: [{ ...summary, usage: null }] },
    ];
    for (const body of cases) {
      const service = new SessionService(
        transport({ apiRequest: () => Promise.resolve({ status: 200, body }) }),
      );
      await expect(service.list()).rejects.toMatchObject({ safe: expect.any(Object) });
    }
  });
});

describe('SessionService singleton chat mutations', () => {
  it('starts and ends the server-owned singleton chat through trusted mutation paths', async () => {
    const apiRequest = vi
      .fn()
      .mockResolvedValueOnce({
        status: 200,
        body: { api_version: 'v1', session_id: '__chat__', result: 'started' },
      })
      .mockResolvedValueOnce({
        status: 200,
        body: { api_version: 'v1', session_id: '__chat__', result: 'ended' },
      });
    const service = new SessionService({ ...transport(), apiRequest });

    await expect(
      service.startChat({ message: 'What is running?', images: ['/tmp/clipboard.png'] }),
    ).resolves.toStrictEqual({
      sessionId: '__chat__',
      result: 'started',
    });
    await expect(service.endChat()).resolves.toStrictEqual({
      sessionId: '__chat__',
      result: 'ended',
    });
    expect(apiRequest).toHaveBeenNthCalledWith(1, '/api/v1/prompts/chat/start', {
      method: 'POST',
      body: { message: 'What is running?', images: ['/tmp/clipboard.png'] },
    });
    expect(apiRequest).toHaveBeenNthCalledWith(2, '/api/v1/prompts/chat/end', {
      method: 'POST',
      body: {},
    });
  });

  it('rejects malformed singleton chat mutation responses', async () => {
    const service = new SessionService(
      transport({
        apiRequest: () =>
          Promise.resolve({
            status: 200,
            body: { api_version: 'v1', session_id: 'chat/unsafe', result: 'started' },
          }),
      }),
    );

    await expect(service.startChat({ message: 'hello' })).rejects.toMatchObject({
      safe: { code: 'E_SCHEMA_MISMATCH' },
    });
  });

  it('refuses staged local image paths while remotely connected (fail, never leak)', async () => {
    const apiRequest = vi.fn(() =>
      Promise.resolve({
        status: 200,
        body: { api_version: 'v1', session_id: '__chat__', result: 'started' },
      }),
    );
    const service = new SessionService(transport({ apiRequest }), undefined, () => 'remote');

    await expect(
      service.startChat({ message: 'What is running?', images: ['/tmp/clipboard.png'] }),
    ).rejects.toMatchObject({ safe: { code: 'E_REQUIRES_LOCAL_SERVER' } });
    expect(apiRequest).not.toHaveBeenCalled();
  });

  it('forwards staged upload references remotely as image_uploads', async () => {
    const apiRequest = vi.fn(() =>
      Promise.resolve({
        status: 200,
        body: { api_version: 'v1', session_id: '__chat__', result: 'started' },
      }),
    );
    const service = new SessionService(transport({ apiRequest }), undefined, () => 'remote');

    await service.startChat({ message: 'Look at this', imageUploads: ['ref-image-1'] });
    expect(apiRequest).toHaveBeenCalledWith('/api/v1/prompts/chat/start', {
      method: 'POST',
      body: { message: 'Look at this', images: [], image_uploads: ['ref-image-1'] },
    });
  });

  it('sends chat images unchanged while locally connected', async () => {
    const apiRequest = vi.fn(() =>
      Promise.resolve({
        status: 200,
        body: { api_version: 'v1', session_id: '__chat__', result: 'started' },
      }),
    );
    const service = new SessionService(transport({ apiRequest }), undefined, () => 'local');

    await service.startChat({ message: 'here', images: ['/tmp/clipboard.png'] });
    expect(apiRequest).toHaveBeenCalledWith('/api/v1/prompts/chat/start', {
      method: 'POST',
      body: { message: 'here', images: ['/tmp/clipboard.png'] },
    });
  });

  it('never emits image_uploads locally', async () => {
    const apiRequest = vi.fn(() =>
      Promise.resolve({
        status: 200,
        body: { api_version: 'v1', session_id: '__chat__', result: 'started' },
      }),
    );
    const service = new SessionService(transport({ apiRequest }), undefined, () => 'local');
    await service.startChat({ message: 'no images' });
    expect(apiRequest).toHaveBeenCalledWith('/api/v1/prompts/chat/start', {
      method: 'POST',
      body: { message: 'no images', images: [] },
    });
  });
});

describe('SessionService output subscriptions', () => {
  it('uses only the transcript row cursor and replaces repeated indexes', async () => {
    const source = stream([
      'id: 4',
      'event: session.output',
      'data: {"api_version":"v1","session_id":"session-1","index":4,"message":{"index":4,"role":"assistant","type":"text","text":"partial"}}',
      '',
      'id: 4',
      'event: session.output',
      'data: {"api_version":"v1","session_id":"session-1","index":4,"message":{"index":4,"role":"assistant","type":"text","text":"complete"}}',
      '',
      'event: session.output.done',
      'data: {"api_version":"v1","session_id":"session-1","index":5,"done":true}',
      '',
    ]);
    const open = vi.fn(() => Promise.resolve(source));
    const service = new SessionService(
      transport({ openSessionOutputStream: open }),
      () => 'sub-fixed',
    );
    const events: unknown[] = [];
    const id = service.subscribe({ sessionId: 'session-1', from: 4 }, (event) =>
      events.push(event),
    );
    expect(id).toBe('sub-fixed');
    await vi.waitFor(() => expect(events).toHaveLength(3));
    expect(open).toHaveBeenCalledWith('session-1', { from: 4 });
    expect(events).toStrictEqual([
      expect.objectContaining({
        type: 'record',
        index: 4,
        message: expect.objectContaining({ text: 'partial' }),
      }),
      expect.objectContaining({
        type: 'record',
        index: 4,
        message: expect.objectContaining({ text: 'complete' }),
      }),
      { subscriptionId: 'sub-fixed', type: 'done', sessionId: 'session-1', nextIndex: 5 },
    ]);
    expect(service.activeSubscriptionCount()).toBe(0);
    expect(source.close).toHaveBeenCalledOnce();
  });

  it('cancels streams idempotently and treats abort as normal cleanup', async () => {
    let release!: () => void;
    const source: SseStream = {
      status: 200,
      lines: (async function* () {
        await new Promise<void>((resolve) => {
          release = resolve;
        });
      })(),
      close: vi.fn(() => release?.()),
    };
    const service = new SessionService(
      transport({ openSessionOutputStream: () => Promise.resolve(source) }),
      () => 'sub-fixed',
    );
    const emit = vi.fn();
    service.subscribe({ sessionId: 'session-1', from: 0 }, emit);
    await vi.waitFor(() => expect(service.activeSubscriptionCount()).toBe(1));
    expect(service.cancel('sub-fixed')).toBe(true);
    expect(service.cancel('sub-fixed')).toBe(false);
    await vi.waitFor(() => expect(service.activeSubscriptionCount()).toBe(0));
    expect(source.close).toHaveBeenCalledOnce();
    expect(emit).not.toHaveBeenCalledWith(expect.objectContaining({ type: 'error' }));
  });

  it('contains a throwing error sink when stream setup fails', async () => {
    const service = new SessionService(
      transport({
        openSessionOutputStream: () => Promise.reject(new Error('stream setup failed')),
      }),
      () => 'sub-fixed',
    );
    const emit = vi.fn(() => {
      throw new Error('renderer was destroyed');
    });

    service.subscribe({ sessionId: 'session-1', from: 0 }, emit);

    await vi.waitFor(() => expect(service.activeSubscriptionCount()).toBe(0));
    expect(emit).toHaveBeenCalledOnce();
  });

  it('delivers a safe stream error when the sink remains available', async () => {
    const service = new SessionService(
      transport({
        openSessionOutputStream: () => Promise.reject(new Error('stream setup failed')),
      }),
      () => 'sub-fixed',
    );
    const emit = vi.fn();

    service.subscribe({ sessionId: 'session-1', from: 0 }, emit);

    await vi.waitFor(() => expect(service.activeSubscriptionCount()).toBe(0));
    expect(emit).toHaveBeenCalledWith({
      subscriptionId: 'sub-fixed',
      type: 'error',
      sessionId: 'session-1',
      error: {
        code: 'E_SESSION_STREAM',
        message: 'stream setup failed',
      },
    });
  });
});

describe('parseSessionOutputBlock', () => {
  it('fails closed on event kinds outside the production protocol', () => {
    expect(() =>
      parseSessionOutputBlock({
        id: '1',
        event: 'session.output.unknown',
        data: '{"api_version":"v1","session_id":"session-1","index":1}',
      }),
    ).toThrow('Unknown session output event.');
  });

  it('fails closed on version mismatch, pollution, oversized text, and cursor disagreement', () => {
    const base = {
      id: '1',
      event: 'session.output',
      data: '{"api_version":"v1","session_id":"session-1","index":1,"message":{"index":1,"role":"assistant","type":"text"}}',
    };
    expect(parseSessionOutputBlock(base)).toMatchObject({ type: 'record', index: 1 });
    for (const data of [
      base.data.replace('"v1"', '"v2"'),
      base.data.replace('"type":"text"', '"type":"text","__proto__":{}'),
      base.data.replace('"index":1,"role"', '"index":2,"role"'),
      `{"api_version":"v1","session_id":"session-1","index":1,"message":{"index":1,"role":"assistant","type":"text","text":"${'x'.repeat(1024 * 1024 + 1)}"}}`,
    ]) {
      expect(() => parseSessionOutputBlock({ ...base, data })).toThrow();
    }
  });
});
