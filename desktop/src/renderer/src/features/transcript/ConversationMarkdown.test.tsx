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

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ConversationMarkdown } from './ConversationMarkdown';

const openExternal = vi.fn();
beforeEach(() => {
  openExternal.mockReset().mockResolvedValue({ ok: true });
  Object.assign(window, { agentico: { openExternal } });
});
afterEach(cleanup);

describe('conversation links', () => {
  it.each([
    ['click', {}],
    ['Cmd-click', { metaKey: true }],
    ['Ctrl-click', { ctrlKey: true }],
  ])('opens %s through the external-browser boundary without navigating', async (_, modifiers) => {
    render(<ConversationMarkdown text="[**Review PR**](https://example.com/pull/1?x=1&y=2)" />);
    const link = screen.getByRole('link', { name: 'Review PR' });
    expect(link).toHaveAttribute('href', 'https://example.com/pull/1?x=1&y=2');
    expect(fireEvent.click(link.querySelector('strong')!, modifiers)).toBe(false);
    await waitFor(() =>
      expect(openExternal).toHaveBeenCalledExactlyOnceWith({
        url: 'https://example.com/pull/1?x=1&y=2',
      }),
    );
  });

  it('supports keyboard activation and bare URLs', async () => {
    render(<ConversationMarkdown text="See https://example.com/guide." />);
    const link = screen.getByRole('link', { name: 'https://example.com/guide' });
    link.focus();
    await userEvent.keyboard('{Enter}');
    expect(openExternal).toHaveBeenCalledExactlyOnceWith({ url: 'https://example.com/guide' });
  });

  it('opens a middle click externally without creating a window', async () => {
    render(<ConversationMarkdown text="[Guide](https://example.com/guide)" />);
    const event = new MouseEvent('auxclick', { button: 1, bubbles: true, cancelable: true });
    expect(fireEvent(screen.getByRole('link'), event)).toBe(false);
    await waitFor(() => expect(openExternal).toHaveBeenCalledTimes(1));
  });

  it.each([
    'javascript:alert%281%29',
    'data:text/html,hello',
    'file:///tmp/a',
    '/tmp/a',
    'http://example.com',
    'https://user:secret@example.com',
    '//example.com',
  ])('keeps unsupported destination %s inert', (url) => {
    render(<ConversationMarkdown text={`[Destination](${url})`} />);
    expect(screen.queryByRole('link')).not.toBeInTheDocument();
    fireEvent.click(screen.getByText(/^Destination/), { metaKey: true });
    expect(openExternal).not.toHaveBeenCalled();
  });

  it('keeps code, raw HTML and images inert', () => {
    const { container } = render(
      <ConversationMarkdown
        text={
          '`[code](https://example.com)`\n\n```\nhttps://example.com\n```\n\n<a href="https://example.com">raw</a>\n\n<script>alert(1)</script>\n\n![Screenshot](https://example.com/track.png)'
        }
      />,
    );
    expect(container.querySelector('a, img, script')).toBeNull();
    expect(container.querySelectorAll('code')).toHaveLength(2);
    expect(container).toHaveTextContent('<script>alert(1)</script>');
    expect(screen.getByText('Screenshot')).toBeVisible();
    expect(openExternal).not.toHaveBeenCalled();
  });

  it.each(['rejected', 'denied'])('reports a %s open and lets the user retry', async (failure) => {
    if (failure === 'rejected') openExternal.mockRejectedValueOnce(new Error('unavailable'));
    else openExternal.mockResolvedValueOnce({ ok: false });
    render(<ConversationMarkdown text="[Guide](https://example.com/guide)" />);
    fireEvent.click(screen.getByRole('link'), { metaKey: true });
    expect(await screen.findByRole('status')).toHaveTextContent('Could not open link.');
    fireEvent.click(screen.getByRole('link'));
    await waitFor(() => expect(screen.queryByRole('status')).not.toBeInTheDocument());
    expect(openExternal).toHaveBeenCalledTimes(2);
  });
});
