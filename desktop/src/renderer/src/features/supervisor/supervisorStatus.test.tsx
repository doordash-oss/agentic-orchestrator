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

import { act, cleanup, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';
import type { SupervisorState } from '../../../../shared/ipc';
import { installAgenticoMock, supervisorState } from '../../test/agenticoMock';
import {
  SupervisorStatusProvider,
  SupervisorStatusStore,
  useSupervisorStatus,
} from './supervisorStatus';

afterEach(() => cleanup());

const KEY_A = 'server-a';
const KEY_B = 'server-b';

function running(overrides: Partial<SupervisorState> = {}): SupervisorState {
  return supervisorState({ lifecycle: 'running', generation: 1, ...overrides });
}

function idle(
  outcome: SupervisorState['lastTurnOutcome'],
  overrides: Partial<SupervisorState> = {},
): SupervisorState {
  return supervisorState({
    lifecycle: 'idle',
    generation: 1,
    lastTurnOutcome: outcome,
    ...overrides,
  });
}

/** A store pointed at server A with the Supervisor page on a feature page. */
function storeOnFeaturePage(focused = true): SupervisorStatusStore {
  const store = new SupervisorStatusStore({ windowFocused: focused });
  store.setServer(KEY_A);
  store.setPageSelected(false);
  return store;
}

describe('SupervisorStatusStore unread', () => {
  it('sets unread when a turn completes while a feature page is selected', () => {
    const store = storeOnFeaturePage();
    store.applyStreamState(KEY_A, running());
    expect(store.getSnapshot().unread).toBe(false);
    store.applyStreamState(KEY_A, idle('completed'));
    expect(store.getSnapshot()).toMatchObject({ lifecycle: 'idle', unread: true });
  });

  it('never sets unread when the turn completes with the page selected and the window focused', () => {
    const store = storeOnFeaturePage();
    store.setPageSelected(true);
    store.applyStreamState(KEY_A, running());
    store.applyStreamState(KEY_A, idle('completed'));
    expect(store.getSnapshot().unread).toBe(false);
  });

  it('sets unread when the turn completes with the page selected but the window unfocused, clearing when focus returns', () => {
    const store = storeOnFeaturePage(false);
    store.setPageSelected(true);
    store.applyStreamState(KEY_A, running());
    store.applyStreamState(KEY_A, idle('completed'));
    expect(store.getSnapshot().unread).toBe(true);
    store.setWindowFocused(true);
    expect(store.getSnapshot().unread).toBe(false);
  });

  it('clears when the page is selected while focused; selected while unfocused waits for focus', () => {
    const store = storeOnFeaturePage(false);
    store.applyStreamState(KEY_A, running());
    store.applyStreamState(KEY_A, idle('completed'));
    store.setPageSelected(true);
    expect(store.getSnapshot().unread).toBe(true);
    store.setWindowFocused(true);
    expect(store.getSnapshot().unread).toBe(false);

    store.setPageSelected(false);
    store.applyStreamState(KEY_A, running());
    store.applyStreamState(KEY_A, idle('completed'));
    expect(store.getSnapshot().unread).toBe(true);
    store.setPageSelected(true);
    expect(store.getSnapshot().unread).toBe(false);
  });

  it('never sets unread for interrupted or failed turns, markers or a launch failure', () => {
    const store = storeOnFeaturePage();
    store.applyStreamState(KEY_A, running());
    store.applyStreamState(KEY_A, idle('interrupted', { interruptedBy: 'user' }));
    store.applyStreamState(KEY_A, running());
    store.applyStreamState(KEY_A, idle('failed'));
    store.applyStreamState(KEY_A, supervisorState({ lifecycle: 'starting' }));
    store.applyStreamState(
      KEY_A,
      supervisorState({
        lifecycle: 'failed',
        failure: { code: 'supervisor_launch_failed', class: 'blocking', title: 'x', summary: 'y' },
      }),
    );
    expect(store.getSnapshot().unread).toBe(false);
  });

  it('does not treat a first load of an already-completed turn, or a repeated idle, as a turn ending', () => {
    const store = storeOnFeaturePage();
    store.applyFetchedState(KEY_A, idle('completed'), 0);
    expect(store.getSnapshot().unread).toBe(false);
    store.applyStreamState(KEY_A, idle('completed'));
    expect(store.getSnapshot().unread).toBe(false);
  });

  it('counts a turn that ended through a waiting state', () => {
    const store = storeOnFeaturePage();
    store.applyStreamState(KEY_A, supervisorState({ lifecycle: 'waiting_question' }));
    store.applyStreamState(KEY_A, idle('completed'));
    expect(store.getSnapshot().unread).toBe(true);
  });
});

describe('SupervisorStatusStore per-server state', () => {
  it('keeps state and unread per server key and shows the current server after a switch', () => {
    const store = storeOnFeaturePage();
    store.applyStreamState(KEY_A, running());
    store.applyStreamState(KEY_A, idle('completed'));
    store.setServer(KEY_B);
    expect(store.getSnapshot()).toMatchObject({ lifecycle: null, unread: false });
    store.applyStreamState(KEY_B, running());
    expect(store.getSnapshot()).toMatchObject({ lifecycle: 'running', unread: false });
    store.setServer(KEY_A);
    expect(store.getSnapshot()).toMatchObject({ lifecycle: 'idle', unread: true });
  });

  it('clears a server’s unread when switching onto it with its page selected and focused', () => {
    const store = storeOnFeaturePage();
    store.applyStreamState(KEY_B, running());
    store.applyStreamState(KEY_B, idle('completed'));
    store.setServer(KEY_B);
    expect(store.getSnapshot().unread).toBe(true);
    store.setServer(KEY_A);
    store.setPageSelected(true);
    expect(store.getSnapshot().unread).toBe(false);
    store.setServer(KEY_B);
    expect(store.getSnapshot().unread).toBe(false);
  });

  it('drops a fetched state that raced a newer stream state', () => {
    const store = storeOnFeaturePage();
    const tick = store.streamTick(KEY_A);
    store.applyStreamState(KEY_A, idle('completed'));
    store.applyFetchedState(KEY_A, running(), tick);
    expect(store.getSnapshot().lifecycle).toBe('idle');
  });

  it('adds a streamed request to the pending list once and ignores a retired generation', () => {
    const store = storeOnFeaturePage();
    store.applyStreamState(
      KEY_A,
      supervisorState({ lifecycle: 'waiting_permission', generation: 2 }),
    );
    const request = {
      kind: 'permission' as const,
      id: 'perm-1',
      sessionId: '__supervisor__.conv-1.2',
      target: 'supervisor' as const,
      toolName: 'Bash',
      waitingSince: '2026-10-06T10:00:00Z',
    };
    store.addRequest(KEY_A, 2, request);
    store.addRequest(KEY_A, 2, request);
    store.addRequest(KEY_A, 1, { ...request, id: 'perm-old' });
    expect(store.getSnapshot().state?.pendingRequests.map((item) => item.id)).toEqual(['perm-1']);
  });

  it('keeps the snapshot object stable while nothing changed', () => {
    const store = storeOnFeaturePage();
    const before = store.getSnapshot();
    store.setPageSelected(false);
    store.setServer(KEY_A);
    expect(store.getSnapshot()).toBe(before);
  });
});

function StatusProbe() {
  const status = useSupervisorStatus();
  return (
    <output
      data-testid="status"
      data-lifecycle={status.lifecycle ?? 'none'}
      data-unread={status.unread}
      data-focused={status.windowFocused}
    />
  );
}

describe('SupervisorStatusProvider', () => {
  it('loads the ready server, follows state and focus pushes, and re-fetches on reset', async () => {
    const mock = installAgenticoMock({ supervisorState: running() });
    const store = new SupervisorStatusStore({ windowFocused: true });
    render(
      <SupervisorStatusProvider store={store} serverKey={KEY_A}>
        <StatusProbe />
      </SupervisorStatusProvider>,
    );
    const probe = screen.getByTestId('status');
    await waitFor(() => expect(probe).toHaveAttribute('data-lifecycle', 'running'));
    expect(mock.api.getSupervisorState).toHaveBeenCalledTimes(1);

    act(() => mock.emitWindowFocus({ focused: false }));
    expect(probe).toHaveAttribute('data-focused', 'false');

    act(() =>
      mock.emitSupervisorEvent({
        type: 'state',
        conversationId: 'supervisor-conversation-1',
        generation: 1,
        streamEpoch: 'epoch-1',
        state: idle('completed'),
      }),
    );
    expect(probe).toHaveAttribute('data-lifecycle', 'idle');
    expect(probe).toHaveAttribute('data-unread', 'true');

    mock.api.getSupervisorState.mockResolvedValueOnce(supervisorState({ lifecycle: 'stopped' }));
    act(() => mock.emitSupervisorEvent({ type: 'reset' }));
    await waitFor(() => expect(probe).toHaveAttribute('data-lifecycle', 'stopped'));
  });

  it('ignores supervisor events while no server is ready', () => {
    const mock = installAgenticoMock();
    const store = new SupervisorStatusStore();
    render(
      <SupervisorStatusProvider store={store} serverKey={null}>
        <StatusProbe />
      </SupervisorStatusProvider>,
    );
    expect(mock.supervisorEventListenerCount()).toBe(0);
    expect(mock.api.getSupervisorState).not.toHaveBeenCalled();
  });

  it('reads as not loaded without a provider', () => {
    render(<StatusProbe />);
    expect(screen.getByTestId('status')).toHaveAttribute('data-lifecycle', 'none');
  });
});
