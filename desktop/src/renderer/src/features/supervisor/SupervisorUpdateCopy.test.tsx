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
 * The update copy rides every install surface — the Settings app-update card
 * and its stop-work dialog, the update popover, the server-update card and
 * its confirmation dialog — while the status store reads a live supervisor,
 * and is absent once it is stopped or failed. The stop-work sentences beside
 * it name only workflows.
 */
import { cleanup, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import type { ReactNode } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { SupervisorLifecycle, UpdateState } from '../../../../shared/ipc';
import { UpdatePopover } from '../../components/UpdatePopover';
import {
  defaultServerUpdateState,
  defaultUpdateState,
  installAgenticoMock,
  readySnapshot,
  supervisorState,
} from '../../test/agenticoMock';
import { ServerUpdateCard } from '../ServerUpdateCard';
import { SettingsPanel } from '../SettingsPanel';
import { SUPERVISOR_UPDATE_COPY, supervisorUpdateCopyApplies } from './SupervisorUpdateCopy';
import { SupervisorStatusContext, SupervisorStatusStore } from './supervisorStatus';

afterEach(cleanup);

const SERVER_KEY = 'a'.repeat(32);
const LIVE: SupervisorLifecycle[] = [
  'idle',
  'starting',
  'running',
  'waiting_permission',
  'waiting_question',
];
const GONE: SupervisorLifecycle[] = ['stopped', 'failed'];

const READY_UPDATE: UpdateState = defaultUpdateState({
  status: 'ready',
  targetVersion: '0.2.0',
  packageFormat: 'macos',
  signatureStatus: 'verified',
  message: 'A verified update is downloaded and ready to install.',
  activeWorkSummary: '1 workflow. The supervisor is working.',
});

function withLifecycle(lifecycle: SupervisorLifecycle, children: ReactNode) {
  const store = new SupervisorStatusStore({ windowFocused: true });
  store.setServer(SERVER_KEY);
  store.applyStreamState(SERVER_KEY, supervisorState({ lifecycle }));
  return (
    <SupervisorStatusContext.Provider value={store}>{children}</SupervisorStatusContext.Provider>
  );
}

function installMock() {
  return installAgenticoMock({
    connection: {
      status: 'ready',
      stage: 'ready',
      detail: 'Connected.',
      ownership: 'external',
      kind: 'remote',
      serverKey: SERVER_KEY,
      serverName: 'flux-box',
    },
    readiness: readySnapshot(),
    updates: READY_UPDATE,
    serverUpdate: defaultServerUpdateState({
      status: 'available',
      latestVersion: '0.2.0',
      activeWorkSummary: '1 feature active on the server. The supervisor is working.',
    }),
  });
}

/** The copy shows exactly when the lifecycle is live. */
function expectCopy(container: HTMLElement, lifecycle: SupervisorLifecycle): void {
  if (supervisorUpdateCopyApplies(lifecycle)) {
    expect(container).toHaveTextContent(SUPERVISOR_UPDATE_COPY);
  } else {
    expect(container).not.toHaveTextContent('Updating restarts the supervisor');
  }
}

describe('supervisorUpdateCopyApplies', () => {
  it('applies to every lifecycle but stopped and failed, and not before the first load', () => {
    for (const lifecycle of LIVE) expect(supervisorUpdateCopyApplies(lifecycle)).toBe(true);
    for (const lifecycle of GONE) expect(supervisorUpdateCopyApplies(lifecycle)).toBe(false);
    expect(supervisorUpdateCopyApplies(null)).toBe(false);
  });
});

describe.each([...LIVE, ...GONE])('update copy with the supervisor %s', (lifecycle) => {
  it('the Settings app-update card and its stop-work dialog', async () => {
    const user = userEvent.setup();
    installMock();
    render(withLifecycle(lifecycle, <SettingsPanel pane="updates" />));

    const updates = await screen.findByRole('region', { name: 'Updates' });
    await within(updates).findByText('1 workflow. The supervisor is working.');
    expectCopy(updates, lifecycle);

    await user.click(within(updates).getByRole('button', { name: 'Stop Work and Install Now' }));
    const dialog = screen.getByRole('dialog', { name: 'Install update confirmation' });
    expectCopy(dialog, lifecycle);
    expect(dialog).toHaveTextContent('Workflows may be interrupted if they do not stop cleanly.');
    expect(dialog).not.toHaveTextContent('Workflows and the supervisor may be interrupted');
  });

  it('the update popover', () => {
    function Popover() {
      return (
        <UpdatePopover
          update={READY_UPDATE}
          dismissedVersion={null}
          scheduling={false}
          open
          onOpenChange={vi.fn()}
          onDismiss={vi.fn()}
          onOpenSettings={vi.fn()}
          onInstallWhenIdle={async () => {}}
        />
      );
    }
    render(withLifecycle(lifecycle, <Popover />));
    const popover = document.getElementById('update-popover');
    expect(popover).not.toBeNull();
    expectCopy(popover!, lifecycle);
  });

  it('the server-update card and its confirmation dialog', async () => {
    const user = userEvent.setup();
    installMock();
    render(
      withLifecycle(
        lifecycle,
        <ServerUpdateCard serverLabel="flux-box" onOpenExternal={vi.fn()} />,
      ),
    );

    const card = await screen.findByRole('region', { name: 'Server updates' });
    const install = await within(card).findByRole('button', { name: 'Stop work and install now' });
    expectCopy(card, lifecycle);

    await user.click(install);
    const dialog = await screen.findByRole('dialog', {
      name: 'Install server update confirmation',
    });
    expectCopy(dialog, lifecycle);
    expect(dialog).toHaveTextContent('Workflows may be interrupted if they do not stop cleanly');
    expect(dialog).not.toHaveTextContent('Workflows and the supervisor may be interrupted');
  });
});
