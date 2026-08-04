import { act, cleanup, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { installAgenticoMock } from '../test/agenticoMock';
import { ArchiveMode } from './ArchiveMode';

afterEach(cleanup);

describe('ArchiveMode activity', () => {
  it('pauses hidden invalidations and refreshes without showing loading on activation', async () => {
    const sealedRun = {
      runNumber: 7,
      startedAt: '2026-08-03T10:00:00.000Z',
      sealedAt: '2026-08-03T11:00:00.000Z',
      sealReason: 'Completed',
      currentPhase: 'Implement',
      artifactCount: 0,
    };
    const mock = installAgenticoMock();
    mock.api.listRuns.mockResolvedValue({
      runs: [sealedRun],
      page: 1,
      pageSize: 5,
      total: 1,
      totalPages: 1,
    });
    mock.api.getRun.mockResolvedValue(sealedRun);

    const archive = (active: boolean) => (
      <ArchiveMode
        featureId="abcd1234ef567890"
        selectedRunNumber={7}
        currentRunNumber={8}
        active={active}
        onReturnToCurrent={vi.fn()}
        onSelectRun={vi.fn()}
      />
    );
    const view = render(archive(true));
    expect(await screen.findByLabelText('Run selection')).toHaveValue('7');
    expect(screen.getByText('Sealed run · Read only')).toBeVisible();

    view.rerender(archive(false));
    const hiddenListCalls = mock.api.listRuns.mock.calls.length;
    const hiddenRunCalls = mock.api.getRun.mock.calls.length;
    act(() =>
      mock.emitAppEvent({
        type: 'invalidated',
        kind: 'feature.updated',
        featureId: 'abcd1234ef567890',
      }),
    );
    expect(mock.api.listRuns).toHaveBeenCalledTimes(hiddenListCalls);
    expect(mock.api.getRun).toHaveBeenCalledTimes(hiddenRunCalls);
    expect(screen.getByText('Sealed run · Read only')).toBeVisible();

    view.rerender(archive(true));
    expect(screen.getByText('Sealed run · Read only')).toBeVisible();
    expect(screen.queryByText('Loading run history…')).not.toBeInTheDocument();
    await waitFor(() => expect(mock.api.listRuns).toHaveBeenCalledTimes(hiddenListCalls + 1));
    await waitFor(() => expect(mock.api.getRun).toHaveBeenCalledTimes(hiddenRunCalls + 1));
  });
});
