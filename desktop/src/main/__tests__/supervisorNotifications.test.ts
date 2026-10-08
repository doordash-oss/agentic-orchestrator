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

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type {
  AttentionSnapshot,
  SupervisorEvent,
  SupervisorLifecycle,
  SupervisorRecord,
  SupervisorState,
  SupervisorTurnOutcome,
} from '../../shared/ipc';
import { AttentionNotificationCoordinator, type NotificationHandle } from '../notifications';
import {
  forwardSupervisorEvent,
  SUPERVISOR_TURN_SETTLE_MS,
  SupervisorNotificationCoordinator,
} from '../supervisorNotifications';

const ENVELOPE = { conversationId: 'conv-1', generation: 1, streamEpoch: 'epoch-1' };

function state(
  lifecycle: SupervisorLifecycle,
  lastTurnOutcome: SupervisorTurnOutcome = 'none',
  overrides: Partial<SupervisorState> = {},
): SupervisorEvent {
  const full: SupervisorState = {
    conversationId: 'conv-1',
    generation: 1,
    sessionId: '__supervisor__.conv-1.1',
    lifecycle,
    lastTurnOutcome,
    interruptedBy: 'none',
    settings: { harness: 'claude', model: 'sonnet', effort: '' },
    effectiveModel: 'sonnet',
    permissionMode: { requested: 'default', effective: 'default', restrictedByPolicy: false },
    pendingRequests: [],
    contextUsage: null,
    headSeq: 0,
    streamEpoch: 'epoch-1',
    ...overrides,
  };
  return {
    type: 'state',
    conversationId: full.conversationId,
    generation: full.generation,
    streamEpoch: 'epoch-1',
    state: full,
  };
}

function assistant(
  seq: number,
  text: string,
  overrides: Partial<SupervisorRecord> = {},
): SupervisorEvent {
  return {
    type: 'record',
    ...ENVELOPE,
    record: {
      seq,
      id: `rec-${String(seq)}`,
      conversationId: 'conv-1',
      generation: 1,
      turnId: 'turn-1',
      kind: 'assistant',
      visibility: 'content',
      createdAt: '2026-10-07T10:00:00Z',
      messages: [{ index: seq, role: 'assistant', type: 'text', text }],
      ...overrides,
    },
  };
}

function setup(options: { previewEnabled?: boolean; shouldNotify?: () => boolean } = {}) {
  const handles: Array<{ click?: () => void; show: () => void }> = [];
  const create = vi.fn((_options: { title: string; body: string }) => {
    const entry: { click?: () => void; show: () => void } = { show: vi.fn<() => void>() };
    const handle: NotificationHandle = {
      show: entry.show,
      on: (_event, listener) => {
        entry.click = listener;
      },
    };
    handles.push(entry);
    return handle;
  });
  const show = vi.fn();
  const route = vi.fn();
  const coordinator = new SupervisorNotificationCoordinator({
    sink: { isSupported: () => true, create },
    shouldNotify: options.shouldNotify ?? (() => true),
    previewEnabled: () => options.previewEnabled ?? false,
    show,
    route,
  });
  const bodies = () => create.mock.calls.map(([call]) => call.body);
  return { coordinator, create, handles, show, route, bodies };
}

/** Drives one turn to a completed idle: running, the reply, then idle. */
function completeTurn(
  coordinator: SupervisorNotificationCoordinator,
  reply = 'Done: the plan is ready.',
): void {
  coordinator.handle(state('idle', 'none'));
  coordinator.handle(state('running', 'none'));
  coordinator.handle(assistant(2, reply));
  coordinator.handle(state('idle', 'completed', { headSeq: 2 }));
}

describe('SupervisorNotificationCoordinator', () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it('notifies once, 10 seconds after a completed turn, with the generic body when previews are off', () => {
    const { coordinator, create, handles, bodies } = setup();
    completeTurn(coordinator);

    vi.advanceTimersByTime(SUPERVISOR_TURN_SETTLE_MS - 1);
    expect(create).not.toHaveBeenCalled();
    vi.advanceTimersByTime(1);
    expect(bodies()).toEqual(['Supervisor finished a turn.']);
    expect(create.mock.calls[0]?.[0].title).toBe('Agentico');
    expect(handles[0]?.show).toHaveBeenCalledOnce();

    vi.advanceTimersByTime(SUPERVISOR_TURN_SETTLE_MS * 3);
    expect(create).toHaveBeenCalledOnce();
  });

  it('previews the redacted first line of the newest reply, capped at 180 characters', () => {
    const { coordinator, bodies } = setup({ previewEnabled: true });
    coordinator.handle(state('running'));
    coordinator.handle(assistant(2, 'An older reply in the same turn.'));
    coordinator.handle(
      assistant(3, '  Read /Users/alice/repo/notes.md and finished.\nSecond line stays out.'),
    );
    coordinator.handle(state('idle', 'completed', { headSeq: 3 }));
    vi.advanceTimersByTime(SUPERVISOR_TURN_SETTLE_MS);
    expect(bodies()).toEqual(['Supervisor · Read [path] and finished.']);
  });

  it('caps a long preview line at 180 characters', () => {
    const { coordinator, bodies } = setup({ previewEnabled: true });
    coordinator.handle(state('running'));
    coordinator.handle(assistant(2, 'x'.repeat(400)));
    coordinator.handle(state('idle', 'completed', { headSeq: 2 }));
    vi.advanceTimersByTime(SUPERVISOR_TURN_SETTLE_MS);
    expect(bodies()).toEqual([`Supervisor · ${'x'.repeat(180)}`]);
  });

  it('falls back to the generic body when the turn has no reply text', () => {
    const { coordinator, bodies } = setup({ previewEnabled: true });
    coordinator.handle(state('running'));
    coordinator.handle(state('idle', 'completed', { headSeq: 1 }));
    vi.advanceTimersByTime(SUPERVISOR_TURN_SETTLE_MS);
    expect(bodies()).toEqual(['Supervisor finished a turn.']);
  });

  it('treats a turn ending from a waiting state as finished but not a relaunch settling', () => {
    const waiting = setup();
    waiting.coordinator.handle(state('waiting_question'));
    waiting.coordinator.handle(state('idle', 'completed', { headSeq: 2 }));
    vi.advanceTimersByTime(SUPERVISOR_TURN_SETTLE_MS);
    expect(waiting.create).toHaveBeenCalledOnce();

    const relaunch = setup();
    relaunch.coordinator.handle(state('starting'));
    relaunch.coordinator.handle(state('idle', 'completed', { headSeq: 2 }));
    vi.advanceTimersByTime(SUPERVISOR_TURN_SETTLE_MS);
    expect(relaunch.create).not.toHaveBeenCalled();
  });

  it('drops the notification when the window becomes focused within the settle', () => {
    const { coordinator, create } = setup();
    completeTurn(coordinator);
    vi.advanceTimersByTime(5_000);
    coordinator.focusChanged(true);
    vi.advanceTimersByTime(SUPERVISOR_TURN_SETTLE_MS);
    expect(create).not.toHaveBeenCalled();
  });

  it('ignores an unfocused publish while armed', () => {
    const { coordinator, create } = setup();
    completeTurn(coordinator);
    coordinator.focusChanged(false);
    vi.advanceTimersByTime(SUPERVISOR_TURN_SETTLE_MS);
    expect(create).toHaveBeenCalledOnce();
  });

  it('drops the notification when a new turn starts within the settle', () => {
    const { coordinator, create } = setup();
    completeTurn(coordinator);
    coordinator.handle(state('running', 'completed', { headSeq: 3 }));
    vi.advanceTimersByTime(SUPERVISOR_TURN_SETTLE_MS);
    expect(create).not.toHaveBeenCalled();
  });

  it('drops the notification when the process stops within the settle', () => {
    const { coordinator, create } = setup();
    completeTurn(coordinator);
    coordinator.handle(state('stopped', 'completed', { headSeq: 2 }));
    vi.advanceTimersByTime(SUPERVISOR_TURN_SETTLE_MS);
    expect(create).not.toHaveBeenCalled();
  });

  it('drops the notification when the stream resets within the settle', () => {
    const { coordinator, create } = setup();
    completeTurn(coordinator);
    coordinator.handle({ type: 'reset' });
    vi.advanceTimersByTime(SUPERVISOR_TURN_SETTLE_MS);
    expect(create).not.toHaveBeenCalled();
  });

  it('drops the notification when the server connection changes within the settle', () => {
    const { coordinator, create } = setup();
    completeTurn(coordinator);
    coordinator.reset();
    vi.advanceTimersByTime(SUPERVISOR_TURN_SETTLE_MS);
    expect(create).not.toHaveBeenCalled();
    // A completed idle replayed by the new server is not a transition.
    coordinator.handle(state('idle', 'completed', { headSeq: 2 }));
    vi.advanceTimersByTime(SUPERVISOR_TURN_SETTLE_MS);
    expect(create).not.toHaveBeenCalled();
  });

  it.each<SupervisorTurnOutcome>(['interrupted', 'failed'])(
    'never arms for a %s outcome',
    (outcome) => {
      const { coordinator, create } = setup();
      coordinator.handle(state('running'));
      coordinator.handle(assistant(2, 'Partial reply'));
      coordinator.handle(state('idle', outcome, { headSeq: 2 }));
      vi.advanceTimersByTime(SUPERVISOR_TURN_SETTLE_MS * 2);
      expect(create).not.toHaveBeenCalled();
    },
  );

  it('does not re-arm on a second idle state for the same turn', () => {
    const { coordinator, create } = setup();
    completeTurn(coordinator);
    vi.advanceTimersByTime(4_000);
    coordinator.handle(state('idle', 'completed', { headSeq: 2 }));
    vi.advanceTimersByTime(SUPERVISOR_TURN_SETTLE_MS - 4_000);
    expect(create).toHaveBeenCalledOnce();
    coordinator.handle(state('idle', 'completed', { headSeq: 2 }));
    vi.advanceTimersByTime(SUPERVISOR_TURN_SETTLE_MS * 2);
    expect(create).toHaveBeenCalledOnce();
  });

  it('notifies each later turn once', () => {
    const { coordinator, create } = setup();
    completeTurn(coordinator);
    vi.advanceTimersByTime(SUPERVISOR_TURN_SETTLE_MS);
    coordinator.handle(state('running', 'completed', { headSeq: 2 }));
    coordinator.handle(assistant(4, 'Second turn'));
    coordinator.handle(state('idle', 'completed', { headSeq: 4 }));
    vi.advanceTimersByTime(SUPERVISOR_TURN_SETTLE_MS);
    expect(create).toHaveBeenCalledTimes(2);
  });

  it('keys the settle by conversation and generation', () => {
    const { coordinator, create } = setup();
    completeTurn(coordinator);
    // A new generation idling (after a relaunch) never fires the old turn.
    coordinator.handle(state('idle', 'none', { generation: 2, headSeq: 2 }));
    vi.advanceTimersByTime(SUPERVISOR_TURN_SETTLE_MS);
    expect(create).not.toHaveBeenCalled();
  });

  it('stays silent when the focus gate refuses at fire time', () => {
    let unfocused = true;
    const { coordinator, create } = setup({ shouldNotify: () => unfocused });
    completeTurn(coordinator);
    unfocused = false;
    vi.advanceTimersByTime(SUPERVISOR_TURN_SETTLE_MS);
    expect(create).not.toHaveBeenCalled();
  });

  it('shows the window and routes home when the notification is clicked', () => {
    const { coordinator, handles, show, route } = setup();
    completeTurn(coordinator);
    vi.advanceTimersByTime(SUPERVISOR_TURN_SETTLE_MS);
    expect(show).not.toHaveBeenCalled();
    handles[0]?.click?.();
    expect(show).toHaveBeenCalledOnce();
    expect(route).toHaveBeenCalledWith({ target: 'home' });
  });
});

describe('forwardSupervisorEvent', () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it('notifies a supervisor request immediately through the attention path, with no settle', () => {
    const notification: NotificationHandle = { show: vi.fn(), on: vi.fn() };
    const create = vi.fn((_options: { title: string; body: string }) => notification);
    const sink = { isSupported: () => true, create };
    const attention = new AttentionNotificationCoordinator({
      sink,
      shouldNotify: () => true,
      show: vi.fn(),
    });
    const turnEnd = new SupervisorNotificationCoordinator({
      sink,
      shouldNotify: () => true,
      previewEnabled: () => true,
      show: vi.fn(),
      route: vi.fn(),
    });
    const request: SupervisorEvent = {
      type: 'request',
      ...ENVELOPE,
      request: {
        kind: 'permission',
        id: 'perm-sup',
        target: 'supervisor',
        sessionId: '__supervisor__.conv-1.1',
        toolName: 'Bash',
        waitingSince: '2026-10-07T10:00:00.000Z',
      },
    };
    // The attention refresh re-reads the snapshot, which now holds the request.
    const refreshAttention = vi.fn(() => {
      const snapshot: AttentionSnapshot = {
        items: request.type === 'request' ? [request.request] : [],
      };
      attention.update(snapshot, { previewEnabled: true, featureLabel: () => 'unused' });
    });

    forwardSupervisorEvent(request, { turnEnd, refreshAttention });

    expect(refreshAttention).toHaveBeenCalledOnce();
    expect(create.mock.calls.map(([options]) => options.body)).toEqual([
      'Permission · Supervisor · Bash',
    ]);
    expect(vi.getTimerCount()).toBe(0);
  });

  it('feeds state events to both paths and records only to the turn-end path', () => {
    const turnEnd = { handle: vi.fn() };
    const refreshAttention = vi.fn();
    forwardSupervisorEvent(state('running'), { turnEnd, refreshAttention });
    forwardSupervisorEvent(assistant(2, 'reply'), { turnEnd, refreshAttention });
    expect(turnEnd.handle).toHaveBeenCalledTimes(2);
    expect(refreshAttention).toHaveBeenCalledOnce();
  });
});
