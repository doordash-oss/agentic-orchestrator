import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { featureSnapshot, installAgenticoMock } from '../test/agenticoMock';
import { CycleJourneys } from './CycleJourneys';

afterEach(cleanup);

describe('CycleJourneys aftercare focus', () => {
  it('features and focuses the cycle selected on the Aftercare runway', async () => {
    installAgenticoMock();
    render(
      <CycleJourneys
        featureId="abcd1234ef567890"
        snapshot={featureSnapshot({
          status: 'Published',
          actions: [{ id: 'refactor', enabled: true, disabledReasons: [] }],
        })}
        initialCycle="refactor"
        onComplete={vi.fn()}
      />,
    );

    const journey = screen.getByRole('region', { name: 'Refactor cycle' });
    expect(journey).toHaveAttribute('data-featured', 'true');
    expect(await screen.findByRole('heading', { name: 'Refactor' })).toHaveFocus();
  });

  it('does not feature a journey when cycles open from the generic menu', () => {
    installAgenticoMock();
    render(
      <CycleJourneys
        featureId="abcd1234ef567890"
        snapshot={featureSnapshot({
          status: 'Published',
          actions: [
            { id: 'rebase', enabled: true, disabledReasons: [] },
            { id: 'refactor', enabled: true, disabledReasons: [] },
          ],
        })}
        onComplete={vi.fn()}
      />,
    );

    expect(screen.getByRole('region', { name: 'Rebase cycle' })).toHaveAttribute(
      'data-featured',
      'false',
    );
    expect(screen.getByRole('region', { name: 'Refactor cycle' })).toHaveAttribute(
      'data-featured',
      'false',
    );
  });
});
