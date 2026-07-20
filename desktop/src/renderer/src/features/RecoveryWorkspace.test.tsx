import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';
import { installAgenticoMock } from '../test/agenticoMock';
import { RecoveryWorkspace } from './RecoveryWorkspace';

afterEach(cleanup);

describe('RecoveryWorkspace', () => {
  it('offers only the supported Resume and Kill outcomes', async () => {
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
          processAlive: true,
          allowedActions: ['resume', 'kill', 'skip'],
          defaultAction: 'resume',
        },
      ],
    });

    render(<RecoveryWorkspace />);

    expect(await screen.findByRole('button', { name: 'Resume' })).toBeVisible();
    expect(screen.getByRole('region', { name: 'Recovery workspace' })).toHaveAttribute(
      'data-attention',
      'true',
    );
    expect(screen.getByRole('button', { name: 'Kill' })).toBeVisible();
    expect(screen.queryByRole('button', { name: 'Skip' })).not.toBeInTheDocument();
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
