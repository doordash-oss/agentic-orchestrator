import { cleanup, render, screen, within } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { featureSnapshot, installAgenticoMock } from '../test/agenticoMock';
import { CycleWorkspace } from './CycleWorkspace';
import { cyclePresentation } from './postImplementationModel';

afterEach(cleanup);

describe('CycleWorkspace', () => {
  it('gives a running cycle its own spine and standard live agent canvas', async () => {
    installAgenticoMock();
    const snapshot = featureSnapshot({
      status: 'Published',
      cycle: {
        type: 'rebase',
        status: 'running',
        count: 2,
        phase: 'final_review',
      },
      actions: [{ id: 'pause-stop', enabled: true, disabledReasons: [] }],
    });
    const presentation = cyclePresentation(snapshot);
    if (presentation === null) throw new Error('expected cycle presentation');
    render(
      <CycleWorkspace
        snapshot={snapshot}
        run={null}
        presentation={presentation}
        onRunMetrics={vi.fn()}
        onStop={vi.fn()}
        onResume={vi.fn()}
        onRetry={vi.fn()}
        onReturnToAftercare={vi.fn()}
        onOpenConfig={vi.fn()}
        onOpenRunRecord={vi.fn()}
        onOpenPullRequest={vi.fn()}
      />,
    );

    const spine = screen.getByRole('list', { name: 'Cycle progress' });
    expect(spine).toBeVisible();
    expect(within(spine).getByText('Final review').closest('li')).toHaveAttribute(
      'aria-current',
      'step',
    );
    expect(screen.getByRole('heading', { name: 'Live agent activity' })).toBeVisible();
    expect(screen.queryByText('Repository progress')).not.toBeInTheDocument();
  });

  it('keeps exact failure context with Retry and Return controls', () => {
    const snapshot = featureSnapshot({
      status: 'Published',
      cycle: { type: 'rebase', status: 'failed', count: 1, phase: 'final_review' },
      failure: { type: 'agent', message: 'Validation could not complete.' },
      actions: [
        {
          id: 'retry',
          enabled: false,
          disabledReasons: [{ code: 'busy', message: 'Wait for the current session.' }],
        },
      ],
    });
    const presentation = cyclePresentation(snapshot);
    if (presentation === null) throw new Error('expected cycle presentation');
    render(
      <CycleWorkspace
        snapshot={snapshot}
        run={null}
        presentation={presentation}
        onRunMetrics={vi.fn()}
        onStop={vi.fn()}
        onResume={vi.fn()}
        onRetry={vi.fn()}
        onReturnToAftercare={vi.fn()}
        onOpenConfig={vi.fn()}
        onOpenRunRecord={vi.fn()}
        onOpenPullRequest={vi.fn()}
      />,
    );
    expect(screen.getByRole('alert')).toHaveTextContent('Validation could not complete.');
    expect(screen.getByRole('button', { name: 'Retry cycle' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Retry cycle' })).toHaveAttribute(
      'title',
      'Wait for the current session.',
    );
    expect(screen.getByRole('button', { name: 'Return to Aftercare' })).toBeVisible();
  });

  it('offers resume and a stopped return path for interrupted cycles', () => {
    const snapshot = featureSnapshot({
      status: 'Interrupted',
      cycle: { type: 'rebase', status: 'interrupted', count: 3, phase: 'publish' },
      actions: [{ id: 'resume', enabled: true, disabledReasons: [] }],
    });
    const presentation = cyclePresentation(snapshot);
    if (presentation === null) throw new Error('expected cycle presentation');
    render(
      <CycleWorkspace
        snapshot={snapshot}
        run={null}
        presentation={presentation}
        onRunMetrics={vi.fn()}
        onStop={vi.fn()}
        onResume={vi.fn()}
        onRetry={vi.fn()}
        onReturnToAftercare={vi.fn()}
        onOpenConfig={vi.fn()}
        onOpenRunRecord={vi.fn()}
        onOpenPullRequest={vi.fn()}
      />,
    );
    expect(screen.getByRole('button', { name: 'Resume cycle' })).toBeVisible();
    expect(screen.getByRole('button', { name: 'Return to Aftercare' })).toBeVisible();
    expect(screen.queryByRole('button', { name: 'Stop cycle' })).not.toBeInTheDocument();
  });
});
