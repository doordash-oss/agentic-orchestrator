import { cleanup, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it } from 'vitest';
import { featureSnapshot, installAgenticoMock } from '../test/agenticoMock';
import { BulkPreviewPanel } from './BulkPreviewPanel';

afterEach(cleanup);

describe('BulkPreviewPanel', () => {
  it('renders both eligible and excluded rows from a fresh preview', async () => {
    const mock = installAgenticoMock();
    mock.api.bulkPreview.mockResolvedValue({
      previewId: 'preview-1',
      eligible: [
        { featureId: 'feature-a', featureName: 'Feature A', action: 'resume', enabled: true },
        { featureId: 'feature-b', featureName: 'Feature B', action: 'retry', enabled: true },
      ],
      excluded: [
        {
          featureId: 'feature-c',
          featureName: 'Feature C',
          action: 'resume',
          enabled: false,
          disabledReason: 'Already running.',
        },
      ],
    });

    render(<BulkPreviewPanel />);
    await userEvent.click(screen.getByRole('button', { name: 'Fresh preview' }));

    const eligibleSection = await screen.findByText('Eligible (2)');
    expect(eligibleSection).toBeVisible();
    expect(screen.getByText('Feature A')).toBeVisible();
    expect(screen.getByText('Feature B')).toBeVisible();

    expect(screen.getByText('Excluded (1)')).toBeVisible();
    expect(screen.getByText('Feature C')).toBeVisible();
    expect(screen.getByText('Already running.')).toBeVisible();
  });

  it('dispatches the eligible queue sequentially and records a success outcome per row', async () => {
    const mock = installAgenticoMock();
    mock.api.bulkPreview.mockResolvedValue({
      previewId: 'preview-1',
      eligible: [
        { featureId: 'feature-a', featureName: 'Feature A', action: 'resume', enabled: true },
        { featureId: 'feature-b', featureName: 'Feature B', action: 'retry', enabled: true },
      ],
      excluded: [],
    });
    mock.api.getFeature.mockImplementation((featureId: string) =>
      Promise.resolve(
        featureSnapshot({
          id: featureId,
          actions: [
            { id: 'resume', enabled: true, disabledReasons: [] },
            { id: 'retry', enabled: true, disabledReasons: [] },
          ],
        }),
      ),
    );

    render(<BulkPreviewPanel />);
    await userEvent.click(screen.getByRole('button', { name: 'Fresh preview' }));
    await screen.findByText('Eligible (2)');

    await userEvent.click(screen.getByRole('button', { name: 'Run 2 actions' }));

    await screen.findByText('Queue complete.');
    expect(mock.api.dispatchFeatureAction).toHaveBeenCalledTimes(2);
    expect(mock.api.dispatchFeatureAction).toHaveBeenNthCalledWith(1, {
      featureId: 'feature-a',
      action: 'resume',
    });
    expect(mock.api.dispatchFeatureAction).toHaveBeenNthCalledWith(2, {
      featureId: 'feature-b',
      action: 'retry',
    });
    expect(screen.getByText('2 succeeded · 0 failed')).toBeVisible();
    const rowA = screen.getByText('Feature A').closest('li')!;
    expect(within(rowA).getByText('✓')).toBeVisible();
  });

  it('cancel stops the queue after the row currently in flight', async () => {
    const mock = installAgenticoMock();
    mock.api.bulkPreview.mockResolvedValue({
      previewId: 'preview-1',
      eligible: [
        { featureId: 'feature-a', featureName: 'Feature A', action: 'resume', enabled: true },
        { featureId: 'feature-b', featureName: 'Feature B', action: 'retry', enabled: true },
      ],
      excluded: [],
    });
    let releaseFirstDispatch: (() => void) | undefined;
    mock.api.getFeature.mockImplementation((featureId: string) =>
      Promise.resolve(
        featureSnapshot({
          id: featureId,
          actions: [
            { id: 'resume', enabled: true, disabledReasons: [] },
            { id: 'retry', enabled: true, disabledReasons: [] },
          ],
        }),
      ),
    );
    mock.api.dispatchFeatureAction.mockImplementation(
      ({ featureId, action }: { featureId: string; action: string }) =>
        new Promise((resolve) => {
          releaseFirstDispatch = () =>
            resolve({ featureId, action, result: 'started', sessionIds: [] });
        }),
    );

    render(<BulkPreviewPanel />);
    await userEvent.click(screen.getByRole('button', { name: 'Fresh preview' }));
    await screen.findByText('Eligible (2)');

    await userEvent.click(screen.getByRole('button', { name: 'Run 2 actions' }));
    await screen.findByRole('button', { name: 'Cancel after current' });
    await userEvent.click(screen.getByRole('button', { name: 'Cancel after current' }));
    releaseFirstDispatch?.();

    await screen.findByText(/Cancelled after current action — 1 not started/);
    expect(mock.api.dispatchFeatureAction).toHaveBeenCalledTimes(1);
    const rowB = screen.getByText('Feature B').closest('li')!;
    expect(within(rowB).getByText('⊘')).toBeVisible();
  });
});
