import { act, cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { installAgenticoMock } from '../test/agenticoMock';
import { AmaDock } from './AmaDock';

afterEach(cleanup);

describe('AmaDock', () => {
  it('marks the expanded drawer for a full-width transcript when no question is pending', async () => {
    installAgenticoMock();
    render(
      <AmaDock
        attentionItems={[]}
        refreshAttention={() => Promise.resolve([])}
        setAttentionDrafts={vi.fn()}
        routeRequest={null}
      />,
    );

    await userEvent.click(screen.getByRole('button', { name: 'AMA' }));

    expect(
      (await screen.findByRole('region', { name: 'AMA transcript' })).parentElement,
    ).toHaveAttribute('data-has-attention', 'false');
  });

  it('renders the prompt, activity, and response as a conversation', async () => {
    installAgenticoMock({
      session: activeChatSession({ initialPrompt: 'What changed?' }),
      transcript: {
        sessionId: '__chat__',
        cursor: { total: 4, start: 0, end: 4 },
        messages: [
          { index: 0, role: 'system', type: 'usage_update' },
          { index: 1, role: 'system', type: 'read', text: '/workspace/README.md' },
          { index: 2, role: 'assistant', type: 'tool_use', tool: 'Read' },
          { index: 3, role: 'assistant', type: 'text', text: 'The renderer changed.' },
        ],
      },
    });
    renderDock();

    await userEvent.click(screen.getByRole('button', { name: 'AMA' }));

    expect(await screen.findByText('What changed?')).toBeVisible();
    expect(screen.getByText('Reading README.md · Using read')).toBeVisible();
    expect(screen.getByText('The renderer changed.')).toBeVisible();
    expect(screen.queryByText('usage_update')).not.toBeInTheDocument();
  });

  it('shows a sent message immediately and submits Enter', async () => {
    const mock = installAgenticoMock();
    mock.api.startChat.mockReturnValue(new Promise(() => undefined));
    renderDock();
    const input = screen.getByRole('textbox', { name: 'Ask Agentico' });

    await userEvent.type(input, 'What is running?{enter}');

    expect(mock.api.startChat).toHaveBeenCalledWith({ message: 'What is running?' });
    expect(await screen.findByText('What is running?')).toBeVisible();
    expect(screen.getByText('Thinking through your question')).toBeVisible();
  });

  it('replaces live tool activity with the response when it arrives', async () => {
    const mock = installAgenticoMock({
      session: activeChatSession({ initialPrompt: 'Inspect the app' }),
    });
    renderDock();
    await userEvent.click(screen.getByRole('button', { name: 'AMA' }));
    await waitFor(() => expect(mock.api.openSessionOutput).toHaveBeenCalled());

    act(() => {
      mock.emitSessionOutput({
        subscriptionId: 'subscription-1',
        type: 'record',
        sessionId: '__chat__',
        index: 0,
        message: { index: 0, role: 'system', type: 'tool_progress', tool: 'Read' },
      });
    });
    expect(await screen.findByText('Using read')).toBeVisible();

    act(() => {
      mock.emitSessionOutput({
        subscriptionId: 'subscription-1',
        type: 'record',
        sessionId: '__chat__',
        index: 1,
        message: { index: 1, role: 'assistant', type: 'text', text: 'Inspection complete.' },
      });
    });
    expect(await screen.findByText('Inspection complete.')).toBeVisible();
    expect(screen.getByText('Worked')).toBeVisible();
  });

  it('keeps Shift+Enter as a newline', async () => {
    const mock = installAgenticoMock();
    renderDock();
    const input = screen.getByRole('textbox', { name: 'Ask Agentico' });

    await userEvent.type(input, 'First line{shift>}{enter}{/shift}Second line');

    expect(input).toHaveValue('First line\nSecond line');
    expect(mock.api.startChat).not.toHaveBeenCalled();
  });

  it('reopens a failed output subscription after sending a message', async () => {
    const mock = installAgenticoMock({ session: activeChatSession() });
    let answerAvailable = false;
    mock.api.getSessionTranscript.mockImplementation(({ sessionId }) =>
      Promise.resolve({
        sessionId,
        cursor: { total: answerAvailable ? 1 : 0, start: 0, end: answerAvailable ? 1 : 0 },
        messages: answerAvailable
          ? [{ index: 0, role: 'assistant', type: 'text', text: 'Yo! How can I help?' }]
          : [],
      }),
    );
    mock.api.openSessionOutput
      .mockRejectedValueOnce(new Error('session not found'))
      .mockImplementationOnce(() => {
        answerAvailable = true;
        return Promise.resolve({ subscriptionId: 'subscription-2' });
      });
    renderDock();
    await userEvent.click(screen.getByRole('button', { name: 'AMA' }));
    await waitFor(() => expect(mock.api.openSessionOutput).toHaveBeenCalledTimes(1));

    await userEvent.type(screen.getByRole('textbox', { name: 'Ask Agentico' }), 'yo{enter}');

    await waitFor(() => expect(mock.api.openSessionOutput).toHaveBeenCalledTimes(2));
    expect(await screen.findByText('Yo! How can I help?')).toBeVisible();
  });
});

function renderDock() {
  render(
    <AmaDock
      attentionItems={[]}
      refreshAttention={() => Promise.resolve([])}
      setAttentionDrafts={vi.fn()}
      routeRequest={null}
    />,
  );
}

function activeChatSession(overrides: Record<string, unknown> = {}) {
  return {
    id: '__chat__',
    featureId: '__chat__',
    runNumber: 0,
    phase: 'AMA',
    kind: 'chat',
    status: 'running',
    turnState: 'running',
    startedAt: '2026-07-21T00:00:00Z',
    usage: {},
    transcriptCursor: { total: 0, start: 0, end: 0 },
    pendingControlCount: 0,
    canAttach: false,
    logAvailable: false,
    ...overrides,
  };
}
