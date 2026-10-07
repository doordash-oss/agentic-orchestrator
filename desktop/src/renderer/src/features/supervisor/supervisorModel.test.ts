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
import { supervisorRecord } from '../../test/agenticoMock';
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

  it('orders the status line Starting over Working over the resting label', () => {
    expect(supervisorStatusLine('starting', false)).toBe('Starting supervisor…');
    expect(supervisorStatusLine('stopped', true)).toBe('Starting supervisor…');
    expect(supervisorStatusLine('running', false)).toBe('Working…');
    expect(supervisorStatusLine('waiting_permission', false)).toBe('Working…');
    expect(supervisorStatusLine('idle', true)).toBe('Working…');
    expect(supervisorStatusLine('idle', false)).toBe('Ready');
    expect(supervisorStatusLine('failed', false)).toBe('Ready');
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
