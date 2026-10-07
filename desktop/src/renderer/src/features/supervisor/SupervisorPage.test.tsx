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
  supervisorRecord,
  supervisorState,
  supervisorTranscriptPage,
} from '../../test/agenticoMock';
import { emptyAttentionDrafts, type AttentionDrafts } from '../AttentionInbox';
import { SupervisorPage } from './SupervisorPage';

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
    codex: [{ id: 'gpt-a', displayName: 'GPT A' }],
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
  outcome: 'pending' | 'allowed' | 'denied' | 'answered',
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

function Harness({ refreshAttention }: { refreshAttention?: () => Promise<AttentionItem[]> }) {
  const [drafts, setDrafts] = useState<AttentionDrafts>(emptyAttentionDrafts);
  return (
    <SupervisorPage
      attentionDrafts={drafts}
      setAttentionDrafts={setDrafts}
      refreshAttention={refreshAttention ?? (async () => [])}
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
): Promise<ReturnType<typeof installAgenticoMock>> {
  const mock = installAgenticoMock(overrides);
  mock.api.getModelCatalogue.mockResolvedValue(CATALOGUE);
  render(<Harness />);
  await screen.findByTestId('supervisor-model-chip');
  await waitFor(() => expect(screen.queryByText('Loading the supervisor…')).toBeNull());
  return mock;
}

function emit(mock: ReturnType<typeof installAgenticoMock>, event: SupervisorEvent): void {
  act(() => mock.emitSupervisorEvent(event));
}

describe('SupervisorPage settings', () => {
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
    ).toEqual(['Opus', 'Sonnet']);

    await user.click(within(claude).getByRole('radio', { name: 'Opus' }));
    expect(mock.api.updateSupervisorSettings).toHaveBeenCalledWith({
      harness: 'claude',
      model: 'claude-opus',
      effort: '',
    });
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
    expect(mock.api.updateSupervisorSettings).toHaveBeenLastCalledWith({
      harness: 'claude',
      model: 'claude-opus',
      effort: 'high',
    });
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

  it('keeps the chip read-only while a supervisor process exists', async () => {
    await renderPage({ supervisorState: supervisorState({ lifecycle: 'idle', settings: CHOSEN }) });
    const user = userEvent.setup();

    const chip = screen.getByTestId('supervisor-model-chip');
    expect(chip).toHaveAccessibleName('Claude Opus · High');
    expect(chip).toHaveAttribute('aria-disabled', 'true');
    await user.click(chip);
    expect(screen.queryByRole('region', { name: 'Harness and model' })).toBeNull();
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
