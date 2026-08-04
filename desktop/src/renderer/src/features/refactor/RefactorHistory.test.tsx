import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';
import type { RelationshipChildView } from '../../../../shared/ipc';
import { RefactorHistory } from './RefactorHistory';

afterEach(cleanup);

function historyEntry(overrides: Partial<RelationshipChildView> = {}): RelationshipChildView {
  return {
    id: 'child1234ef567890',
    name: 'Settled pass',
    kind: 'refactor',
    displayToken: 'refactor:child1234ef567890',
    displayState: 'Closed',
    pipeline: 'medium',
    status: 'Done',
    relationshipState: 'closed',
    startedAt: '2026-07-30T10:00:00Z',
    closedAt: '2026-08-01T10:00:00Z',
    cost: { totalUsd: 0, byPhase: {} },
    integrationState: 'pending',
    attention: [],
    cleanupWarnings: [],
    ...overrides,
  };
}

describe('RefactorHistory kind label', () => {
  it('labels a closed refactor pass "Refactor"', () => {
    render(<RefactorHistory entries={[historyEntry({ kind: 'refactor' })]} />);
    expect(screen.getByText('Refactor')).toBeInTheDocument();
  });

  it('labels a closed review-feedback pass "Review feedback"', () => {
    render(<RefactorHistory entries={[historyEntry({ kind: 'review-feedback' })]} />);
    expect(screen.getByText('Review feedback')).toBeInTheDocument();
  });

  it('labels a closed rebase pass "Rebase"', () => {
    render(<RefactorHistory entries={[historyEntry({ kind: 'rebase' })]} />);
    expect(screen.getByText('Rebase')).toBeInTheDocument();
  });

  it('falls back to a neutral "Pass" for an unknown kind', () => {
    render(<RefactorHistory entries={[historyEntry({ kind: 'mystery' })]} />);
    expect(screen.getByText('Pass')).toBeInTheDocument();
  });
});
