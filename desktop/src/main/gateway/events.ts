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
 * Main-process consumption of the server's global SSE invalidation stream
 * (`GET /api/v1/events`), mirroring the Go client's cursor/epoch semantics
 * (internal/server/client_sse.go):
 *
 *  - Every event advances a `(seq, epoch)` cursor; reconnects resume with
 *    `after=<seq>&epoch=<epoch>` so the ring buffer replays missed events.
 *  - `connected` and `stream.reset` (the server's cursor-outside-replay-buffer
 *    signal) force a full resync — the renderer refetches its snapshots.
 *  - An epoch change (server restart) likewise forces a full resync and the
 *    cursor is adopted from the new epoch.
 *  - Duplicate or out-of-order events are ignored by sequence. Forward
 *    sequence gaps are accepted: the broker coalesces events per resource,
 *    so a skipped seq never means a lost invalidation — genuine replay gaps
 *    surface as `stream.reset`.
 *
 * Only invalidation metadata (kind, resource identity) ever leaves this
 * module; summaries, payloads, and credentials never cross to the renderer.
 */
import { z } from 'zod';
import { redactText, toSafeError } from '../../shared/errors';
import { AppEventSchema, type AppEvent } from '../../shared/ipc';
import { assertNoPrototypePollution, assertWithinByteSize } from '../../shared/sanitize';

/** Upper bound for one SSE event payload (invalidations are tiny). */
const MAX_EVENT_BYTES = 64 * 1024;

// --- SSE block assembly (pure) ----------------------------------------------

export interface SseBlock {
  id: string;
  event: string;
  data: string;
}

/**
 * Accumulates `id:`/`event:`/`data:` lines into blocks separated by blank
 * lines — the same framing the Go client's scanSSEBlocks implements.
 */
export class SseBlockAssembler {
  private id = '';
  private event = '';
  private data: string[] = [];

  /** Feeds one line; returns a completed block on the blank separator. */
  push(line: string): SseBlock | null {
    if (line === '') {
      if (this.data.length === 0) {
        this.reset();
        return null;
      }
      const block: SseBlock = { id: this.id, event: this.event, data: this.data.join('\n') };
      this.reset();
      return block;
    }
    const colon = line.indexOf(':');
    if (colon <= 0) {
      return null; // comment line or field-less line — ignored per SSE spec
    }
    const name = line.slice(0, colon);
    let value = line.slice(colon + 1);
    if (value.startsWith(' ')) {
      value = value.slice(1);
    }
    switch (name) {
      case 'id':
        this.id = value;
        break;
      case 'event':
        this.event = value;
        break;
      case 'data':
        this.data.push(value);
        break;
      default:
        break;
    }
    return null;
  }

  private reset(): void {
    this.id = '';
    this.event = '';
    this.data = [];
  }
}

// --- Envelope parsing (fail closed, drop unusable events) --------------------

/** Lenient view of the server SSEEvent envelope: only what convergence needs. */
const SseEventEnvelopeSchema = z.object({
  seq: z.number().int().nonnegative().optional(),
  epoch: z.string().max(200).optional(),
  kind: z.string().max(200).optional(),
  resource: z
    .object({
      type: z.string().max(200).optional(),
      id: z.string().max(500).optional(),
      feature_id: z.string().max(500).optional(),
      parent_id: z.string().max(500).optional(),
      child_id: z.string().max(500).optional(),
      relationship_deleted: z.boolean().optional(),
    })
    .optional(),
  snapshot_required: z.boolean().optional(),
});

export interface SseEventEnvelope {
  seq: number;
  epoch: string;
  kind: string;
  resource?: {
    type?: string;
    id?: string;
    feature_id?: string;
    parent_id?: string;
    child_id?: string;
    relationship_deleted?: boolean;
  };
  snapshot_required: boolean;
}

/**
 * Parses one SSE data payload into the envelope, filling kind/seq from the
 * block's `event:`/`id:` fields when the JSON omits them (as the Go client
 * does). Returns null — the event is dropped — on any malformed, oversized,
 * polluted, or kind-less payload.
 */
export function parseSseEvent(
  data: string,
  block: { id: string; event: string },
): SseEventEnvelope | null {
  try {
    assertWithinByteSize(data, MAX_EVENT_BYTES);
    const raw: unknown = JSON.parse(data);
    assertNoPrototypePollution(raw);
    const parsed = SseEventEnvelopeSchema.safeParse(raw);
    if (!parsed.success) {
      return null;
    }
    const kind = parsed.data.kind ?? block.event;
    if (kind === '') {
      return null;
    }
    let seq = parsed.data.seq ?? 0;
    if (seq === 0 && block.id !== '') {
      const fromId = Number.parseInt(block.id, 10);
      if (Number.isSafeInteger(fromId) && fromId > 0) {
        seq = fromId;
      }
    }
    return {
      seq,
      epoch: parsed.data.epoch ?? '',
      kind,
      ...(parsed.data.resource === undefined ? {} : { resource: parsed.data.resource }),
      snapshot_required: parsed.data.snapshot_required ?? false,
    };
  } catch {
    return null;
  }
}

// --- Cursor tracking / convergence (pure) -------------------------------------

export interface EventCursor {
  seq: number;
  epoch: string;
}

export type IngestDecision =
  | { action: 'ignore' }
  | { action: 'resync' }
  | {
      action: 'invalidate';
      kind: string;
      resourceType?: string;
      resourceId?: string;
      featureId?: string;
      parentId?: string;
      childId?: string;
      relationshipDeleted?: boolean;
    };

const RESYNC_KINDS = new Set(['connected', 'stream.reset']);

/**
 * The pure convergence core: decides, per event, whether to ignore it
 * (duplicate/out-of-order/no-snapshot), surface an invalidation, or force a
 * full resync — while tracking the `(seq, epoch)` reconnect cursor.
 */
export class EventCursorTracker {
  private cursor: EventCursor = { seq: 0, epoch: '' };

  getCursor(): EventCursor {
    return { ...this.cursor };
  }

  /**
   * Drops the tracked cursor: the next subscription starts fresh. Called when
   * the attached server identity changes — replaying another server's
   * (seq, epoch) space would resync or drop events incorrectly. Same-server
   * reconnects never reset, keeping replay-only-missed semantics.
   */
  reset(): void {
    this.cursor = { seq: 0, epoch: '' };
  }

  ingest(event: SseEventEnvelope): IngestDecision {
    // Broker-issued full-resync signals: adopt the cursor they carry.
    if (RESYNC_KINDS.has(event.kind)) {
      this.adopt(event);
      return { action: 'resync' };
    }
    // Epoch change (server restart): everything known is stale.
    if (event.epoch !== '' && this.cursor.epoch !== '' && event.epoch !== this.cursor.epoch) {
      this.cursor = { seq: event.seq, epoch: event.epoch };
      return { action: 'resync' };
    }
    if (event.epoch !== '' && this.cursor.epoch === '') {
      this.cursor.epoch = event.epoch;
    }
    // Duplicate / out-of-order within the same epoch.
    if (event.seq > 0 && event.seq <= this.cursor.seq) {
      return { action: 'ignore' };
    }
    if (event.seq > 0) {
      this.cursor.seq = event.seq;
    }
    if (event.kind === 'heartbeat') {
      // A forced-resync heartbeat mirrors the Go client's full re-snapshot.
      return event.snapshot_required ? { action: 'resync' } : { action: 'ignore' };
    }
    if (!event.snapshot_required) {
      return { action: 'ignore' }; // e.g. session.output.activity liveness
    }
    const resource = event.resource ?? {};
    return {
      action: 'invalidate',
      kind: event.kind,
      ...(resource.type === undefined ? {} : { resourceType: resource.type }),
      ...(resource.id === undefined ? {} : { resourceId: resource.id }),
      ...(resource.feature_id === undefined ? {} : { featureId: resource.feature_id }),
      ...(resource.parent_id === undefined ? {} : { parentId: resource.parent_id }),
      ...(resource.child_id === undefined ? {} : { childId: resource.child_id }),
      ...(resource.relationship_deleted === undefined
        ? {}
        : { relationshipDeleted: resource.relationship_deleted }),
    };
  }

  private adopt(event: SseEventEnvelope): void {
    if (event.seq > 0) {
      this.cursor.seq = event.seq;
    }
    if (event.epoch !== '') {
      this.cursor.epoch = event.epoch;
    }
  }
}

// --- Supervisor (reconnect loop with capped backoff) ---------------------------

/** One open SSE response: HTTP status plus a bounded line iterator. */
export interface SseStream {
  status: number;
  lines: AsyncIterable<string>;
  close(): void;
}

/** The narrow surface the supervisor needs from the runtime gateway. */
export interface EventStreamSource {
  openEventStream(options: { afterSeq?: number; epoch?: string }): Promise<SseStream>;
}

export interface EventSupervisorDeps {
  source: EventStreamSource;
  sleep(ms: number): Promise<void>;
  /** Redacted local diagnostics sink. */
  log(line: string): void;
  /** Receives schema-valid pushes destined for the renderer. */
  onPush(event: AppEvent): void;
  /** Lets the gateway verify whether a stale stream means the server was lost. */
  onStale?(): void | Promise<void>;
  backoff?: { initialMs: number; maxMs: number };
}

const DEFAULT_BACKOFF = { initialMs: 250, maxMs: 5000 };

/**
 * Owns the reconnect loop while the gateway is ready: consumes the stream,
 * feeds the tracker, and pushes invalidation/status events. Stopped whenever
 * the connection leaves the ready state; the cursor survives stop/start so a
 * resumed subscription replays only what was missed.
 */
export class EventStreamSupervisor {
  private readonly tracker = new EventCursorTracker();
  private readonly backoff: { initialMs: number; maxMs: number };
  private running = false;
  private generation = 0;
  private current: SseStream | null = null;

  constructor(private readonly deps: EventSupervisorDeps) {
    this.backoff = deps.backoff ?? DEFAULT_BACKOFF;
  }

  /** Idempotent; resumes from the tracked cursor. */
  start(): void {
    if (this.running) {
      return;
    }
    this.running = true;
    const generation = ++this.generation;
    this.emit({ type: 'status', stream: 'connecting' });
    void this.loop(generation);
  }

  stop(): void {
    if (!this.running) {
      return;
    }
    this.running = false;
    this.generation += 1;
    this.current?.close();
    this.current = null;
  }

  /**
   * Resets the replay cursor for a new server identity. Safe while running:
   * the next loop iteration re-opens the stream from the cleared cursor.
   */
  resetCursor(): void {
    this.tracker.reset();
  }

  private async loop(generation: number): Promise<void> {
    let delay = this.backoff.initialMs;
    while (this.active(generation)) {
      try {
        const cursor = this.tracker.getCursor();
        const stream = await this.deps.source.openEventStream(
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
          throw new Error(`event stream answered status ${String(stream.status)}`);
        }
        this.emit({ type: 'status', stream: 'live' });
        delay = this.backoff.initialMs;
        const assembler = new SseBlockAssembler();
        for await (const line of stream.lines) {
          if (!this.active(generation)) {
            return;
          }
          const block = assembler.push(line);
          if (block !== null) {
            this.handleBlock(block);
          }
        }
      } catch (err) {
        const safe = toSafeError(err, 'E_EVENT_STREAM');
        this.deps.log(`event stream attempt failed: ${safe.code}: ${redactText(safe.message)}`);
      } finally {
        this.current?.close();
        this.current = null;
      }
      if (!this.active(generation)) {
        return;
      }
      this.emit({ type: 'status', stream: 'stale' });
      await this.deps.onStale?.();
      if (!this.active(generation)) {
        return;
      }
      await this.deps.sleep(delay);
      delay = Math.min(delay * 2, this.backoff.maxMs);
    }
  }

  private handleBlock(block: SseBlock): void {
    const event = parseSseEvent(block.data, block);
    if (event === null) {
      this.deps.log('dropped an unusable event payload from the stream');
      return;
    }
    const decision = this.tracker.ingest(event);
    if (decision.action === 'resync') {
      this.emit({ type: 'invalidated', kind: 'resync' });
    } else if (decision.action === 'invalidate') {
      this.emit({
        type: 'invalidated',
        kind: decision.kind,
        ...(decision.resourceType === undefined ? {} : { resourceType: decision.resourceType }),
        ...(decision.resourceId === undefined ? {} : { resourceId: decision.resourceId }),
        ...(decision.featureId === undefined ? {} : { featureId: decision.featureId }),
        ...(decision.parentId === undefined ? {} : { parentId: decision.parentId }),
        ...(decision.childId === undefined ? {} : { childId: decision.childId }),
        ...(decision.relationshipDeleted === undefined
          ? {}
          : { relationshipDeleted: decision.relationshipDeleted }),
      });
    }
  }

  /** Defense in depth: only schema-valid pushes ever reach the renderer. */
  private emit(event: AppEvent): void {
    const parsed = AppEventSchema.safeParse(event);
    if (!parsed.success) {
      this.deps.log('dropped an event push that violated the renderer event schema');
      return;
    }
    this.deps.onPush(parsed.data);
  }

  private active(generation: number): boolean {
    return this.running && generation === this.generation;
  }
}
