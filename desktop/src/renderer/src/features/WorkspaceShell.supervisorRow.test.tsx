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
 * The pinned Supervisor row reads the app-root supervisor status store: its
 * marker and sub-line follow the lifecycle by priority (needs response,
 * working, error), and an unread dot marks a finished turn nobody has seen.
 * The page and the cockpit are stubbed so only the row's contract is under
 * test.
 */
import { act, cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import type { ReactNode } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { defaultSettings, type SupervisorState } from '../../../shared/ipc';
import {
  featureSnapshot,
  installAgenticoMock,
  supervisorState,
  type AgenticoMock,
} from '../test/agenticoMock';
import { SupervisorStatusProvider, SupervisorStatusStore } from './supervisor/supervisorStatus';
import { WorkspaceShell } from './WorkspaceShell';

vi.mock('./supervisor/SupervisorPage', () => ({
  SupervisorPage: () => <section aria-label="Supervisor page" />,
}));

vi.mock('./FeatureCockpit', () => ({
  FeatureCockpit: ({ featureId }: { featureId: string }) => (
    <section aria-label={`Cockpit ${featureId}`} />
  ),
}));

afterEach(() => cleanup());

const FEATURE_ID = 'abcd1234ef567890';
const SERVER_KEY = 'default-runtime';

const PERMISSION = {
  kind: 'permission' as const,
  id: 'perm-1',
  sessionId: '__supervisor__.supervisor-conversation-1.1',
  target: 'supervisor' as const,
  toolName: 'Bash',
  waitingSince: '2026-10-06T10:00:00Z',
};

const QUESTION = {
  kind: 'questions' as const,
  id: 'question-1',
  sessionId: '__supervisor__.supervisor-conversation-1.1',
  target: 'supervisor' as const,
  waitingSince: '2026-10-06T10:00:00Z',
  questions: [{ index: 1, question: 'q1', header: 'Which repo?', multiSelect: false, options: [] }],
};

/** Installs one feature and opens the shell on its page (or on Supervisor). */
function install(page: 'feature' | 'supervisor' = 'feature'): AgenticoMock {
  const feature = featureSnapshot({ id: FEATURE_ID, name: 'Search revamp', status: 'Created' });
  const mock = installAgenticoMock({
    settings: {
      ...defaultSettings(),
      shell: {
        featureByServer: page === 'feature' ? { [SERVER_KEY]: FEATURE_ID } : {},
        sidebarCollapsed: false,
      },
    },
    features: [
      {
        id: feature.id,
        name: feature.name,
        status: feature.status,
        currentPhase: feature.currentPhase,
        repos: feature.repos,
        createdAt: feature.createdAt,
        activeRun: feature.activeRun,
        runCount: 1,
        warnings: [],
        errors: feature.errors,
      },
    ],
    feature,
  });
  return mock;
}

function Harness({
  store,
  serverKey = SERVER_KEY,
  children,
}: {
  store: SupervisorStatusStore;
  serverKey?: string;
  children: ReactNode;
}) {
  return (
    <SupervisorStatusProvider store={store} serverKey={serverKey}>
      {children}
    </SupervisorStatusProvider>
  );
}

async function renderShell(page: 'feature' | 'supervisor' = 'feature') {
  const mock = install(page);
  const store = new SupervisorStatusStore({ windowFocused: true });
  const view = render(
    <Harness store={store}>
      <WorkspaceShell />
    </Harness>,
  );
  if (page === 'feature') {
    await screen.findByRole('region', { name: `Cockpit ${FEATURE_ID}` });
  } else {
    await screen.findByRole('region', { name: 'Supervisor page' });
  }
  // The provider's first load lands before any scripted stream event.
  await waitFor(() => expect(mock.api.getSupervisorState).toHaveBeenCalled());
  await act(async () => {});
  return { mock, store, view };
}

function emitState(mock: AgenticoMock, overrides: Partial<SupervisorState>): void {
  act(() =>
    mock.emitSupervisorEvent({
      type: 'state',
      conversationId: 'supervisor-conversation-1',
      generation: 1,
      streamEpoch: 'epoch-1',
      state: supervisorState({ generation: 1, ...overrides }),
    }),
  );
}

function finishTurn(mock: AgenticoMock, outcome: SupervisorState['lastTurnOutcome'] = 'completed') {
  emitState(mock, { lifecycle: 'running' });
  emitState(mock, { lifecycle: 'idle', lastTurnOutcome: outcome });
}

function supervisorRow(): HTMLElement {
  return screen.getByRole('option', { name: 'Supervisor' });
}

function subline(): string | null {
  return supervisorRow().querySelector('.sidebar__row-subline')?.textContent ?? null;
}

function glyphTone(): string | null {
  return (
    supervisorRow().querySelector('.sidebar__row-glyph--supervisor')?.getAttribute('data-tone') ??
    null
  );
}

function hasDot(): boolean {
  return supervisorRow().querySelector('.sidebar__row-unread') !== null;
}

describe('Supervisor sidebar row states', () => {
  it('shows each lifecycle by priority from a feature page, keeping the name "Supervisor"', async () => {
    const { mock } = await renderShell();

    emitState(mock, { lifecycle: 'waiting_permission', pendingRequests: [PERMISSION] });
    expect(supervisorRow()).toHaveAttribute('data-supervisor-state', 'needs-response');
    expect(glyphTone()).toBe('attention');
    expect(subline()).toBe('Approve 1 request');

    emitState(mock, { lifecycle: 'waiting_question', pendingRequests: [QUESTION] });
    expect(supervisorRow()).toHaveAttribute('data-supervisor-state', 'needs-response');
    expect(glyphTone()).toBe('attention');
    expect(subline()).toBe('Answer 1 question');

    for (const lifecycle of ['running', 'starting'] as const) {
      emitState(mock, { lifecycle });
      expect(supervisorRow()).toHaveAttribute('data-supervisor-state', 'working');
      expect(glyphTone()).toBe('progress');
      expect(subline()).toBe('Working');
    }

    emitState(mock, {
      lifecycle: 'failed',
      failure: {
        code: 'supervisor_launch_failed',
        class: 'blocking',
        title: 'Supervisor could not start',
        summary: 'The harness exited during the handshake.',
      },
    });
    expect(supervisorRow()).toHaveAttribute('data-supervisor-state', 'error');
    expect(glyphTone()).toBe('danger');
    expect(subline()).toBe('Supervisor could not start');

    for (const lifecycle of ['idle', 'stopped'] as const) {
      emitState(mock, { lifecycle });
      expect(supervisorRow()).toHaveAttribute('data-supervisor-state', 'none');
      expect(glyphTone()).toBeNull();
      expect(subline()).toBeNull();
    }

    // Paused after a restart reads as rest too.
    emitState(mock, {
      lifecycle: 'stopped',
      lastTurnOutcome: 'interrupted',
      interruptedBy: 'shutdown',
    });
    expect(supervisorRow()).toHaveAttribute('data-supervisor-state', 'none');
    expect(subline()).toBeNull();
  });

  it('keeps the subline described rather than named', async () => {
    const { mock } = await renderShell();
    emitState(mock, { lifecycle: 'running' });
    expect(supervisorRow()).toHaveAccessibleName('Supervisor');
    expect(supervisorRow()).toHaveAccessibleDescription('Working');
  });
});

describe('Supervisor sidebar row unread', () => {
  it('marks a turn completed on a feature page; selecting the row while focused clears it', async () => {
    const { mock } = await renderShell();
    finishTurn(mock);
    expect(hasDot()).toBe(true);
    expect(supervisorRow()).toHaveAttribute('data-unread', 'true');
    expect(subline()).toBeNull();

    await userEvent.click(supervisorRow());
    expect(hasDot()).toBe(false);
    expect(supervisorRow()).toHaveAttribute('data-unread', 'false');
  });

  it('keeps the dot when the row is selected while unfocused, until focus returns', async () => {
    const { mock } = await renderShell();
    finishTurn(mock);
    act(() => mock.emitWindowFocus({ focused: false }));
    await userEvent.click(supervisorRow());
    expect(hasDot()).toBe(true);

    act(() => mock.emitWindowFocus({ focused: true }));
    expect(hasDot()).toBe(false);
  });

  it('never marks a turn completed on the selected, focused Supervisor page', async () => {
    const { mock } = await renderShell('supervisor');
    finishTurn(mock);
    expect(hasDot()).toBe(false);
  });

  it('marks a turn completed on the selected page while the window is unfocused', async () => {
    const { mock } = await renderShell('supervisor');
    act(() => mock.emitWindowFocus({ focused: false }));
    finishTurn(mock);
    expect(hasDot()).toBe(true);
    act(() => mock.emitWindowFocus({ focused: true }));
    expect(hasDot()).toBe(false);
  });

  it('never marks interrupted or failed turns', async () => {
    const { mock } = await renderShell();
    finishTurn(mock, 'interrupted');
    expect(hasDot()).toBe(false);
    finishTurn(mock, 'failed');
    expect(hasDot()).toBe(false);
  });

  it('shows no dot while working or waiting, and brings it back at rest', async () => {
    const { mock } = await renderShell();
    finishTurn(mock);
    expect(hasDot()).toBe(true);

    emitState(mock, { lifecycle: 'running' });
    expect(hasDot()).toBe(false);
    expect(supervisorRow()).toHaveAttribute('data-unread', 'false');
    emitState(mock, { lifecycle: 'waiting_permission', pendingRequests: [PERMISSION] });
    expect(hasDot()).toBe(false);
    emitState(mock, { lifecycle: 'idle', lastTurnOutcome: 'completed' });
    expect(hasDot()).toBe(true);
  });
});

describe('Supervisor sidebar row per server', () => {
  it('keeps state and unread per server key across a switch', async () => {
    const { mock, store, view } = await renderShell();
    finishTurn(mock);
    expect(hasDot()).toBe(true);

    mock.api.getSupervisorState.mockResolvedValueOnce(supervisorState({ lifecycle: 'running' }));
    view.rerender(
      <Harness store={store} serverKey="server-b">
        <WorkspaceShell />
      </Harness>,
    );
    await waitFor(() => expect(subline()).toBe('Working'));
    expect(hasDot()).toBe(false);
    expect(supervisorRow()).toHaveAccessibleName('Supervisor');

    mock.api.getSupervisorState.mockResolvedValueOnce(
      supervisorState({ lifecycle: 'idle', generation: 1, lastTurnOutcome: 'completed' }),
    );
    view.rerender(
      <Harness store={store} serverKey={SERVER_KEY}>
        <WorkspaceShell />
      </Harness>,
    );
    await waitFor(() => expect(subline()).toBeNull());
    expect(hasDot()).toBe(true);
  });
});
