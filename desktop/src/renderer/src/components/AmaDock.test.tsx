import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
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

    expect(mock.api.startChat).toHaveBeenCalledWith({ message: 'What is running?', images: [] });
    expect(await screen.findByText('What is running?')).toBeVisible();
    expect(screen.getByText('Thinking through your question')).toBeVisible();
  });

  it('pastes a clipboard bitmap and submits it with the AMA message', async () => {
    const mock = installAgenticoMock();
    mock.api.readClipboardImage.mockResolvedValue({ paths: ['/tmp/clipboard-image.png'] });
    renderDock();
    const input = screen.getByRole('textbox', { name: 'Ask Agentico' });

    fireEvent.paste(input, {
      clipboardData: {
        files: [new File(['image'], 'image.png', { type: 'image/png' })],
        items: [{ type: 'image/png' }],
      },
    });

    expect(await screen.findByText('clipboard-image.png')).toBeVisible();
    await userEvent.type(input, 'What is shown?{enter}');
    expect(mock.api.startChat).toHaveBeenCalledWith({
      message: 'What is shown?',
      images: ['/tmp/clipboard-image.png'],
    });
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

  it('keeps every block of a multi-block response instead of overwriting', async () => {
    installAgenticoMock({
      session: activeChatSession({ initialPrompt: 'Explain the change' }),
      transcript: {
        sessionId: '__chat__',
        cursor: { total: 2, start: 0, end: 2 },
        messages: [
          { index: 5, blockIndex: 0, role: 'assistant', type: 'text', text: 'First, the setup.' },
          { index: 5, blockIndex: 1, role: 'assistant', type: 'text', text: 'Then, the fix.' },
        ],
      },
    });
    renderDock();

    await userEvent.click(screen.getByRole('button', { name: 'AMA' }));

    expect(await screen.findByText('First, the setup.')).toBeVisible();
    expect(screen.getByText('Then, the fix.')).toBeVisible();
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

  it('opens one expanded AMA dialog and keeps the draft when it closes', async () => {
    installAgenticoMock();
    renderDock();
    const input = screen.getByRole('textbox', { name: 'Ask Agentico' });
    await userEvent.type(input, 'Keep this draft');

    const expand = screen.getByRole('button', { name: 'Expand AMA' });
    await userEvent.click(expand);

    const dialog = screen.getByRole('dialog', { name: 'Expanded AMA' });
    expect(dialog).toBeVisible();
    expect(screen.getAllByRole('textbox', { name: 'Ask Agentico' })).toHaveLength(1);
    expect(screen.getAllByRole('region', { name: 'AMA transcript' })).toHaveLength(1);

    await userEvent.click(screen.getByRole('button', { name: 'Close expanded AMA' }));

    expect(screen.queryByRole('dialog', { name: 'Expanded AMA' })).not.toBeInTheDocument();
    expect(screen.getByRole('textbox', { name: 'Ask Agentico' })).toHaveValue('Keep this draft');
    await waitFor(() => expect(screen.getByRole('button', { name: 'Expand AMA' })).toHaveFocus());
  });

  it('keeps the compact drawer transcript node and scroll position across modal closes', async () => {
    installAgenticoMock();
    renderDock();

    await userEvent.click(screen.getByRole('button', { name: 'Expand AMA' }));
    const transcript = screen.getByRole('region', { name: 'AMA transcript' });
    transcript.scrollTop = 137;

    await userEvent.click(screen.getByRole('button', { name: 'Close expanded AMA' }));
    await userEvent.click(screen.getByRole('button', { name: 'Expand AMA' }));

    const reopened = screen.getByRole('region', { name: 'AMA transcript' });
    expect(reopened).toBe(transcript);
    expect(reopened.scrollTop).toBe(137);
    expect(screen.getAllByRole('region', { name: 'AMA transcript' })).toHaveLength(1);
  });

  it('dismisses expanded AMA with Escape without changing the saved drawer mode', async () => {
    const mock = installAgenticoMock();
    renderDock();

    await userEvent.click(screen.getByRole('button', { name: 'Expand AMA' }));
    expect(screen.getByRole('dialog', { name: 'Expanded AMA' })).toBeVisible();

    fireEvent.keyDown(window, { key: 'Escape' });

    await waitFor(() =>
      expect(screen.queryByRole('dialog', { name: 'Expanded AMA' })).not.toBeInTheDocument(),
    );
    expect(mock.api.updateSettings).not.toHaveBeenCalled();
    await waitFor(() => expect(screen.getByRole('button', { name: 'Expand AMA' })).toHaveFocus());
  });

  it('dismisses expanded AMA from the backdrop', async () => {
    installAgenticoMock();
    renderDock();

    await userEvent.click(screen.getByRole('button', { name: 'Expand AMA' }));
    const dialog = screen.getByRole('dialog', { name: 'Expanded AMA' });

    fireEvent.mouseDown(dialog.parentElement!);

    await waitFor(() =>
      expect(screen.queryByRole('dialog', { name: 'Expanded AMA' })).not.toBeInTheDocument(),
    );
    await waitFor(() => expect(screen.getByRole('button', { name: 'Expand AMA' })).toHaveFocus());
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
