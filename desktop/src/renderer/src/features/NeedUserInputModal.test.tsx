import { cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { AttentionItem } from '../../../shared/ipc';
import { emptyAttentionDrafts } from './AttentionInbox';
import { NeedUserInputModal } from './NeedUserInputModal';
import { installAgenticoMock } from '../test/agenticoMock';
import { useState } from 'react';

afterEach(cleanup);

const gate: Extract<AttentionItem, { kind: 'gate' }> = {
  kind: 'gate',
  id: 'gate-1',
  featureId: 'abcd1234ef567890',
  waitingSince: '2026-07-25T00:00:00Z',
  repoName: 'repo-a',
  cycleType: 'review-comments',
  summary: 'Clarify the delivery window.',
  questions: [{ index: 1, prompt: 'Deployment window?', answer: '' }],
};

function Harness({
  onAnswerLater = vi.fn(),
  onResolved = vi.fn().mockResolvedValue(undefined),
}: {
  onAnswerLater?: () => void;
  onResolved?: () => Promise<void>;
}): React.ReactElement {
  const [drafts, setDrafts] = useState(emptyAttentionDrafts);
  return (
    <NeedUserInputModal
      item={gate}
      busy={false}
      drafts={drafts}
      setDrafts={setDrafts}
      onAnswerLater={onAnswerLater}
      onResolved={onResolved}
    />
  );
}

describe('NeedUserInputModal', () => {
  it('saves free-text answers for later without resuming', async () => {
    const mock = installAgenticoMock();
    const onAnswerLater = vi.fn();
    const user = userEvent.setup();
    render(<Harness onAnswerLater={onAnswerLater} />);

    expect(screen.getByRole('dialog', { name: 'Agent needs your input' })).toBeVisible();
    await user.type(screen.getByLabelText(/Deployment window/), 'After verification passes.');
    await user.click(screen.getByRole('button', { name: 'Answer later' }));
    await waitFor(() =>
      expect(mock.api.saveGateDraft).toHaveBeenCalledWith({
        featureId: gate.featureId,
        repoName: 'repo-a',
        cycleType: 'review-comments',
        answers: { 'Deployment window?': 'After verification passes.' },
      }),
    );
    expect(mock.api.resolveGate).not.toHaveBeenCalled();
    expect(onAnswerLater).toHaveBeenCalledOnce();
  });

  it('requires every answer and resumes the exact cycle gate', async () => {
    const mock = installAgenticoMock();
    mock.api.saveGateDraft.mockResolvedValue({ result: 'saved' });
    mock.api.resolveGate.mockResolvedValue({ result: 'resumed' });
    const onResolved = vi.fn().mockResolvedValue(undefined);
    const user = userEvent.setup();
    render(<Harness onResolved={onResolved} />);

    const resume = screen.getByRole('button', { name: 'Resume agent' });
    expect(resume).toBeDisabled();
    await user.type(screen.getByLabelText(/Deployment window/), 'Tomorrow morning.');
    await user.click(resume);
    await waitFor(() =>
      expect(mock.api.resolveGate).toHaveBeenCalledWith({
        featureId: gate.featureId,
        repoName: 'repo-a',
        cycleType: 'review-comments',
        decision: 'resume',
      }),
    );
    expect(onResolved).toHaveBeenCalledOnce();
  });
});
