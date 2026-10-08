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
import { supervisorMarkerRecord, supervisorRecord } from '../../test/agenticoMock';
import type { SupervisorStatusInput } from './supervisorModel';
import {
  buildSupervisorConversation,
  harnessLabel,
  markerNoticeText,
  mergeRecords,
  mergeSnapshotRecords,
  supervisorConversationBuilder,
  supervisorConversationTail,
  supervisorChipLabel,
  supervisorStatusLine,
} from './supervisorModel';

describe('supervisorModel', () => {
  it('labels harnesses and the chip, with an empty effort reading Default', () => {
    expect(harnessLabel('claude')).toBe('Claude');
    expect(harnessLabel('opencode')).toBe('OpenCode');
    expect(harnessLabel('gemini')).toBe('Gemini');
    expect(supervisorChipLabel({ harness: '', model: '', effort: '' }, null)).toBe(
      'Choose harness and model',
    );
    expect(supervisorChipLabel({ harness: 'claude', model: 'opus', effort: '' }, null)).toBe(
      'Claude opus · Default',
    );
    expect(supervisorChipLabel({ harness: 'claude', model: 'opus', effort: 'xhigh' }, null)).toBe(
      'Claude opus · XHigh',
    );
  });

  it('orders the status line Rebuilding over Starting over Waiting over Working over Failed over Paused over Ready', () => {
    const line = (
      overrides: Partial<SupervisorStatusInput>,
      sending = false,
      requestPending = false,
    ): string =>
      supervisorStatusLine(
        {
          lifecycle: 'stopped',
          lastTurnOutcome: 'none',
          interruptedBy: 'none',
          harness: 'claude',
          ...overrides,
        },
        sending,
        requestPending,
      );
    expect(line({ lifecycle: 'starting', startingStep: 'rebuilding' })).toBe(
      'Rebuilding history for Claude…',
    );
    expect(line({ lifecycle: 'starting', startingStep: 'rebuilding', harness: 'codex' })).toBe(
      'Rebuilding history for Codex…',
    );
    expect(line({ lifecycle: 'starting', startingStep: 'launching' })).toBe('Starting supervisor…');
    expect(line({ lifecycle: 'starting' })).toBe('Starting supervisor…');
    expect(line({ lifecycle: 'stopped' }, true)).toBe('Starting supervisor…');
    expect(line({ lifecycle: 'failed' }, true)).toBe('Starting supervisor…');
    expect(line({ lifecycle: 'running' })).toBe('Working…');
    // A pending question or permission hides Working (Stop stays live).
    expect(line({ lifecycle: 'waiting_permission' })).toBe('Waiting for your response…');
    expect(line({ lifecycle: 'waiting_question' })).toBe('Waiting for your response…');
    expect(line({ lifecycle: 'running' }, false, true)).toBe('Waiting for your response…');
    expect(line({ lifecycle: 'starting' }, false, true)).toBe('Starting supervisor…');
    expect(line({ lifecycle: 'idle' }, false, true)).toBe('Ready');
    expect(line({ lifecycle: 'idle' }, true)).toBe('Working…');
    expect(line({ lifecycle: 'idle' })).toBe('Ready');
    expect(line({ lifecycle: 'failed' })).toBe('Supervisor failed — Retry');
    const paused = { lastTurnOutcome: 'interrupted', interruptedBy: 'shutdown' } as const;
    expect(line(paused)).toBe('Paused — interrupted before restart. Send a message to continue.');
    expect(line(paused, true)).toBe('Starting supervisor…');
    expect(line({ ...paused, lifecycle: 'idle' })).toBe('Ready');
    expect(line({ ...paused, interruptedBy: 'user' })).toBe('Ready');
    expect(line({ lastTurnOutcome: 'completed', interruptedBy: 'shutdown' })).toBe('Ready');
  });

  it('turns markers into notice rows and foots the cut reply of the same turn', () => {
    const user = supervisorRecord({
      seq: 1,
      turnId: 'turn-a',
      messages: [{ index: 1, role: 'user', type: 'text', text: 'Draft the plan' }],
    });
    const partial = supervisorRecord({
      seq: 2,
      turnId: 'turn-a',
      kind: 'assistant',
      messages: [{ index: 2, role: 'assistant', type: 'text', text: 'Here is the start' }],
    });
    const items = buildSupervisorConversation([
      user,
      partial,
      supervisorMarkerRecord(
        { marker: 'interrupted', text: 'server wording' },
        { seq: 3, turnId: 'turn-a' },
      ),
      supervisorMarkerRecord(
        { marker: 'history_not_restored', text: 'History could not be restored' },
        { seq: 4, turnId: '' },
      ),
      supervisorMarkerRecord(
        { marker: 'permission_restricted', text: 'Running in plan mode by policy' },
        { seq: 5, turnId: '' },
      ),
      supervisorMarkerRecord(
        { marker: 'error', text: 'The harness exited.', code: 'supervisor_launch_failed' },
        { seq: 6, turnId: '' },
      ),
    ]);
    expect(items).toEqual([
      expect.objectContaining({ kind: 'message', role: 'user', text: 'Draft the plan' }),
      expect.objectContaining({
        kind: 'message',
        role: 'assistant',
        text: 'Here is the start',
        footer: 'Interrupted',
      }),
      { kind: 'notice', key: 'notice-3', tone: 'interrupted', text: 'Interrupted before restart' },
      {
        kind: 'notice',
        key: 'notice-4',
        tone: 'caveat',
        text: 'History could not be restored',
      },
      {
        kind: 'notice',
        key: 'notice-5',
        tone: 'caveat',
        text: 'Running in plan mode by policy',
      },
      {
        kind: 'notice',
        key: 'notice-6',
        tone: 'failed',
        text: 'Supervisor failed to start · The harness exited.',
      },
    ]);
  });

  it('keeps the launch label off persistence and settings error markers', () => {
    expect(
      markerNoticeText({
        marker: 'error',
        text: "Couldn't save part of this turn: disk full",
      }),
    ).toBe("Couldn't save part of this turn: disk full");
    expect(
      markerNoticeText({ marker: 'error', text: "Couldn't apply settings change: rejected" }),
    ).toBe("Couldn't apply settings change: rejected");
    expect(markerNoticeText({ marker: 'error', text: '' })).toBe('Supervisor error');
    expect(markerNoticeText({ marker: 'error', text: '', code: 'supervisor_launch_failed' })).toBe(
      'Supervisor failed to start',
    );
  });

  it('shows setting notices but never renders a model-only note', () => {
    const changed = supervisorRecord({
      seq: 1,
      kind: 'marker',
      visibility: 'display_only',
      messages: [],
      marker: { marker: 'settings_changed', text: 'Model changed to Sonnet' },
    });
    const note = supervisorRecord({
      seq: 2,
      kind: 'note',
      visibility: 'model_only',
      messages: [{ index: 2, role: 'user', type: 'text', text: 'Agentico note' }],
    });
    const reverted = supervisorRecord({
      seq: 3,
      kind: 'marker',
      visibility: 'display_only',
      messages: [],
      marker: { marker: 'settings_reverted', text: 'Could not apply Sonnet' },
    });
    expect(buildSupervisorConversation([changed, note, reverted])).toEqual([
      { kind: 'notice', key: 'notice-1', tone: 'neutral', text: 'Model changed to Sonnet' },
      { kind: 'notice', key: 'notice-3', tone: 'caveat', text: 'Could not apply Sonnet' },
    ]);
  });

  it('never foots a reply from an earlier turn', () => {
    const items = buildSupervisorConversation([
      supervisorRecord({
        seq: 1,
        turnId: 'turn-a',
        kind: 'assistant',
        messages: [{ index: 1, role: 'assistant', type: 'text', text: 'Earlier reply' }],
      }),
      supervisorRecord({
        seq: 2,
        turnId: 'turn-b',
        messages: [{ index: 2, role: 'user', type: 'text', text: 'Next prompt' }],
      }),
      supervisorMarkerRecord(undefined, { seq: 3, turnId: 'turn-b' }),
    ]);
    expect(
      items.find((item) => item.kind === 'message' && item.role === 'assistant'),
    ).not.toHaveProperty('footer');
  });

  it('renders a request the restart resolved as an interrupted verdict', () => {
    const resolved = (kind: 'permission' | 'question', toolName: string) =>
      supervisorRecord({
        seq: 4,
        kind,
        visibility: 'display_only',
        messages: [],
        request: { requestId: 'r-1', toolName, stage: 'resolved', outcome: 'interrupted' },
      });
    expect(buildSupervisorConversation([resolved('permission', 'Bash')])).toEqual([
      { kind: 'verdict', key: 'verdict-4', outcome: 'interrupted', text: 'Interrupted · Bash' },
    ]);
    expect(buildSupervisorConversation([resolved('question', 'AskUserQuestion')])).toEqual([
      {
        kind: 'verdict',
        key: 'verdict-4',
        outcome: 'interrupted',
        text: 'Interrupted · Question',
      },
    ]);
  });

  it('dedupes records by seq and keeps them ordered', () => {
    const merged = mergeRecords(
      [supervisorRecord({ seq: 3 }), supervisorRecord({ seq: 1 })],
      [supervisorRecord({ seq: 2 }), supervisorRecord({ seq: 3 })],
    );
    expect(merged.map((record) => record.seq)).toEqual([1, 2, 3]);
  });

  it('drops a provisional reply once a record with its stream id commits', () => {
    const provisional = [
      { streamMessageId: 'stream-1', generation: 1, chunks: new Map([[0, 'Draft']]) },
    ];
    const committed = supervisorRecord({
      seq: 2,
      kind: 'assistant',
      streamMessageId: 'stream-1',
      messages: [{ index: 2, role: 'assistant', type: 'text', text: 'Final' }],
    });
    expect(buildSupervisorConversation([], { provisional }).map((item) => item.key)).toEqual([
      'provisional-1-stream-1',
    ]);
    const items = buildSupervisorConversation([committed], { provisional });
    expect(items).toHaveLength(1);
    expect(items[0]).toMatchObject({ kind: 'message', role: 'assistant', text: 'Final' });
  });

  it('keys provisional rows by generation, so a reused stream id from a new generation still renders', () => {
    const committedOld = supervisorRecord({
      seq: 2,
      generation: 1,
      kind: 'assistant',
      streamMessageId: 'stream-1',
      messages: [{ index: 2, role: 'assistant', type: 'text', text: 'Old generation reply' }],
    });
    const items = buildSupervisorConversation([committedOld], {
      provisional: [
        // The retired generation's stream is committed: no row.
        { streamMessageId: 'stream-1', generation: 1, chunks: new Map([[0, 'Old draft']]) },
        // The new generation reused the id: it renders.
        { streamMessageId: 'stream-1', generation: 2, chunks: new Map([[0, 'New draft']]) },
      ],
    });
    expect(items.map((item) => item.key)).toEqual(['message-2:0', 'provisional-2-stream-1']);
    expect(items[1]).toMatchObject({ role: 'assistant', text: 'New draft' });
  });

  it('keeps row keys unique when records from two generations are loaded', () => {
    const records = [
      supervisorRecord({ seq: 1, generation: 1, turnId: 'turn-a' }),
      supervisorRecord({
        seq: 2,
        generation: 1,
        turnId: 'turn-a',
        kind: 'assistant',
        streamMessageId: 'stream-1',
        messages: [{ index: 2, role: 'assistant', type: 'text', text: 'First' }],
      }),
      supervisorMarkerRecord(
        { marker: 'harness_change', text: 'Switched to Codex' },
        { seq: 3, generation: 2, turnId: 'turn-b' },
      ),
      supervisorRecord({ seq: 4, generation: 2, turnId: 'turn-c' }),
      supervisorRecord({
        seq: 5,
        generation: 2,
        turnId: 'turn-c',
        kind: 'assistant',
        streamMessageId: 'stream-1',
        messages: [{ index: 5, role: 'assistant', type: 'text', text: 'Second' }],
      }),
    ];
    const keys = buildSupervisorConversation(records, {
      provisional: [
        { streamMessageId: 'stream-2', generation: 2, chunks: new Map([[0, 'Streaming']]) },
      ],
      optimistic: 'Next',
    }).map((item) => item.key);
    expect(new Set(keys).size).toBe(keys.length);
    expect(keys).toContain('provisional-2-stream-2');
  });

  it('builds the committed conversation once and recomputes only the live tail', () => {
    const records = [
      supervisorRecord({ seq: 1 }),
      supervisorRecord({
        seq: 2,
        kind: 'assistant',
        messages: [{ index: 2, role: 'assistant', type: 'text', text: 'Committed' }],
      }),
    ];
    const spy = vi.spyOn(supervisorConversationBuilder, 'committed');
    try {
      const committed = supervisorConversationBuilder.committed(records);
      const tail = (text: string) =>
        supervisorConversationTail(committed, {
          provisional: [{ streamMessageId: 's', generation: 1, chunks: new Map([[0, text]]) }],
        });
      expect(tail('A').map((item) => item.key)).toEqual([
        'message-1:0',
        'message-2:0',
        'provisional-1-s',
      ]);
      expect(tail('AB').at(-1)).toMatchObject({ text: 'AB' });
      expect(spy).toHaveBeenCalledTimes(1);
    } finally {
      spy.mockRestore();
    }
  });

  it('merges a snapshot page with streamed records above its head and replaces the rest', () => {
    const page = [
      supervisorRecord({
        seq: 4,
        messages: [{ index: 4, role: 'user', type: 'text', text: 'Fetched four' }],
      }),
      supervisorRecord({
        seq: 5,
        messages: [{ index: 5, role: 'user', type: 'text', text: 'Fetched five' }],
      }),
    ];
    const streamed = [
      supervisorRecord({
        seq: 5,
        messages: [{ index: 5, role: 'user', type: 'text', text: 'Streamed five' }],
      }),
      supervisorRecord({
        seq: 6,
        messages: [{ index: 6, role: 'user', type: 'text', text: 'Streamed six' }],
      }),
    ];
    const merged = mergeSnapshotRecords(page, 5, streamed);
    expect(merged.map((record) => [record.seq, record.messages[0]?.text])).toEqual([
      [4, 'Fetched four'],
      [5, 'Fetched five'],
      [6, 'Streamed six'],
    ]);
  });

  it('renders a transcript_recovered marker as a caveat notice', () => {
    const items = buildSupervisorConversation([
      supervisorMarkerRecord(
        {
          marker: 'transcript_recovered',
          text: '3 records could not be read; the original is saved beside the transcript.',
        },
        { seq: 9 },
      ),
    ]);
    expect(items).toEqual([
      {
        kind: 'notice',
        key: 'notice-9',
        tone: 'caveat',
        text: '3 records could not be read; the original is saved beside the transcript.',
      },
    ]);
  });
});

it('keeps one task across verdict seams and settles missing final reports honestly', () => {
  const records = [
    supervisorRecord({
      seq: 1,
      turnId: 't',
      kind: 'tool_use',
      messages: [
        {
          index: 1,
          role: 'system',
          type: 'task_started',
          task: { id: 'a', description: 'Review tests' },
        },
      ],
    }),
    supervisorRecord({
      seq: 2,
      turnId: 't',
      kind: 'permission',
      request: {
        requestId: 'p',
        toolName: 'Read',
        stage: 'resolved',
        outcome: 'allowed',
        origin: 'root',
      },
      messages: [],
    }),
    supervisorRecord({
      seq: 3,
      turnId: 't',
      kind: 'tool_use',
      messages: [
        {
          index: 3,
          role: 'system',
          type: 'task_progress',
          task: { id: 'a', lastToolName: 'Read' },
        },
      ],
    }),
  ];
  const live = buildSupervisorConversation(records, { activeTurnId: 't' }).filter(
    (item) => item.kind === 'subagents',
  );
  expect(live).toHaveLength(1);
  expect(live[0]?.agents[0]).toMatchObject({
    state: 'running',
    description: 'Review tests',
    lastTool: 'Read',
  });
  const stopped = buildSupervisorConversation(records, { activeTurnId: '' }).filter(
    (item) => item.kind === 'subagents',
  );
  expect(stopped[0]?.agents[0]?.state).toBe('unknown');
});
