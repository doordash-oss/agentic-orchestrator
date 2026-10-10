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
import { SseEventEnvelopeSchema, type SseEventEnvelope } from '../../shared/api/parse';
import { AppEventSchema, type AppEvent } from '../../shared/ipc';
import { assertNoPrototypePollution, assertWithinByteSize } from '../../shared/sanitize';
import { SseRunner, type SseCursor, type SseStream } from './sseRunner';
import { defined, definedNonEmpty } from '../../shared/defined';
export { SseBlockAssembler, DEFAULT_EVENT_STREAM_BACKOFF } from './sseRunner';
export type { SseBlock, SseStream } from './sseRunner';

/** Upper bound for one SSE event payload (invalidations are tiny). */
const MAX_EVENT_BYTES = 64 * 1024;

// --- Envelope parsing (fail closed, drop unusable events) --------------------

/** Lenient view of the server SSEEvent envelope: only what convergence needs. */
/**
 * The shared fail-closed decode of one SSE `data:` payload: byte bound,
 * JSON parse, and prototype-pollution scan. Returns undefined — the caller
 * drops the event — on any oversized, malformed, or polluted payload.
 */
export function decodeSsePayload(data: string, maxBytes: number): unknown {
  try {
    assertWithinByteSize(data, maxBytes);
    const raw: unknown = JSON.parse(data);
    assertNoPrototypePollution(raw);
    return raw;
  } catch {
    return undefined;
  }
}

/**
 * Parses one SSE data payload, filling seq from the block id when omitted.
 * Malformed, oversized, or polluted payloads are dropped.
 */
export function parseSseEvent(
  data: string,
  block: { id: string; event: string },
): SseEventEnvelope | null {
  try {
    const raw = decodeSsePayload(data, MAX_EVENT_BYTES);
    if (raw === undefined) {
      return null;
    }
    const parsed = SseEventEnvelopeSchema.safeParse(raw);
    if (!parsed.success) {
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
      kind: parsed.data.kind,
      resource: parsed.data.resource,
      snapshot_required: parsed.data.snapshot_required,
    };
  } catch {
    return null;
  }
}

// --- Event-specific convergence ---------------------------------------------

type EventCursor = SseCursor;
type IngestDecision =
  | { action: 'ignore' }
  | { action: 'resync' }
  | { action: 'invalidate'; event: Extract<AppEvent, { type: 'invalidated' }> };

const RESYNC_KINDS = new Set(['connected', 'stream.reset']);

/** Pure adapter rule; the runner stores the returned cursor. */
export function ingestEvent(
  event: SseEventEnvelope,
  cursor: EventCursor,
): { cursor: EventCursor; decision: IngestDecision } {
  const next = { ...cursor };
  if (RESYNC_KINDS.has(event.kind)) {
    if (event.seq !== undefined && event.seq > 0) next.seq = event.seq;
    if (event.epoch !== undefined && event.epoch !== '') next.epoch = event.epoch;
    return { cursor: next, decision: { action: 'resync' } };
  }
  const seq = event.seq ?? 0;
  const epoch = event.epoch ?? '';
  if (epoch !== '' && next.epoch !== '' && epoch !== next.epoch) {
    return { cursor: { seq, epoch }, decision: { action: 'resync' } };
  }
  if (epoch !== '' && next.epoch === '') next.epoch = epoch;
  if (seq > 0 && seq <= next.seq) return { cursor: next, decision: { action: 'ignore' } };
  if (seq > 0) next.seq = seq;
  if (event.kind === 'heartbeat') {
    return { cursor: next, decision: { action: event.snapshot_required ? 'resync' : 'ignore' } };
  }
  if (!event.snapshot_required) return { cursor: next, decision: { action: 'ignore' } };
  const resource = event.resource;
  return {
    cursor: next,
    decision: {
      action: 'invalidate',
      event: {
        type: 'invalidated',
        kind: event.kind,
        resourceType: resource.type,
        ...defined('resourceId', resource.id),
        ...defined('featureId', resource.feature_id),
        ...defined('parentId', resource.parent_id),
        ...defined('childId', resource.child_id),
        ...defined('relationshipDeleted', resource.relationship_deleted),
      },
    },
  };
}

/** The narrow surface the supervisor needs from the runtime gateway. */
export interface EventStreamSource {
  openEventStream(options: { afterSeq?: number; epoch?: string }): Promise<SseStream>;
}

interface EventSupervisorDeps {
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

/** Global event adapter: the shared runner owns reconnect and cursor storage. */
export class EventStreamSupervisor {
  private readonly runner: SseRunner<SseEventEnvelope, AppEvent>;

  constructor(private readonly deps: EventSupervisorDeps) {
    this.runner = new SseRunner<SseEventEnvelope, AppEvent>({
      open: (cursor) =>
        deps.source.openEventStream(
          cursor.seq > 0 ? { afterSeq: cursor.seq, ...definedNonEmpty('epoch', cursor.epoch) } : {},
        ),
      parse: (block) => parseSseEvent(block.data, block),
      ingest: (event, cursor) => {
        const result = ingestEvent(event, cursor);
        return {
          cursor: result.cursor,
          pushes:
            result.decision.action === 'ignore'
              ? []
              : result.decision.action === 'resync'
                ? [{ type: 'invalidated', kind: 'resync' }]
                : [result.decision.event],
        };
      },
      status: (state) => ({ type: 'status', stream: state }),
      emit: (push) => {
        const parsed = AppEventSchema.safeParse(push);
        if (!parsed.success) {
          deps.log('dropped an event push that violated the renderer event schema');
          return;
        }
        deps.onPush(parsed.data);
      },
      sleep: deps.sleep,
      log: deps.log,
      label: 'event stream',
      dropped: 'dropped an unusable event payload from the stream',
      ...defined('backoff', deps.backoff),
      ...defined('onStale', deps.onStale),
    });
  }

  start(): void {
    this.runner.start();
  }
  stop(): void {
    this.runner.stop();
  }
  resetCursor(): void {
    this.runner.resetCursor();
  }
}
