import { cleanup, render, screen, within } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';
import { featureSnapshot } from '../test/agenticoMock';
import { PhaseLadder } from './PhaseLadder';

afterEach(cleanup);

describe('PhaseLadder', () => {
  it('marks completed, active, and upcoming pipeline phases from the snapshot', () => {
    render(
      <PhaseLadder
        snapshot={featureSnapshot({
          pipeline: 'large',
          status: 'Designing',
          currentPhase: 'Design',
          setup: { status: 'done', attempt: 1, tasks: [] },
        })}
      />,
    );

    const ladder = screen.getByRole('group', { name: 'Feature pipeline' });
    const rows = within(ladder).getAllByRole('listitem');
    expect(rows).toHaveLength(9);
    expect(rows[0]).toHaveAttribute('data-state', 'done');
    expect(rows[0]).toHaveAccessibleName('Setup, completed');
    expect(rows[3]).toHaveAttribute('data-state', 'done');
    expect(rows[4]).toHaveAttribute('data-state', 'active');
    expect(rows[4]).toHaveAttribute('aria-current', 'step');
    expect(rows[4]).toHaveAccessibleName('Design, active, Designing');
    expect(rows[4]).toHaveTextContent('Design');
    expect(rows[4]).toHaveTextContent('Designing');
    expect(rows[5]).toHaveAttribute('data-state', 'upcoming');
    expect(rows[5]).toHaveAccessibleName('Plan, upcoming');
  });

  it('marks the current phase done when the run is at rest', () => {
    render(
      <PhaseLadder
        snapshot={featureSnapshot({
          pipeline: 'medium',
          status: 'Published',
          currentPhase: 'Publish',
          setup: { status: 'done', attempt: 1, tasks: [] },
        })}
      />,
    );

    const rows = within(screen.getByRole('group', { name: 'Feature pipeline' })).getAllByRole(
      'listitem',
    );
    expect(rows.at(-1)).toHaveAttribute('data-state', 'done');
    expect(rows.at(-1)).not.toHaveAttribute('aria-current');
  });

  it('exposes the error tone on a failed active phase', () => {
    render(
      <PhaseLadder
        snapshot={featureSnapshot({
          status: 'Failed',
          currentPhase: 'Plan',
          setup: { status: 'done', attempt: 1, tasks: [] },
        })}
      />,
    );

    expect(screen.getByRole('group', { name: 'Feature pipeline' })).toHaveAttribute(
      'data-tone',
      'error',
    );
  });
});
