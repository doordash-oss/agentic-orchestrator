import { render, screen } from '@testing-library/react';
import { beforeEach, describe, expect, it } from 'vitest';
import { PhaseSpine } from './PhaseSpine';
import { matchMediaState } from '../test/setup';

const stages = [
  { id: 'resolve-runtime', label: 'Resolve' },
  { id: 'discover', label: 'Discover' },
  { id: 'attach', label: 'Attach' },
  { id: 'authenticate', label: 'Auth' },
  { id: 'ready', label: 'Ready' },
] as const;

beforeEach(() => {
  matchMediaState.reducedMotion = false;
});

describe('PhaseSpine', () => {
  it('renders every stage label on the rail', () => {
    render(<PhaseSpine stages={stages} activeIndex={1} tone="progress" />);
    for (const stage of stages) {
      expect(screen.getByText(stage.label)).toBeInTheDocument();
    }
  });

  it('marks exactly the active stage with aria-current="step"', () => {
    render(<PhaseSpine stages={stages} activeIndex={2} tone="progress" />);
    const items = screen.getAllByRole('listitem');
    expect(items).toHaveLength(5);
    expect(items[2]).toHaveAttribute('aria-current', 'step');
    for (const idx of [0, 1, 3, 4]) {
      expect(items[idx]).not.toHaveAttribute('aria-current');
    }
  });

  it('exposes done/active/upcoming states as data attributes', () => {
    render(<PhaseSpine stages={stages} activeIndex={2} tone="progress" />);
    const items = screen.getAllByRole('listitem');
    expect(items[0]).toHaveAttribute('data-state', 'done');
    expect(items[1]).toHaveAttribute('data-state', 'done');
    expect(items[2]).toHaveAttribute('data-state', 'active');
    expect(items[3]).toHaveAttribute('data-state', 'upcoming');
    expect(items[4]).toHaveAttribute('data-state', 'upcoming');
  });

  it('flags the active tick with an error tone when the connection fails', () => {
    render(<PhaseSpine stages={stages} activeIndex={1} tone="error" />);
    const items = screen.getAllByRole('listitem');
    expect(items[1]).toHaveAttribute('data-tone', 'error');
  });

  it('is labelled as a lifecycle group for assistive tech', () => {
    render(<PhaseSpine stages={stages} activeIndex={0} tone="progress" />);
    expect(screen.getByRole('group', { name: /connection lifecycle/i })).toBeInTheDocument();
  });

  it('renders long phase names in full with a title fallback', () => {
    const longStages = [
      { id: 'setup', label: 'Setup' },
      { id: 'knowledge-base', label: 'Knowledge Base' },
      { id: 'implement', label: 'Implement' },
    ] as const;
    render(<PhaseSpine stages={longStages} activeIndex={1} tone="progress" />);
    const label = screen.getByText('Knowledge Base');
    expect(label).toHaveTextContent('Knowledge Base');
    expect(label.closest('.phase-spine__label')).toHaveAttribute('title', 'Knowledge Base');
    expect(label.closest('.phase-spine__label')).toHaveAttribute('aria-label', 'Knowledge Base');
  });

  it('provides intentional compact labels without splitting phase names', () => {
    const longStages = [
      { id: 'knowledge-base', label: 'Knowledge Base' },
      { id: 'inquire', label: 'Inquire' },
      { id: 'implement', label: 'Implement' },
    ] as const;
    render(<PhaseSpine stages={longStages} activeIndex={1} tone="progress" />);

    expect(screen.getByText('KB')).toHaveAttribute('aria-hidden', 'true');
    expect(screen.getByText('INQ')).toHaveAttribute('aria-hidden', 'true');
    expect(screen.getByText('IMP')).toHaveAttribute('aria-hidden', 'true');
  });

  it('settles the active stage as done when the run is at rest', () => {
    render(<PhaseSpine stages={stages} activeIndex={2} tone="progress" atRest />);
    const items = screen.getAllByRole('listitem');
    expect(items[2]).toHaveAttribute('data-state', 'done');
    expect(items[2]).not.toHaveAttribute('aria-current');
    expect(items[2]!.querySelector('.phase-spine__needle')).toBeNull();
    expect(items[3]).toHaveAttribute('data-state', 'upcoming');
  });

  it('pulses the active needle only when motion is allowed', () => {
    matchMediaState.reducedMotion = false;
    const { unmount } = render(<PhaseSpine stages={stages} activeIndex={1} tone="progress" />);
    expect(screen.getAllByRole('listitem')[1]!.querySelector('.phase-spine__needle')).toHaveClass(
      'phase-spine__needle--pulse',
    );
    unmount();

    matchMediaState.reducedMotion = true;
    render(<PhaseSpine stages={stages} activeIndex={1} tone="progress" />);
    expect(
      screen.getAllByRole('listitem')[1]!.querySelector('.phase-spine__needle'),
    ).not.toHaveClass('phase-spine__needle--pulse');
  });
});
