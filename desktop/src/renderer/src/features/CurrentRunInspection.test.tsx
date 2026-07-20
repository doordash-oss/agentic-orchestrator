import { cleanup, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it } from 'vitest';
import { installAgenticoMock } from '../test/agenticoMock';
import { CurrentRunInspection } from './CurrentRunInspection';

afterEach(cleanup);

describe('CurrentRunInspection', () => {
  it('shows authoritative live activity and loads bounded artifacts and logs', async () => {
    const user = userEvent.setup();
    const mock = installAgenticoMock();
    mock.api.getLivePreview.mockResolvedValue({
      featureId: 'abcd1234ef567890',
      activity: 'Running implementation',
      contextPercentage: 42,
      totalSeconds: 73,
      totalUsd: 0.12,
      transcript: [],
    });
    mock.api.listRunArtifacts.mockResolvedValue({
      artifacts: [
        {
          id: 'phase-plan',
          runNumber: 2,
          phase: 'Implement',
          contentAvailable: true,
        },
      ],
    });
    mock.api.getRunArtifactContent.mockResolvedValue({
      id: 'phase-plan',
      offset: 0,
      limit: 65536,
      size: 18,
      text: '# Current artifact',
      truncated: false,
    });
    mock.api.getRunLogContent.mockResolvedValue({
      id: 'session',
      offset: 0,
      limit: 65536,
      size: 16,
      text: '\u001b[31mcurrent log\u001b[0m',
      truncated: false,
    });

    render(<CurrentRunInspection featureId="abcd1234ef567890" runNumber={2} />);

    expect(await screen.findByText('Running implementation')).toBeVisible();
    expect(screen.getByText('42%')).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Open artifact phase-plan' }));
    expect(await screen.findByLabelText('Current run artifact content')).toHaveTextContent(
      '# Current artifact',
    );

    await user.click(screen.getByRole('button', { name: 'Open session log' }));
    expect(await screen.findByLabelText('Current run log content')).toHaveTextContent(
      'current log',
    );
    expect(screen.getByLabelText('Current run log content')).not.toHaveTextContent('\u001b');
    expect(mock.api.getRunArtifactContent).toHaveBeenCalledWith(
      expect.objectContaining({ artifactId: 'phase-plan', limit: 64 * 1024 }),
    );
  });
});
