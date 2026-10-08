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

/**
 * The shell's half of "New conversation": the toolbar button and the routed
 * target both hand the Supervisor page one fresh request through the same
 * prop, and the page clears it once handled. The page itself is stubbed so
 * the delivery contract is asserted on its own.
 */
import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { defaultSettings } from '../../../shared/ipc';
import { installAgenticoMock } from '../test/agenticoMock';
import type { SupervisorPageProps } from './supervisor/SupervisorPage';
import { WorkspaceShell } from './WorkspaceShell';

const pageProps = vi.hoisted(() => ({ latest: null as SupervisorPageProps | null }));

vi.mock('./supervisor/SupervisorPage', () => ({
  SupervisorPage: (props: SupervisorPageProps) => {
    pageProps.latest = props;
    return <section aria-label="Supervisor" />;
  },
}));

afterEach(() => {
  cleanup();
  pageProps.latest = null;
});

function installHome() {
  return installAgenticoMock({
    settings: { ...defaultSettings(), shell: { featureByServer: {}, sidebarCollapsed: false } },
    features: [],
  });
}

describe('WorkspaceShell new-conversation delivery', () => {
  it('hands the page one fresh request per toolbar click, cleared once handled', async () => {
    installHome();
    render(<WorkspaceShell />);
    const toolbar = await screen.findByRole('banner', { name: 'Workspace toolbar' });
    const button = await within(toolbar).findByRole('button', { name: 'New conversation' });
    expect(pageProps.latest?.newConversationRequest ?? null).toBeNull();

    await userEvent.click(button);
    const first = pageProps.latest?.newConversationRequest;
    expect(first).toEqual({ id: expect.any(Number) });

    act(() => pageProps.latest?.onNewConversationRequestHandled?.());
    expect(pageProps.latest?.newConversationRequest ?? null).toBeNull();

    await userEvent.click(button);
    const second = pageProps.latest?.newConversationRequest;
    expect(second?.id).not.toBe(first?.id);
  });

  it('delivers the routed palette/menu target through the same request prop', async () => {
    installHome();
    const { rerender } = render(<WorkspaceShell />);
    await screen.findByRole('banner', { name: 'Workspace toolbar' });
    await waitFor(() => expect(pageProps.latest).not.toBeNull());

    rerender(<WorkspaceShell routeRequest={{ id: 7, event: { target: 'new-conversation' } }} />);
    await waitFor(() =>
      expect(pageProps.latest?.newConversationRequest).toEqual({ id: expect.any(Number) }),
    );
    // A routed request never doubles as a compose request.
    expect(pageProps.latest?.composeRequest ?? null).toBeNull();
  });
});
