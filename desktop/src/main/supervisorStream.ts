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

/**
 * Main-process consumption of the per-server supervisor conversation stream
 * (`GET /api/v1/supervisor/events`), modelled on the global event-stream
 * supervisor (gateway/events.ts) and sharing its SSE framing, payload
 * decode, and reconnect backoff:
 *
 *  - Only committed `record` events advance the `(seq, epoch)` resume
 *    cursor; reconnects resume with `after=<seq>&epoch=<epoch>` so the
 *    server replays exactly the records that were missed. Replayed records
 *    at or below the cursor are dropped as duplicates.
 *  - `stream.reset` (cursor beyond head or stale epoch) and an observed
 *    epoch change drop the cursor and push `{type: 'reset'}` so the
 *    renderer re-snapshots state and transcript. A server identity change
 *    (resetCursor) does the same.
 *  - Every payload is decoded fail-closed, validated against the bounded
 *    wire schema, mapped to the camelCase IPC shape, and re-validated
 *    against the strict renderer schema; anything else is dropped.
 *  - Stream liveness is pushed as `{type: 'stream-status', status}`.
 *
 * No bearer, URL, or other connection material ever leaves this module.
 */
import {
  ServerSupervisorStreamEventSchema,
  type ServerSupervisorStreamEvent,
} from '../shared/api/parse';
import {
  SupervisorEventSchema,
  SupervisorStreamEpochSchema,
  type SupervisorEvent,
} from '../shared/ipc';
import { decodeSsePayload, type SseBlock, type SseStream } from './gateway/events';
import { supervisorPendingRequest } from './attention';
import { SseRunner, type SseCursor } from './gateway/sseRunner';
import { toSupervisorRecord, toSupervisorState } from './supervisor';
import { defined, definedNonEmpty } from '../shared/defined';

/**
 * Upper bound for one supervisor event payload. Records carry transcript
 * rows, so this matches the SSE line bound rather than the tiny global
 * invalidation bound.
 */
const MAX_SUPERVISOR_EVENT_BYTES = 1024 * 1024;

/** The narrow surface the runner needs from the runtime gateway. */
interface SupervisorStreamSource {
  openSupervisorEventStream(options: { afterSeq?: number; epoch?: string }): Promise<SseStream>;
}

interface SupervisorStreamDeps {
  source: SupervisorStreamSource;
  sleep(ms: number): Promise<void>;
  /** Redacted local diagnostics sink. */
  log(line: string): void;
  /** Receives schema-valid pushes destined for the renderer. */
  onPush(event: SupervisorEvent): void;
  backoff?: { initialMs: number; maxMs: number };
}

type SupervisorCursor = SseCursor;

/**
 * Parses one SSE block into the wire event, or null (dropped) when the
 * payload is oversized, malformed, polluted, schema-invalid, missing the
 * body its kind requires, or disagrees with the block's own framing.
 */
export function parseSupervisorStreamEvent(block: SseBlock): ServerSupervisorStreamEvent | null {
  const raw = decodeSsePayload(block.data, MAX_SUPERVISOR_EVENT_BYTES);
  if (raw === undefined) return null;
  const parsed = ServerSupervisorStreamEventSchema.safeParse(raw);
  if (!parsed.success) return null;
  const event = parsed.data;
  if (block.event !== '' && block.event !== event.kind) return null;
  // The epoch feeds the resume query; it must be URL-safe before it is trusted.
  if (!SupervisorStreamEpochSchema.safeParse(event.stream_epoch).success) return null;
  switch (event.kind) {
    case 'record':
      if (event.record === undefined) return null;
      // Only record events carry an SSE id, and it must be the record seq.
      if (block.id !== '' && block.id !== String(event.record.seq)) return null;
      return event;
    case 'delta':
      return event.delta === undefined ? null : event;
    case 'state':
      return event.state === undefined ? null : event;
    case 'request':
      return event.request === undefined ? null : event;
    case 'stream.reset':
    case 'heartbeat':
      return event;
    default: {
      const exhaustive: never = event.kind;
      return exhaustive;
    }
  }
}

/** Maps a parsed wire event to its renderer push (null for heartbeats/resets). */
function toRendererEvent(event: ServerSupervisorStreamEvent): SupervisorEvent | null {
  const envelope = {
    conversationId: event.conversation_id,
    generation: event.generation,
    streamEpoch: event.stream_epoch,
  };
  if (event.kind === 'record' && event.record !== undefined) {
    return { type: 'record', ...envelope, record: toSupervisorRecord(event.record) };
  }
  if (event.kind === 'delta' && event.delta !== undefined) {
    return {
      type: 'delta',
      ...envelope,
      delta: {
        turnId: event.delta.turn_id,
        streamMessageId: event.delta.stream_message_id,
        chunkIndex: event.delta.chunk_index,
        text: event.delta.text,
      },
    };
  }
  if (event.kind === 'state' && event.state !== undefined) {
    return { type: 'state', ...envelope, state: toSupervisorState(event.state) };
  }
  if (event.kind === 'request' && event.request !== undefined) {
    return { type: 'request', ...envelope, request: supervisorPendingRequest(event.request) };
  }
  return null;
}

/** Supervisor-specific cursor and renderer mapping over the shared runner. */
export class SupervisorStreamRunner {
  private readonly runner: SseRunner<ServerSupervisorStreamEvent, SupervisorEvent>;

  constructor(private readonly deps: SupervisorStreamDeps) {
    this.runner = new SseRunner({
      open: (cursor) =>
        deps.source.openSupervisorEventStream(
          cursor.seq > 0 ? { afterSeq: cursor.seq, ...definedNonEmpty('epoch', cursor.epoch) } : {},
        ),
      parse: parseSupervisorStreamEvent,
      ingest: (event, cursor) => this.ingest(event, cursor),
      status: (state) => (state === 'connecting' ? null : { type: 'stream-status', status: state }),
      emit: (push) => this.emit(push),
      sleep: deps.sleep,
      log: deps.log,
      label: 'supervisor stream',
      dropped: 'dropped an unusable supervisor stream payload',
      ...defined('backoff', deps.backoff),
    });
  }

  getCursor(): SupervisorCursor {
    return this.runner.getCursor();
  }
  start(): void {
    this.runner.start();
  }
  stop(): void {
    this.runner.stop();
  }

  resetCursor(): void {
    this.runner.resetCursor();
    this.emit({ type: 'reset' });
  }

  private ingest(
    event: ServerSupervisorStreamEvent,
    cursor: SseCursor,
  ): { cursor: SseCursor; pushes: SupervisorEvent[] } {
    if (event.kind === 'stream.reset') {
      return { cursor: { seq: 0, epoch: '' }, pushes: [{ type: 'reset' }] };
    }
    let push: SupervisorEvent | null;
    try {
      const mapped = toRendererEvent(event);
      push = mapped === null ? null : SupervisorEventSchema.parse(mapped);
    } catch {
      this.deps.log('dropped a supervisor stream payload that failed renderer validation');
      return { cursor, pushes: [] };
    }
    let next = { ...cursor };
    const pushes: SupervisorEvent[] = [];
    if (event.stream_epoch !== '') {
      if (next.epoch !== '' && next.epoch !== event.stream_epoch) {
        next = { seq: 0, epoch: '' };
        pushes.push({ type: 'reset' });
      }
      if (next.epoch === '') next.epoch = event.stream_epoch;
    }
    if (push === null) return { cursor: next, pushes };
    if (push.type === 'record') {
      if (push.record.seq <= next.seq) return { cursor: next, pushes };
      next.seq = push.record.seq;
    }
    pushes.push(push);
    return { cursor: next, pushes };
  }

  private emit(event: SupervisorEvent): void {
    const parsed = SupervisorEventSchema.safeParse(event);
    if (!parsed.success) {
      this.deps.log('dropped a supervisor push that violated the renderer event schema');
      return;
    }
    this.deps.onPush(parsed.data);
  }
}
