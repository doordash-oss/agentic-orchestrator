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

import { redactText } from '../shared/errors';
import type {
  AppRouteEvent,
  SupervisorEvent,
  SupervisorLifecycle,
  SupervisorRecord,
  SupervisorState,
} from '../shared/ipc';
import type { NotificationSink } from './notifications';

/** How long a completed turn must stay idle before it notifies. */
export const SUPERVISOR_TURN_SETTLE_MS = 10_000;
/** Upper bound for the reply line a preview carries. */
const PREVIEW_LINE_MAX = 180;
const GENERIC_BODY = 'Supervisor finished a turn.';

export interface SupervisorNotificationCoordinatorDeps {
  /** The attention coordinator's sink. */
  sink: NotificationSink;
  /** The attention coordinator's focus gate; re-read when the settle fires. */
  shouldNotify(): boolean;
  /** The notification settings' `previewEnabled`, read when the settle fires. */
  previewEnabled(): boolean;
  /** Shows the main window. */
  show(): void;
  /** Window-aware route dispatch. */
  route(event: AppRouteEvent): void;
}

interface ArmedTurn {
  /** `conversation.generation` the settle belongs to. */
  generation: string;
  timer: ReturnType<typeof setTimeout>;
}

/** Lifecycles inside a turn: leaving one for a completed `idle` ends the turn. */
function inTurn(lifecycle: SupervisorLifecycle): boolean {
  return (
    lifecycle === 'running' ||
    lifecycle === 'waiting_permission' ||
    lifecycle === 'waiting_question'
  );
}

function generationKey(conversationId: string, generation: number): string {
  return `${conversationId}.${String(generation)}`;
}

/**
 * Turn-end native notification. A turn that returns the lifecycle from an
 * in-turn state to `idle` with outcome `completed` arms a settle timer keyed
 * by conversation and generation; the notification shows only when the
 * settle elapses with the lifecycle still idle, the stream unbroken, the
 * server unchanged and the focus gate still permitting. Interrupted and
 * failed turns never arm. Permission and question requests stay on the
 * attention coordinator's immediate path.
 */
export class SupervisorNotificationCoordinator {
  private previous: SupervisorLifecycle | null = null;
  private armed: ArmedTurn | null = null;
  /** `generation.headSeq` of the last turn armed, so a repeated idle never re-arms. */
  private lastTurn: string | null = null;
  /** Newest visible assistant reply, keyed by its generation. */
  private reply: { generation: string; text: string } | null = null;

  constructor(private readonly deps: SupervisorNotificationCoordinatorDeps) {}

  handle(event: SupervisorEvent): void {
    switch (event.type) {
      case 'record':
        this.record(event.record);
        return;
      case 'state':
        this.state(event.state);
        return;
      case 'reset':
        this.reset();
        return;
      case 'delta':
      case 'request':
      case 'stream-status':
        return;
      default: {
        const exhaustive: never = event;
        return exhaustive;
      }
    }
  }

  /** Main-window effective focus changes; focusing drops the settle. */
  focusChanged(focused: boolean): void {
    if (focused) this.cancel();
  }

  /**
   * Stream reset, server connection change or packaged-test reset: drops
   * the settle and forgets the lifecycle, so a replayed completed idle is
   * never read as a fresh transition.
   */
  reset(): void {
    this.cancel();
    this.previous = null;
    this.reply = null;
  }

  private record(record: SupervisorRecord): void {
    const generation = generationKey(record.conversationId, record.generation);
    if (record.kind === 'user') {
      this.reply = null;
      return;
    }
    if (record.kind !== 'assistant' || record.visibility === 'model_only') return;
    const text = record.messages
      .filter((message) => message.role === 'assistant' && message.text !== undefined)
      .map((message) => message.text ?? '')
      .join('\n');
    if (text.trim() === '') return;
    this.reply = { generation, text };
  }

  private state(state: SupervisorState): void {
    const generation = generationKey(state.conversationId, state.generation);
    const previous = this.previous;
    this.previous = state.lifecycle;
    if (
      this.armed !== null &&
      (this.armed.generation !== generation || state.lifecycle !== 'idle')
    ) {
      this.cancel();
    }
    if (
      previous === null ||
      !inTurn(previous) ||
      state.lifecycle !== 'idle' ||
      state.lastTurnOutcome !== 'completed'
    ) {
      return;
    }
    const turn = `${generation}.${String(state.headSeq)}`;
    if (this.lastTurn === turn) return;
    this.lastTurn = turn;
    this.cancel();
    const timer = setTimeout(() => {
      this.armed = null;
      this.fire(generation);
    }, SUPERVISOR_TURN_SETTLE_MS);
    this.armed = { generation, timer };
  }

  private fire(generation: string): void {
    if (!this.deps.shouldNotify() || !this.deps.sink.isSupported()) return;
    const reply = this.reply?.generation === generation ? this.reply.text : '';
    const notification = this.deps.sink.create({
      title: 'Agentico',
      body: this.deps.previewEnabled() ? previewBody(reply) : GENERIC_BODY,
    });
    notification.on('click', () => {
      this.deps.show();
      this.deps.route({ target: 'home' });
    });
    notification.show();
  }

  private cancel(): void {
    if (this.armed === null) return;
    clearTimeout(this.armed.timer);
    this.armed = null;
  }
}

/** "Supervisor · " and the redacted first line of the reply, capped. */
function previewBody(reply: string): string {
  const line =
    reply
      .split('\n')
      .map((candidate) => candidate.trim())
      .find((candidate) => candidate !== '') ?? '';
  const redacted = redactText(line).slice(0, PREVIEW_LINE_MAX);
  return redacted === '' ? GENERIC_BODY : `Supervisor · ${redacted}`;
}

/**
 * The main process's notification fan-out for one supervisor stream event:
 * the turn-end coordinator sees every event, and `request` and `state`
 * events re-read the attention snapshot, so supervisor permissions and
 * questions notify immediately through the attention path.
 */
export function forwardSupervisorEvent(
  event: SupervisorEvent,
  deps: {
    turnEnd: Pick<SupervisorNotificationCoordinator, 'handle'>;
    refreshAttention(): void;
  },
): void {
  deps.turnEnd.handle(event);
  if (event.type === 'request' || event.type === 'state') deps.refreshAttention();
}
