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

import { cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { installAgenticoMock, ipcError } from '../test/agenticoMock';
import { parseItemIds, TestingContractWaiveDialog } from './TestingContractWaiveDialog';

afterEach(cleanup);

const candidates = [
  { itemId: 'deploy-smoke', name: 'Deployment smoke test' },
  { itemId: 'ui-capture', name: 'Vendor console capture' },
];

describe('TestingContractWaiveDialog', () => {
  it('splits free-text ids on newlines and commas without repeats', () => {
    expect(parseItemIds(' a \nb, c\n\n a ')).toEqual(['a', 'b', 'c']);
    expect(parseItemIds('')).toEqual([]);
  });

  it('requires at least one item and a reason, then records the waiver', async () => {
    const mock = installAgenticoMock();
    mock.api.waiveTestingContract.mockResolvedValue({
      result: 'waived',
      contractRevision: 4,
      waivedItems: ['deploy-smoke', 'manual-review'],
    });
    const onClose = vi.fn();
    const onWaived = vi.fn().mockResolvedValue(undefined);
    const user = userEvent.setup();
    render(
      <TestingContractWaiveDialog
        featureId="abcd1234ef567890"
        candidates={candidates}
        onClose={onClose}
        onWaived={onWaived}
      />,
    );

    const dialog = screen.getByRole('dialog', { name: 'Waive contract items?' });
    expect(dialog).toBeVisible();
    const waive = screen.getByRole('button', { name: 'Waive items' });
    expect(waive).toBeDisabled();

    await user.click(screen.getByRole('checkbox', { name: 'deploy-smoke' }));
    expect(waive).toBeDisabled();
    await user.type(screen.getByRole('textbox', { name: 'Contract item ids' }), 'manual-review');
    await user.type(
      screen.getByRole('textbox', { name: 'Waiver reason' }),
      'Vendor console is unreachable from CI.',
    );
    expect(waive).toBeEnabled();

    await user.click(waive);
    await waitFor(() =>
      expect(mock.api.waiveTestingContract).toHaveBeenCalledWith({
        featureId: 'abcd1234ef567890',
        itemIds: ['deploy-smoke', 'manual-review'],
        reason: 'Vendor console is unreachable from CI.',
      }),
    );
    await waitFor(() => expect(onWaived).toHaveBeenCalledOnce());
    expect(onClose).toHaveBeenCalledOnce();
  });

  it('keeps the dialog open and surfaces the canonical error when the server rejects', async () => {
    const mock = installAgenticoMock();
    mock.api.waiveTestingContract.mockRejectedValue(
      ipcError('E_INTERNAL', 'the current phase has no testing contract yet'),
    );
    const onClose = vi.fn();
    const user = userEvent.setup();
    render(
      <TestingContractWaiveDialog
        featureId="abcd1234ef567890"
        candidates={[]}
        onClose={onClose}
        onWaived={vi.fn()}
      />,
    );

    expect(screen.queryByRole('checkbox')).not.toBeInTheDocument();
    await user.type(screen.getByRole('textbox', { name: 'Contract item ids' }), 'deploy-smoke');
    await user.type(screen.getByRole('textbox', { name: 'Waiver reason' }), 'No vendor access.');
    await user.click(screen.getByRole('button', { name: 'Waive items' }));

    expect(await screen.findByRole('alert')).toBeVisible();
    expect(onClose).not.toHaveBeenCalled();
    expect(screen.getByRole('dialog', { name: 'Waive contract items?' })).toBeVisible();

    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(onClose).toHaveBeenCalledOnce();
  });
});
