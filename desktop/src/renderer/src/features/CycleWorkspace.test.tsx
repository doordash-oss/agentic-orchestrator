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
        type: 'review-comments',
        status: 'running',
        count: 2,
        phase: 'address_validate',
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
        onRetry={vi.fn()}
        onReturnToAftercare={vi.fn()}
        onOpenRunRecord={vi.fn()}
      />,
    );

    const spine = screen.getByRole('list', { name: 'Cycle progress' });
    expect(spine).toBeVisible();
    expect(within(spine).getByText('Address & validate').closest('li')).toHaveAttribute(
      'aria-current',
      'step',
    );
    expect(screen.getByRole('heading', { name: 'Live agent activity' })).toBeVisible();
    expect(screen.queryByText('Repository progress')).not.toBeInTheDocument();
    expect(screen.queryByText('Comment worklist')).not.toBeInTheDocument();
  });

  it('keeps exact failure context with Retry and Return controls', () => {
    const snapshot = featureSnapshot({
      status: 'Published',
      cycle: { type: 'refactor', status: 'failed', count: 1, phase: 'implement_validate' },
      failure: { type: 'agent', message: 'Validation could not complete.' },
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
        onRetry={vi.fn()}
        onReturnToAftercare={vi.fn()}
        onOpenRunRecord={vi.fn()}
      />,
    );
    expect(screen.getByRole('alert')).toHaveTextContent('Validation could not complete.');
    expect(screen.getByRole('button', { name: 'Retry cycle' })).toBeVisible();
    expect(screen.getByRole('button', { name: 'Return to Aftercare' })).toBeVisible();
  });
});
