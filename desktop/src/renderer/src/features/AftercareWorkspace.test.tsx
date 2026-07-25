import { cleanup, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { RunDetailView } from '../../../shared/ipc';
import { featureSnapshot } from '../test/agenticoMock';
import { AftercareWorkspace } from './AftercareWorkspace';

afterEach(cleanup);

const completedRun: RunDetailView = {
  runNumber: 8,
  artifactCount: 5,
  timing: { totalSeconds: 14700, byPhase: {} },
  cost: { totalUsd: 95.18, byPhase: {} },
};

describe('AftercareWorkspace', () => {
  it('shows CodeReady publication first and keeps only useful facts', () => {
    render(
      <AftercareWorkspace
        snapshot={featureSnapshot({
          name: 'Do not repeat me',
          status: 'CodeReady',
          activeRun: 8,
          actions: [
            { id: 'rebase', enabled: true, disabledReasons: [] },
            { id: 'publish', enabled: true, disabledReasons: [] },
          ],
        })}
        run={completedRun}
        onAction={vi.fn()}
        onOpenRunRecord={vi.fn()}
        onOpenChanges={vi.fn()}
      />,
    );

    expect(screen.getByRole('heading', { name: 'Implementation complete.' })).toBeVisible();
    expect(screen.getAllByRole('button').map((button) => button.textContent)).toContainEqual(
      expect.stringContaining('Prepare publish'),
    );
    expect(screen.getByText('$95.18')).toBeVisible();
    expect(screen.queryByText('Durable setup')).not.toBeInTheDocument();
    expect(screen.queryByText('Do not repeat me')).not.toBeInTheDocument();
  });

  it('omits Publish after publication and routes compact archive actions', async () => {
    const onAction = vi.fn();
    const onOpenRunRecord = vi.fn();
    const onOpenChanges = vi.fn();
    const user = userEvent.setup();
    render(
      <AftercareWorkspace
        snapshot={featureSnapshot({
          status: 'Published',
          actions: [{ id: 'refactor', enabled: true, disabledReasons: [] }],
        })}
        run={completedRun}
        onAction={onAction}
        onOpenRunRecord={onOpenRunRecord}
        onOpenChanges={onOpenChanges}
      />,
    );

    expect(screen.queryByText('Prepare publish')).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Run record' }));
    await user.click(screen.getByRole('button', { name: 'Changes' }));
    await user.click(screen.getByRole('button', { name: /Start another focused pass/ }));
    expect(onOpenRunRecord).toHaveBeenCalledOnce();
    expect(onOpenChanges).toHaveBeenCalledOnce();
    expect(onAction).toHaveBeenCalledWith(expect.objectContaining({ id: 'refactor' }));
  });
});
