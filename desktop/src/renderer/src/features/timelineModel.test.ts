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
import type { TranscriptMessage } from '../../../shared/ipc';
import {
  MAX_RETAINED_BYTES,
  MAX_RETAINED_MESSAGES,
  reconcileTranscript,
  semanticTimeline,
  stripUnsafeAnsi,
} from './timelineModel';

const message = (index: number, overrides: Partial<TranscriptMessage> = {}): TranscriptMessage => ({
  index,
  role: 'assistant',
  type: 'text',
  text: `message ${index}`,
  ...overrides,
});

describe('reconcileTranscript', () => {
  it('replaces repeated session/index records and keeps a bounded tail', () => {
    const initial = Array.from({ length: MAX_RETAINED_MESSAGES }, (_, index) => message(index));
    const reconciled = reconcileTranscript(initial, [
      message(MAX_RETAINED_MESSAGES - 1, { text: 'replacement' }),
      message(MAX_RETAINED_MESSAGES),
    ]);
    expect(reconciled).toHaveLength(MAX_RETAINED_MESSAGES);
    expect(reconciled[0]?.index).toBe(1);
    expect(reconciled.at(-2)?.text).toBe('replacement');
    expect(reconciled.at(-1)?.index).toBe(MAX_RETAINED_MESSAGES);
  });

  it('bounds retained transcript bytes when valid rows contain large output', () => {
    const largeText = 'x'.repeat(1024 * 1024);
    const reconciled = reconcileTranscript(
      [],
      Array.from({ length: 12 }, (_, index) => message(index, { text: largeText })),
    );

    const retainedBytes = reconciled.reduce(
      (total, record) => total + new TextEncoder().encode(JSON.stringify(record)).byteLength,
      0,
    );
    expect(retainedBytes).toBeLessThanOrEqual(MAX_RETAINED_BYTES);
    expect(reconciled.at(-1)?.index).toBe(11);
    expect(reconciled.length).toBeLessThan(12);
  });
});

describe('semanticTimeline', () => {
  it('promotes agent, phase, error, and result entries while grouping adjacent routine activity', () => {
    const entries = semanticTimeline([
      message(1, { role: 'assistant', text: 'Working on it.' }),
      message(2, { role: 'tool', type: 'tool', tool: 'Read', blockIndex: 7 }),
      message(3, {
        role: 'tool',
        type: 'task',
        task: { description: 'Inspect tests' },
        blockIndex: 7,
      }),
      message(4, { role: 'system', type: 'phase', text: 'Implement' }),
      message(5, { role: 'system', type: 'error', text: 'Build failed' }),
      message(6, { role: 'system', type: 'result', text: 'Complete' }),
    ]);
    expect(entries.map((entry) => entry.kind)).toStrictEqual([
      'agent',
      'routine-group',
      'phase',
      'error',
      'result',
    ]);
    expect(entries[1]?.records).toHaveLength(2);
  });

  it('removes terminal control sequences instead of exposing executable styling', () => {
    expect(stripUnsafeAnsi('\u001b[31mDanger\u001b[0m\u001b]8;;https://evil.test\u0007link')).toBe(
      'Dangerlink',
    );
  });
});
