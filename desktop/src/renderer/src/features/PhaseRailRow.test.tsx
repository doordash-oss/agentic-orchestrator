import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { PhaseRail, PhaseRailTrack } from './PhaseRailRow';
import type { RailSegment } from './phaseRail';

function segment(overrides: Partial<RailSegment> = {}): RailSegment {
  return {
    id: 'implement',
    label: 'Implement',
    state: 'current',
    held: false,
    accessibleName: 'Implement, current',
    ...overrides,
  };
}

const fourSegments: RailSegment[] = [
  {
    id: 'setup',
    label: 'Setup',
    state: 'completed',
    held: false,
    accessibleName: 'Setup, completed',
  },
  {
    id: 'knowledge-base',
    label: 'Knowledge',
    state: 'completed',
    held: false,
    accessibleName: 'Knowledge Base, completed',
  },
  segment({ id: 'implement', label: 'Implement', state: 'current' }),
  {
    id: 'review',
    label: 'Review',
    state: 'upcoming',
    held: false,
    accessibleName: 'Review, upcoming',
  },
];

describe('PhaseRailTrack', () => {
  it('renders one segment per RailSegment with its state as a data attribute', () => {
    render(<PhaseRailTrack segments={fourSegments} label="Run phases" />);
    const group = screen.getByRole('group', { name: 'Run phases' });
    const items = group.querySelectorAll('.phase-rail__segment');
    expect(items).toHaveLength(4);
    expect(items[0]).toHaveAttribute('data-state', 'completed');
    expect(items[2]).toHaveAttribute('data-state', 'current');
    expect(items[3]).toHaveAttribute('data-state', 'upcoming');
  });

  it('carries aria-current="step" on exactly the current segment', () => {
    render(<PhaseRailTrack segments={fourSegments} />);
    const items = screen.getAllByRole('group')[0]!.querySelectorAll('.phase-rail__segment');
    expect(items[2]).toHaveAttribute('aria-current', 'step');
    for (const index of [0, 1, 3]) {
      expect(items[index]).not.toHaveAttribute('aria-current');
    }
  });

  it("encodes each segment's state in its accessible name, reusing the view-model verbatim", () => {
    render(<PhaseRailTrack segments={fourSegments} />);
    expect(screen.getByLabelText('Setup, completed')).toBeInTheDocument();
    expect(screen.getByLabelText('Knowledge Base, completed')).toBeInTheDocument();
    expect(screen.getByLabelText('Implement, current')).toBeInTheDocument();
    expect(screen.getByLabelText('Review, upcoming')).toBeInTheDocument();
  });

  it('never renders a dot when the caller omits one — the bare-track contract the connection shell and setup wizard rely on', () => {
    render(<PhaseRailTrack segments={fourSegments} />);
    expect(document.querySelector('.phase-rail__dot')).toBeNull();
  });

  it('renders a supplied dot only on the current segment', () => {
    render(<PhaseRailTrack segments={fourSegments} dot={<span data-testid="dot" />} />);
    const items = document.querySelectorAll('.phase-rail__segment');
    expect(items[2]!.querySelector('[data-testid="dot"]')).not.toBeNull();
    expect(items[0]!.querySelector('[data-testid="dot"]')).toBeNull();
  });

  it('applies the error tone only to the current segment, not upcoming/completed ones', () => {
    render(<PhaseRailTrack segments={fourSegments} tone="error" />);
    const items = document.querySelectorAll('.phase-rail__segment');
    expect(items[2]).toHaveAttribute('data-tone', 'error');
    expect(items[0]).toHaveAttribute('data-tone', 'progress');
    expect(items[3]).toHaveAttribute('data-tone', 'progress');
  });

  it('gives every segment the same layout class regardless of how long a phase took — equal width comes from CSS, not inline styles', () => {
    render(<PhaseRailTrack segments={fourSegments} />);
    for (const item of document.querySelectorAll('.phase-rail__segment')) {
      expect(item.getAttribute('style')).toBeNull();
    }
  });
});

describe('PhaseRail', () => {
  it('renders the trio right-aligned alongside the track', () => {
    render(
      <PhaseRail
        segments={fourSegments}
        hold={null}
        trio={[
          { kind: 'elapsed', label: 'Elapsed', value: '2h 55m', attention: false },
          { kind: 'cost', label: 'Cost', value: '$3.08', attention: false },
          { kind: 'context', label: 'Context', value: '42%', attention: false },
        ]}
      />,
    );
    expect(screen.getByText('Elapsed')).toBeInTheDocument();
    expect(screen.getByText('2h 55m')).toBeInTheDocument();
    expect(screen.getByText('Cost')).toBeInTheDocument();
    expect(screen.getByText('$3.08')).toBeInTheDocument();
    expect(screen.getByText('Context')).toBeInTheDocument();
    expect(screen.getByText('42%')).toBeInTheDocument();
  });

  it('renders no trio markup when every entry is omitted', () => {
    render(<PhaseRail segments={fourSegments} hold={null} trio={[]} />);
    expect(document.querySelector('.phase-rail__trio')).toBeNull();
  });

  it('shows the held dot with the native waiting-duration tooltip while a hold is open', () => {
    render(
      <PhaseRail
        segments={fourSegments}
        hold={{ kind: 'waiting', waitingSince: new Date(Date.now() - 6 * 60_000).toISOString() }}
        trio={[{ kind: 'waiting', label: 'Waiting', value: '6m', attention: true }]}
      />,
    );
    const dot = document.querySelector('.phase-rail__dot');
    expect(dot).not.toBeNull();
    expect(dot).toHaveAttribute('title', 'Held 6m for your answer');
  });

  it('falls back to a durationless tooltip when the hold carries no waitingSince', () => {
    render(<PhaseRail segments={fourSegments} hold={{ kind: 'paused' }} trio={[]} />);
    const dot = document.querySelector('.phase-rail__dot');
    expect(dot).toHaveAttribute('title', 'Held for your answer');
  });

  it('removes the dot once the hold resolves', () => {
    const { rerender } = render(
      <PhaseRail segments={fourSegments} hold={{ kind: 'paused' }} trio={[]} />,
    );
    expect(document.querySelector('.phase-rail__dot')).not.toBeNull();
    rerender(<PhaseRail segments={fourSegments} hold={null} trio={[]} />);
    expect(document.querySelector('.phase-rail__dot')).toBeNull();
  });

  it('marks the current segment attention-held while a hold is open', () => {
    const heldSegments = fourSegments.map((seg) =>
      seg.state === 'current' ? { ...seg, held: true } : seg,
    );
    render(<PhaseRail segments={heldSegments} hold={{ kind: 'paused' }} trio={[]} />);
    const items = document.querySelectorAll('.phase-rail__segment');
    expect(items[2]).toHaveAttribute('data-held', 'true');
  });

  it('shows the error tone on the current segment with no dot when the run failed (no hold)', () => {
    render(<PhaseRail segments={fourSegments} hold={null} tone="error" trio={[]} />);
    const items = document.querySelectorAll('.phase-rail__segment');
    expect(items[2]).toHaveAttribute('data-tone', 'error');
    expect(document.querySelector('.phase-rail__dot')).toBeNull();
  });
});
