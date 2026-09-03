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

import type { TranscriptMessage } from '../../../shared/ipc';

export const MAX_RETAINED_MESSAGES = 1000;
/** Aggregate raw-record budget; row count alone permits gigabytes of valid text. */
export const MAX_RETAINED_BYTES = 8 * 1024 * 1024;
export const MAX_RENDERED_ENTRIES = 200;

export type SemanticKind = 'agent' | 'phase' | 'error' | 'result' | 'routine-group';

export interface SemanticEntry {
  id: string;
  kind: SemanticKind;
  label: string;
  text: string;
  records: TranscriptMessage[];
}

/** Reconcile the REST/live shared row index, replacing legitimate updates. */
export function reconcileTranscript(
  current: readonly TranscriptMessage[],
  incoming: readonly TranscriptMessage[],
): TranscriptMessage[] {
  const rows = new Map(current.map((message) => [message.index, message]));
  for (const message of incoming) rows.set(message.index, message);
  const tail = [...rows.values()]
    .sort((left, right) => left.index - right.index)
    .slice(-MAX_RETAINED_MESSAGES);
  const encoder = new TextEncoder();
  let retainedBytes = 0;
  let start = tail.length;
  while (start > 0) {
    const record = tail[start - 1]!;
    const recordBytes = encoder.encode(JSON.stringify(record)).byteLength;
    if (retainedBytes + recordBytes > MAX_RETAINED_BYTES) break;
    retainedBytes += recordBytes;
    start -= 1;
  }
  return tail.slice(start);
}

export function semanticTimeline(messages: readonly TranscriptMessage[]): SemanticEntry[] {
  const entries: SemanticEntry[] = [];
  for (const record of messages) {
    const kind = semanticKind(record);
    if (kind === 'routine-group') {
      const turn = record.blockIndex ?? record.index;
      const previous = entries.at(-1);
      if (previous?.kind === 'routine-group' && previous.id === `turn-${turn}`) {
        previous.records.push(record);
        previous.text = `${previous.records.length} routine activities`;
        continue;
      }
      entries.push({
        id: `turn-${turn}`,
        kind,
        label: 'Routine activity',
        text: '1 routine activity',
        records: [record],
      });
      continue;
    }
    entries.push({
      id: `row-${record.index}`,
      kind,
      label: semanticLabel(kind, record),
      text: stripUnsafeAnsi(record.text ?? record.task?.summary ?? record.task?.description ?? ''),
      records: [record],
    });
  }
  return entries;
}

function semanticKind(record: TranscriptMessage): SemanticKind {
  const type = record.type.toLowerCase();
  if (type.includes('error') || record.status?.toLowerCase() === 'failed') return 'error';
  if (type.includes('result')) return 'result';
  if (type.includes('phase') || type.includes('status')) return 'phase';
  if (
    record.role.toLowerCase() === 'assistant' ||
    record.role.toLowerCase() === 'agent' ||
    type.includes('agent')
  ) {
    return 'agent';
  }
  return 'routine-group';
}

function semanticLabel(kind: Exclude<SemanticKind, 'routine-group'>, record: TranscriptMessage) {
  if (kind === 'agent') return 'Agent';
  if (kind === 'phase')
    return record.status === undefined ? 'Phase change' : `Status · ${record.status}`;
  if (kind === 'error') return 'Error';
  return 'Result';
}

/** Plain-text terminal policy: remove CSI and OSC sequences, retain no HTML. */
export function stripUnsafeAnsi(value: string): string {
  const escape = '\u001b';
  const bell = '\u0007';
  return value
    .replace(new RegExp(`${escape}\\][^${bell}]*(?:${bell}|${escape}\\\\)`, 'g'), '')
    .replace(new RegExp(`${escape}\\[[0-?]*[ -/]*[@-~]`, 'g'), '');
}
