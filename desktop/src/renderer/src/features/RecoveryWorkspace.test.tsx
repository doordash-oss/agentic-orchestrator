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

import { cleanup, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it } from 'vitest';
import { installAgenticoMock, orphanSessionError } from '../test/agenticoMock';
import { RecoveryWorkspace } from './RecoveryWorkspace';

afterEach(cleanup);

describe('RecoveryWorkspace', () => {
  it('renders a live orphan as one needs-action card with an in-card Resume and the PID in the header', async () => {
    const mock = installAgenticoMock();
    mock.api.scanRecovery.mockResolvedValue({
      snapshotId: 'recovery-1',
      items: [
        {
          key: 'feature-a:repo-a',
          featureId: 'abcd1234ef567890',
          featureName: 'Recovery target',
          repoName: 'repo-a',
          phase: 'Implement',
          iteration: 2,
          pid: 4242,
          processAlive: true,
          error: orphanSessionError(),
          allowedActions: ['resume', 'kill', 'skip'],
          defaultAction: 'resume',
        },
      ],
    });
    mock.api.executeRecovery.mockResolvedValue({ result: 'submitted' });
    render(<RecoveryWorkspace />);
    const user = userEvent.setup();

    const queue = await screen.findByRole('list', { name: 'Recovery items' });
    const alerts = within(queue).getAllByRole('alert');
    expect(alerts).toHaveLength(1);
    const card = alerts[0]!;
    expect(card).toHaveClass(
      'error-surface',
      'error-surface--compact',
      'error-surface--needs-action',
    );
    expect(within(card).getByText('Needs your action')).toBeVisible();
    expect(within(card).getByText('orphan_session_live')).toHaveClass('error-surface__code');
    expect(within(card).getByText('Orphaned session running')).toBeVisible();
    expect(
      within(card).getByText(
        "The Implement phase at iteration 2 is still running outside Agentico's supervision.",
      ),
    ).toBeVisible();
    const disclosure = card.querySelector('details.error-surface__details--compact');
    expect(disclosure).not.toBeNull();
    expect(disclosure).toHaveTextContent('Phase: Implement');
    expect(disclosure).toHaveTextContent('Iteration 2');

    // The old facts list is gone; the PID moved into the item header.
    expect(document.querySelector('.recovery-workspace__item-facts')).toBeNull();
    const pidTag = document.querySelector('.recovery-workspace__item-pid');
    expect(pidTag).not.toBeNull();
    expect(pidTag).toHaveTextContent('PID 4242');
    expect(pidTag?.closest('.recovery-workspace__item-header')).not.toBeNull();

    // Kill stays a sibling control and still opens the impact dialog.
    const kill = within(queue).getByRole('button', { name: 'Kill' });
    expect(kill).toHaveClass('recovery-workspace__action--kill');
    await user.click(kill);
    expect(await screen.findByRole('dialog', { name: 'Confirm kill' })).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Cancel' }));

    // Exactly one Resume button, inside the card, dispatching the single-item request.
    expect(within(queue).getAllByRole('button', { name: 'Resume' })).toHaveLength(1);
    await user.click(within(card).getByRole('button', { name: 'Resume' }));
    expect(mock.api.executeRecovery).toHaveBeenCalledTimes(1);
    expect(mock.api.executeRecovery).toHaveBeenCalledWith({
      snapshotId: 'recovery-1',
      actions: { 'feature-a:repo-a': 'resume' },
    });
    expect(await within(queue).findByText('↳ Resume submitted')).toBeVisible();
    // Only the supported outcomes are offered.
    expect(screen.queryByRole('button', { name: 'Skip' })).not.toBeInTheDocument();
  });

  it('renders a stale orphan as the orphan_session_stale card the same way', async () => {
    const mock = installAgenticoMock();
    mock.api.scanRecovery.mockResolvedValue({
      snapshotId: 'recovery-2',
      items: [
        {
          key: 'feature-b:repo-b',
          featureId: 'bbbb1234ef567890',
          featureName: 'Stale target',
          repoName: 'repo-b',
          phase: 'Final review',
          iteration: 1,
          pid: 5353,
          processAlive: false,
          error: orphanSessionError({
            code: 'orphan_session_stale',
            title: 'Orphaned session state',
            summary:
              'The Final review phase at iteration 1 left recovery state behind with no process running.',
            remediation: {
              hint: 'Resume to relaunch the phase where it stopped, or kill to discard the state.',
              actions: ['resume'],
            },
            context: { phase: { name: 'final_review', iteration: 1 } },
          }),
          allowedActions: ['resume', 'kill'],
          defaultAction: 'resume',
        },
      ],
    });
    render(<RecoveryWorkspace />);

    const queue = await screen.findByRole('list', { name: 'Recovery items' });
    const alerts = within(queue).getAllByRole('alert');
    expect(alerts).toHaveLength(1);
    const card = alerts[0]!;
    expect(within(card).getByText('Needs your action')).toBeVisible();
    expect(within(card).getByText('orphan_session_stale')).toHaveClass('error-surface__code');
    expect(within(card).getByText('Orphaned session state')).toBeVisible();
    const disclosure = card.querySelector('details.error-surface__details--compact');
    expect(disclosure).not.toBeNull();
    expect(disclosure).toHaveTextContent('Phase: final_review');
    expect(disclosure).toHaveTextContent('Iteration 1');
    expect(within(card).getByRole('button', { name: 'Resume' })).toBeEnabled();
    expect(within(queue).getByRole('button', { name: 'Kill' })).toBeVisible();
    // A dead session renders no PID tag even when the PID is known.
    expect(document.querySelector('.recovery-workspace__item-pid')).toBeNull();
    expect(document.querySelector('.recovery-workspace__item-facts')).toBeNull();
  });

  it('labels the in-card button "Resuming…" while the resume request is in flight', async () => {
    const mock = installAgenticoMock();
    mock.api.scanRecovery.mockResolvedValue({
      snapshotId: 'recovery-3',
      items: [
        {
          key: 'feature-c:repo-c',
          featureId: 'cccc1234ef567890',
          featureName: 'Live target',
          repoName: 'repo-c',
          phase: 'Implement',
          iteration: 2,
          pid: 6464,
          processAlive: true,
          error: orphanSessionError(),
          allowedActions: ['resume', 'kill'],
          defaultAction: 'resume',
        },
      ],
    });
    let settle: (value: { result: string }) => void = () => {};
    mock.api.executeRecovery.mockImplementation(
      () =>
        new Promise((resolve) => {
          settle = resolve;
        }),
    );
    render(<RecoveryWorkspace />);
    const user = userEvent.setup();

    await user.click(await screen.findByRole('button', { name: 'Resume' }));
    expect(await screen.findByRole('button', { name: 'Resuming…' })).toBeVisible();
    expect(screen.queryByRole('button', { name: 'Resume' })).not.toBeInTheDocument();
    settle({ result: 'submitted' });
    expect(await screen.findByText('↳ Resume submitted')).toBeVisible();
  });

  it('marks an empty recovery scan as neutral', async () => {
    const mock = installAgenticoMock();
    mock.api.scanRecovery.mockResolvedValue({ snapshotId: 'recovery-empty', items: [] });

    render(<RecoveryWorkspace />);

    expect(await screen.findByRole('button', { name: 'Scan for orphans' })).toBeVisible();
    expect(screen.getByRole('region', { name: 'Recovery workspace' })).toHaveAttribute(
      'data-attention',
      'false',
    );
  });
});
