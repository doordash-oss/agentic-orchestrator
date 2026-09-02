/*
Copyright 2026 DoorDash, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

import { cleanup, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { RelationshipChildView } from '../../../../shared/ipc';
import { canonicalWarning } from '../../test/agenticoMock';
import { ExplainChatProvider } from '../../explainChat';
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
    warnings: [],
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

describe('RefactorHistory preserved diff', () => {
  it('renders an inlined body without asking for a fetch', () => {
    const onLoadFullHistory = vi.fn();
    render(
      <RefactorHistory
        entries={[historyEntry({ diffSummary: '3 files changed', hasDiffSummary: true })]}
        onLoadFullHistory={onLoadFullHistory}
      />,
    );
    expect(screen.getByText('3 files changed')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Load diff' })).toBeNull();
    expect(onLoadFullHistory).not.toHaveBeenCalled();
  });

  it('loads a flagged body on demand when the projection omitted it', async () => {
    const user = userEvent.setup();
    const entry = historyEntry({ hasDiffSummary: true });
    const onLoadFullHistory = vi
      .fn()
      .mockResolvedValue([{ ...entry, diffSummary: '3 files changed' }]);
    render(<RefactorHistory entries={[entry]} onLoadFullHistory={onLoadFullHistory} />);
    await user.click(screen.getByRole('button', { name: 'Load diff' }));
    expect(onLoadFullHistory).toHaveBeenCalledTimes(1);
    expect(await screen.findByText('3 files changed')).toBeInTheDocument();
  });

  it('reports a failed load instead of showing an empty diff', async () => {
    const user = userEvent.setup();
    const onLoadFullHistory = vi.fn().mockRejectedValue(new Error('offline'));
    render(
      <RefactorHistory
        entries={[historyEntry({ hasDiffSummary: true })]}
        onLoadFullHistory={onLoadFullHistory}
      />,
    );
    await user.click(screen.getByRole('button', { name: 'Load diff' }));
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Could not load the preserved history.',
    );
  });

  it('offers no diff affordance when no body was preserved', () => {
    render(<RefactorHistory entries={[historyEntry()]} onLoadFullHistory={vi.fn()} />);
    expect(screen.queryByText('Preserved diff (read-only)')).toBeNull();
  });
});

describe('RefactorHistory relationship warnings', () => {
  it('renders one status-role surface per warning with the repository under the disclosure', async () => {
    const user = userEvent.setup();
    render(
      <RefactorHistory
        entries={[
          historyEntry({
            warnings: [
              canonicalWarning({
                code: 'child_cleanup_incomplete',
                title: 'Cleanup incomplete',
                summary: 'The worktree for repository "repo-a" could not be removed.',
                context: { repositories: [{ name: 'repo-a', branch: 'agentico/pass-3' }] },
                diagnostics: 'remove worktree: directory busy',
              }),
              canonicalWarning({
                code: 'review_feedback_tail_incomplete',
                title: 'Review-feedback tail not delivered',
                summary: 'The review-feedback tail for repository "repo-b" was not delivered.',
                context: { repositories: [{ name: 'repo-b', branch: 'agentico/pass-3' }] },
              }),
            ],
          }),
        ]}
      />,
    );

    await user.click(screen.getByText('Pass history'));
    const surfaces = document.querySelectorAll('.refactor-history .error-surface');
    expect(surfaces).toHaveLength(2);
    surfaces.forEach((surface) => {
      expect(surface).toHaveAttribute('role', 'status');
      expect(surface.querySelector('.error-surface__label')).toHaveTextContent('Warning');
      expect(surface.querySelector('.error-surface__action')).toBeNull();
    });
    expect(screen.getByText('child_cleanup_incomplete')).toHaveClass('error-surface__code');
    expect(screen.getByText('review_feedback_tail_incomplete')).toHaveClass('error-surface__code');

    // Each repository sits under the surface's compact disclosure.
    surfaces.forEach((surface) => {
      const disclosure = surface.querySelector('details.error-surface__details--compact');
      expect(disclosure).not.toBeNull();
      expect(disclosure?.textContent).toMatch(/repo-[ab]/);
    });

    // The bespoke cleanup-warning list is gone.
    expect(document.querySelector('.refactor-history__warnings')).toBeNull();
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  });

  it('routes a warning as a transaction reference with its repository and the pass name', async () => {
    const user = userEvent.setup();
    const requestRoute = vi.fn();
    render(
      <ExplainChatProvider requestRoute={requestRoute}>
        <RefactorHistory
          entries={[
            historyEntry({
              warnings: [
                canonicalWarning({
                  code: 'child_cleanup_incomplete',
                  title: 'Cleanup incomplete',
                  summary: 'The worktree for repository "repo-a" could not be removed.',
                  context: { repositories: [{ name: 'repo-a', branch: 'agentico/pass-3' }] },
                }),
              ],
            }),
          ]}
        />
      </ExplainChatProvider>,
    );

    await user.click(screen.getByText('Pass history'));
    const surface = document.querySelector('.refactor-history .error-surface') as HTMLElement;
    expect(surface).not.toBeNull();
    await user.click(within(surface).getByRole('button', { name: 'Explain in chat' }));
    expect(requestRoute).toHaveBeenCalledTimes(1);
    expect(requestRoute).toHaveBeenCalledWith({
      target: 'ama',
      draft:
        'Explain the "Cleanup incomplete" error (child_cleanup_incomplete) on Settled pass and what I should do next.',
      autoSubmit: true,
      chatContext: {
        scope: 'transaction',
        code: 'child_cleanup_incomplete',
        featureId: 'child1234ef567890',
        repository: 'repo-a',
      },
    });
  });
});

describe('RefactorHistory truncated history', () => {
  const entries = [
    historyEntry({ id: 'child0001ef567890' }),
    historyEntry({ id: 'child0002ef567890' }),
  ];

  it('states the true total rather than the listed count', () => {
    render(<RefactorHistory entries={entries} total={12} truncated />);
    expect(screen.getByText('2 of 12')).toBeInTheDocument();
    expect(screen.getByText(/Showing the 2 most recent of 12 settled passes/)).toBeInTheDocument();
  });

  it('replaces the bounded list with the full history on demand', async () => {
    const user = userEvent.setup();
    const full = [...entries, historyEntry({ id: 'child0003ef567890', name: 'Older pass' })];
    const onLoadFullHistory = vi.fn().mockResolvedValue(full);
    render(
      <RefactorHistory
        entries={entries}
        total={3}
        truncated
        onLoadFullHistory={onLoadFullHistory}
      />,
    );
    await user.click(screen.getByRole('button', { name: 'Load the full history' }));
    expect(await screen.findByText('Older pass')).toBeInTheDocument();
    expect(screen.queryByText(/most recent of/)).toBeNull();
  });

  it('shows a plain count when the list carried every pass', () => {
    render(<RefactorHistory entries={entries} total={2} />);
    expect(screen.getByText('2')).toBeInTheDocument();
    expect(screen.queryByText(/most recent of/)).toBeNull();
  });
});
