import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it } from 'vitest';
import type { ReadinessSnapshot } from '../../../shared/ipc';
import { installAgenticoMock, readySnapshot, unreadySnapshot } from '../test/agenticoMock';
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
    mock.api.getReadiness.mockReturnValueOnce(gate.promise);
    render(<ReadinessGate />);

    expect(screen.getByRole('status')).toHaveTextContent(/checking runtime readiness/i);
    expect(screen.queryByLabelText(/first-launch setup/i)).not.toBeInTheDocument();

    gate.resolve(unreadySnapshot());
    await waitFor(() =>
      expect(screen.getByRole('heading', { name: /set up agentico/i })).toBeInTheDocument(),
    );
  });

  it('sends an already-ready runtime straight to the main view without the wizard', async () => {
    installAgenticoMock({ readiness: readySnapshot() });
    render(<ReadinessGate />);
    expect(await screen.findByRole('tab', { name: 'Home' })).toBeInTheDocument();
    expect(await screen.findByRole('form', { name: /create a feature/i })).toBeInTheDocument();
    expect(screen.queryByLabelText(/first-launch setup/i)).not.toBeInTheDocument();
  });

  it('renders an actionable error with retry when the readiness fetch fails', async () => {
    const mock = installAgenticoMock();
    mock.api.getReadiness
      .mockRejectedValueOnce(new Error('E_NOT_CONNECTED: The app is not connected.'))
      .mockResolvedValueOnce(unreadySnapshot());
    render(<ReadinessGate />);

    await waitFor(() => expect(screen.getByText('E_NOT_CONNECTED')).toBeInTheDocument());
    await userEvent.click(screen.getByRole('button', { name: /try again/i }));
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

  it('yields the main view when the final gate is satisfied through consented init', async () => {
    const repositoryStep = readySnapshot({ repositories: [] });
    const mock = installAgenticoMock({ readiness: repositoryStep });
    mock.api.pickWorkspaceDirectory.mockResolvedValue({ path: '/work/space/new-repo' });
    mock.api.initRepository.mockResolvedValue(
      readySnapshot({
        repositories: [{ name: 'new-repo', path: '/work/space/new-repo', valid: true }],
      }),
    );
    render(<ReadinessGate />);

    await waitFor(() =>
      expect(screen.getByRole('heading', { name: /pick a repository/i })).toBeInTheDocument(),
    );
    await userEvent.click(screen.getByRole('button', { name: /choose repository folder/i }));
    await waitFor(() => expect(screen.getByRole('dialog')).toBeInTheDocument());
    await userEvent.click(screen.getByRole('button', { name: /initialize repository/i }));

    expect(await screen.findByRole('tab', { name: 'Home' })).toBeInTheDocument();
    expect(mock.api.initRepository).toHaveBeenCalledWith({
      path: '/work/space/new-repo',
      consent: true,
    });
    expect(screen.queryByLabelText(/first-launch setup/i)).not.toBeInTheDocument();
  });

  it('re-derives the step from the authoritative snapshot on every mount (resume)', async () => {
    // A restart after providers were fixed externally resumes at workspace,
    // not at the first step and not at an inferred local position.
    installAgenticoMock({
      readiness: readySnapshot({ workspaceRoots: [], repositories: [] }),
    });
    render(<ReadinessGate />);
    await waitFor(() =>
      expect(screen.getByRole('heading', { name: /choose a workspace/i })).toBeInTheDocument(),
    );
  });
});
