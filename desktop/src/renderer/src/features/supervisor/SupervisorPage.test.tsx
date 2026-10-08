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

import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type {
  AttentionItem,
  ModelCatalogue,
  SupervisorEvent,
  SupervisorPendingRequest,
  SupervisorRecord,
  SupervisorState,
} from '../../../../shared/ipc';
import {
  installAgenticoMock,
  ipcError,
  supervisorLaunchFailure,
  supervisorCheckpointRecord,
  supervisorCompactedMarkerRecord,
  supervisorMarkerRecord,
  supervisorPendingPermission,
  supervisorPendingQuestion,
  supervisorRecord,
  supervisorState,
  supervisorTranscriptPage,
} from '../../test/agenticoMock';
import { emptyAttentionDrafts, type AttentionDrafts } from '../AttentionInbox';
import { SupervisorPage, type SupervisorComposeRequest } from './SupervisorPage';

afterEach(cleanup);

const CATALOGUE: ModelCatalogue = {
  providerOrder: ['claude', 'codex'],
  providerModels: {
    claude: [
      { id: 'claude-opus', displayName: 'Opus', effortCapabilities: ['low', 'medium', 'high'] },
      { id: 'claude-sonnet', displayName: 'Sonnet' },
      // Not chat-eligible: never offered.
      { id: 'claude-reviewer', displayName: 'Reviewer' },
    ],
    codex: [{ id: 'gpt-a', displayName: 'GPT A', effortCapabilities: ['medium'] }],
  },
  phaseDefaults: {},
  phaseProviderModels: {
    chat: { claude: ['claude-opus', 'claude-sonnet'], codex: [] },
  },
};

const CHOSEN = { harness: 'claude', model: 'claude-opus', effort: 'high' };
const SESSION_ID = '__supervisor__.supervisor-conversation-1.1';

const permissionRequest: SupervisorPendingRequest = {
  kind: 'permission',
  id: 'perm-supervisor-1',
  sessionId: SESSION_ID,
  target: 'supervisor',
  toolName: 'Bash',
  summary: 'make test',
  input: { command: 'make test' },
  // The placeholder phase the supervisor's session carries; never shown.
  phase: 'research',
  waitingSince: '2026-10-06T10:00:00.000Z',
};

const questionRequest: SupervisorPendingRequest = {
  kind: 'questions',
  id: 'question-supervisor-1',
  sessionId: SESSION_ID,
  target: 'supervisor',
  waitingSince: '2026-10-06T10:00:00.000Z',
  questions: [
    {
      key: 'Which database should the service use?',
      header: 'Database',
      multiSelect: false,
      options: [{ label: 'Postgres' }, { label: 'SQLite' }],
    },
  ],
};

function envelope(state: SupervisorState = supervisorState()) {
  return {
    conversationId: state.conversationId,
    generation: 1,
    streamEpoch: state.streamEpoch,
  };
}

function requestRecord(
  seq: number,
  kind: 'permission' | 'question',
  requestId: string,
  stage: 'requested' | 'resolved',
  outcome: 'pending' | 'allowed' | 'denied' | 'answered' | 'interrupted',
  toolName = kind === 'question' ? 'AskUserQuestion' : 'Bash',
): SupervisorRecord {
  return supervisorRecord({
    seq,
    kind,
    visibility: 'display_only',
    messages: [
      {
        index: seq,
        role: 'system',
        type: 'control_request',
        tool: toolName,
        status: outcome,
        redacted: true,
      },
    ],
    request: {
      requestId,
      toolName,
      stage,
      outcome,
      ...(stage === 'requested' ? { summary: kind === 'question' ? 'Database' : 'make test' } : {}),
    },
  });
}

function Harness({
  refreshAttention,
  composeRequest = null,
  onComposeRequestHandled,
}: {
  refreshAttention?: () => Promise<AttentionItem[]>;
  composeRequest?: SupervisorComposeRequest | null;
  onComposeRequestHandled?: () => void;
}) {
  const [drafts, setDrafts] = useState<AttentionDrafts>(emptyAttentionDrafts);
  return (
    <SupervisorPage
      attentionDrafts={drafts}
      setAttentionDrafts={setDrafts}
      refreshAttention={refreshAttention ?? (async () => [])}
      composeRequest={composeRequest}
      onComposeRequestHandled={onComposeRequestHandled}
    />
  );
}

function composer(): HTMLElement {
  return screen.getByRole('textbox', { name: 'Message the supervisor' });
}

function transcript(): HTMLElement {
  return screen.getByRole('region', { name: 'Supervisor conversation' });
}

function status(): HTMLElement {
  return screen.getByTestId('supervisor-status');
}

async function renderPage(
  overrides: Parameters<typeof installAgenticoMock>[0] = {},
  modelCatalogue: ModelCatalogue = CATALOGUE,
): Promise<
  ReturnType<typeof installAgenticoMock> & {
    compose(request: SupervisorComposeRequest): void;
    handled: ReturnType<typeof vi.fn>;
  }
> {
  const mock = installAgenticoMock(overrides);
  mock.api.getModelCatalogue.mockResolvedValue(modelCatalogue);
  const handled = vi.fn();
  const { rerender } = render(<Harness onComposeRequestHandled={handled} />);
  await screen.findByTestId('supervisor-model-chip');
  await waitFor(() => expect(screen.queryByText('Loading the supervisor…')).toBeNull());
  const compose = (request: SupervisorComposeRequest): void =>
    rerender(<Harness composeRequest={request} onComposeRequestHandled={handled} />);
  return Object.assign(mock, { compose, handled });
}

function emit(mock: ReturnType<typeof installAgenticoMock>, event: SupervisorEvent): void {
  act(() => mock.emitSupervisorEvent(event));
}

describe('SupervisorPage settings', () => {
  it('shows a pending target while retaining the committed chip and cancels by id', async () => {
    const pending = {
      requestId: 'req-next',
      kind: 'model' as const,
      target: { ...CHOSEN, model: 'claude-sonnet' },
      requestedAt: '2026-10-06T10:00:00Z',
    };
    const mock = await renderPage({
      supervisorState: supervisorState({
        lifecycle: 'running',
        settings: CHOSEN,
        pendingChange: pending,
      }),
    });
    expect(screen.getByTestId('supervisor-model-chip')).toHaveAccessibleName('Claude Opus · High');
    expect(screen.getByText(/Model change pending/)).toHaveTextContent(
      'applies when this turn ends',
    );
    expect(screen.getByText('claude-sonnet')).toBeVisible();
    await userEvent.setup().click(screen.getByRole('button', { name: 'Cancel' }));
    expect(mock.api.cancelSupervisorPendingChange).toHaveBeenCalledWith({ requestId: 'req-next' });
  });

  it('handles model and effort slash commands without sending messages', async () => {
    const mock = await renderPage({ supervisorState: supervisorState({ settings: CHOSEN }) });
    const user = userEvent.setup();
    await user.type(composer(), '/effort high{Enter}');
    await waitFor(() =>
      expect(mock.api.updateSupervisorSettings).toHaveBeenCalledWith(
        expect.objectContaining({ effort: 'high' }),
      ),
    );
    await user.type(composer(), '/model Sonnet{Enter}');
    await waitFor(() =>
      expect(mock.api.updateSupervisorSettings).toHaveBeenCalledWith(
        expect.objectContaining({ model: 'claude-sonnet', effort: '' }),
      ),
    );
    expect(mock.api.sendSupervisorMessage).not.toHaveBeenCalled();
  });
  it('disables Send with the choose placeholder while no harness and model are set', async () => {
    await renderPage();
    const user = userEvent.setup();

    expect(composer()).toHaveAttribute('placeholder', 'Choose a harness and model to start');
    expect(screen.getByTestId('supervisor-model-chip')).toHaveAccessibleName(
      'Choose harness and model',
    );
    await user.type(composer(), 'Hello');
    expect(screen.getByRole('button', { name: 'Send' })).toBeDisabled();
    expect(status()).toHaveTextContent('Ready');
    // No uploads in this phase.
    expect(screen.queryByRole('button', { name: 'Attach files or photos' })).toBeNull();
  });

  it('commits a harness and model through the chip, shows the committed values, and enables Send', async () => {
    const mock = await renderPage();
    const user = userEvent.setup();

    await user.click(screen.getByTestId('supervisor-model-chip'));
    const popover = screen.getByRole('region', { name: 'Harness and model' });
    const claude = within(popover).getByRole('group', { name: 'Claude' });
    // Only chat-eligible models, in the server's order.
    expect(
      within(claude)
        .getAllByRole('radio')
        .map((radio) => radio.parentElement?.textContent),
    ).toEqual(['Opus ★', 'Sonnet']);

    await user.click(within(claude).getByRole('radio', { name: 'Opus' }));
    expect(mock.api.updateSupervisorSettings).toHaveBeenCalledWith(
      expect.objectContaining({
        harness: 'claude',
        model: 'claude-opus',
        effort: '',
      }),
    );
    await waitFor(() =>
      expect(screen.getByTestId('supervisor-model-chip')).toHaveAccessibleName(
        'Claude Opus · Default',
      ),
    );
    expect(within(claude).getByRole('radio', { name: 'Opus' })).toBeChecked();

    const effort = within(popover).getByRole('group', { name: 'Effort' });
    expect(
      within(effort)
        .getAllByRole('radio')
        .map((radio) => radio.parentElement?.textContent),
    ).toEqual(['Default', 'Low', 'Medium', 'High']);
    await user.click(within(effort).getByRole('radio', { name: 'High' }));
    expect(mock.api.updateSupervisorSettings).toHaveBeenLastCalledWith(
      expect.objectContaining({
        harness: 'claude',
        model: 'claude-opus',
        effort: 'high',
      }),
    );
    await waitFor(() =>
      expect(screen.getByTestId('supervisor-model-chip')).toHaveAccessibleName(
        'Claude Opus · High',
      ),
    );

    await user.keyboard('{Escape}');
    expect(composer()).toHaveAttribute('placeholder', 'Message the supervisor');
    await user.type(composer(), 'Hello');
    expect(screen.getByRole('button', { name: 'Send' })).toBeEnabled();
  });

  it('says so when a harness has no usable models', async () => {
    await renderPage();
    const user = userEvent.setup();

    await user.click(screen.getByTestId('supervisor-model-chip'));
    const codex = screen.getByRole('group', { name: 'Codex' });
    expect(
      within(codex).getByText('No usable models are available for this harness.'),
    ).toBeVisible();
    expect(within(codex).queryByRole('radio')).toBeNull();
  });

  it('keeps the chip available while a supervisor process exists', async () => {
    await renderPage({ supervisorState: supervisorState({ lifecycle: 'idle', settings: CHOSEN }) });
    const user = userEvent.setup();

    const chip = screen.getByTestId('supervisor-model-chip');
    expect(chip).toHaveAccessibleName('Claude Opus · High');
    await user.click(chip);
    expect(screen.getByRole('region', { name: 'Harness and model' })).toBeVisible();
  });
});

describe('SupervisorPage conversation', () => {
  it('sends with an optimistic row, clears the draft, and keeps the committed record', async () => {
    const mock = await renderPage({ supervisorState: supervisorState({ settings: CHOSEN }) });
    let release: () => void = () => undefined;
    const original = mock.api.sendSupervisorMessage.getMockImplementation() as (request: {
      text: string;
    }) => Promise<unknown>;
    mock.api.sendSupervisorMessage.mockImplementation(
      (request: { text: string }) =>
        new Promise((resolve) => {
          release = () => resolve(original(request));
        }),
    );
    const user = userEvent.setup();

    await user.type(composer(), 'Plan the release{Enter}');
    expect(mock.api.sendSupervisorMessage).toHaveBeenCalledWith({ text: 'Plan the release' });
    expect(composer()).toHaveValue('');
    expect(within(transcript()).getByText('Plan the release')).toBeVisible();
    expect(status()).toHaveTextContent('Starting supervisor…');

    await act(async () => release());
    await waitFor(() => expect(status()).toHaveTextContent('Working…'));
    expect(within(transcript()).getAllByText('Plan the release')).toHaveLength(1);
    expect(screen.getByRole('button', { name: 'Stop' })).toBeEnabled();
  });

  it('restores the draft and shows the error inline when a send fails', async () => {
    const mock = await renderPage({ supervisorState: supervisorState({ settings: CHOSEN }) });
    mock.api.sendSupervisorMessage.mockRejectedValue(
      ipcError('supervisor_launch_failed', 'The supervisor process could not start.', {
        title: 'Supervisor launch failed',
      }),
    );
    const user = userEvent.setup();

    await user.type(composer(), 'Hello there');
    await user.click(screen.getByRole('button', { name: 'Send' }));

    expect(await screen.findByText('Supervisor launch failed')).toBeVisible();
    expect(composer()).toHaveValue('Hello there');
    expect(within(transcript()).queryByText('Hello there')).toBeNull();
    expect(screen.getByRole('button', { name: 'Send' })).toBeEnabled();
  });

  it('shows streamed text as a provisional reply until the committed record replaces it', async () => {
    const state = supervisorState({
      settings: CHOSEN,
      lifecycle: 'running',
      sessionId: SESSION_ID,
    });
    const mock = await renderPage({ supervisorState: state });
    const delta = (chunkIndex: number, text: string): SupervisorEvent => ({
      type: 'delta',
      ...envelope(state),
      delta: { turnId: 'turn-1', streamMessageId: 'stream-1', chunkIndex, text },
    });

    // Out-of-order chunks still read in chunk order.
    emit(mock, delta(1, 'lo, '));
    emit(mock, delta(0, 'Hel'));
    expect(within(transcript()).getByText('Hello,')).toBeVisible();

    const committed = supervisorRecord({
      seq: 2,
      kind: 'assistant',
      streamMessageId: 'stream-1',
      messages: [{ index: 2, role: 'assistant', type: 'text', text: 'Hello, I am ready.' }],
    });
    emit(mock, { type: 'record', ...envelope(state), record: committed });
    // A replayed record (same seq) never duplicates.
    emit(mock, { type: 'record', ...envelope(state), record: committed });

    expect(within(transcript()).getAllByText('Hello, I am ready.')).toHaveLength(1);
    expect(within(transcript()).queryByText('Hello,')).toBeNull();
    expect(within(transcript()).getByText('Supervisor')).toBeVisible();
  });

  it('runs the status line from Starting through Working to the resting label', async () => {
    const state = supervisorState({ settings: CHOSEN });
    const mock = await renderPage({ supervisorState: state });
    const pushState = (overrides: Partial<SupervisorState>) =>
      emit(mock, { type: 'state', ...envelope(state), state: { ...state, ...overrides } });

    expect(status()).toHaveTextContent('Ready');
    pushState({ lifecycle: 'starting', startingStep: 'launching' });
    expect(status()).toHaveTextContent('Starting supervisor…');
    pushState({ lifecycle: 'starting', startingStep: 'handshake' });
    expect(status()).toHaveTextContent('Starting supervisor…');
    pushState({ lifecycle: 'running', startingStep: undefined });
    expect(status()).toHaveTextContent('Working…');
    pushState({ lifecycle: 'idle' });
    expect(status()).toHaveTextContent('Ready');
  });

  it('keeps a streamed idle state over a refresh that was fetched before it', async () => {
    const idle = supervisorState({ settings: CHOSEN, lifecycle: 'idle', sessionId: SESSION_ID });
    const mock = await renderPage({ supervisorState: idle });
    let answerRefresh: (state: SupervisorState) => void = () => undefined;
    mock.api.getSupervisorState.mockImplementation(
      () =>
        new Promise<SupervisorState>((resolve) => {
          answerRefresh = resolve;
        }),
    );
    const user = userEvent.setup();

    await user.type(composer(), 'Quick question{Enter}');
    await waitFor(() => expect(mock.api.getSupervisorState).toHaveBeenCalledTimes(2));
    // The turn finishes on the stream while the post-send refresh is in flight…
    emit(mock, { type: 'state', ...envelope(idle), state: { ...idle, lifecycle: 'running' } });
    emit(mock, { type: 'state', ...envelope(idle), state: idle });
    // …and the refresh, read before the result, must not resurrect "running".
    await act(async () => answerRefresh({ ...idle, lifecycle: 'running' }));

    expect(status()).toHaveAttribute('data-lifecycle', 'idle');
    expect(status()).toHaveTextContent('Ready');
  });

  it('re-snapshots state and the newest page on a stream reset', async () => {
    const mock = await renderPage({ supervisorState: supervisorState({ settings: CHOSEN }) });
    expect(mock.api.getSupervisorTranscript).toHaveBeenCalledTimes(1);
    mock.api.getSupervisorTranscript.mockResolvedValue(
      supervisorTranscriptPage({
        items: [
          supervisorRecord({
            seq: 4,
            messages: [{ index: 4, role: 'user', type: 'text', text: 'After reset' }],
          }),
        ],
        firstSeq: 4,
        lastSeq: 4,
        headSeq: 4,
      }),
    );

    emit(mock, { type: 'reset' });
    expect(await within(transcript()).findByText('After reset')).toBeVisible();
    expect(mock.api.getSupervisorState).toHaveBeenCalledTimes(2);
  });
});

describe('SupervisorPage Send and Stop', () => {
  it('morphs Send into Stop during a turn and interrupts through Stop', async () => {
    const state = supervisorState({
      settings: CHOSEN,
      lifecycle: 'running',
      sessionId: SESSION_ID,
    });
    const mock = await renderPage({ supervisorState: state });
    const user = userEvent.setup();

    expect(screen.queryByRole('button', { name: 'Send' })).toBeNull();
    await user.click(screen.getByRole('button', { name: 'Stop' }));
    expect(mock.api.interruptSupervisor).toHaveBeenCalledTimes(1);

    emit(mock, { type: 'state', ...envelope(state), state: { ...state, lifecycle: 'idle' } });
    expect(screen.queryByRole('button', { name: 'Stop' })).toBeNull();
    expect(screen.getByRole('button', { name: 'Send' })).toBeInTheDocument();
  });

  it('keeps Stop live while a request is pending', async () => {
    const mock = await renderPage({
      supervisorState: supervisorState({
        settings: CHOSEN,
        lifecycle: 'waiting_permission',
        sessionId: SESSION_ID,
        pendingRequests: [permissionRequest],
      }),
    });
    const user = userEvent.setup();

    const stop = screen.getByRole('button', { name: 'Stop' });
    expect(stop).toBeEnabled();
    await user.click(stop);
    expect(mock.api.interruptSupervisor).toHaveBeenCalledTimes(1);
  });
});

describe('SupervisorPage pending requests', () => {
  it('blocks Send with the pending placeholder while still allowing typing', async () => {
    const state = supervisorState({ settings: CHOSEN, lifecycle: 'idle', sessionId: SESSION_ID });
    const mock = await renderPage({ supervisorState: state });
    const user = userEvent.setup();

    // The request event lands before its state change.
    emit(mock, { type: 'request', ...envelope(state), request: permissionRequest });
    expect(composer()).toHaveAttribute(
      'placeholder',
      'Respond to the pending request above to continue',
    );
    await user.type(composer(), 'Queued thought{Enter}');
    expect(composer()).toHaveValue('Queued thought');
    expect(screen.getByRole('button', { name: 'Send' })).toBeDisabled();
    expect(mock.api.sendSupervisorMessage).not.toHaveBeenCalled();
  });

  it('renders a pending permission inline and collapses it to a verdict once allowed', async () => {
    const state = supervisorState({
      settings: CHOSEN,
      lifecycle: 'waiting_permission',
      sessionId: SESSION_ID,
      pendingRequests: [permissionRequest],
    });
    const refreshAttention = vi.fn(async () => [] as AttentionItem[]);
    const mock = installAgenticoMock({
      supervisorState: state,
      supervisorTranscript: supervisorTranscriptPage({
        items: [requestRecord(1, 'permission', permissionRequest.id, 'requested', 'pending')],
        firstSeq: 1,
        lastSeq: 1,
        headSeq: 1,
      }),
    });
    mock.api.getModelCatalogue.mockResolvedValue(CATALOGUE);
    render(<Harness refreshAttention={refreshAttention} />);
    const user = userEvent.setup();

    const allow = await within(transcript()).findByRole('button', { name: 'Allow once' });
    expect(within(transcript()).getByRole('button', { name: 'Deny' })).toBeVisible();
    expect(within(transcript()).getByLabelText('Attention context')).not.toHaveTextContent(
      /research/i,
    );
    expect(within(transcript()).getByText('Permission request')).toBeVisible();
    // The requested-stage record never duplicates the live card.
    expect(within(transcript()).queryByText(/Allowed Bash|Denied Bash/)).toBeNull();

    await user.click(allow);
    expect(mock.api.answerPermission).toHaveBeenCalledWith({
      requestId: permissionRequest.id,
      sessionId: SESSION_ID,
      decision: 'allow_once',
    });
    expect(refreshAttention).toHaveBeenCalled();
    await waitFor(() =>
      expect(within(transcript()).queryByRole('button', { name: 'Allow once' })).toBeNull(),
    );

    emit(mock, {
      type: 'record',
      ...envelope(state),
      record: requestRecord(2, 'permission', permissionRequest.id, 'resolved', 'allowed'),
    });
    expect(within(transcript()).getByText('Allowed Bash · make test')).toBeVisible();
  });

  it('renders answered requests from the transcript as one-line verdicts after a reload', async () => {
    await renderPage({
      supervisorState: supervisorState({ settings: CHOSEN, lifecycle: 'idle' }),
      supervisorTranscript: supervisorTranscriptPage({
        items: [
          requestRecord(1, 'permission', 'perm-a', 'requested', 'pending'),
          requestRecord(2, 'permission', 'perm-a', 'resolved', 'denied'),
          requestRecord(3, 'question', 'question-a', 'requested', 'pending'),
          requestRecord(4, 'question', 'question-a', 'resolved', 'answered'),
        ],
        firstSeq: 1,
        lastSeq: 4,
        headSeq: 4,
      }),
    });

    expect(within(transcript()).getByText('Denied Bash · make test')).toBeVisible();
    expect(within(transcript()).getByText('Answered · Database')).toBeVisible();
    expect(within(transcript()).queryByRole('button', { name: 'Allow once' })).toBeNull();
    // Control-request rows never leak into the activity labels.
    expect(within(transcript()).queryByText(/Using bash/)).toBeNull();
  });

  it('renders a pending question as the question turn and collapses it once answered', async () => {
    const state = supervisorState({
      settings: CHOSEN,
      lifecycle: 'waiting_question',
      sessionId: SESSION_ID,
      pendingRequests: [questionRequest],
    });
    const mock = await renderPage({ supervisorState: state });
    const user = userEvent.setup();

    const turn = within(transcript()).getByRole('group', { name: 'Agent question' });
    expect(within(turn).getByText('Which database should the service use?')).toBeVisible();
    await user.click(within(turn).getByRole('radio', { name: /Postgres/ }));
    await user.click(within(transcript()).getByRole('button', { name: 'Send' }));

    expect(mock.api.answerQuestions).toHaveBeenCalledWith({
      requestId: questionRequest.id,
      sessionId: SESSION_ID,
      answers: { 'Which database should the service use?': 'Postgres' },
    });
    await waitFor(() =>
      expect(within(transcript()).queryByRole('group', { name: 'Agent question' })).toBeNull(),
    );

    emit(mock, {
      type: 'record',
      ...envelope(state),
      record: requestRecord(5, 'question', questionRequest.id, 'resolved', 'answered'),
    });
    expect(within(transcript()).getByText('Answered the question')).toBeVisible();
  });
});

const EXPLAIN_DRAFT =
  'Explain the "Run failed" error (run_failed) on Search revamp and what I should do next.';
const RUN_REFERENCE = { scope: 'run', code: 'run_failed', featureId: 'abcd1234' } as const;
const SETUP_REFERENCE = {
  scope: 'setup',
  code: 'worktree_setup_failed',
  featureId: 'abcd1234',
  taskKey: 'web',
} as const;

describe('SupervisorPage explain drafts', () => {
  it('sets an empty composer to the routed draft, focuses it, and sends nothing', async () => {
    const mock = await renderPage({ supervisorState: supervisorState({ settings: CHOSEN }) });

    mock.compose({ id: 1, draft: EXPLAIN_DRAFT, errorReference: RUN_REFERENCE });

    await waitFor(() => expect(composer()).toHaveValue(EXPLAIN_DRAFT));
    expect(composer()).toHaveFocus();
    expect(mock.handled).toHaveBeenCalledTimes(1);
    expect(mock.api.sendSupervisorMessage).not.toHaveBeenCalled();
    expect(screen.getByRole('button', { name: 'Send' })).toBeEnabled();
  });

  it('appends the routed draft after a blank line, keeping what the person typed', async () => {
    const mock = await renderPage({ supervisorState: supervisorState({ settings: CHOSEN }) });
    const user = userEvent.setup();
    await user.type(composer(), 'My own note');

    mock.compose({ id: 1, draft: EXPLAIN_DRAFT, errorReference: RUN_REFERENCE });

    await waitFor(() => expect(composer()).toHaveValue(`My own note\n\n${EXPLAIN_DRAFT}`));
    expect(composer()).toHaveFocus();
  });

  it('handles each request once and focuses without touching the text when no draft rides it', async () => {
    const mock = await renderPage({ supervisorState: supervisorState({ settings: CHOSEN }) });
    const user = userEvent.setup();

    mock.compose({ id: 1, draft: EXPLAIN_DRAFT });
    await waitFor(() => expect(composer()).toHaveValue(EXPLAIN_DRAFT));
    // A re-render carrying the same request never replays its draft.
    mock.compose({ id: 1, draft: EXPLAIN_DRAFT });
    expect(composer()).toHaveValue(EXPLAIN_DRAFT);

    await user.click(screen.getByTestId('supervisor-status'));
    expect(composer()).not.toHaveFocus();
    mock.compose({ id: 2 });
    await waitFor(() => expect(composer()).toHaveFocus());
    expect(composer()).toHaveValue(EXPLAIN_DRAFT);
    expect(mock.handled).toHaveBeenCalledTimes(2);
  });

  it('sends the attached reference with the drafted text, then clears it after the send', async () => {
    const state = supervisorState({ settings: CHOSEN });
    const mock = await renderPage({ supervisorState: state });
    const user = userEvent.setup();

    mock.compose({ id: 1, draft: EXPLAIN_DRAFT, errorReference: RUN_REFERENCE });
    await waitFor(() => expect(composer()).toHaveValue(EXPLAIN_DRAFT));
    await user.click(screen.getByRole('button', { name: 'Send' }));
    expect(mock.api.sendSupervisorMessage).toHaveBeenCalledWith({
      text: EXPLAIN_DRAFT,
      errorReference: RUN_REFERENCE,
    });

    // Once the turn settles, the next message carries no reference.
    await screen.findByRole('button', { name: 'Stop' });
    await waitFor(() => expect(mock.api.getSupervisorState).toHaveBeenCalledTimes(2));
    emit(mock, { type: 'state', ...envelope(state), state: { ...state, lifecycle: 'idle' } });
    await user.type(composer(), 'And then?{Enter}');
    expect(mock.api.sendSupervisorMessage).toHaveBeenLastCalledWith({ text: 'And then?' });
  });

  it('replaces the attached reference with a later explain', async () => {
    const mock = await renderPage({ supervisorState: supervisorState({ settings: CHOSEN }) });
    const user = userEvent.setup();

    mock.compose({ id: 1, draft: 'First explain', errorReference: RUN_REFERENCE });
    await waitFor(() => expect(composer()).toHaveValue('First explain'));
    mock.compose({ id: 2, draft: 'Second explain', errorReference: SETUP_REFERENCE });
    await waitFor(() => expect(composer()).toHaveValue('First explain\n\nSecond explain'));

    await user.click(screen.getByRole('button', { name: 'Send' }));
    expect(mock.api.sendSupervisorMessage).toHaveBeenCalledWith({
      text: 'First explain\n\nSecond explain',
      errorReference: SETUP_REFERENCE,
    });
  });

  it('drops the attached reference when the person empties the composer', async () => {
    const mock = await renderPage({ supervisorState: supervisorState({ settings: CHOSEN }) });
    const user = userEvent.setup();

    mock.compose({ id: 1, draft: EXPLAIN_DRAFT, errorReference: RUN_REFERENCE });
    await waitFor(() => expect(composer()).toHaveValue(EXPLAIN_DRAFT));
    await user.clear(composer());
    await user.type(composer(), 'Something else{Enter}');

    expect(mock.api.sendSupervisorMessage).toHaveBeenCalledWith({ text: 'Something else' });
  });

  it('keeps the reference attached when a send fails and the draft is restored', async () => {
    const mock = await renderPage({ supervisorState: supervisorState({ settings: CHOSEN }) });
    mock.api.sendSupervisorMessage.mockRejectedValueOnce(
      ipcError('supervisor_launch_failed', 'The supervisor process could not start.', {
        title: 'Supervisor launch failed',
      }),
    );
    const user = userEvent.setup();

    mock.compose({ id: 1, draft: EXPLAIN_DRAFT, errorReference: RUN_REFERENCE });
    await waitFor(() => expect(composer()).toHaveValue(EXPLAIN_DRAFT));
    await user.click(screen.getByRole('button', { name: 'Send' }));
    expect(await screen.findByText('Supervisor launch failed')).toBeVisible();
    expect(composer()).toHaveValue(EXPLAIN_DRAFT);

    await user.click(screen.getByRole('button', { name: 'Send' }));
    expect(mock.api.sendSupervisorMessage).toHaveBeenLastCalledWith({
      text: EXPLAIN_DRAFT,
      errorReference: RUN_REFERENCE,
    });
  });

  it('retains the draft with Send disabled until a harness and model are chosen', async () => {
    const mock = await renderPage();
    const user = userEvent.setup();

    mock.compose({ id: 1, draft: EXPLAIN_DRAFT, errorReference: RUN_REFERENCE });
    await waitFor(() => expect(composer()).toHaveValue(EXPLAIN_DRAFT));
    expect(screen.getByRole('button', { name: 'Send' })).toBeDisabled();
    await user.keyboard('{Enter}');
    expect(mock.api.sendSupervisorMessage).not.toHaveBeenCalled();

    await user.click(screen.getByTestId('supervisor-model-chip'));
    const popover = screen.getByRole('region', { name: 'Harness and model' });
    await user.click(
      within(within(popover).getByRole('group', { name: 'Claude' })).getByRole('radio', {
        name: 'Opus',
      }),
    );
    await user.keyboard('{Escape}');
    await waitFor(() => expect(screen.getByRole('button', { name: 'Send' })).toBeEnabled());
    expect(composer()).toHaveValue(EXPLAIN_DRAFT);

    await user.click(screen.getByRole('button', { name: 'Send' }));
    expect(mock.api.sendSupervisorMessage).toHaveBeenCalledWith({
      text: EXPLAIN_DRAFT,
      errorReference: RUN_REFERENCE,
    });
  });
});

describe('SupervisorPage restart and launch failure', () => {
  const pausedState = supervisorState({
    settings: CHOSEN,
    generation: 1,
    lifecycle: 'stopped',
    lastTurnOutcome: 'interrupted',
    interruptedBy: 'shutdown',
    headSeq: 4,
  });
  const cutTurn = supervisorTranscriptPage({
    items: [
      supervisorRecord({
        seq: 1,
        turnId: 'turn-1',
        messages: [{ index: 1, role: 'user', type: 'text', text: 'Audit the open features' }],
      }),
      supervisorRecord({
        seq: 2,
        turnId: 'turn-1',
        kind: 'assistant',
        messages: [{ index: 2, role: 'assistant', type: 'text', text: 'Starting with the first' }],
      }),
      // The restart resolved the cut request with the interrupted outcome.
      {
        ...requestRecord(3, 'permission', 'perm-cut', 'resolved', 'interrupted'),
        turnId: 'turn-1',
      },
      supervisorMarkerRecord(undefined, { seq: 4, turnId: 'turn-1' }),
    ],
    firstSeq: 1,
    lastSeq: 4,
    headSeq: 4,
  });

  it('shows a restart-cut conversation paused with the marker row, the footer, and the interrupted verdict', async () => {
    await renderPage({ supervisorState: pausedState, supervisorTranscript: cutTurn });

    expect(status()).toHaveTextContent(
      'Paused — interrupted before restart. Send a message to continue.',
    );
    expect(status()).toHaveAttribute('data-tone', 'paused');
    const conversation = within(transcript());
    expect(conversation.getByText('Interrupted before restart')).toBeVisible();
    expect(conversation.getByText('Interrupted · Bash')).toBeVisible();
    const reply = conversation.getByText('Starting with the first').closest('article')!;
    expect(within(reply).getByText('Interrupted')).toBeVisible();
    // The person's own prompt carries no footer.
    const prompt = conversation.getByText('Audit the open features').closest('article')!;
    expect(within(prompt).queryByText('Interrupted')).toBeNull();
    // Paused is not a lock: the next message sends as usual.
    const user = userEvent.setup();
    await user.type(composer(), 'Continue');
    expect(screen.getByRole('button', { name: 'Send' })).toBeEnabled();
  });

  it('keeps Ready for a turn the person stopped', async () => {
    await renderPage({
      supervisorState: { ...pausedState, interruptedBy: 'user' },
      supervisorTranscript: supervisorTranscriptPage(),
    });
    expect(status()).toHaveTextContent('Ready');
    expect(status()).not.toHaveAttribute('data-tone');
  });

  it('reads Rebuilding history for the chosen harness ahead of Starting supervisor', async () => {
    const mock = await renderPage({ supervisorState: pausedState });
    const pushState = (overrides: Partial<SupervisorState>) =>
      emit(mock, {
        type: 'state',
        ...envelope(pausedState),
        state: { ...pausedState, ...overrides },
      });

    pushState({ lifecycle: 'starting', startingStep: 'rebuilding' });
    expect(status()).toHaveTextContent('Rebuilding history for Claude…');
    pushState({ lifecycle: 'starting', startingStep: 'launching' });
    expect(status()).toHaveTextContent('Starting supervisor…');
  });

  it('renders the history and permission markers as notice rows', async () => {
    await renderPage({
      supervisorState: supervisorState({ settings: CHOSEN, lifecycle: 'idle' }),
      supervisorTranscript: supervisorTranscriptPage({
        items: [
          supervisorMarkerRecord(
            {
              marker: 'history_not_restored',
              text: 'History could not be restored for this session; starting without it',
            },
            { seq: 1, turnId: '' },
          ),
          supervisorMarkerRecord(
            {
              marker: 'permission_restricted',
              text: 'The supervisor runs in plan mode because a policy restricts it.',
            },
            { seq: 2, turnId: '' },
          ),
        ],
        firstSeq: 1,
        lastSeq: 2,
        headSeq: 2,
      }),
    });
    const conversation = within(transcript());
    expect(
      conversation.getByText('History could not be restored for this session; starting without it'),
    ).toBeVisible();
    expect(
      conversation.getByText('The supervisor runs in plan mode because a policy restricts it.'),
    ).toBeVisible();
  });

  it('shows a sanitized compaction summary only when opened', async () => {
    await renderPage({
      supervisorState: supervisorState({ settings: CHOSEN }),
      supervisorTranscript: supervisorTranscriptPage({
        items: [
          supervisorCompactedMarkerRecord('**A fact** <img src=x onerror=alert(1)>', {
            seq: 1,
            marker: {
              marker: 'compacted',
              text: 'Conversation compacted',
              summary: '**A fact** <img src=x onerror=alert(1)>',
              truncated: true,
            },
          }),
          supervisorCompactedMarkerRecord(undefined, { seq: 2 }),
          supervisorCompactedMarkerRecord(undefined, {
            seq: 3,
            marker: {
              marker: 'compacted',
              text: 'Conversation compacted',
              code: 'checkpoint_unavailable',
            },
          }),
        ],
        firstSeq: 1,
        lastSeq: 3,
        headSeq: 3,
      }),
    });
    expect(screen.getAllByText('Conversation compacted')).toHaveLength(2);
    expect(
      screen.getByText('Conversation compacted · history checkpoint unavailable'),
    ).toBeVisible();
    const toggle = screen.getByRole('button', { name: 'Show summary' });
    expect(toggle).toHaveAttribute('aria-expanded', 'false');
    expect(screen.queryByText('A fact')).toBeNull();
    await userEvent.setup().click(toggle);
    expect(screen.getByRole('button', { name: 'Hide summary' })).toHaveAttribute(
      'aria-expanded',
      'true',
    );
    expect(screen.getByText('A fact')).toBeVisible();
    expect(screen.getByText('Summary truncated')).toBeVisible();
    expect(transcript().querySelector('img')).toBeNull();
  });

  it('keeps assistant folding intact around a checkpoint', async () => {
    await renderPage({
      supervisorState: supervisorState({ settings: CHOSEN }),
      supervisorTranscript: supervisorTranscriptPage({
        items: [
          supervisorRecord({
            seq: 1,
            kind: 'assistant',
            turnId: 'same',
            messages: [{ index: 1, role: 'assistant', type: 'text', text: 'Before' }],
          }),
          supervisorCheckpointRecord({ seq: 2, turnId: 'same' }),
          supervisorRecord({
            seq: 3,
            kind: 'assistant',
            turnId: 'same',
            messages: [{ index: 3, role: 'assistant', type: 'text', text: 'After' }],
          }),
        ],
        firstSeq: 1,
        lastSeq: 3,
        headSeq: 3,
      }),
    });
    expect(within(transcript()).getByText('Before')).toBeVisible();
    expect(within(transcript()).getByText('After')).toBeVisible();
    expect(transcript().querySelectorAll('.conversation__notice')).toHaveLength(0);
  });

  it('shows live context fill and switches to warning at 80 percent', async () => {
    const initial = supervisorState({ settings: CHOSEN, contextUsage: null });
    const mock = await renderPage({ supervisorState: initial });
    const ring = screen.getByTestId('supervisor-context-ring');
    const chip = screen.getByTestId('supervisor-model-chip');
    const chipLabel = chip.textContent;
    expect(ring).toHaveAccessibleName('Context usage unknown');
    const update = (percent: number) =>
      emit(mock, {
        type: 'state',
        ...envelope(initial),
        state: supervisorState({
          ...initial,
          contextUsage: { percent, usedTokens: percent * 2_000, windowTokens: 200_000 },
        }),
      });
    update(79);
    expect(ring).toHaveAccessibleName('Context usage 79%');
    expect(ring).toHaveAttribute('data-tone', 'neutral');
    expect(ring).toHaveAttribute('title', '158,000 of 200,000 context tokens used');
    update(80);
    expect(ring).toHaveAccessibleName('Context usage 80%, near the limit');
    expect(ring).toHaveAttribute('data-tone', 'warning');
    expect(chip.textContent).toBe(chipLabel);
  });

  const failedState = supervisorState({
    settings: CHOSEN,
    generation: 1,
    lifecycle: 'failed',
    failure: supervisorLaunchFailure(),
    headSeq: 1,
  });
  const failedTranscript = supervisorTranscriptPage({
    items: [
      supervisorMarkerRecord(
        {
          marker: 'error',
          text: 'The harness exited before it answered the handshake.',
          code: 'supervisor_launch_failed',
        },
        { seq: 1, turnId: '' },
      ),
    ],
    firstSeq: 1,
    lastSeq: 1,
    headSeq: 1,
  });

  it('shows a failed launch as the status line, the error card, and the marker row', async () => {
    await renderPage({ supervisorState: failedState, supervisorTranscript: failedTranscript });

    expect(status()).toHaveTextContent('Supervisor failed — Retry');
    expect(status()).toHaveAttribute('data-lifecycle', 'failed');
    const card = screen.getByRole('alert');
    expect(within(card).getByText('Supervisor failed to start')).toBeVisible();
    expect(within(card).getByText('supervisor_launch_failed')).toBeVisible();
    expect(
      within(transcript()).getByText(
        'Supervisor failed to start · The harness exited before it answered the handshake.',
      ),
    ).toBeVisible();
  });

  it('disables Retry with a reason while the composer is empty', async () => {
    await renderPage({ supervisorState: failedState, supervisorTranscript: failedTranscript });

    const card = screen.getByRole('alert');
    expect(within(card).queryByRole('button', { name: 'Retry' })).toBeNull();
    expect(within(card).getByText('Type a message to retry.')).toBeVisible();

    const user = userEvent.setup();
    await user.type(composer(), 'Try again');
    expect(within(card).getByRole('button', { name: 'Retry' })).toBeEnabled();
    expect(within(card).queryByText('Type a message to retry.')).toBeNull();
  });

  it('re-sends the composer text through the ordinary send path on Retry and drops the card once the launch moves on', async () => {
    const mock = await renderPage({
      supervisorState: failedState,
      supervisorTranscript: failedTranscript,
    });
    const user = userEvent.setup();
    await user.type(composer(), 'Summarize the open features');

    await user.click(within(screen.getByRole('alert')).getByRole('button', { name: 'Retry' }));

    expect(mock.api.sendSupervisorMessage).toHaveBeenCalledTimes(1);
    expect(mock.api.sendSupervisorMessage).toHaveBeenCalledWith({
      text: 'Summarize the open features',
    });
    expect(composer()).toHaveValue('');
    await waitFor(() => expect(screen.queryByRole('alert')).toBeNull());
    expect(status()).not.toHaveTextContent('Supervisor failed — Retry');
  });

  it('keeps one card when the failed send returns the same launch failure, and restores the draft', async () => {
    const mock = await renderPage({ supervisorState: supervisorState({ settings: CHOSEN }) });
    mock.api.sendSupervisorMessage.mockImplementation(() => {
      mock.api.getSupervisorState.mockResolvedValue(failedState);
      return Promise.reject(
        ipcError('supervisor_launch_failed', 'The harness exited before it answered.', {
          title: 'Supervisor failed to start',
        }),
      );
    });
    const user = userEvent.setup();

    await user.type(composer(), 'Hello there');
    await user.click(screen.getByRole('button', { name: 'Send' }));

    await waitFor(() => expect(status()).toHaveTextContent('Supervisor failed — Retry'));
    expect(composer()).toHaveValue('Hello there');
    expect(screen.getAllByRole('alert')).toHaveLength(1);
    expect(within(screen.getByRole('alert')).getByRole('button', { name: 'Retry' })).toBeEnabled();
  });
});

describe('SupervisorPage sub-agent requests', () => {
  const CHILD = { origin: 'child' as const, childSessionId: 'agent_sub_1' };

  function childRecord(
    seq: number,
    kind: 'permission' | 'question',
    requestId: string,
    stage: 'requested' | 'resolved',
    outcome: 'pending' | 'allowed' | 'denied' | 'answered' | 'interrupted',
  ): SupervisorRecord {
    const record = requestRecord(seq, kind, requestId, stage, outcome);
    return { ...record, request: { ...record.request!, ...CHILD } };
  }

  it('tags a sub-agent permission card and leaves a root card untagged', async () => {
    await renderPage({
      supervisorState: supervisorState({
        settings: CHOSEN,
        lifecycle: 'waiting_permission',
        sessionId: SESSION_ID,
        pendingRequests: [
          supervisorPendingPermission({ id: 'perm-child', sessionId: SESSION_ID, ...CHILD }),
          supervisorPendingPermission({ id: 'perm-root', sessionId: SESSION_ID }),
        ],
      }),
    });

    const cards = await within(transcript()).findAllByText(/^Permission request/);
    expect(cards).toHaveLength(2);
    expect(cards[0]).toHaveTextContent('Permission requestSub-agent');
    expect(within(cards[0]!).getByText('Sub-agent')).toBeVisible();
    expect(within(cards[1]!).queryByText('Sub-agent')).toBeNull();
    expect(cards[1]).toHaveTextContent(/^Permission request$/);
  });

  it('tags a sub-agent question turn beside its topic', async () => {
    await renderPage({
      supervisorState: supervisorState({
        settings: CHOSEN,
        lifecycle: 'waiting_question',
        sessionId: SESSION_ID,
        pendingRequests: [supervisorPendingQuestion({ sessionId: SESSION_ID, ...CHILD })],
      }),
    });

    const turn = within(transcript()).getByRole('group', { name: 'Agent question' });
    expect(within(turn).getByText('Branch').parentElement).toHaveTextContent('BranchSub-agent');
    expect(within(turn).getByText('Sub-agent')).toBeVisible();
  });

  it('leaves a root question turn untagged', async () => {
    await renderPage({
      supervisorState: supervisorState({
        settings: CHOSEN,
        lifecycle: 'waiting_question',
        sessionId: SESSION_ID,
        pendingRequests: [supervisorPendingQuestion({ sessionId: SESSION_ID })],
      }),
    });

    const turn = within(transcript()).getByRole('group', { name: 'Agent question' });
    expect(within(turn).getByText('Branch')).toBeVisible();
    expect(within(turn).queryByText('Sub-agent')).toBeNull();
  });

  it('renders answered sub-agent records as tagged verdicts beside untagged root ones', async () => {
    await renderPage({
      supervisorState: supervisorState({ settings: CHOSEN, lifecycle: 'idle' }),
      supervisorTranscript: supervisorTranscriptPage({
        items: [
          childRecord(1, 'permission', 'perm-a', 'requested', 'pending'),
          childRecord(2, 'permission', 'perm-a', 'resolved', 'allowed'),
          childRecord(3, 'permission', 'perm-b', 'resolved', 'denied'),
          childRecord(4, 'question', 'question-a', 'requested', 'pending'),
          childRecord(5, 'question', 'question-a', 'resolved', 'answered'),
          childRecord(6, 'permission', 'perm-c', 'resolved', 'interrupted'),
          requestRecord(7, 'permission', 'perm-root', 'requested', 'pending'),
          requestRecord(8, 'permission', 'perm-root', 'resolved', 'allowed'),
        ],
        firstSeq: 1,
        lastSeq: 8,
        headSeq: 8,
      }),
    });

    expect(within(transcript()).getByText('Allowed · Bash · Sub-agent · make test')).toBeVisible();
    expect(within(transcript()).getByText('Denied · Bash · Sub-agent')).toBeVisible();
    expect(
      within(transcript()).getByText('Answered · Question · Sub-agent · Database'),
    ).toBeVisible();
    expect(within(transcript()).getByText('Interrupted · Bash · Sub-agent')).toBeVisible();
    expect(within(transcript()).getByText('Allowed Bash · make test')).toBeVisible();
  });
});

describe('SupervisorPage harness switch', () => {
  const switchCatalogue: ModelCatalogue = {
    ...CATALOGUE,
    phaseProviderModels: { chat: { claude: ['claude-opus', 'claude-sonnet'], codex: ['gpt-a'] } },
    chatDefaultEffort: { codex: 'medium' },
  };

  it('opens confirmation from the group action and sends only touched fields', async () => {
    const mock = await renderPage(
      { supervisorState: supervisorState({ settings: CHOSEN, headSeq: 2, lifecycle: 'idle' }) },
      switchCatalogue,
    );
    const user = userEvent.setup();
    await user.click(screen.getByTestId('supervisor-model-chip'));
    await user.click(screen.getByRole('button', { name: 'Switch to Codex…' }));
    const dialog = screen.getByRole('dialog', { name: 'Switch to Codex' });
    expect(within(dialog).getByText(/Sub-agents and background shells/)).toBeVisible();
    expect(within(dialog).getByRole('combobox', { name: 'Model' })).toHaveValue('gpt-a');
    await user.click(within(dialog).getByRole('button', { name: 'Switch' }));
    await waitFor(() =>
      expect(mock.api.updateSupervisorSettings).toHaveBeenCalledWith(
        expect.objectContaining({ harness: 'codex' }),
      ),
    );
    const request = mock.api.updateSupervisorSettings.mock.lastCall?.[0];
    expect(request).not.toHaveProperty('model');
    expect(request).not.toHaveProperty('effort');
  });

  it('preselects a foreign model, supports Escape, and commits touched pickers', async () => {
    const mock = await renderPage(
      { supervisorState: supervisorState({ settings: CHOSEN, headSeq: 2, lifecycle: 'stopped' }) },
      switchCatalogue,
    );
    const user = userEvent.setup();
    await user.click(screen.getByTestId('supervisor-model-chip'));
    await user.click(screen.getByRole('radio', { name: 'GPT A' }));
    const dialog = screen.getByRole('dialog', { name: 'Switch to Codex' });
    expect(within(dialog).queryByText(/Sub-agents and background shells/)).toBeNull();
    await user.keyboard('{Escape}');
    expect(mock.api.updateSupervisorSettings).not.toHaveBeenCalled();
    await user.click(screen.getByTestId('supervisor-model-chip'));
    await user.click(screen.getByRole('radio', { name: 'GPT A' }));
    await user.selectOptions(
      within(screen.getByRole('dialog', { name: 'Switch to Codex' })).getByRole('combobox', {
        name: 'Effort',
      }),
      'medium',
    );
    await user.click(
      within(screen.getByRole('dialog', { name: 'Switch to Codex' })).getByRole('button', {
        name: 'Switch',
      }),
    );
    await waitFor(() =>
      expect(mock.api.updateSupervisorSettings).toHaveBeenCalledWith(
        expect.objectContaining({ harness: 'codex', model: 'gpt-a', effort: 'medium' }),
      ),
    );
  });

  it('routes a unique foreign /model match to confirmation', async () => {
    const mock = await renderPage(
      { supervisorState: supervisorState({ settings: CHOSEN, headSeq: 2 }) },
      switchCatalogue,
    );
    const user = userEvent.setup();
    await user.type(composer(), '/model GPT A{Enter}');
    expect(screen.getByRole('dialog', { name: 'Switch to Codex' })).toBeVisible();
    expect(mock.api.updateSupervisorSettings).not.toHaveBeenCalled();
    await user.click(
      within(screen.getByRole('dialog', { name: 'Switch to Codex' })).getByRole('button', {
        name: 'Cancel',
      }),
    );
    expect(screen.queryByRole('dialog', { name: 'Switch to Codex' })).toBeNull();
    expect(mock.api.updateSupervisorSettings).not.toHaveBeenCalled();
  });

  it('commits directly for an empty transcript and disables an empty destination', async () => {
    const mock = await renderPage(
      { supervisorState: supervisorState({ settings: CHOSEN, headSeq: 0 }) },
      switchCatalogue,
    );
    const user = userEvent.setup();
    await user.click(screen.getByTestId('supervisor-model-chip'));
    await user.click(screen.getByRole('button', { name: 'Switch to Codex…' }));
    expect(screen.queryByRole('dialog', { name: 'Switch to Codex' })).toBeNull();
    expect(mock.api.updateSupervisorSettings).toHaveBeenCalledWith(
      expect.objectContaining({ harness: 'codex' }),
    );
  });

  it('renders a pending switch with the committed chip and a neutral switch marker', async () => {
    await renderPage({
      supervisorState: supervisorState({
        settings: CHOSEN,
        lifecycle: 'running',
        pendingChange: {
          requestId: 'switch-1',
          kind: 'harness',
          target: { harness: 'codex', model: 'gpt-a', effort: '' },
          requestedAt: '2026-10-06T10:00:00Z',
        },
      }),
      supervisorTranscript: supervisorTranscriptPage({
        items: [
          supervisorMarkerRecord(
            { marker: 'harness_change', text: 'Switched to Codex · gpt-a' },
            { seq: 1, turnId: '' },
          ),
        ],
        firstSeq: 1,
        lastSeq: 1,
        headSeq: 1,
      }),
    });
    expect(screen.getByText(/Switch to Codex pending/)).toBeVisible();
    expect(screen.getByText('Codex · gpt-a')).toBeVisible();
    expect(screen.getByTestId('supervisor-model-chip')).toHaveAccessibleName('Claude Opus · High');
    expect(within(transcript()).getByText('Switched to Codex · gpt-a')).toBeVisible();
  });
});

describe('SupervisorPage switch recovery', () => {
  it('reapplies attempted settings before retrying the composer message', async () => {
    const attemptedSettings = { harness: 'codex', model: 'gpt-a', effort: '' };
    const mock = await renderPage({
      supervisorState: supervisorState({
        settings: CHOSEN,
        lifecycle: 'failed',
        failure: supervisorLaunchFailure({ attemptedSettings }),
      }),
    });
    const user = userEvent.setup();
    expect(
      within(screen.getByRole('alert')).getByText("Couldn't switch to Codex — still using Claude"),
    ).toBeVisible();
    await user.type(composer(), 'Continue my work');
    await user.click(within(screen.getByRole('alert')).getByRole('button', { name: 'Retry' }));
    await waitFor(() =>
      expect(mock.api.sendSupervisorMessage).toHaveBeenCalledWith({ text: 'Continue my work' }),
    );
    expect(mock.api.updateSupervisorSettings).toHaveBeenCalledWith(
      expect.objectContaining(attemptedSettings),
    );
    expect(mock.api.updateSupervisorSettings.mock.invocationCallOrder[0]).toBeLessThan(
      mock.api.sendSupervisorMessage.mock.invocationCallOrder[0]!,
    );
  });
});
