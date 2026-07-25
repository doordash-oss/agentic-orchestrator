import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { featureSnapshot, installAgenticoMock } from '../../test/agenticoMock';
import { RebaseModal } from './RebaseModal';

afterEach(cleanup);

function props() {
  return {
    featureId: 'abcd1234ef567890',
    snapshot: featureSnapshot({
      status: 'Published',
      actions: [{ id: 'rebase', enabled: true, disabledReasons: [] }],
    }),
    onCancel: vi.fn(),
    onDispatched: vi.fn(),
  };
}

describe('RebaseModal', () => {
  it('loads the guarded preflight on mount and starts with its source revision', async () => {
    const mock = installAgenticoMock();
    mock.api.preflightRebase.mockResolvedValue({
      featureId: 'abcd1234ef567890',
      sourceRevision: 'revision-7',
      repos: [
        {
          repo: 'repo-a',
          target: 'origin/main',
          publishable: true,
          freshness: 'behind',
          behind: true,
        },
      ],
    });
    mock.api.startRebase.mockResolvedValue({
      featureId: 'abcd1234ef567890',
      cycleType: 'rebase',
      result: 'started',
    });
    const modalProps = props();
    render(<RebaseModal {...modalProps} />);

    expect(mock.api.preflightRebase).toHaveBeenCalledWith({
      featureId: 'abcd1234ef567890',
    });
    expect(await screen.findByText('repo-a')).toBeVisible();
    expect(screen.getByText('origin/main')).toBeVisible();
    expect(screen.getByText('Behind main')).toBeVisible();

    fireEvent.click(screen.getByRole('button', { name: 'Start rebase' }));
    await waitFor(() =>
      expect(mock.api.startRebase).toHaveBeenCalledWith({
        featureId: 'abcd1234ef567890',
        sourceRevision: 'revision-7',
      }),
    );
    expect(modalProps.onDispatched).toHaveBeenCalled();
    expect(modalProps.onCancel).toHaveBeenCalled();
  });

  it('offers a retry when preflight loading fails', async () => {
    const mock = installAgenticoMock();
    mock.api.preflightRebase
      .mockRejectedValueOnce(new Error('E_PREFLIGHT: unable to inspect repositories'))
      .mockResolvedValueOnce({
        featureId: 'abcd1234ef567890',
        sourceRevision: 'revision-8',
        repos: [],
      });
    render(<RebaseModal {...props()} />);

    expect(await screen.findByRole('alert')).toHaveTextContent('unable to inspect repositories');
    fireEvent.click(screen.getByRole('button', { name: 'Retry preflight' }));
    await waitFor(() => expect(mock.api.preflightRebase).toHaveBeenCalledTimes(2));
  });

  it('shows a stale-start error while refreshing the guarded preflight', async () => {
    const mock = installAgenticoMock();
    mock.api.preflightRebase
      .mockResolvedValueOnce({
        featureId: 'abcd1234ef567890',
        sourceRevision: 'revision-stale',
        repos: [],
      })
      .mockResolvedValueOnce({
        featureId: 'abcd1234ef567890',
        sourceRevision: 'revision-fresh',
        repos: [],
      });
    mock.api.startRebase.mockRejectedValue(
      new Error('conflict: Repository state changed. Review the refreshed preflight.'),
    );
    render(<RebaseModal {...props()} />);

    fireEvent.click(await screen.findByRole('button', { name: 'Start rebase' }));

    expect(await screen.findByRole('alert')).toHaveTextContent('Repository state changed');
    await waitFor(() => expect(mock.api.preflightRebase).toHaveBeenCalledTimes(2));
  });
});
