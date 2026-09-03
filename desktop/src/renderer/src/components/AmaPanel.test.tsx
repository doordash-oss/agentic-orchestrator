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

import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  defaultSettings,
  type AttentionItem,
  type ConnectionState,
  type Settings,
} from '../../../shared/ipc';
import { installAgenticoMock } from '../test/agenticoMock';
import { AmaPanel } from './AmaPanel';

afterEach(cleanup);

/** ⌥Space, as the renderer sees it on macOS: alt plus the Space code. */
function pressOptionSpace(target: Window | Element = window): void {
  fireEvent.keyDown(target, { key: ' ', code: 'Space', altKey: true });
}

describe('AmaPanel open and close', () => {
  it('stays closed on first run and reserves nothing in the frame', async () => {
    const mock = installAgenticoMock();
    renderPanel();

    await waitFor(() => expect(mock.api.getSettings).toHaveBeenCalled());
    expect(screen.queryByRole('complementary', { name: 'Ask Agentico' })).not.toBeInTheDocument();
    expect(screen.queryByRole('textbox', { name: 'Ask Agentico' })).not.toBeInTheDocument();
  });

  it('opens at the default bottom-trailing placement and closes again on ⌥Space', async () => {
    const mock = installAgenticoMock();
    renderPanel();
    await waitFor(() => expect(mock.api.getSettings).toHaveBeenCalled());

    pressOptionSpace();

    const panel = await screen.findByRole('complementary', { name: 'Ask Agentico' });
    expect(panel).toHaveStyle({ right: '20px', bottom: '20px', width: '404px', height: '560px' });
    expect(mock.api.updateSettings).toHaveBeenCalledWith({
      ama: {
        drawer: 'expanded',
        geometry: { right: 20, bottom: 20, width: 404, height: 560 },
      },
    });

    pressOptionSpace();

    await waitFor(() =>
      expect(screen.queryByRole('complementary', { name: 'Ask Agentico' })).not.toBeInTheDocument(),
    );
    expect(mock.api.updateSettings).toHaveBeenLastCalledWith({
      ama: {
        drawer: 'compact',
        geometry: { right: 20, bottom: 20, width: 404, height: 560 },
      },
    });
  });

  it('toggles from a focused composer without typing a character', async () => {
    installAgenticoMock({ settings: settingsWithAma({ drawer: 'expanded' }) });
    renderPanel();
    const input = await screen.findByRole('textbox', { name: 'Ask Agentico' });
    await userEvent.type(input, 'Draft');

    pressOptionSpace(input);

    expect(input).toHaveValue('Draft');
    await waitFor(() =>
      expect(screen.queryByRole('complementary', { name: 'Ask Agentico' })).not.toBeInTheDocument(),
    );
  });

  it('closes with ✕ and persists the closed state', async () => {
    const mock = installAgenticoMock({ settings: settingsWithAma({ drawer: 'expanded' }) });
    renderPanel();

    await userEvent.click(await screen.findByRole('button', { name: 'Close Ask Agentico' }));

    await waitFor(() =>
      expect(screen.queryByRole('complementary', { name: 'Ask Agentico' })).not.toBeInTheDocument(),
    );
    expect(mock.api.updateSettings).toHaveBeenLastCalledWith({
      ama: { drawer: 'compact', geometry: { right: 20, bottom: 20, width: 404, height: 560 } },
    });
  });

  it('restores the persisted open state and geometry, and the live transcript', async () => {
    installAgenticoMock({
      settings: settingsWithAma({
        drawer: 'expanded',
        geometry: { right: 64, bottom: 96, width: 480, height: 420 },
      }),
      transcript: {
        sessionId: '__chat__',
        cursor: { total: 1, start: 0, end: 1 },
        messages: [{ index: 0, role: 'assistant', type: 'text', text: 'Restored transcript.' }],
      },
    });
    renderPanel();

    const panel = await screen.findByRole('complementary', { name: 'Ask Agentico' });
    expect(panel).toHaveStyle({ right: '64px', bottom: '96px', width: '480px', height: '420px' });
    expect(await screen.findByText('Restored transcript.')).toBeVisible();
  });

  it('opens, and never closes, on a routed ama request and focuses the composer', async () => {
    installAgenticoMock();
    const { rerender } = render(panelElement({ routeRequest: null }));
    await waitFor(() =>
      expect(screen.queryByRole('complementary', { name: 'Ask Agentico' })).not.toBeInTheDocument(),
    );

    rerender(panelElement({ routeRequest: { id: 1, event: { target: 'ama' } } }));
    await waitFor(() =>
      expect(screen.getByRole('textbox', { name: 'Ask Agentico' })).toHaveFocus(),
    );

    rerender(panelElement({ routeRequest: { id: 2, event: { target: 'ama' } } }));
    expect(await screen.findByRole('complementary', { name: 'Ask Agentico' })).toBeVisible();
  });
});

describe('AmaPanel first open', () => {
  // Before the first question there is no chat session, so a not_found
  // transcript read is the empty state — never an error banner.
  it('shows only the empty state when no chat session exists yet', async () => {
    const mock = installAgenticoMock();
    mock.api.getSessionTranscript.mockRejectedValue(
      new Error('not_found: session not found The session no longer exists.'),
    );
    mock.api.getSession.mockRejectedValue(new Error('not_found: session not found'));
    renderPanel();
    await waitFor(() => expect(mock.api.getSettings).toHaveBeenCalled());

    pressOptionSpace();
    await screen.findByRole('complementary', { name: 'Ask Agentico' });
    await waitFor(() => expect(mock.api.getSessionTranscript).toHaveBeenCalled());

    expect(screen.getByText('Ask anything about this workspace.')).toBeVisible();
    expect(screen.queryByText(/not_found/)).not.toBeInTheDocument();
    expect(screen.queryByText(/session not found/)).not.toBeInTheDocument();
  });
});

describe('AmaPanel geometry', () => {
  it('drags by the header and persists the new placement on release', async () => {
    const mock = installAgenticoMock({ settings: settingsWithAma({ drawer: 'expanded' }) });
    renderPanel();
    const panel = await screen.findByRole('complementary', { name: 'Ask Agentico' });

    dragBy(panel.querySelector('.ama-panel__header')!, {
      fromX: 200,
      fromY: 200,
      dx: -60,
      dy: -40,
    });

    expect(panel).toHaveStyle({ right: '80px', bottom: '60px' });
    expect(mock.api.updateSettings).toHaveBeenLastCalledWith({
      ama: { drawer: 'expanded', geometry: { right: 80, bottom: 60, width: 404, height: 560 } },
    });
  });

  it('never drags the panel out of the window', async () => {
    installAgenticoMock({ settings: settingsWithAma({ drawer: 'expanded' }) });
    renderPanel();
    const panel = await screen.findByRole('complementary', { name: 'Ask Agentico' });

    dragBy(panel.querySelector('.ama-panel__header')!, {
      fromX: 200,
      fromY: 200,
      dx: 4000,
      dy: 4000,
    });

    expect(panel).toHaveStyle({ right: '0px', bottom: '0px' });
  });

  it('resizes from a corner grip within the usable minimum', async () => {
    const mock = installAgenticoMock({ settings: settingsWithAma({ drawer: 'expanded' }) });
    renderPanel();
    const panel = await screen.findByRole('complementary', { name: 'Ask Agentico' });

    dragBy(panel.querySelector('.ama-panel__grip[data-edge="nw"]')!, {
      fromX: 600,
      fromY: 200,
      dx: -50,
      dy: -30,
    });
    expect(panel).toHaveStyle({ width: '454px', height: '590px' });

    dragBy(panel.querySelector('.ama-panel__grip[data-edge="e"]')!, {
      fromX: 600,
      fromY: 200,
      dx: -4000,
      dy: 0,
    });
    expect(panel).toHaveStyle({ width: '320px' });
    expect(mock.api.updateSettings).toHaveBeenLastCalledWith({
      ama: expect.objectContaining({ drawer: 'expanded' }),
    });
  });

  it('clamps back inside the window when the window is resized around it', async () => {
    installAgenticoMock({
      settings: settingsWithAma({
        drawer: 'expanded',
        geometry: { right: 20, bottom: 20, width: 404, height: 560 },
      }),
    });
    renderPanel();
    const panel = await screen.findByRole('complementary', { name: 'Ask Agentico' });

    act(() => {
      window.innerWidth = 400;
      window.innerHeight = 480;
      window.dispatchEvent(new Event('resize'));
    });

    expect(panel).toHaveStyle({ width: '400px', height: '480px', right: '0px', bottom: '0px' });

    act(() => {
      window.innerWidth = 1024;
      window.innerHeight = 768;
      window.dispatchEvent(new Event('resize'));
    });
  });
});

describe('AmaPanel capabilities', () => {
  it('shows the live status line with the pending-question suffix', async () => {
    installAgenticoMock({
      settings: settingsWithAma({ drawer: 'expanded' }),
      session: activeChatSession(),
    });
    render(panelElement({ attentionItems: [chatQuestionItem] }));

    expect(await screen.findByText('Active · 1 pending')).toBeVisible();
    expect(
      screen.getByText('Which overall direction should this project take?'),
    ).toBeInTheDocument();
  });

  it('keeps the idle between-turn wait out of the pending strip and the suffix', async () => {
    installAgenticoMock({
      settings: settingsWithAma({ drawer: 'expanded' }),
      session: activeChatSession(),
    });
    render(panelElement({ attentionItems: [chatIdleWaitItem()] }));

    expect(await screen.findByText('Active')).toBeVisible();
    expect(screen.queryByText(/pending/)).not.toBeInTheDocument();
    expect(screen.queryByText('Agent is waiting')).not.toBeInTheDocument();
  });

  it('reports unread only for a reply that lands while the panel is closed', async () => {
    const mock = installAgenticoMock();
    const onUnreadChange = vi.fn();
    const { rerender } = render(panelElement({ onUnreadChange }));
    await waitFor(() => expect(mock.api.getSettings).toHaveBeenCalled());
    expect(onUnreadChange).toHaveBeenLastCalledWith(false);

    // A turn ends while closed: the wait appears with a fresh timestamp.
    rerender(panelElement({ onUnreadChange, attentionItems: [chatIdleWaitItem()] }));
    expect(onUnreadChange).toHaveBeenLastCalledWith(true);

    // Opening the panel is what marks the reply as seen.
    pressOptionSpace();
    await screen.findByRole('complementary', { name: 'Ask Agentico' });
    expect(onUnreadChange).toHaveBeenLastCalledWith(false);

    // Closing again over the same wait stays read…
    pressOptionSpace();
    await waitFor(() =>
      expect(screen.queryByRole('complementary', { name: 'Ask Agentico' })).not.toBeInTheDocument(),
    );
    expect(onUnreadChange).toHaveBeenLastCalledWith(false);

    // …until the next turn ends while closed.
    rerender(
      panelElement({
        onUnreadChange,
        attentionItems: [chatIdleWaitItem('2026-07-21T00:10:00Z')],
      }),
    );
    expect(onUnreadChange).toHaveBeenLastCalledWith(true);
  });

  it('reads Read-only transcript once an ended session still has messages', async () => {
    installAgenticoMock({
      settings: settingsWithAma({ drawer: 'expanded' }),
      session: activeChatSession({ status: 'completed' }),
      transcript: {
        sessionId: '__chat__',
        cursor: { total: 1, start: 0, end: 1 },
        messages: [{ index: 0, role: 'assistant', type: 'text', text: 'Archived answer.' }],
      },
    });
    renderPanel();

    expect(await screen.findByText('Read-only transcript')).toBeVisible();
  });

  it('ends the session from the composer row behind a confirmation', async () => {
    const mock = installAgenticoMock({
      settings: settingsWithAma({ drawer: 'expanded' }),
      session: activeChatSession(),
    });
    renderPanel();

    await userEvent.click(await screen.findByRole('button', { name: 'End session' }));
    const confirm = screen.getByRole('group', { name: 'End session confirmation' });
    expect(confirm).toHaveTextContent('transcript stays read-only');
    await waitFor(() => expect(confirm.querySelector('.ama-panel__danger')).toHaveFocus());

    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(
      screen.queryByRole('group', { name: 'End session confirmation' }),
    ).not.toBeInTheDocument();
    expect(mock.api.endChat).not.toHaveBeenCalled();

    await userEvent.click(screen.getByRole('button', { name: 'End session' }));
    await userEvent.click(
      screen
        .getByRole('group', { name: 'End session confirmation' })
        .querySelector('.ama-panel__danger')!,
    );

    expect(mock.api.endChat).toHaveBeenCalledTimes(1);
    expect(await screen.findByText('AMA ended.')).toBeVisible();
  });

  it('renders the user turn with the accent rule and the assistant turn plain', async () => {
    installAgenticoMock({
      settings: settingsWithAma({ drawer: 'expanded' }),
      session: activeChatSession({ initialPrompt: 'What changed?' }),
      transcript: {
        sessionId: '__chat__',
        cursor: { total: 1, start: 0, end: 1 },
        messages: [{ index: 0, role: 'assistant', type: 'text', text: 'The renderer changed.' }],
      },
    });
    renderPanel();

    const assistant = (await screen.findByText('The renderer changed.')).closest(
      '.conversation__message',
    );
    const user = screen.getByText('What changed?').closest('.conversation__message');
    expect(user).toHaveAttribute('data-role', 'user');
    expect(assistant).toHaveAttribute('data-role', 'assistant');
  });

  it('keeps the composer behaviors: Enter submits, Shift+Enter newlines, paste attaches', async () => {
    const mock = installAgenticoMock({ settings: settingsWithAma({ drawer: 'expanded' }) });
    mock.api.readClipboardImage.mockResolvedValue({ paths: ['/tmp/clipboard-image.png'] });
    renderPanel();
    const input = await screen.findByRole('textbox', { name: 'Ask Agentico' });

    await userEvent.type(input, 'First line{shift>}{enter}{/shift}Second line');
    expect(input).toHaveValue('First line\nSecond line');
    expect(mock.api.startChat).not.toHaveBeenCalled();
    await userEvent.clear(input);

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

  it('removes an attachment chip on its own control', async () => {
    const mock = installAgenticoMock({ settings: settingsWithAma({ drawer: 'expanded' }) });
    mock.api.readClipboardImage.mockResolvedValue({ paths: ['/tmp/clipboard-image.png'] });
    renderPanel();
    const input = await screen.findByRole('textbox', { name: 'Ask Agentico' });

    fireEvent.paste(input, {
      clipboardData: {
        files: [new File(['image'], 'image.png', { type: 'image/png' })],
        items: [{ type: 'image/png' }],
      },
    });
    await userEvent.click(
      await screen.findByRole('button', { name: 'Remove clipboard-image.png' }),
    );

    expect(screen.queryByText('clipboard-image.png')).not.toBeInTheDocument();
  });

  it('shows a sent message immediately while the turn is in flight', async () => {
    const mock = installAgenticoMock({ settings: settingsWithAma({ drawer: 'expanded' }) });
    mock.api.startChat.mockReturnValue(new Promise(() => undefined));
    renderPanel();

    await userEvent.type(
      await screen.findByRole('textbox', { name: 'Ask Agentico' }),
      'What is running?{enter}',
    );

    expect(mock.api.startChat).toHaveBeenCalledWith({ message: 'What is running?', images: [] });
    expect(await screen.findByText('What is running?')).toBeVisible();
    expect(screen.getByText('Thinking through your question')).toBeVisible();
  });

  it('streams live output into the open transcript', async () => {
    const mock = installAgenticoMock({
      settings: settingsWithAma({ drawer: 'expanded' }),
      session: activeChatSession({ initialPrompt: 'Inspect the app' }),
    });
    renderPanel();
    await waitFor(() => expect(mock.api.openSessionOutput).toHaveBeenCalled());

    act(() => {
      mock.emitSessionOutput({
        subscriptionId: 'subscription-1',
        type: 'record',
        sessionId: '__chat__',
        index: 0,
        message: { index: 0, role: 'assistant', type: 'text', text: 'Inspection complete.' },
      });
    });

    expect(await screen.findByText('Inspection complete.')).toBeVisible();
  });
});

describe('AmaPanel expanded presentation', () => {
  it('keeps the draft, transcript node, and scroll position across expand and close', async () => {
    installAgenticoMock({ settings: settingsWithAma({ drawer: 'expanded' }) });
    renderPanel();
    await userEvent.type(
      await screen.findByRole('textbox', { name: 'Ask Agentico' }),
      'Keep this draft',
    );

    await userEvent.click(screen.getByRole('button', { name: 'Expand AMA' }));
    const dialog = screen.getByRole('dialog', { name: 'Expanded AMA' });
    expect(dialog).toBeVisible();
    expect(screen.getAllByRole('textbox', { name: 'Ask Agentico' })).toHaveLength(1);
    const transcript = screen.getByRole('region', { name: 'AMA transcript' });
    transcript.scrollTop = 137;

    await userEvent.click(screen.getByRole('button', { name: 'Close expanded AMA' }));

    expect(screen.queryByRole('dialog', { name: 'Expanded AMA' })).not.toBeInTheDocument();
    expect(screen.getByRole('textbox', { name: 'Ask Agentico' })).toHaveValue('Keep this draft');
    const reopened = screen.getByRole('region', { name: 'AMA transcript' });
    expect(reopened).toBe(transcript);
    expect(reopened.scrollTop).toBe(137);
    await waitFor(() => expect(screen.getByRole('button', { name: 'Expand AMA' })).toHaveFocus());
  });

  it('dismisses with Escape without changing the persisted open state', async () => {
    const mock = installAgenticoMock({ settings: settingsWithAma({ drawer: 'expanded' }) });
    renderPanel();

    await userEvent.click(await screen.findByRole('button', { name: 'Expand AMA' }));
    expect(screen.getByRole('dialog', { name: 'Expanded AMA' })).toBeVisible();
    mock.api.updateSettings.mockClear();

    fireEvent.keyDown(window, { key: 'Escape' });

    await waitFor(() =>
      expect(screen.queryByRole('dialog', { name: 'Expanded AMA' })).not.toBeInTheDocument(),
    );
    expect(mock.api.updateSettings).not.toHaveBeenCalled();
    expect(screen.getByRole('complementary', { name: 'Ask Agentico' })).toBeVisible();
    await waitFor(() => expect(screen.getByRole('button', { name: 'Expand AMA' })).toHaveFocus());
  });

  it('dismisses from the backdrop without changing the persisted open state', async () => {
    const mock = installAgenticoMock({ settings: settingsWithAma({ drawer: 'expanded' }) });
    renderPanel();

    await userEvent.click(await screen.findByRole('button', { name: 'Expand AMA' }));
    const dialog = screen.getByRole('dialog', { name: 'Expanded AMA' });
    mock.api.updateSettings.mockClear();

    fireEvent.mouseDown(dialog.parentElement!);

    await waitFor(() =>
      expect(screen.queryByRole('dialog', { name: 'Expanded AMA' })).not.toBeInTheDocument(),
    );
    expect(mock.api.updateSettings).not.toHaveBeenCalled();
    expect(screen.getByRole('complementary', { name: 'Ask Agentico' })).toBeVisible();
  });
});

describe('AmaPanel on a remote server', () => {
  const remoteConnection: ConnectionState = {
    status: 'ready',
    stage: 'ready',
    detail: 'Connected.',
    ownership: 'external',
    kind: 'remote',
    serverKey: 'server-key-1',
  };

  it('stages pasted images as uploads and sends their references', async () => {
    const mock = installAgenticoMock({
      connection: remoteConnection,
      settings: settingsWithAma({ drawer: 'expanded' }),
    });
    mock.api.importDroppedCreationFiles.mockImplementation(() => ({ paths: [] }));
    mock.api.readClipboardImage.mockResolvedValue({ paths: ['/tmp/clipboard-image.png'] });
    mock.api.startChat.mockResolvedValue({ sessionId: '__chat__', result: 'started' });
    renderPanel();
    const input = await screen.findByRole('textbox', { name: 'Ask Agentico' });

    fireEvent.paste(input, {
      clipboardData: {
        files: [new File(['image'], 'image.png', { type: 'image/png' })],
        items: [{ type: 'image/png' }],
      },
    });

    expect(await screen.findByText('clipboard-image.png')).toBeVisible();
    expect(mock.api.uploadCreationFiles).toHaveBeenCalledWith('image', [
      '/tmp/clipboard-image.png',
    ]);

    await userEvent.type(input, 'What is shown?{enter}');
    await waitFor(() =>
      expect(mock.api.startChat).toHaveBeenCalledWith({
        message: 'What is shown?',
        images: [],
        imageUploads: ['ref-clipboardimagepng'],
      }),
    );
  });

  it('blocks sending while an upload is in flight and re-enables after it fails+removes', async () => {
    const mock = installAgenticoMock({
      connection: remoteConnection,
      settings: settingsWithAma({ drawer: 'expanded' }),
    });
    mock.api.importDroppedCreationFiles.mockImplementation(() => ({ paths: [] }));
    mock.api.readClipboardImage.mockResolvedValue({ paths: ['/tmp/clipboard-image.png'] });
    mock.api.uploadCreationFiles.mockResolvedValue({
      results: [
        {
          ok: false,
          name: 'clipboard-image.png',
          error: { code: 'request_too_large', message: 'File exceeds limit.' },
        },
      ],
    });
    renderPanel();
    const input = await screen.findByRole('textbox', { name: 'Ask Agentico' });
    fireEvent.paste(input, {
      clipboardData: {
        files: [new File(['image'], 'image.png', { type: 'image/png' })],
        items: [{ type: 'image/png' }],
      },
    });
    expect(await screen.findByText('File exceeds limit.')).toBeVisible();
    await userEvent.type(input, 'hello');
    expect(screen.getByRole('button', { name: /Send/ })).toBeDisabled();

    await userEvent.click(screen.getByRole('button', { name: 'Remove clipboard-image.png' }));
    expect(screen.getByRole('button', { name: /Send/ })).toBeEnabled();
  });

  it('leaves plain-text pastes alone and re-routes image import after switching back to local', async () => {
    const mock = installAgenticoMock({
      connection: remoteConnection,
      settings: settingsWithAma({ drawer: 'expanded' }),
    });
    mock.api.readClipboardImage.mockResolvedValue({ paths: ['/tmp/clipboard-image.png'] });
    renderPanel();
    const input = await screen.findByRole('textbox', { name: 'Ask Agentico' });

    fireEvent.paste(input, {
      clipboardData: { files: [], items: [{ type: 'text/plain' }] },
    });
    expect(screen.queryByLabelText('Attached images')).not.toBeInTheDocument();
    expect(mock.api.importDroppedCreationFiles).not.toHaveBeenCalled();

    act(() => {
      mock.emitConnection({ ...remoteConnection, kind: 'local', ownership: 'app-owned' });
    });

    fireEvent.paste(input, {
      clipboardData: {
        files: [new File(['image'], 'image.png', { type: 'image/png' })],
        items: [{ type: 'image/png' }],
      },
    });
    expect(await screen.findByText('clipboard-image.png')).toBeVisible();
  });
});

function panelElement(
  overrides: {
    attentionItems?: AttentionItem[];
    routeRequest?: Parameters<typeof AmaPanel>[0]['routeRequest'];
    onUnreadChange?: (unread: boolean) => void;
  } = {},
) {
  return (
    <AmaPanel
      attentionItems={overrides.attentionItems ?? []}
      refreshAttention={() => Promise.resolve(overrides.attentionItems ?? [])}
      setAttentionDrafts={vi.fn()}
      routeRequest={overrides.routeRequest ?? null}
      {...(overrides.onUnreadChange === undefined
        ? {}
        : { onUnreadChange: overrides.onUnreadChange })}
    />
  );
}

function renderPanel(): void {
  render(panelElement());
}

function settingsWithAma(ama: Partial<Settings['ama']>): Settings {
  const base = defaultSettings();
  return { ...base, ama: { ...base.ama, ...ama } };
}

/** A full pointer gesture: press, one move, release. */
function dragBy(
  handle: Element,
  { fromX, fromY, dx, dy }: { fromX: number; fromY: number; dx: number; dy: number },
): void {
  fireEvent.pointerDown(handle, { button: 0, pointerId: 1, clientX: fromX, clientY: fromY });
  fireEvent.pointerMove(window, { pointerId: 1, clientX: fromX + dx, clientY: fromY + dy });
  fireEvent.pointerUp(window, { pointerId: 1, clientX: fromX + dx, clientY: fromY + dy });
}

const chatQuestionItem: AttentionItem = {
  kind: 'questions',
  id: 'questions-chat',
  featureId: '__chat__',
  sessionId: '__chat__',
  phase: 'AMA',
  waitingSince: '2026-07-21T00:00:00Z',
  questions: [
    {
      key: 'Which overall direction should this project take?',
      header: 'Project direction',
      multiSelect: false,
      options: [
        { label: 'Harden the review pipeline', confidence: 0.8 },
        { label: 'Build user-facing features', confidence: 0.5 },
      ],
    },
  ],
};

/** The chat's idle between-turn wait: a delivered reply, not a pending ask. */
function chatIdleWaitItem(waitingSince = '2026-07-21T00:05:00Z'): AttentionItem {
  return {
    kind: 'help',
    id: '__chat__:',
    sessionId: '__chat__',
    waitingSince,
    prompt: 'Agent has a question',
    waitingKind: 'input',
  };
}

function activeChatSession(overrides: Record<string, unknown> = {}) {
  return {
    id: '__chat__',
    featureId: '__chat__',
    runNumber: 0,
    phase: 'AMA',
    kind: 'chat',
    status: 'running',
    turnState: 'idle',
    startedAt: '2026-07-21T00:00:00Z',
    taskActivities: [],
    runningTaskCount: 0,
    usage: {},
    transcriptCursor: { total: 0, start: 0, end: 0 },
    pendingControlCount: 0,
    canAttach: false,
    logAvailable: false,
    ...overrides,
  };
}
