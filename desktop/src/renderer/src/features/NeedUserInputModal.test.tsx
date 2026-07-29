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

const verificationGate: Extract<AttentionItem, { kind: 'gate' }> = {
  ...gate,
  id: 'verification-gate-1',
  summary: 'Verification could not finish.',
  questions: [{ index: 1, prompt: 'How should Agentico continue?', answer: '' }],
  verification: {
    blockers: [
      {
        itemId: 'deploy',
        name: 'Deployment smoke test',
        repoName: 'repo-a',
        command: 'make deploy-smoke',
        reason: 'missing declared capability "Okta session"',
        capabilities: ['Okta session'],
        remediation: 'Make Okta session available, then retry verification.',
      },
    ],
    allowedActions: ['WAIVE', 'RETRY_AFTER_AUTH'],
  },
};

function Harness({
  item = gate,
  onAnswerLater = vi.fn(),
  onResolved = vi.fn().mockResolvedValue(undefined),
}: {
  item?: Extract<AttentionItem, { kind: 'gate' }>;
  onAnswerLater?: () => void;
  onResolved?: () => Promise<void>;
}): React.ReactElement {
  const [drafts, setDrafts] = useState(emptyAttentionDrafts);
  return (
    <NeedUserInputModal
      item={item}
      busy={false}
      drafts={drafts}
      setDrafts={setDrafts}
      onAnswerLater={onAnswerLater}
      onResolved={onResolved}
    />
  );
}

describe('NeedUserInputModal', () => {
  it('keeps legacy free-text drafting available without resuming', async () => {
    const mock = installAgenticoMock();
    const onAnswerLater = vi.fn();
    const user = userEvent.setup();
    render(<Harness onAnswerLater={onAnswerLater} />);

    expect(screen.getByRole('dialog', { name: 'Agent needs your input' })).toBeVisible();
    expect(screen.getByRole('button', { name: 'Resume agent' })).toBeDisabled();
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
      }),
    );
    expect(onResolved).toHaveBeenCalledOnce();
  });

  it('persists the newly selected retry action and resumes the exact cycle gate', async () => {
    const mock = installAgenticoMock();
    mock.api.saveGateDraft.mockResolvedValue({ result: 'saved' });
    mock.api.resolveGate.mockResolvedValue({ result: 'resumed' });
    const onResolved = vi.fn().mockResolvedValue(undefined);
    const user = userEvent.setup();
    render(<Harness item={verificationGate} onResolved={onResolved} />);

    expect(screen.getByRole('dialog', { name: 'Verification needs your input' })).toBeVisible();
    expect(screen.getByText('1 required check(s) could not run.')).toBeVisible();
    const retry = screen.getByRole('button', { name: 'Retry verification' });
    expect(retry).toBeDisabled();

    await user.click(screen.getByRole('radio', { name: /retry verification/ }));
    await waitFor(() =>
      expect(mock.api.saveGateDraft).toHaveBeenCalledWith({
        featureId: verificationGate.featureId,
        repoName: 'repo-a',
        cycleType: 'review-comments',
        answers: { [verificationGate.questions[0]!.prompt]: 'RETRY_AFTER_AUTH' },
      }),
    );

    expect(retry).toBeEnabled();
    await user.click(retry);
    await waitFor(() =>
      expect(mock.api.resolveGate).toHaveBeenCalledWith({
        featureId: verificationGate.featureId,
        repoName: 'repo-a',
        cycleType: 'review-comments',
      }),
    );
    expect(onResolved).toHaveBeenCalledOnce();
  });

  it('marks waiver as a warning and changes the resume action label', async () => {
    installAgenticoMock();
    const user = userEvent.setup();
    render(<Harness item={verificationGate} />);

    const waiver = screen.getByRole('radio', { name: /Waive blocked checks/ });
    await user.click(waiver);

    expect(waiver.closest('label')).toHaveAttribute('data-selected', 'true');
    expect(waiver.closest('label')).toHaveAttribute('data-tone', 'warning');
    const waiveAndResume = screen.getByRole('button', { name: 'Waive and resume' });
    expect(waiveAndResume).toBeEnabled();
    expect(waiveAndResume).toHaveAttribute('data-tone', 'warning');
  });

  it('serializes rapid decisions and persists the final waiver after a failed save', async () => {
    const mock = installAgenticoMock();
    let rejectRetrySave: (reason?: unknown) => void = () => undefined;
    mock.api.saveGateDraft
      .mockImplementationOnce(
        () =>
          new Promise((_, reject) => {
            rejectRetrySave = reject;
          }),
      )
      .mockResolvedValue({ result: 'saved' });
    const user = userEvent.setup();
    render(<Harness item={verificationGate} />);

    await user.click(screen.getByRole('radio', { name: /retry verification/ }));
    await waitFor(() => expect(mock.api.saveGateDraft).toHaveBeenCalledTimes(1));

    await user.click(screen.getByRole('radio', { name: /Waive blocked checks/ }));
    expect(mock.api.saveGateDraft).toHaveBeenCalledTimes(1);

    rejectRetrySave(new Error('retry draft save failed'));
    await waitFor(() => {
      expect(mock.api.saveGateDraft).toHaveBeenCalledTimes(2);
      expect(mock.api.saveGateDraft).toHaveBeenLastCalledWith({
        featureId: verificationGate.featureId,
        repoName: 'repo-a',
        cycleType: 'review-comments',
        answers: { [verificationGate.questions[0]!.prompt]: 'WAIVE' },
      });
    });
  });
});
