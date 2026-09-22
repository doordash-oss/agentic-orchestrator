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
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { TestingContractItem, TestingContractSnapshot } from '../../../shared/ipc';
import { installAgenticoMock, ipcError } from '../test/agenticoMock';
import { itemLock, TestingContractWaiveDialog } from './TestingContractWaiveDialog';

afterEach(cleanup);

function item(overrides: Partial<TestingContractItem> & { itemId: string }): TestingContractItem {
  return {
    source: 'plan',
    owner: 'harness',
    name: overrides.itemId,
    command: `make ${overrides.itemId}`,
    required: true,
    allowSubstitution: false,
    allowBlocked: false,
    allowWaiver: true,
    capabilities: [],
    ...overrides,
  };
}

const contract: TestingContractSnapshot = {
  available: true,
  featureId: 'abcd1234ef567890',
  roadmapPhase: 2,
  revision: 3,
  items: [
    item({ itemId: 'deploy-smoke', name: 'Deployment smoke test', capabilities: ['network'] }),
    item({
      itemId: 'ui-capture',
      name: 'Vendor console capture',
      source: 'visual',
      owner: 'agent',
      allowSubstitution: true,
    }),
    item({
      itemId: 'manual-review',
      name: 'Manual review',
      source: 'manual',
      disposition: { status: 'waived', reason: 'Reviewed offline.', changedBy: 'user' },
    }),
    item({ itemId: 'unit-tests', name: 'Unit tests', allowWaiver: false }),
  ],
};

describe('TestingContractWaiveDialog', () => {
  it('locks waived rows and rows the contract forbids waiving', () => {
    expect(itemLock(contract.items[0]!)).toBeUndefined();
    expect(itemLock(contract.items[2]!)).toBe('waived');
    expect(itemLock(contract.items[3]!)).toBe('unwaivable');
  });

  it('renders the fetched contract and submits only the ticked, waivable ids', async () => {
    const mock = installAgenticoMock();
    mock.api.getTestingContract.mockResolvedValue(contract);
    mock.api.waiveTestingContract.mockResolvedValue({
      result: 'waived',
      contractRevision: 4,
      waivedItems: ['deploy-smoke', 'ui-capture'],
    });
    const onClose = vi.fn();
    const onWaived = vi.fn().mockResolvedValue(undefined);
    const user = userEvent.setup();
    render(
      <TestingContractWaiveDialog
        featureId="abcd1234ef567890"
        onClose={onClose}
        onWaived={onWaived}
      />,
    );

    const dialog = screen.getByRole('dialog', { name: 'Waive contract items?' });
    expect(dialog).toBeVisible();
    expect(screen.getByRole('status')).toHaveTextContent('Loading contract…');
    expect(await screen.findByText('Deployment smoke test')).toBeVisible();
    expect(mock.api.getTestingContract).toHaveBeenCalledWith({ featureId: 'abcd1234ef567890' });
    expect(screen.queryByRole('textbox', { name: 'Contract item ids' })).not.toBeInTheDocument();
    expect(screen.getByText('Phase 2 contract · revision 3')).toBeVisible();

    expect(screen.getByRole('checkbox', { name: 'manual-review' })).toBeDisabled();
    expect(screen.getByText('Waived')).toBeVisible();
    expect(screen.getByText('Reviewed offline.')).toBeVisible();
    expect(screen.getByRole('checkbox', { name: 'unit-tests' })).toBeDisabled();
    expect(screen.getByText('Cannot be waived')).toBeVisible();
    expect(screen.getByText('Substitute accepted')).toBeVisible();
    expect(screen.getByText('Needs network')).toBeVisible();
    expect(screen.getByText('visual')).toBeVisible();
    expect(screen.getByText('agent')).toBeVisible();

    const waive = screen.getByRole('button', { name: 'Waive items' });
    expect(waive).toBeDisabled();
    await user.click(screen.getByRole('checkbox', { name: 'deploy-smoke' }));
    await user.click(screen.getByRole('checkbox', { name: 'ui-capture' }));
    expect(waive).toBeDisabled();
    await user.type(
      screen.getByRole('textbox', { name: 'Waiver reason' }),
      'Vendor console is unreachable from CI.',
    );
    expect(waive).toBeEnabled();

    await user.click(waive);
    await waitFor(() =>
      expect(mock.api.waiveTestingContract).toHaveBeenCalledWith({
        featureId: 'abcd1234ef567890',
        itemIds: ['deploy-smoke', 'ui-capture'],
        reason: 'Vendor console is unreachable from CI.',
        roadmapPhase: 2,
        contractRevision: 3,
      }),
    );
    await waitFor(() => expect(onWaived).toHaveBeenCalledOnce());
    expect(onClose).toHaveBeenCalledOnce();
  });

  it('explains when the phase has no contract yet and keeps the submit disabled', async () => {
    const mock = installAgenticoMock();
    mock.api.getTestingContract.mockResolvedValue({ available: false });
    render(
      <TestingContractWaiveDialog
        featureId="abcd1234ef567890"
        onClose={vi.fn()}
        onWaived={vi.fn()}
      />,
    );

    expect(await screen.findByText('This phase has no testing contract yet.')).toBeVisible();
    expect(screen.queryByRole('checkbox')).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Waive items' })).toBeDisabled();
  });

  it('surfaces a contract read failure without closing', async () => {
    const mock = installAgenticoMock();
    mock.api.getTestingContract.mockRejectedValue(ipcError('E_INTERNAL', 'contract read failed'));
    const onClose = vi.fn();
    render(
      <TestingContractWaiveDialog
        featureId="abcd1234ef567890"
        onClose={onClose}
        onWaived={vi.fn()}
      />,
    );

    expect(await screen.findByRole('alert')).toBeVisible();
    expect(screen.queryByRole('checkbox')).not.toBeInTheDocument();
    expect(onClose).not.toHaveBeenCalled();
  });

  it('keeps the dialog open and surfaces the canonical error when the server rejects', async () => {
    const mock = installAgenticoMock();
    mock.api.getTestingContract.mockResolvedValue(contract);
    mock.api.waiveTestingContract.mockRejectedValue(
      ipcError('E_INTERNAL', 'the current phase has no testing contract yet'),
    );
    const onClose = vi.fn();
    const user = userEvent.setup();
    render(
      <TestingContractWaiveDialog
        featureId="abcd1234ef567890"
        onClose={onClose}
        onWaived={vi.fn()}
      />,
    );

    await user.click(await screen.findByRole('checkbox', { name: 'deploy-smoke' }));
    await user.type(screen.getByRole('textbox', { name: 'Waiver reason' }), 'No vendor access.');
    await user.click(screen.getByRole('button', { name: 'Waive items' }));

    expect(await screen.findByRole('alert')).toBeVisible();
    expect(onClose).not.toHaveBeenCalled();
    expect(screen.getByRole('dialog', { name: 'Waive contract items?' })).toBeVisible();

    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(onClose).toHaveBeenCalledOnce();
  });

  it('reloads the contract on a 409 conflict and drops ticks the new phase no longer offers', async () => {
    const mock = installAgenticoMock();
    const later: TestingContractSnapshot = {
      available: true,
      featureId: 'abcd1234ef567890',
      roadmapPhase: 3,
      revision: 1,
      items: [
        item({ itemId: 'ui-capture', name: 'Vendor console capture' }),
        item({ itemId: 'load-test', name: 'Load test' }),
      ],
    };
    mock.api.getTestingContract.mockResolvedValueOnce(contract).mockResolvedValueOnce(later);
    mock.api.waiveTestingContract.mockRejectedValue(
      ipcError(
        'conflict',
        'testing contract changed since it was read: reload the contract and select again',
      ),
    );
    const onClose = vi.fn();
    const onWaived = vi.fn();
    const user = userEvent.setup();
    render(
      <TestingContractWaiveDialog
        featureId="abcd1234ef567890"
        onClose={onClose}
        onWaived={onWaived}
      />,
    );

    await user.click(await screen.findByRole('checkbox', { name: 'deploy-smoke' }));
    await user.click(screen.getByRole('checkbox', { name: 'ui-capture' }));
    await user.type(screen.getByRole('textbox', { name: 'Waiver reason' }), 'Offline vendor.');
    await user.click(screen.getByRole('button', { name: 'Waive items' }));

    expect(await screen.findByRole('alert')).toHaveTextContent(
      'testing contract changed since it was read',
    );
    expect(mock.api.waiveTestingContract).toHaveBeenCalledWith(
      expect.objectContaining({ roadmapPhase: 2, contractRevision: 3 }),
    );
    expect(await screen.findByText('Phase 3 contract · revision 1')).toBeVisible();
    expect(mock.api.getTestingContract).toHaveBeenCalledTimes(2);
    expect(screen.getByRole('dialog', { name: 'Waive contract items?' })).toBeVisible();
    expect(screen.queryByRole('checkbox', { name: 'deploy-smoke' })).not.toBeInTheDocument();
    expect(screen.getByRole('checkbox', { name: 'ui-capture' })).toBeChecked();
    expect(screen.getByRole('checkbox', { name: 'load-test' })).not.toBeChecked();
    expect(onWaived).not.toHaveBeenCalled();
    expect(onClose).not.toHaveBeenCalled();

    // The retry binds to the reloaded snapshot.
    mock.api.waiveTestingContract.mockResolvedValue({
      result: 'waived',
      contractRevision: 2,
      waivedItems: ['ui-capture'],
    });
    await user.click(screen.getByRole('button', { name: 'Waive items' }));
    await waitFor(() =>
      expect(mock.api.waiveTestingContract).toHaveBeenLastCalledWith({
        featureId: 'abcd1234ef567890',
        itemIds: ['ui-capture'],
        reason: 'Offline vendor.',
        roadmapPhase: 3,
        contractRevision: 1,
      }),
    );
  });

  it('keeps the footer mounted behind a scrolling checklist when the contract is long', async () => {
    const mock = installAgenticoMock();
    mock.api.getTestingContract.mockResolvedValue({
      ...contract,
      items: Array.from({ length: 14 }, (_, i) => item({ itemId: `check-${i}` })),
    });
    render(
      <TestingContractWaiveDialog
        featureId="abcd1234ef567890"
        onClose={vi.fn()}
        onWaived={vi.fn()}
      />,
    );

    const first = await screen.findByRole('checkbox', { name: 'check-0' });
    expect(screen.getAllByRole('checkbox')).toHaveLength(14);
    expect(first.closest('fieldset')).toHaveClass('contract-waive-dialog__items');
    expect(screen.getByRole('button', { name: 'Waive items' })).toBeInTheDocument();
    expect(screen.getByRole('textbox', { name: 'Waiver reason' })).toBeInTheDocument();
  });

  it('keeps focus in the reason field when the cockpit hands down a new onClose', async () => {
    const mock = installAgenticoMock();
    mock.api.getTestingContract.mockResolvedValue(contract);
    const user = userEvent.setup();
    const onWaived = vi.fn();
    const view = render(
      <TestingContractWaiveDialog
        featureId="abcd1234ef567890"
        onClose={vi.fn()}
        onWaived={onWaived}
      />,
    );
    await screen.findByRole('checkbox', { name: 'deploy-smoke' });

    const textarea = screen.getByRole('textbox', { name: 'Waiver reason' });
    await user.click(textarea);
    expect(textarea).toHaveFocus();

    const nextClose = vi.fn();
    view.rerender(
      <TestingContractWaiveDialog
        featureId="abcd1234ef567890"
        onClose={nextClose}
        onWaived={onWaived}
      />,
    );
    expect(textarea).toHaveFocus();

    // Escape still reaches the latest callback.
    await act(async () => {
      await user.keyboard('{Escape}');
    });
    expect(nextClose).toHaveBeenCalledOnce();
  });
});
