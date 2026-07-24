import { cleanup, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { RunDetailView } from '../../../shared/ipc';
import { featureSnapshot } from '../test/agenticoMock';
import { AftercareDesk } from './AftercareDesk';

afterEach(cleanup);

const completedRun: RunDetailView = {
  runNumber: 8,
  artifactCount: 5,
  timing: { totalSeconds: 14700, byPhase: {} },
  cost: { totalUsd: 95.18, byPhase: {} },
};

describe('AftercareDesk', () => {
  it('turns a published feature into an operational handoff', async () => {
    const onOpenCycle = vi.fn();
    const user = userEvent.setup();
    render(
      <AftercareDesk
        snapshot={featureSnapshot({
          status: 'Published',
          activeRun: 8,
          repos: ['agentic-orchestrator'],
          actions: [{ id: 'rebase', enabled: true, disabledReasons: [] }],
          repoStatus: [
            {
              name: 'agentic-orchestrator',
              publishable: true,
              freshness: 'in sync',
              prUrl: 'https://example.test/pr/107',
            },
          ],
        })}
        run={completedRun}
        onOpenCycle={onOpenCycle}
      />,
    );

    const desk = screen.getByRole('region', { name: 'Feature aftercare' });
    expect(
      within(desk).getByRole('heading', {
        name: 'Published and ready for what comes next',
      }),
    ).toBeVisible();
    expect(within(desk).getByText('Run 8')).toBeVisible();

    const ledger = within(desk).getByRole('region', { name: 'Run ledger' });
    expect(within(ledger).getByText('4h 05m')).toBeVisible();
    expect(within(ledger).getByText('$95.18')).toBeVisible();
    expect(within(ledger).getByText('5')).toBeVisible();

    const repo = within(desk).getByRole('listitem', { name: 'agentic-orchestrator readiness' });
    expect(within(repo).getByText('In sync')).toBeVisible();
    expect(within(repo).getByRole('link', { name: 'PR open' })).toHaveAttribute(
      'href',
      'https://example.test/pr/107',
    );

    await user.click(within(desk).getByRole('button', { name: /Prepare rebase/ }));
    expect(onOpenCycle).toHaveBeenCalledWith('rebase');
  });

  it.each([
    ['CodeReady', 'Implementation complete'],
    ['Published', 'Published and ready for what comes next'],
    ['Done', 'Work complete'],
  ])('uses the %s handoff language', (status, heading) => {
    render(
      <AftercareDesk
        snapshot={featureSnapshot({ status, actions: [] })}
        run={null}
        onOpenCycle={vi.fn()}
      />,
    );

    expect(screen.getByRole('heading', { name: heading })).toBeVisible();
  });

  it('keeps missing run evidence honest and the empty runway useful', () => {
    render(
      <AftercareDesk
        snapshot={featureSnapshot({
          status: 'Done',
          activeRun: 3,
          actions: [{ id: 'rebase', enabled: false, disabledReasons: [] }],
          repoStatus: undefined,
        })}
        run={null}
        onOpenCycle={vi.fn()}
      />,
    );

    const ledger = screen.getByRole('region', { name: 'Run ledger' });
    expect(within(ledger).getAllByText('—')).toHaveLength(3);
    expect(screen.getByText('No maintenance cycle is available from this state.')).toBeVisible();
    expect(screen.getByText('Freshness unavailable')).toBeVisible();
    expect(screen.getByText('Publishability unavailable')).toBeVisible();
  });
});
