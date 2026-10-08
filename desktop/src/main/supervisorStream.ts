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
import { redactText, toCanonicalError } from '../shared/errors';
import {
  ServerSupervisorStreamEventSchema,
  type ServerSupervisorStreamEvent,
} from '../shared/api/parse';
import {
  SupervisorEventSchema,
  SupervisorStreamEpochSchema,
  type SupervisorEvent,
} from '../shared/ipc';
import {
  DEFAULT_EVENT_STREAM_BACKOFF,
  SseBlockAssembler,
  decodeSsePayload,
  type SseBlock,
  type SseStream,
} from './gateway/events';
import { supervisorPendingRequest } from './attention';
import { toSupervisorRecord, toSupervisorState } from './supervisor';

/**
 * Upper bound for one supervisor event payload. Records carry transcript
 * rows, so this matches the SSE line bound rather than the tiny global
 * invalidation bound.
 */
export const MAX_SUPERVISOR_EVENT_BYTES = 1024 * 1024;

/** The narrow surface the runner needs from the runtime gateway. */
export interface SupervisorStreamSource {
  openSupervisorEventStream(options: { afterSeq?: number; epoch?: string }): Promise<SseStream>;
}

export interface SupervisorStreamDeps {
  source: SupervisorStreamSource;
  sleep(ms: number): Promise<void>;
  /** Redacted local diagnostics sink. */
  log(line: string): void;
  /** Receives schema-valid pushes destined for the renderer. */
  onPush(event: SupervisorEvent): void;
  backoff?: { initialMs: number; maxMs: number };
}

export interface SupervisorCursor {
  seq: number;
  epoch: string;
}

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

/**
 * Owns the supervisor stream's reconnect loop while the gateway is ready.
 * Stopped whenever the connection leaves the ready state; the cursor
 * survives stop/start so a resumed subscription replays only what was
 * missed, and is dropped on a server identity change or a stream reset.
 */
export class SupervisorStreamRunner {
  private cursor: SupervisorCursor = { seq: 0, epoch: '' };
  private readonly backoff: { initialMs: number; maxMs: number };
  private running = false;
  private generation = 0;
  private current: SseStream | null = null;

  constructor(private readonly deps: SupervisorStreamDeps) {
    this.backoff = deps.backoff ?? DEFAULT_EVENT_STREAM_BACKOFF;
  }

  getCursor(): SupervisorCursor {
    return { ...this.cursor };
  }

  /** Idempotent; resumes from the tracked cursor. */
  start(): void {
    if (this.running) return;
    this.running = true;
    const generation = ++this.generation;
    void this.loop(generation);
  }

  stop(): void {
    if (!this.running) return;
    this.running = false;
    this.generation += 1;
    this.current?.close();
    this.current = null;
  }

  /**
   * Drops the resume cursor for a new server identity — replaying another
   * server's (seq, epoch) space would skip or duplicate records — and tells
   * the renderer to re-snapshot. Safe while running: the next attempt opens
   * from the cleared cursor.
   */
  resetCursor(): void {
    this.dropCursor();
  }

  private dropCursor(): void {
    this.cursor = { seq: 0, epoch: '' };
    this.emit({ type: 'reset' });
  }

  private async loop(generation: number): Promise<void> {
    let delay = this.backoff.initialMs;
    while (this.active(generation)) {
      // The attempt's own stream: a late finally from a stopped loop must
      // never close the stream a newer start() has since opened.
      let stream: SseStream | null = null;
      try {
        const cursor = this.getCursor();
        stream = await this.deps.source.openSupervisorEventStream(
          cursor.seq > 0
            ? { afterSeq: cursor.seq, ...(cursor.epoch === '' ? {} : { epoch: cursor.epoch }) }
            : {},
        );
        if (!this.active(generation)) {
          stream.close();
          return;
        }
        this.current = stream;
        if (stream.status !== 200) {
          throw new Error(`supervisor stream answered status ${String(stream.status)}`);
        }
        this.emit({ type: 'stream-status', status: 'live' });
        delay = this.backoff.initialMs;
        const assembler = new SseBlockAssembler();
        for await (const line of stream.lines) {
          if (!this.active(generation)) return;
          const block = assembler.push(line);
          if (block !== null) this.handleBlock(block);
        }
      } catch (err) {
        const safe = toCanonicalError(err, 'E_EVENT_STREAM');
        this.deps.log(
          `supervisor stream attempt failed: ${safe.code}: ${redactText(safe.summary)}`,
        );
      } finally {
        stream?.close();
        if (this.current === stream) this.current = null;
      }
      if (!this.active(generation)) return;
      this.emit({ type: 'stream-status', status: 'stale' });
      await this.deps.sleep(delay);
      delay = Math.min(delay * 2, this.backoff.maxMs);
    }
  }

  private handleBlock(block: SseBlock): void {
    const event = parseSupervisorStreamEvent(block);
    if (event === null) {
      this.deps.log('dropped an unusable supervisor stream payload');
      return;
    }
    if (event.kind === 'stream.reset') {
      this.dropCursor();
      return;
    }
    let push: SupervisorEvent | null;
    try {
      const mapped = toRendererEvent(event);
      push = mapped === null ? null : SupervisorEventSchema.parse(mapped);
    } catch {
      // Validated before the cursor moves: a rejected event never resets or advances it.
      this.deps.log('dropped a supervisor stream payload that failed renderer validation');
      return;
    }
    // A new epoch (server restart) invalidates everything known.
    if (event.stream_epoch !== '') {
      if (this.cursor.epoch !== '' && this.cursor.epoch !== event.stream_epoch) {
        this.dropCursor();
      }
      if (this.cursor.epoch === '') this.cursor.epoch = event.stream_epoch;
    }
    if (push === null) return; // heartbeat
    if (push.type === 'record') {
      if (push.record.seq <= this.cursor.seq) return; // replayed duplicate
      this.cursor.seq = push.record.seq;
    }
    this.emit(push);
  }

  /** Defense in depth: only schema-valid pushes ever reach the renderer. */
  private emit(event: SupervisorEvent): void {
    const parsed = SupervisorEventSchema.safeParse(event);
    if (!parsed.success) {
      this.deps.log('dropped a supervisor push that violated the renderer event schema');
      return;
    }
    this.deps.onPush(parsed.data);
  }

  private active(generation: number): boolean {
    return this.running && generation === this.generation;
  }
}
