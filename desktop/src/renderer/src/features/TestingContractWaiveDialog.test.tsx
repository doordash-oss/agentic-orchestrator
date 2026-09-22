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
import { useState } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { TestingContractItem, TestingContractSnapshot } from '../../../shared/ipc';
import { installAgenticoMock, ipcError } from '../test/agenticoMock';
import { itemLock, TestingContractWaiveDialog, waiveLabel } from './TestingContractWaiveDialog';

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
    item({ itemId: 'unit-tests', name: 'Unit tests', repo: 'api', allowWaiver: false }),
  ],
};

describe('TestingContractWaiveDialog', () => {
  it('locks waived rows and rows the contract forbids waiving', () => {
    expect(itemLock(contract.items[0]!)).toBeUndefined();
    expect(itemLock(contract.items[2]!)).toBe('waived');
    expect(itemLock(contract.items[3]!)).toBe('unwaivable');
  });

  it('counts the ticked checks in the primary verb', () => {
    expect(waiveLabel(0)).toBe('Waive checks');
    expect(waiveLabel(1)).toBe('Waive 1 check');
    expect(waiveLabel(2)).toBe('Waive 2 checks');
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

    // Check name plus repository is the row's identity and the checkbox's
    // accessible name; the id, source, and owner sit on a secondary line.
    expect(screen.getByRole('checkbox', { name: 'Manual review' })).toBeDisabled();
    expect(screen.getByText('Waived')).toBeVisible();
    expect(screen.getByText('Reviewed offline.')).toBeVisible();
    expect(screen.getByRole('checkbox', { name: 'Unit tests · api' })).toBeDisabled();
    expect(screen.getByText('Cannot be waived')).toBeVisible();
    expect(screen.getByText('Substitution allowed')).toBeVisible();
    expect(screen.queryByText('Substitute accepted')).not.toBeInTheDocument();
    expect(screen.getByText('Needs network')).toBeVisible();
    expect(screen.getByText('ui-capture').closest('small')).toHaveTextContent(
      'ui-capture · visual · agent',
    );

    const waive = screen.getByRole('button', { name: 'Waive checks' });
    expect(waive).toBeDisabled();
    const reason = screen.getByRole('textbox', { name: 'Waiver reason' });
    expect(reason).toBeRequired();
    await user.click(screen.getByRole('checkbox', { name: 'Deployment smoke test' }));
    expect(waive).toHaveTextContent('Waive 1 check');
    await user.click(screen.getByRole('checkbox', { name: 'Vendor console capture' }));
    expect(waive).toHaveTextContent('Waive 2 checks');
    expect(waive).toBeDisabled();
    await user.type(reason, 'Vendor console is unreachable from CI.');
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

  it('explains when the phase has no contract yet and offers a reload instead of the form', async () => {
    const mock = installAgenticoMock();
    mock.api.getTestingContract.mockResolvedValueOnce({ available: false });
    const user = userEvent.setup();
    render(
      <TestingContractWaiveDialog
        featureId="abcd1234ef567890"
        onClose={vi.fn()}
        onWaived={vi.fn()}
      />,
    );

    expect(await screen.findByText('This phase has no testing contract yet.')).toBeVisible();
    expect(screen.queryByRole('checkbox')).not.toBeInTheDocument();
    expect(screen.queryByRole('textbox', { name: 'Waiver reason' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^Waive/ })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Cancel' })).toBeEnabled();

    mock.api.getTestingContract.mockResolvedValueOnce(contract);
    await user.click(screen.getByRole('button', { name: 'Reload' }));
    expect(await screen.findByRole('checkbox', { name: 'Deployment smoke test' })).toBeVisible();
    expect(screen.getByRole('textbox', { name: 'Waiver reason' })).toBeVisible();
    expect(mock.api.getTestingContract).toHaveBeenCalledTimes(2);
  });

  it('hides the form when the contract has no items', async () => {
    const mock = installAgenticoMock();
    mock.api.getTestingContract.mockResolvedValue({ ...contract, items: [] });
    render(
      <TestingContractWaiveDialog
        featureId="abcd1234ef567890"
        onClose={vi.fn()}
        onWaived={vi.fn()}
      />,
    );

    expect(await screen.findByText('The contract has no items.')).toBeVisible();
    expect(screen.getByRole('button', { name: 'Reload' })).toBeVisible();
    expect(screen.queryByRole('textbox', { name: 'Waiver reason' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^Waive/ })).not.toBeInTheDocument();
  });

  it('hides the form while loading and when every row is locked', async () => {
    const mock = installAgenticoMock();
    mock.api.getTestingContract.mockResolvedValue({
      ...contract,
      items: [contract.items[2]!, contract.items[3]!],
    });
    render(
      <TestingContractWaiveDialog
        featureId="abcd1234ef567890"
        onClose={vi.fn()}
        onWaived={vi.fn()}
      />,
    );

    expect(screen.getByRole('status')).toHaveTextContent('Loading contract…');
    expect(screen.queryByRole('textbox', { name: 'Waiver reason' })).not.toBeInTheDocument();
    expect(
      await screen.findByText('Every item is already waived or cannot be waived.'),
    ).toBeVisible();
    expect(screen.getAllByRole('checkbox')).toHaveLength(2);
    expect(screen.queryByRole('textbox', { name: 'Waiver reason' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^Waive/ })).not.toBeInTheDocument();
  });

  it('surfaces a contract read failure with a retry and without closing', async () => {
    const mock = installAgenticoMock();
    mock.api.getTestingContract.mockRejectedValueOnce(
      ipcError('E_INTERNAL', 'contract read failed'),
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

    expect(await screen.findByRole('alert')).toBeVisible();
    expect(screen.queryByRole('checkbox')).not.toBeInTheDocument();
    expect(screen.queryByRole('textbox', { name: 'Waiver reason' })).not.toBeInTheDocument();
    expect(onClose).not.toHaveBeenCalled();

    mock.api.getTestingContract.mockResolvedValueOnce(contract);
    await user.click(screen.getByRole('button', { name: 'Retry' }));
    expect(await screen.findByRole('checkbox', { name: 'Deployment smoke test' })).toBeVisible();
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
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

    await user.click(await screen.findByRole('checkbox', { name: 'Deployment smoke test' }));
    await user.type(screen.getByRole('textbox', { name: 'Waiver reason' }), 'No vendor access.');
    await user.click(screen.getByRole('button', { name: 'Waive 1 check' }));

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

    await user.click(await screen.findByRole('checkbox', { name: 'Deployment smoke test' }));
    await user.click(screen.getByRole('checkbox', { name: 'Vendor console capture' }));
    await user.type(screen.getByRole('textbox', { name: 'Waiver reason' }), 'Offline vendor.');
    await user.click(screen.getByRole('button', { name: 'Waive 2 checks' }));

    expect(await screen.findByRole('alert')).toHaveTextContent(
      'testing contract changed since it was read',
    );
    expect(mock.api.waiveTestingContract).toHaveBeenCalledWith(
      expect.objectContaining({ roadmapPhase: 2, contractRevision: 3 }),
    );
    expect(await screen.findByText('Phase 3 contract · revision 1')).toBeVisible();
    expect(mock.api.getTestingContract).toHaveBeenCalledTimes(2);
    expect(screen.getByRole('dialog', { name: 'Waive contract items?' })).toBeVisible();
    expect(
      screen.queryByRole('checkbox', { name: 'Deployment smoke test' }),
    ).not.toBeInTheDocument();
    expect(screen.getByRole('checkbox', { name: 'Vendor console capture' })).toBeChecked();
    expect(screen.getByRole('checkbox', { name: 'Load test' })).not.toBeChecked();
    expect(onWaived).not.toHaveBeenCalled();
    expect(onClose).not.toHaveBeenCalled();

    // The retry binds to the reloaded snapshot.
    mock.api.waiveTestingContract.mockResolvedValue({
      result: 'waived',
      contractRevision: 2,
      waivedItems: ['ui-capture'],
    });
    await user.click(screen.getByRole('button', { name: 'Waive 1 check' }));
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
    expect(first.closest('.contract-waive-dialog__body')).toHaveClass(
      'contract-waive-dialog__body',
    );
    const footer = screen.getByRole('button', { name: 'Waive checks' }).closest('footer');
    expect(footer).toHaveClass('contract-waive-dialog__footer');
    expect(footer).not.toContainElement(first);
    expect(footer).toContainElement(screen.getByRole('textbox', { name: 'Waiver reason' }));
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
    await screen.findByRole('checkbox', { name: 'Deployment smoke test' });

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

  it('cycles Tab inside the dialog and hands focus back to the opener on close', async () => {
    const mock = installAgenticoMock();
    mock.api.getTestingContract.mockResolvedValue(contract);
    const user = userEvent.setup();

    function Host() {
      const [open, setOpen] = useState(false);
      return (
        <>
          <button type="button" onClick={() => setOpen(true)}>
            Waive contract items
          </button>
          <button type="button">Hide sidebar</button>
          {open ? (
            <TestingContractWaiveDialog
              featureId="abcd1234ef567890"
              onClose={() => setOpen(false)}
              onWaived={vi.fn()}
            />
          ) : null}
        </>
      );
    }
    render(<Host />);

    const opener = screen.getByRole('button', { name: 'Waive contract items' });
    await user.click(opener);
    const dialog = await screen.findByRole('dialog', { name: 'Waive contract items?' });
    await screen.findByRole('checkbox', { name: 'Deployment smoke test' });
    expect(dialog).toContainElement(document.activeElement as HTMLElement);

    // Tab past the last control wraps to the first; Shift+Tab wraps back.
    const cancel = screen.getByRole('button', { name: 'Cancel' });
    screen.getByRole('button', { name: 'Waive checks' }).focus();
    await user.tab();
    expect(dialog).toContainElement(document.activeElement as HTMLElement);
    expect(screen.getByRole('button', { name: 'Hide sidebar' })).not.toHaveFocus();
    for (let i = 0; i < 12 && document.activeElement !== cancel; i += 1) {
      await user.tab();
      expect(dialog).toContainElement(document.activeElement as HTMLElement);
    }
    expect(cancel).toHaveFocus();
    await user.tab({ shift: true });
    expect(dialog).toContainElement(document.activeElement as HTMLElement);

    await user.keyboard('{Escape}');
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    await waitFor(() => expect(opener).toHaveFocus());

    await user.click(opener);
    await screen.findByRole('checkbox', { name: 'Deployment smoke test' });
    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    await waitFor(() => expect(opener).toHaveFocus());
  });
});
