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

import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { ReadinessSnapshot } from '../../../shared/ipc';
import {
  installAgenticoMock,
  ipcError,
  readySnapshot,
  unreadySnapshot,
} from '../test/agenticoMock';
import { matchMediaState } from '../test/setup';
import { ReadinessGate } from './ReadinessGate';

beforeEach(() => {
  matchMediaState.darkScheme = true;
  matchMediaState.reducedMotion = false;
});

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

describe('ReadinessGate first snapshot', () => {
  it('shows a loading state and never flashes the wizard before the snapshot arrives', async () => {
    const mock = installAgenticoMock();
    const gate = deferred<ReadinessSnapshot>();
    mock.api.getRuntimeReadiness.mockReturnValueOnce(gate.promise);
    render(<ReadinessGate />);

    expect(screen.getByRole('status')).toHaveTextContent(/checking runtime readiness/i);
    expect(screen.queryByLabelText(/first-launch setup/i)).not.toBeInTheDocument();

    gate.resolve(unreadySnapshot());
    await waitFor(() =>
      expect(screen.getByRole('heading', { name: /set up agentico/i })).toBeInTheDocument(),
    );
  });

  it('opens the dashboard while repository readiness remains unresolved', async () => {
    const mock = installAgenticoMock({ readiness: readySnapshot() });
    mock.api.getReadiness.mockReturnValue(new Promise(() => {}));
    render(<ReadinessGate />);
    expect(await screen.findByRole('option', { name: 'Supervisor' })).toBeVisible();
    expect(mock.api.getReadiness).not.toHaveBeenCalled();
    expect(mock.api.getRuntimeReadiness).toHaveBeenCalledTimes(1);
  });

  it('sends an already-ready runtime straight to the main view without the wizard', async () => {
    installAgenticoMock({ readiness: readySnapshot() });
    render(<ReadinessGate />);
    expect(await screen.findByRole('option', { name: 'Supervisor' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'New feature' })).toBeInTheDocument();
    expect(screen.queryByRole('form', { name: /create a feature/i })).not.toBeInTheDocument();
    expect(screen.queryByLabelText(/first-launch setup/i)).not.toBeInTheDocument();
  });

  it('renders an actionable error with retry when the readiness fetch fails', async () => {
    const mock = installAgenticoMock();
    mock.api.getRuntimeReadiness
      .mockRejectedValueOnce(ipcError('E_NOT_CONNECTED', 'The app is not connected.'))
      .mockResolvedValueOnce(unreadySnapshot());
    render(<ReadinessGate />);

    // The parsed canonical error renders as one compact ErrorSurface; the
    // hand-written remediation sentence is gone.
    const surface = await screen.findByRole('alert');
    expect(surface).toHaveClass('error-surface', 'error-surface--compact');
    expect(screen.getByText('E_NOT_CONNECTED')).toHaveClass('error-surface__code');
    expect(screen.queryByText(/readiness check could not be completed/i)).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: /retry/i }));
    await waitFor(() =>
      expect(screen.getByRole('heading', { name: /set up agentico/i })).toBeInTheDocument(),
    );
  });
});

describe('ReadinessGate gating', () => {
  it('never offers feature creation while any mandatory gate is unsatisfied', async () => {
    installAgenticoMock({ readiness: unreadySnapshot() });
    render(<ReadinessGate />);
    await waitFor(() =>
      expect(screen.getByRole('heading', { name: /set up agentico/i })).toBeInTheDocument(),
    );
    expect(screen.queryByRole('button', { name: /create|new feature/i })).not.toBeInTheDocument();
  });

  it('yields the main view when a recheck satisfies the provider gate', async () => {
    const mock = installAgenticoMock({ readiness: unreadySnapshot() });
    mock.api.refreshRuntimeReadiness.mockResolvedValue(readySnapshot());
    render(<ReadinessGate />);

    await waitFor(() =>
      expect(screen.getByRole('heading', { name: /set up agentico/i })).toBeInTheDocument(),
    );
    await userEvent.click(screen.getByRole('button', { name: /check again/i }));

    expect(await screen.findByRole('option', { name: 'Supervisor' })).toBeInTheDocument();
    expect(screen.queryByLabelText(/first-launch setup/i)).not.toBeInTheDocument();
  });

  it('does not gate on an empty workspace: repositories are chosen during creation', async () => {
    installAgenticoMock({ readiness: readySnapshot({ workspaceRoots: [], repositories: [] }) });
    render(<ReadinessGate />);

    expect(await screen.findByRole('option', { name: 'Supervisor' })).toBeInTheDocument();
    expect(screen.queryByLabelText(/first-launch setup/i)).not.toBeInTheDocument();
  });

  it('re-derives the step from the authoritative snapshot on every mount (resume)', async () => {
    // A restart after the CLI was installed externally resumes at models, not
    // at the first step and not at an inferred local position — now inside the
    // on-demand sheet, because one ready provider mounts the shell.
    installAgenticoMock({ readiness: modelsPartialSnapshot() });
    render(<ReadinessGate />);
    await userEvent.click(await screen.findByRole('button', { name: 'Open setup' }));
    const sheet = await screen.findByRole('dialog', { name: 'Set up Agentico' });
    expect(within(sheet).getByRole('heading', { name: /model availability/i })).toBeVisible();
  });
});

const CONFIG_ISSUE = {
  code: 'invalid_configuration',
  class: 'blocking',
  title: 'Invalid configuration',
  summary: 'The pipeline profile "turbo" is not defined.',
} as const;

/** One ready provider, a usable model catalogue, but an unusable configuration. */
function configPartialSnapshot(): ReadinessSnapshot {
  return readySnapshot({
    ready: false,
    configuration: { valid: false, issue: CONFIG_ISSUE },
    issues: [CONFIG_ISSUE],
  });
}

/** One ready provider (two missing) and no models discovered yet. */
function modelsPartialSnapshot(): ReadinessSnapshot {
  const base = unreadySnapshot();
  return {
    ...base,
    providers: [
      { name: 'claude', installed: true, version: '2.1.0', ready: true },
      ...base.providers.filter((provider) => provider.name !== 'claude'),
    ],
    issues: base.issues.filter((issue) => issue.code !== 'unauthenticated'),
  };
}

const setupBanner = () => screen.queryByRole('region', { name: 'Setup incomplete' });

describe('ReadinessGate partial readiness', () => {
  it('keeps the full-page wizard while no provider is ready', async () => {
    installAgenticoMock({ readiness: unreadySnapshot() });
    render(<ReadinessGate />);
    expect(await screen.findByLabelText(/first-launch setup/i)).toBeInTheDocument();
    expect(screen.queryByRole('option', { name: 'Supervisor' })).not.toBeInTheDocument();
    expect(setupBanner()).toBeNull();
  });

  it('mounts the shell with a banner naming an invalid configuration first', async () => {
    installAgenticoMock({ readiness: configPartialSnapshot() });
    render(<ReadinessGate />);
    expect(await screen.findByRole('option', { name: 'Supervisor' })).toBeInTheDocument();
    expect(screen.queryByLabelText(/first-launch setup/i)).not.toBeInTheDocument();

    const banner = setupBanner();
    expect(banner).not.toBeNull();
    // The banner is the compact canonical surface; its role follows the class.
    const surface = within(banner!).getByRole('alert');
    expect(surface).toHaveClass('error-surface', 'error-surface--compact');
    expect(within(surface).getByText('invalid_configuration')).toHaveClass('error-surface__code');
    expect(within(surface).getByText('Invalid configuration')).toBeVisible();
    expect(within(surface).getByRole('button', { name: 'Open setup' })).toBeVisible();
    // The sheet never opens on its own.
    expect(screen.queryByRole('dialog', { name: 'Set up Agentico' })).not.toBeInTheDocument();
  });

  it('names the models issue when the active step is models', async () => {
    installAgenticoMock({ readiness: modelsPartialSnapshot() });
    render(<ReadinessGate />);
    expect(await screen.findByRole('option', { name: 'Supervisor' })).toBeInTheDocument();
    const banner = setupBanner();
    expect(banner).not.toBeNull();
    expect(within(banner!).getByText('models_unavailable')).toBeVisible();
    expect(within(banner!).getByText('Models unavailable')).toBeVisible();
  });

  it('renders no banner for a complete runtime', async () => {
    installAgenticoMock({ readiness: readySnapshot() });
    render(<ReadinessGate />);
    expect(await screen.findByRole('option', { name: 'Supervisor' })).toBeInTheDocument();
    expect(setupBanner()).toBeNull();
    expect(screen.queryByRole('button', { name: 'Open setup' })).not.toBeInTheDocument();
  });

  it('opens the wizard sheet from the banner and closes it on Escape, restoring focus', async () => {
    const user = userEvent.setup();
    installAgenticoMock({ readiness: configPartialSnapshot() });
    render(<ReadinessGate />);
    const opener = await screen.findByRole('button', { name: 'Open setup' });
    await user.click(opener);

    const sheet = await screen.findByRole('dialog', { name: 'Set up Agentico' });
    expect(sheet).toHaveAttribute('aria-modal', 'true');
    expect(sheet).toHaveClass('sheet');
    expect(sheet.parentElement).toHaveClass('sheet-scrim');
    // The configuration issue leads; every gate otherwise passes, so the
    // active step is the final one.
    expect(within(sheet).getByText('invalid_configuration')).toBeVisible();
    expect(within(sheet).getByRole('heading', { name: 'Almost there' })).toBeVisible();

    await user.keyboard('{Escape}');
    expect(screen.queryByRole('dialog', { name: 'Set up Agentico' })).not.toBeInTheDocument();
    await waitFor(() => expect(opener).toHaveFocus());
    // Closing the sheet leaves the banner in place.
    expect(setupBanner()).not.toBeNull();
  });

  it('closes the sheet on a scrim press and on its Close button', async () => {
    const user = userEvent.setup();
    installAgenticoMock({ readiness: modelsPartialSnapshot() });
    render(<ReadinessGate />);
    const opener = await screen.findByRole('button', { name: 'Open setup' });

    await user.click(opener);
    const sheet = await screen.findByRole('dialog', { name: 'Set up Agentico' });
    fireEvent.mouseDown(sheet.parentElement!);
    expect(screen.queryByRole('dialog', { name: 'Set up Agentico' })).not.toBeInTheDocument();
    await waitFor(() => expect(opener).toHaveFocus());

    await user.click(opener);
    const again = await screen.findByRole('dialog', { name: 'Set up Agentico' });
    // A press inside the sheet is not a scrim press.
    fireEvent.mouseDown(again);
    expect(screen.getByRole('dialog', { name: 'Set up Agentico' })).toBeInTheDocument();
    await user.click(within(again).getByRole('button', { name: 'Close' }));
    expect(screen.queryByRole('dialog', { name: 'Set up Agentico' })).not.toBeInTheDocument();
    await waitFor(() => expect(opener).toHaveFocus());
  });

  it('opens the sheet from the setup route with the active step shown', async () => {
    installAgenticoMock({ readiness: modelsPartialSnapshot() });
    const { rerender } = render(<ReadinessGate />);
    await screen.findByRole('option', { name: 'Supervisor' });
    await screen.findByRole('button', { name: 'Open setup' });

    rerender(<ReadinessGate routeRequest={{ id: 1, event: { target: 'setup' } }} />);
    const sheet = await screen.findByRole('dialog', { name: 'Set up Agentico' });
    expect(within(sheet).getByRole('heading', { name: /model availability/i })).toBeVisible();
  });

  it('ignores the setup route once the runtime is complete', async () => {
    installAgenticoMock({ readiness: readySnapshot() });
    const { rerender } = render(<ReadinessGate />);
    await screen.findByRole('option', { name: 'Supervisor' });
    rerender(<ReadinessGate routeRequest={{ id: 1, event: { target: 'setup' } }} />);
    await waitFor(() => expect(screen.getByRole('option', { name: 'Supervisor' })).toBeVisible());
    expect(screen.queryByRole('dialog', { name: 'Set up Agentico' })).not.toBeInTheDocument();
  });

  it('closes the sheet and removes the banner when Check again returns a complete snapshot', async () => {
    const user = userEvent.setup();
    const mock = installAgenticoMock({ readiness: configPartialSnapshot() });
    mock.api.refreshRuntimeReadiness.mockResolvedValue(readySnapshot());
    render(<ReadinessGate />);
    await user.click(await screen.findByRole('button', { name: 'Open setup' }));
    const sheet = await screen.findByRole('dialog', { name: 'Set up Agentico' });

    await user.click(within(sheet).getByRole('button', { name: 'Check again' }));

    await waitFor(() =>
      expect(screen.queryByRole('dialog', { name: 'Set up Agentico' })).not.toBeInTheDocument(),
    );
    expect(setupBanner()).toBeNull();
    // The shell stayed mounted throughout.
    expect(screen.getByRole('option', { name: 'Supervisor' })).toBeInTheDocument();
  });

  it('updates the banner and the sheet together when Check again stays incomplete', async () => {
    const user = userEvent.setup();
    const mock = installAgenticoMock({ readiness: configPartialSnapshot() });
    mock.api.refreshRuntimeReadiness.mockResolvedValue(modelsPartialSnapshot());
    render(<ReadinessGate />);
    await user.click(await screen.findByRole('button', { name: 'Open setup' }));
    const sheet = await screen.findByRole('dialog', { name: 'Set up Agentico' });

    await user.click(within(sheet).getByRole('button', { name: 'Check again' }));

    await waitFor(() =>
      expect(within(sheet).getByRole('heading', { name: /model availability/i })).toBeVisible(),
    );
    expect(within(setupBanner()!).getByText('Models unavailable')).toBeVisible();
    expect(within(setupBanner()!).queryByText('Invalid configuration')).toBeNull();
  });

  it('tells the native menu that setup is incomplete, and clears it once complete', async () => {
    const user = userEvent.setup();
    const mock = installAgenticoMock({ readiness: configPartialSnapshot() });
    mock.api.refreshRuntimeReadiness.mockResolvedValue(readySnapshot());
    render(<ReadinessGate />);
    await screen.findByRole('button', { name: 'Open setup' });
    await waitFor(() =>
      expect(mock.api.publishUiState).toHaveBeenLastCalledWith(
        expect.objectContaining({ setupIncomplete: true }),
      ),
    );

    await user.click(screen.getByRole('button', { name: 'Open setup' }));
    await user.click(
      within(await screen.findByRole('dialog', { name: 'Set up Agentico' })).getByRole('button', {
        name: 'Check again',
      }),
    );
    await waitFor(() =>
      expect(mock.api.publishUiState).toHaveBeenLastCalledWith(
        expect.objectContaining({ setupIncomplete: false }),
      ),
    );
  });

  it('reports partial mode to its host so the palette can offer Setup…', async () => {
    const user = userEvent.setup();
    const onSetupIncompleteChange = vi.fn();
    const mock = installAgenticoMock({ readiness: configPartialSnapshot() });
    mock.api.refreshRuntimeReadiness.mockResolvedValue(readySnapshot());
    render(<ReadinessGate onSetupIncompleteChange={onSetupIncompleteChange} />);
    await screen.findByRole('button', { name: 'Open setup' });
    expect(onSetupIncompleteChange).toHaveBeenLastCalledWith(true);

    await user.click(screen.getByRole('button', { name: 'Open setup' }));
    await user.click(
      within(await screen.findByRole('dialog', { name: 'Set up Agentico' })).getByRole('button', {
        name: 'Check again',
      }),
    );
    await waitFor(() => expect(onSetupIncompleteChange).toHaveBeenLastCalledWith(false));
  });

  it('never reports partial mode for the full-page wizard', async () => {
    const onSetupIncompleteChange = vi.fn();
    installAgenticoMock({ readiness: unreadySnapshot() });
    render(<ReadinessGate onSetupIncompleteChange={onSetupIncompleteChange} />);
    await screen.findByLabelText(/first-launch setup/i);
    expect(onSetupIncompleteChange).not.toHaveBeenCalledWith(true);
  });

  it("keeps New feature enabled and shows the server's not_ready error in the creation sheet", async () => {
    const user = userEvent.setup();
    const mock = installAgenticoMock({ readiness: modelsPartialSnapshot() });
    mock.api.getCreationDefaults.mockRejectedValue(
      ipcError('not_ready', 'The runtime is not ready to create features: Models unavailable.', {
        class: 'needs_action',
        title: 'Runtime not ready',
      }),
    );
    render(<ReadinessGate />);
    const newFeature = await screen.findByRole('button', { name: 'New feature' });
    expect(newFeature).toBeEnabled();
    await user.click(newFeature);

    const creation = await screen.findByRole('dialog', { name: 'New feature' });
    expect(await within(creation).findByText('not_ready')).toHaveClass('error-surface__code');
    expect(within(creation).getByText('Runtime not ready')).toBeVisible();
  });
});
