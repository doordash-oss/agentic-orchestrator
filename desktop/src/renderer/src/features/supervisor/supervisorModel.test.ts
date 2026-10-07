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
import { supervisorMarkerRecord, supervisorRecord } from '../../test/agenticoMock';
import type { SupervisorStatusInput } from './supervisorModel';
import {
  buildSupervisorConversation,
  harnessLabel,
  mergeRecords,
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

  it('orders the status line Rebuilding over Starting over Working over Failed over Paused over Ready', () => {
    const line = (overrides: Partial<SupervisorStatusInput>, sending = false): string =>
      supervisorStatusLine(
        {
          lifecycle: 'stopped',
          lastTurnOutcome: 'none',
          interruptedBy: 'none',
          harness: 'claude',
          ...overrides,
        },
        sending,
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
    expect(line({ lifecycle: 'waiting_permission' })).toBe('Working…');
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
      'provisional-stream-1',
    ]);
    const items = buildSupervisorConversation([committed], { provisional });
    expect(items).toHaveLength(1);
    expect(items[0]).toMatchObject({ kind: 'message', role: 'assistant', text: 'Final' });
  });
});
