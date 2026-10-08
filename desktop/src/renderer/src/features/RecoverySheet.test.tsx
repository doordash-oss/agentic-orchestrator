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

import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { installAgenticoMock, orphanSessionError } from '../test/agenticoMock';
import { RecoverySheet } from './RecoverySheet';

afterEach(cleanup);

function liveOrphanScan() {
  return {
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
  };
}

describe('RecoverySheet', () => {
  it('stacks the Recovery workspace above Bulk resume and retry inside one modal sheet', async () => {
    const mock = installAgenticoMock();
    mock.api.scanRecovery.mockResolvedValue({ snapshotId: 'recovery-1', items: [] });
    render(<RecoverySheet bulkPreviewKey={null} onClose={() => {}} />);

    const sheet = screen.getByRole('dialog', { name: 'Recovery' });
    expect(sheet).toHaveAttribute('aria-modal', 'true');
    expect(sheet).toHaveClass('sheet');
    expect(sheet.parentElement).toHaveClass('sheet-scrim');
    const recovery = within(sheet).getByRole('region', { name: 'Recovery workspace' });
    const bulk = within(sheet).getByRole('region', { name: 'Bulk resume and retry' });
    expect(recovery).toHaveClass('recovery-workspace');
    expect(bulk).toHaveClass('bulk-preview');
    expect(recovery.compareDocumentPosition(bulk) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(within(recovery).getByRole('heading', { name: 'Recovery' })).toBeVisible();
    expect(within(bulk).getByRole('heading', { name: 'Bulk resume / retry' })).toBeVisible();

    // Recovery auto-scans exactly once per open; Bulk previews only when asked.
    await waitFor(() => expect(mock.api.scanRecovery).toHaveBeenCalledTimes(1));
    expect(mock.api.bulkPreview).not.toHaveBeenCalled();
  });

  it('loads the bulk preview on open when this open asked for it', async () => {
    const mock = installAgenticoMock();
    mock.api.scanRecovery.mockResolvedValue({ snapshotId: 'recovery-1', items: [] });
    mock.api.bulkPreview.mockResolvedValue({ previewId: 'p-1', eligible: [], excluded: [] });
    render(<RecoverySheet bulkPreviewKey={3} onClose={() => {}} />);

    expect(await screen.findByText('No features are eligible for resume or retry.')).toBeVisible();
    expect(mock.api.bulkPreview).toHaveBeenCalledTimes(1);
  });

  it('closes on Escape, on the scrim, and on its Close button, but not on a click inside', async () => {
    const mock = installAgenticoMock();
    mock.api.scanRecovery.mockResolvedValue({ snapshotId: 'recovery-1', items: [] });
    const onClose = vi.fn();
    render(<RecoverySheet bulkPreviewKey={null} onClose={onClose} />);
    const user = userEvent.setup();
    const sheet = screen.getByRole('dialog', { name: 'Recovery' });

    fireEvent.mouseDown(within(sheet).getByRole('region', { name: 'Recovery workspace' }));
    expect(onClose).not.toHaveBeenCalled();

    await user.keyboard('{Escape}');
    expect(onClose).toHaveBeenCalledTimes(1);

    fireEvent.mouseDown(sheet.parentElement!);
    expect(onClose).toHaveBeenCalledTimes(2);

    await user.click(within(sheet).getByRole('button', { name: 'Close' }));
    expect(onClose).toHaveBeenCalledTimes(3);
  });

  it('keeps the kill confirmation working as a dialog nested inside the sheet', async () => {
    const mock = installAgenticoMock();
    mock.api.scanRecovery.mockResolvedValue(liveOrphanScan());
    const onClose = vi.fn();
    render(<RecoverySheet bulkPreviewKey={null} onClose={onClose} />);
    const user = userEvent.setup();

    const sheet = screen.getByRole('dialog', { name: 'Recovery' });
    const kill = await within(sheet).findByRole('button', { name: 'Kill' });
    await user.click(kill);
    const confirm = within(sheet).getByRole('dialog', { name: 'Confirm kill' });
    expect(confirm).toBeVisible();

    // Escape dismisses only the innermost dialog: the sheet stays open.
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('dialog', { name: 'Confirm kill' })).toBeNull();
    expect(onClose).not.toHaveBeenCalled();
    expect(kill).toHaveFocus();

    // A mousedown on the confirmation's own backdrop never reaches the scrim.
    await user.click(kill);
    const reopened = within(sheet).getByRole('dialog', { name: 'Confirm kill' });
    fireEvent.mouseDown(reopened);
    expect(onClose).not.toHaveBeenCalled();
    await user.click(within(reopened).getByRole('button', { name: 'Cancel' }));
    expect(screen.queryByRole('dialog', { name: 'Confirm kill' })).toBeNull();
    expect(screen.getByRole('dialog', { name: 'Recovery' })).toBeVisible();
  });

  it('hands a feature jump to its caller', async () => {
    const mock = installAgenticoMock();
    mock.api.scanRecovery.mockResolvedValue(liveOrphanScan());
    const onNavigateToFeature = vi.fn();
    render(
      <RecoverySheet
        bulkPreviewKey={null}
        onClose={() => {}}
        onNavigateToFeature={onNavigateToFeature}
      />,
    );
    const queue = await screen.findByRole('list', { name: 'Recovery items' });
    await userEvent.click(within(queue).getByRole('button', { name: 'Recovery target' }));
    expect(onNavigateToFeature).toHaveBeenCalledWith('abcd1234ef567890');
  });
});
