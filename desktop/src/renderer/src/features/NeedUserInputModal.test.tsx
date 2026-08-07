import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { AttentionItem } from '../../../shared/ipc';
import { emptyAttentionDrafts } from './AttentionInbox';
import { gateSheetTitle, gateStoppedSentence, NeedUserInputModal } from './NeedUserInputModal';
import { installAgenticoMock } from '../test/agenticoMock';
import { useState } from 'react';

afterEach(cleanup);

const gate: Extract<AttentionItem, { kind: 'gate' }> = {
  kind: 'gate',
  id: 'gate-1',
  featureId: 'abcd1234ef567890',
  waitingSince: '2026-07-25T00:00:00Z',
  repoName: 'repo-a',
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
  phase,
  onAnswerLater = vi.fn(),
  onResolved = vi.fn().mockResolvedValue(undefined),
}: {
  item?: Extract<AttentionItem, { kind: 'gate' }>;
  phase?: string;
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
      {...(phase === undefined ? {} : { phase })}
      onAnswerLater={onAnswerLater}
      onResolved={onResolved}
    />
  );
}

describe('gateSheetTitle', () => {
  it('spells the count out with the right plural', () => {
    expect(gateSheetTitle(1)).toBe('Answer one question to resume');
    expect(gateSheetTitle(2)).toBe('Answer two questions to resume');
    expect(gateSheetTitle(12)).toBe('Answer twelve questions to resume');
  });

  it('falls back to a numeral past the spelled-out range', () => {
    expect(gateSheetTitle(13)).toBe('Answer 13 questions to resume');
  });

  it('names the outcome alone when the gate carries no questions', () => {
    expect(gateSheetTitle(0)).toBe('Resume the paused agent');
  });
});

describe('gateStoppedSentence', () => {
  const waitingSince = new Date(Date.now() - 11 * 60_000).toISOString();

  it('names the phase and iteration when both are known', () => {
    expect(gateStoppedSentence({ phase: 'Implement', iteration: 3, waitingSince })).toBe(
      'Implement #3 stopped 11 minutes ago.',
    );
  });

  it('falls back to whichever subject is known', () => {
    expect(gateStoppedSentence({ phase: 'Implement', waitingSince })).toBe(
      'Implement stopped 11 minutes ago.',
    );
    expect(gateStoppedSentence({ iteration: 3, waitingSince })).toBe(
      'Iteration 3 stopped 11 minutes ago.',
    );
  });

  it('drops the sentence whole when a fact is missing or unparseable', () => {
    expect(gateStoppedSentence({ waitingSince })).toBeUndefined();
    expect(gateStoppedSentence({ phase: 'Implement', iteration: 3 })).toBeUndefined();
    expect(
      gateStoppedSentence({ phase: 'Implement', iteration: 3, waitingSince: 'not a date' }),
    ).toBeUndefined();
  });

  it('buckets in the same units as the rail, singular at one', () => {
    const ago = (ms: number) => new Date(Date.now() - ms).toISOString();
    const say = (ms: number) => gateStoppedSentence({ phase: 'Implement', waitingSince: ago(ms) });
    expect(say(5_000)).toBe('Implement stopped moments ago.');
    expect(say(60_000)).toBe('Implement stopped 1 minute ago.');
    expect(say(3 * 3_600_000)).toBe('Implement stopped 3 hours ago.');
    // 30h stays in hours, matching the rail's 48h boundary rather than diverging.
    expect(say(30 * 3_600_000)).toBe('Implement stopped 30 hours ago.');
    expect(say(50 * 3_600_000)).toBe('Implement stopped 2 days ago.');
  });
});

describe('NeedUserInputModal', () => {
  it('titles the plain branch with the spelled-out count and reduces the lede to known facts', () => {
    installAgenticoMock();
    render(<Harness />);

    expect(screen.getByRole('dialog', { name: 'Answer one question to resume' })).toBeVisible();
    // Neither a phase nor an iteration is known here, so the dynamic sentence
    // drops whole instead of naming a placeholder; the fixed one always shows.
    expect(
      screen.getByText('It resumes from the same checkpoint — nothing is re-run.'),
    ).toBeVisible();
    expect(screen.getByText('Clarify the delivery window.')).toBeVisible();
    expect(screen.getByText('Saved as you type')).toBeVisible();
  });

  it('names the phase, iteration, and stop time when the run supplies them', () => {
    installAgenticoMock();
    render(
      <Harness
        phase="Implement"
        item={{
          ...gate,
          iteration: 3,
          waitingSince: new Date(Date.now() - 11 * 60_000).toISOString(),
          questions: [
            { index: 1, prompt: 'Deployment window?', answer: '' },
            { index: 2, prompt: 'Rollback owner?', answer: '' },
          ],
        }}
      />,
    );

    expect(screen.getByRole('dialog', { name: 'Answer two questions to resume' })).toBeVisible();
    expect(screen.getByText(/^Implement #3 stopped 11 minutes ago\./)).toBeVisible();
  });

  it('saves the draft and answers later when the scrim is clicked', async () => {
    const mock = installAgenticoMock();
    const onAnswerLater = vi.fn();
    const user = userEvent.setup();
    render(<Harness onAnswerLater={onAnswerLater} />);

    await user.type(screen.getByLabelText(/Deployment window/), 'Next Tuesday.');
    fireEvent.mouseDown(document.querySelector('.sheet-scrim')!);

    await waitFor(() =>
      expect(mock.api.saveGateDraft).toHaveBeenCalledWith({
        featureId: gate.featureId,
        repoName: 'repo-a',
        answers: { '1': 'Next Tuesday.' },
      }),
    );
    expect(mock.api.resolveGate).not.toHaveBeenCalled();
    expect(onAnswerLater).toHaveBeenCalledOnce();
  });

  it('keeps legacy free-text drafting available without resuming', async () => {
    const mock = installAgenticoMock();
    const onAnswerLater = vi.fn();
    const user = userEvent.setup();
    render(<Harness onAnswerLater={onAnswerLater} />);

    expect(screen.getByRole('dialog', { name: 'Answer one question to resume' })).toBeVisible();
    expect(screen.getByRole('button', { name: 'Resume agent' })).toBeDisabled();
    await user.type(screen.getByLabelText(/Deployment window/), 'After verification passes.');
    await user.click(screen.getByRole('button', { name: 'Answer later' }));
    await waitFor(() =>
      expect(mock.api.saveGateDraft).toHaveBeenCalledWith({
        featureId: gate.featureId,
        repoName: 'repo-a',
        answers: { '1': 'After verification passes.' },
      }),
    );
    expect(mock.api.resolveGate).not.toHaveBeenCalled();
    expect(onAnswerLater).toHaveBeenCalledOnce();
  });

  it('requires every answer and resumes the exact need-user-input gate', async () => {
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
      }),
    );
    expect(onResolved).toHaveBeenCalledOnce();
  });

  it('persists the newly selected retry action and resumes the exact need-user-input gate', async () => {
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
        answers: { '1': 'RETRY_AFTER_AUTH' },
      }),
    );

    expect(retry).toBeEnabled();
    await user.click(retry);
    await waitFor(() =>
      expect(mock.api.resolveGate).toHaveBeenCalledWith({
        featureId: verificationGate.featureId,
        repoName: 'repo-a',
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
        answers: { '1': 'WAIVE' },
      });
    });
  });
});
