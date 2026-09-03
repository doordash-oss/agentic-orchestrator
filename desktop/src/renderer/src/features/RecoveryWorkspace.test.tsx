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

import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { installAgenticoMock, ipcError, orphanSessionError } from '../test/agenticoMock';
import { ExplainChatProvider } from '../explainChat';
import { RecoveryWorkspace } from './RecoveryWorkspace';

afterEach(cleanup);

describe('RecoveryWorkspace scan and dispatch errors', () => {
  it('renders a rejected scan as a compact ErrorSurface whose scan action re-invokes it', async () => {
    const mock = installAgenticoMock();
    mock.api.scanRecovery.mockRejectedValue(ipcError('E_INTERNAL', 'The recovery scan failed.'));
    render(<RecoveryWorkspace />);
    const user = userEvent.setup();

    // The auto-scan on mount rejected: one compact ErrorSurface, no queue.
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveClass('error-surface', 'error-surface--compact');
    expect(within(alert).getByText('E_INTERNAL')).toHaveClass('error-surface__code');
    expect(within(alert).getByText('The recovery scan failed.')).toBeVisible();
    expect(within(alert).getByRole('button', { name: 'Scan for orphans' })).toHaveClass(
      'error-surface__action',
    );
    // The legacy hand-rolled error markup is gone.
    expect(document.querySelector('.form-field__error')).toBeNull();
    expect(document.querySelector('.recovery-workspace__error-actions')).toBeNull();
    expect(document.querySelector('.recovery-attention')).toBeNull();

    await user.click(within(alert).getByRole('button', { name: 'Scan for orphans' }));
    await waitFor(() => expect(mock.api.scanRecovery).toHaveBeenCalledTimes(2));
  });
});

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

    // Resume and Kill form one remediation group inside the owning card.
    const actionRow = card.querySelector('.error-surface__action-row');
    expect(actionRow).not.toBeNull();
    expect(within(actionRow as HTMLElement).getByRole('button', { name: 'Resume' })).toBeVisible();
    const kill = within(actionRow as HTMLElement).getByRole('button', { name: 'Kill' });
    expect(kill).toHaveClass('error-surface__secondary-action');
    await user.click(kill);
    expect(await screen.findByRole('dialog', { name: 'Confirm kill' })).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(kill).toHaveFocus();

    // Exactly one Resume button, inside the card, dispatching the single-item request.
    expect(within(queue).getAllByRole('button', { name: 'Resume' })).toHaveLength(1);
    await user.click(within(card).getByRole('button', { name: 'Resume' }));
    expect(mock.api.executeRecovery).toHaveBeenCalledTimes(1);
    expect(mock.api.executeRecovery).toHaveBeenCalledWith({
      snapshotId: 'recovery-1',
      actions: { 'feature-a:repo-a': 'resume' },
    });
    expect(await within(queue).findByText('↳ Resume submitted')).toBeVisible();
    // Once the outcome is recorded, the Resume control disarms instead of
    // offering a second dispatch of the same action.
    expect(within(card).queryByRole('button', { name: 'Resume' })).not.toBeInTheDocument();
    expect(within(card).getByText('This recovery action already finished.')).toBeVisible();
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
    expect(within(card).getByRole('button', { name: 'Kill' })).toBeVisible();
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

  it('routes the recovery card as a recovery reference with the snapshot ID and item key', async () => {
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
    const requestRoute = vi.fn();
    render(
      <ExplainChatProvider requestRoute={requestRoute}>
        <RecoveryWorkspace />
      </ExplainChatProvider>,
    );
    const user = userEvent.setup();

    const queue = await screen.findByRole('list', { name: 'Recovery items' });
    const card = within(queue).getByRole('alert');
    await user.click(within(card).getByRole('button', { name: 'Explain in chat' }));
    expect(requestRoute).toHaveBeenCalledTimes(1);
    expect(requestRoute).toHaveBeenCalledWith({
      target: 'ama',
      draft:
        'Explain the "Orphaned session running" error (orphan_session_live) on Recovery target and what I should do next.',
      autoSubmit: true,
      chatContext: {
        scope: 'recovery',
        code: 'orphan_session_live',
        snapshotId: 'recovery-1',
        key: 'feature-a:repo-a',
      },
    });
  });
});
