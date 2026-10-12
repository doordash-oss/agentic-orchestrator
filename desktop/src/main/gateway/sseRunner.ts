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

import { redactText, toCanonicalError } from '../../shared/errors';

export interface SseBlock {
  id: string;
  event: string;
  data: string;
}

export class SseBlockAssembler {
  private id = '';
  private event = '';
  private data: string[] = [];

  push(line: string): SseBlock | null {
    if (line === '') {
      if (this.data.length === 0) {
        this.reset();
        return null;
      }
      const block = { id: this.id, event: this.event, data: this.data.join('\n') };
      this.reset();
      return block;
    }
    const colon = line.indexOf(':');
    if (colon <= 0) return null;
    const name = line.slice(0, colon);
    let value = line.slice(colon + 1);
    if (value.startsWith(' ')) value = value.slice(1);
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

export interface SseStream {
  status: number;
  lines: AsyncIterable<string>;
  close(): void;
}

export interface SseCursor {
  seq: number;
  epoch: string;
}

export const DEFAULT_EVENT_STREAM_BACKOFF = { initialMs: 250, maxMs: 5000 };

interface SseRunnerAdapter<Wire, Push> {
  open(cursor: SseCursor): Promise<SseStream>;
  parse(block: SseBlock): Wire | null;
  ingest(event: Wire, cursor: SseCursor): { cursor: SseCursor; pushes: Push[] };
  status(state: 'connecting' | 'live' | 'stale'): Push | null;
  emit(push: Push): void;
  sleep(ms: number): Promise<void>;
  log(line: string): void;
  label: string;
  dropped: string;
  backoff?: { initialMs: number; maxMs: number };
  onStale?(): void | Promise<void>;
}

/** One generation-fenced reconnect loop for all resumable SSE subscriptions. */
export class SseRunner<Wire, Push> {
  private cursor: SseCursor = { seq: 0, epoch: '' };
  private running = false;
  private generation = 0;
  private current: SseStream | null = null;

  constructor(private readonly adapter: SseRunnerAdapter<Wire, Push>) {}

  getCursor(): SseCursor {
    return { ...this.cursor };
  }

  resetCursor(): void {
    this.cursor = { seq: 0, epoch: '' };
  }

  start(): void {
    if (this.running) return;
    this.running = true;
    const generation = ++this.generation;
    this.status('connecting');
    void this.loop(generation);
  }

  stop(): void {
    if (!this.running) return;
    this.running = false;
    this.generation += 1;
    this.current?.close();
    this.current = null;
  }

  private status(state: 'connecting' | 'live' | 'stale'): void {
    const push = this.adapter.status(state);
    if (push !== null) this.adapter.emit(push);
  }

  private active(generation: number): boolean {
    return this.running && this.generation === generation;
  }

  private async loop(generation: number): Promise<void> {
    const backoff = this.adapter.backoff ?? DEFAULT_EVENT_STREAM_BACKOFF;
    let delay = backoff.initialMs;
    while (this.active(generation)) {
      let stream: SseStream | null = null;
      try {
        stream = await this.adapter.open(this.getCursor());
        if (!this.active(generation)) {
          return;
        }
        this.current = stream;
        if (stream.status !== 200) {
          throw new Error(`${this.adapter.label} answered status ${String(stream.status)}`);
        }
        this.status('live');
        delay = backoff.initialMs;
        const assembler = new SseBlockAssembler();
        for await (const line of stream.lines) {
          if (!this.active(generation)) return;
          const block = assembler.push(line);
          if (block === null) continue;
          const event = this.adapter.parse(block);
          if (event === null) {
            this.adapter.log(this.adapter.dropped);
            continue;
          }
          const decision = this.adapter.ingest(event, this.getCursor());
          this.cursor = decision.cursor;
          for (const push of decision.pushes) this.adapter.emit(push);
        }
      } catch (err) {
        const safe = toCanonicalError(err, 'E_EVENT_STREAM');
        this.adapter.log(
          `${this.adapter.label} attempt failed: ${safe.code}: ${redactText(safe.summary)}`,
        );
      } finally {
        stream?.close();
        if (this.current === stream) this.current = null;
      }
      if (!this.active(generation)) return;
      this.status('stale');
      await this.adapter.onStale?.();
      if (!this.active(generation)) return;
      await this.adapter.sleep(delay);
      delay = Math.min(delay * 2, backoff.maxMs);
    }
  }
}
