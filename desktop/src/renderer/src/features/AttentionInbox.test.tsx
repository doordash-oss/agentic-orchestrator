import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import { afterEach, describe, expect, it } from 'vitest';
import type { AttentionItem } from '../../../shared/ipc';
import { installAgenticoMock } from '../test/agenticoMock';
import { AttentionInbox, emptyAttentionDrafts, type AttentionDrafts } from './AttentionInbox';

afterEach(cleanup);

const askBundle: AttentionItem = {
  kind: 'questions',
  id: 'ask-bundle',
  featureId: 'feature-1',
  sessionId: 'session-1',
  phase: 'Implement',
  waitingSince: '2026-07-15T10:00:00.000Z',
  questions: [
    {
      key: 'Which verification tracks should be included?',
      header: 'Verification tracks',
      multiSelect: true,
      options: [
        {
          label: 'Unit tests',
          description: 'Exercise renderer and server contracts.',
          confidence: 0.5,
        },
        {
          label: 'Packaged smoke',
          description: 'Drive the shipped Electron app.',
          confidence: 0.5,
        },
      ],
    },
    {
      key: 'Which note should be attached to the evidence bundle?',
      header: 'Evidence note',
      multiSelect: false,
      options: [],
    },
  ],
};

const permissionItem: AttentionItem = {
  kind: 'permission',
  id: 'perm-stale',
  featureId: 'feature-1',
  sessionId: 'session-1',
  phase: 'Implement',
  toolName: 'Bash',
  input: { command: 'printf stale-resolution' },
  waitingSince: '2026-07-15T10:00:00.000Z',
};

const secondPermissionItem: AttentionItem = {
  kind: 'permission',
  id: 'perm-next',
  featureId: 'feature-1',
  sessionId: 'session-1',
  phase: 'Implement',
  toolName: 'Bash',
  input: { command: 'printf next-item' },
  waitingSince: '2026-07-15T10:01:00.000Z',
};

const gateItem: AttentionItem = {
  kind: 'gate',
  id: 'feature-1::',
  featureId: 'feature-1',
  waitingSince: '2026-07-15T10:00:00.000Z',
  summary: 'Choose the deployment window before implementation continues.',
  iteration: 6,
  questions: [
    {
      index: 1,
      prompt: 'Which deployment window should implementation use?',
      answer: '',
    },
  ],
};

const cycleGateItem: AttentionItem = {
  kind: 'gate',
  id: 'feature-1::repo-a::review-comments',
  featureId: 'feature-1',
  repoName: 'repo-a',
  cycleType: 'review-comments',
  waitingSince: '2026-07-15T10:00:00.000Z',
  summary: 'Resolve the review-comments cycle before continuing.',
  iteration: 4,
  questions: [
    {
      index: 1,
      prompt: 'Which review comment batch should this cycle use?',
      answer: '',
    },
  ],
};

function renderInbox(items: AttentionItem[], refresh: () => Promise<AttentionItem[]>) {
  function Harness() {
    const [currentItems, setCurrentItems] = useState(items);
    const [drafts, setDrafts] = useState<AttentionDrafts>(emptyAttentionDrafts);
    return (
      <AttentionInbox
        items={currentItems}
        refresh={async () => {
          const latest = await refresh();
          setCurrentItems(latest);
          return latest;
        }}
        featureLabel={() => 'Search revamp'}
        drafts={drafts}
        setDrafts={setDrafts}
        onJump={() => undefined}
      />
    );
  }
  render(<Harness />);
}

describe('AttentionInbox questions', () => {
  it('shows full question prompts, option confidence, and keeps drafts while navigating items', async () => {
    const mock = installAgenticoMock();
    let latest = [askBundle];
    mock.api.getAttention.mockImplementation(() => Promise.resolve({ items: latest }));
    mock.api.answerQuestions.mockImplementation((request: unknown) => {
      latest = [];
      return Promise.resolve({ result: 'submitted', request });
    });
    renderInbox([askBundle], async () => (await window.agentico.getAttention()).items);
    const user = userEvent.setup();

    await user.click(screen.getByRole('button', { name: /Attention inbox, 1 pending/ }));
    const inbox = screen.getByRole('complementary', { name: 'Attention inbox' });
    await user.click(within(inbox).getByRole('button', { name: /Questions/ }));

    expect(
      within(inbox).getByText('Which verification tracks should be included?'),
    ).toBeInTheDocument();
    expect(within(inbox).getAllByText('50%')).toHaveLength(2);
    expect(within(inbox).getAllByText('Free text replaces selected options.')).toHaveLength(2);
    await user.click(within(inbox).getByLabelText(/Unit tests/));
    await user.click(within(inbox).getByLabelText(/Packaged smoke/));

    await user.click(within(inbox).getByRole('button', { name: /Questions/ }));
    await user.click(within(inbox).getByRole('button', { name: /Questions/ }));
    expect(within(inbox).getByLabelText(/Unit tests/)).toBeChecked();
    expect(within(inbox).getByLabelText(/Packaged smoke/)).toBeChecked();

    await user.type(
      within(inbox).getByLabelText('Evidence note free text'),
      'Attach the redacted packaged trace bundle.',
    );
    await user.click(within(inbox).getByRole('button', { name: 'Submit answers' }));

    expect(mock.api.answerQuestions).toHaveBeenCalledWith({
      requestId: 'ask-bundle',
      sessionId: 'session-1',
      answers: {
        'Which verification tracks should be included?': 'Unit tests, Packaged smoke',
        'Which note should be attached to the evidence bundle?':
          'Attach the redacted packaged trace bundle.',
      },
    });
    await waitFor(() =>
      expect(screen.getByRole('button', { name: /Attention inbox, 0 pending/ })).toBeVisible(),
    );
  });

  it('makes explicit that free text replaces selected options when submitted', async () => {
    const mock = installAgenticoMock();
    let latest = [askBundle];
    mock.api.getAttention.mockImplementation(() => Promise.resolve({ items: latest }));
    mock.api.answerQuestions.mockImplementation((request: unknown) => {
      latest = [];
      return Promise.resolve({ result: 'submitted', request });
    });
    renderInbox([askBundle], async () => (await window.agentico.getAttention()).items);
    const user = userEvent.setup();

    await user.click(screen.getByRole('button', { name: /Attention inbox, 1 pending/ }));
    const inbox = screen.getByRole('complementary', { name: 'Attention inbox' });
    await user.click(within(inbox).getByRole('button', { name: /Questions/ }));
    await user.click(within(inbox).getByLabelText(/Unit tests/));
    await user.type(
      within(inbox).getByLabelText('Verification tracks free text'),
      'Run the full packaged suite instead.',
    );
    await user.type(
      within(inbox).getByLabelText('Evidence note free text'),
      'Attach the redacted packaged trace bundle.',
    );

    await user.click(within(inbox).getByRole('button', { name: 'Submit answers' }));

    expect(mock.api.answerQuestions).toHaveBeenCalledWith({
      requestId: 'ask-bundle',
      sessionId: 'session-1',
      answers: {
        'Which verification tracks should be included?': 'Run the full packaged suite instead.',
        'Which note should be attached to the evidence bundle?':
          'Attach the redacted packaged trace bundle.',
      },
    });
  });
});

describe('AttentionInbox accessibility', () => {
  it('moves keyboard focus with arrows and advances focus after panel resolution', async () => {
    const mock = installAgenticoMock();
    let latest = [permissionItem, secondPermissionItem];
    mock.api.getAttention.mockImplementation(() => Promise.resolve({ items: latest }));
    mock.api.answerPermission.mockImplementation((request: unknown) => {
      latest = [secondPermissionItem];
      return Promise.resolve({ result: 'submitted', request });
    });
    renderInbox([permissionItem, secondPermissionItem], async () => latest);
    const user = userEvent.setup();

    await user.click(screen.getByRole('button', { name: /Attention inbox, 2 pending/ }));
    const inbox = screen.getByRole('complementary', { name: 'Attention inbox' });
    const permissionButtons = within(inbox).getAllByRole('button', { name: /Permission/ });
    const firstPermission = permissionButtons[0]!;
    const secondPermission = permissionButtons[1]!;
    firstPermission.focus();

    await user.keyboard('{ArrowDown}');
    expect(secondPermission).toHaveFocus();
    await user.keyboard('{ArrowUp}');
    expect(firstPermission).toHaveFocus();

    await user.click(firstPermission);
    await user.click(within(inbox).getByRole('button', { name: 'Allow once' }));

    await waitFor(() =>
      expect(screen.getByRole('button', { name: /Attention inbox, 1 pending/ })).toBeVisible(),
    );
    expect(within(inbox).getByRole('status')).toHaveTextContent(
      'Submitted. Waiting for the server snapshot...',
    );
    expect(within(inbox).getByRole('button', { name: /Permission/ })).toHaveFocus();
  });
});

describe('AttentionInbox gate drafts', () => {
  it('saves a gate draft on blur without disabling an immediate abort click', async () => {
    const mock = installAgenticoMock();
    let resolveDraft!: () => void;
    mock.api.saveGateDraft.mockImplementation(
      () =>
        new Promise((resolve) => {
          resolveDraft = () => resolve({ result: 'drafted' });
        }),
    );
    renderInbox([gateItem], async () => [gateItem]);
    const user = userEvent.setup();

    await user.click(screen.getByRole('button', { name: /Attention inbox, 1 pending/ }));
    const inbox = screen.getByRole('complementary', { name: 'Attention inbox' });
    await user.click(within(inbox).getByRole('button', { name: /Input gate/ }));
    await user.type(
      within(inbox).getByLabelText(/Which deployment window should implementation use/),
      'After packaged attention evidence passes.',
    );
    await user.click(within(inbox).getByRole('button', { name: 'Abort gate' }));

    expect(mock.api.saveGateDraft).toHaveBeenCalledWith({
      featureId: 'feature-1',
      answers: {
        'Which deployment window should implementation use?':
          'After packaged attention evidence passes.',
      },
    });
    expect(screen.getByRole('dialog', { name: 'Confirm abort' })).toBeVisible();
    expect(mock.api.resolveGate).not.toHaveBeenCalled();

    resolveDraft();
    await waitFor(() => expect(within(inbox).getByText('Draft saved.')).toBeVisible());
  });

  it('targets cycle-scoped gate draft, resume, and abort requests with repo and cycle', async () => {
    const mock = installAgenticoMock();
    let latest = [cycleGateItem];
    mock.api.getAttention.mockImplementation(() => Promise.resolve({ items: latest }));
    mock.api.resolveGate.mockImplementation((request: unknown) => {
      latest = [];
      return Promise.resolve({ result: 'resolved', request });
    });
    renderInbox([cycleGateItem], async () => latest);
    const user = userEvent.setup();

    await user.click(screen.getByRole('button', { name: /Attention inbox, 1 pending/ }));
    const inbox = screen.getByRole('complementary', { name: 'Attention inbox' });
    await user.click(within(inbox).getByRole('button', { name: /Input gate/ }));
    expect(within(inbox).getByLabelText('Attention context')).toHaveTextContent('repo-a');
    expect(within(inbox).getByLabelText('Attention context')).toHaveTextContent('review-comments');
    await user.type(
      within(inbox).getByLabelText(/Which review comment batch should this cycle use/),
      'Use the current review-comment batch.',
    );
    await user.click(within(inbox).getByRole('button', { name: 'Resume' }));

    expect(mock.api.saveGateDraft).toHaveBeenCalledWith({
      featureId: 'feature-1',
      repoName: 'repo-a',
      cycleType: 'review-comments',
      answers: {
        'Which review comment batch should this cycle use?':
          'Use the current review-comment batch.',
      },
    });
    expect(mock.api.resolveGate).toHaveBeenCalledWith({
      featureId: 'feature-1',
      repoName: 'repo-a',
      cycleType: 'review-comments',
      decision: 'resume',
    });

    cleanup();
    latest = [cycleGateItem];
    mock.api.resolveGate.mockImplementation((request: unknown) => {
      latest = [];
      return Promise.resolve({ result: 'resolved', request });
    });
    renderInbox([cycleGateItem], async () => latest);
    await user.click(screen.getByRole('button', { name: /Attention inbox, 1 pending/ }));
    const secondInbox = screen.getByRole('complementary', { name: 'Attention inbox' });
    await user.click(within(secondInbox).getByRole('button', { name: /Input gate/ }));
    await user.click(within(secondInbox).getByRole('button', { name: 'Abort gate' }));
    const dialog = screen.getByRole('dialog', { name: 'Confirm abort' });
    expect(dialog).toHaveTextContent(
      'Abort will fail only this repository cycle; sibling cycles continue.',
    );
    await user.click(within(dialog).getByRole('button', { name: 'Confirm abort' }));

    expect(mock.api.resolveGate).toHaveBeenLastCalledWith({
      featureId: 'feature-1',
      repoName: 'repo-a',
      cycleType: 'review-comments',
      decision: 'abort',
    });
  });
});

describe('AttentionInbox stale refreshes', () => {
  it('shows an already-resolved notice when an expanded item disappears externally', async () => {
    function Harness() {
      const [currentItems, setCurrentItems] = useState<AttentionItem[]>([permissionItem]);
      const [drafts, setDrafts] = useState<AttentionDrafts>(emptyAttentionDrafts);
      return (
        <>
          <button type="button" onClick={() => setCurrentItems([])}>
            External resolve
          </button>
          <AttentionInbox
            items={currentItems}
            refresh={() => Promise.resolve(currentItems)}
            featureLabel={() => 'Search revamp'}
            drafts={drafts}
            setDrafts={setDrafts}
            onJump={() => undefined}
          />
        </>
      );
    }

    render(<Harness />);
    const user = userEvent.setup();

    await user.click(screen.getByRole('button', { name: /Attention inbox, 1 pending/ }));
    const inbox = screen.getByRole('complementary', { name: 'Attention inbox' });
    await user.click(within(inbox).getByRole('button', { name: /Permission/ }));
    expect(within(inbox).getByText(/stale-resolution/)).toBeVisible();

    await user.click(screen.getByRole('button', { name: 'External resolve' }));

    expect(
      within(inbox).getByText('This item was already resolved. The inbox has been refreshed.'),
    ).toBeVisible();
    expect(screen.getByRole('button', { name: /Attention inbox, 0 pending/ })).toBeVisible();
  });
});
