import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import { beforeEach, describe, expect, it } from 'vitest';
import type { ReadinessSnapshot } from '../../../../shared/ipc';
import { installAgenticoMock, readySnapshot, unreadySnapshot } from '../../test/agenticoMock';
import { dispatchMediaChange, matchMediaState } from '../../test/setup';
import { SetupWizard } from './SetupWizard';

beforeEach(() => {
  matchMediaState.darkScheme = true;
  matchMediaState.reducedMotion = false;
  delete document.documentElement.dataset['theme'];
});

/** Renders the wizard the way the gate does: snapshot state fed by actions. */
function Harness({ initial }: { initial: ReadinessSnapshot }) {
  const [snapshot, setSnapshot] = useState(initial);
  return <SetupWizard snapshot={snapshot} onSnapshot={setSnapshot} />;
}

/** Providers satisfied, models still outstanding. */
const modelsStep = () =>
  unreadySnapshot({ providers: [{ name: 'claude', installed: true, ready: true }] });

describe('SetupWizard provider step', () => {
  it('renders server-reported executable, version, and authentication state per provider', async () => {
    installAgenticoMock();
    render(<Harness initial={unreadySnapshot()} />);

    const claude = screen.getByText('claude').closest('li')!;
    expect(within(claude).getByText('installed')).toBeInTheDocument();
    expect(within(claude).getByText('2.1.0')).toBeInTheDocument();
    expect(within(claude).getByText('not signed in')).toBeInTheDocument();
    expect(within(claude).getByText(/needs attention/i)).toBeInTheDocument();

    const codex = screen.getByText('codex').closest('li')!;
    expect(within(codex).getByText('not found')).toBeInTheDocument();
    await waitFor(() =>
      expect(screen.getByRole('group', { name: /setup progress/i })).toBeInTheDocument(),
    );
  });

  it('shows the provider CLI remediation as copyable code and announces the copy', async () => {
    installAgenticoMock();
    const user = userEvent.setup();
    render(<Harness initial={unreadySnapshot()} />);

    expect(screen.getByText('claude login')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: /copy the claude command/i }));
    await waitFor(() =>
      expect(screen.getByRole('status')).toHaveTextContent(/copied the claude command/i),
    );
    await expect(window.navigator.clipboard.readText()).resolves.toBe('claude login');
  });

  it('"Check again" refreshes from the server and advances to the derived step', async () => {
    const mock = installAgenticoMock();
    mock.api.refreshReadiness.mockResolvedValue(modelsStep());
    render(<Harness initial={unreadySnapshot()} />);

    await userEvent.click(screen.getByRole('button', { name: /check again/i }));
    expect(mock.api.refreshReadiness).toHaveBeenCalledTimes(1);
    await waitFor(() =>
      expect(screen.getByRole('heading', { name: /model availability/i })).toBeInTheDocument(),
    );
  });

  it('moves focus to a safe error region when the recheck fails', async () => {
    const mock = installAgenticoMock();
    mock.api.refreshReadiness.mockRejectedValue(
      new Error('E_NOT_CONNECTED: The app is not connected to an Agentico runtime.'),
    );
    render(<Harness initial={unreadySnapshot()} />);

    await userEvent.click(screen.getByRole('button', { name: /check again/i }));
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('E_NOT_CONNECTED');
    expect(alert).toHaveFocus();
    // The step and its retry action remain actionable.
    expect(screen.getByRole('button', { name: /check again/i })).toBeEnabled();
  });

  it('renders an actionable empty state when the server reports no providers', () => {
    installAgenticoMock();
    render(<Harness initial={unreadySnapshot({ providers: [] })} />);
    expect(screen.getByText(/reported no providers/i)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /check again/i })).toBeEnabled();
  });
});

describe('SetupWizard cross-cutting a11y and presentation', () => {
  it('is operable keyboard-only: tab order reaches help, copy, and check again', async () => {
    const mock = installAgenticoMock();
    mock.api.refreshReadiness.mockResolvedValue(unreadySnapshot());
    const user = userEvent.setup();
    render(<Harness initial={unreadySnapshot()} />);

    await user.tab();
    expect(screen.getByRole('button', { name: /hide help/i })).toHaveFocus();
    await user.tab();
    expect(screen.getByRole('button', { name: /copy the claude command/i })).toHaveFocus();
    await user.tab();
    expect(screen.getByRole('button', { name: /copy the codex command/i })).toHaveFocus();
    await user.tab();
    expect(screen.getByRole('button', { name: /check again/i })).toHaveFocus();
    await user.keyboard('{Enter}');
    expect(mock.api.refreshReadiness).toHaveBeenCalledTimes(1);
  });

  it('announces action outcomes via a polite live region', async () => {
    installAgenticoMock();
    render(<Harness initial={unreadySnapshot()} />);
    const region = screen.getByRole('status');
    expect(region).toHaveAttribute('aria-live', 'polite');
  });

  it('honors prefers-reduced-motion by not pulsing the spine needle', () => {
    matchMediaState.reducedMotion = true;
    installAgenticoMock();
    render(<Harness initial={unreadySnapshot()} />);
    const needle = document.querySelector('.phase-spine__needle');
    expect(needle).not.toBeNull();
    expect(needle!.className).not.toContain('pulse');
  });

  it('adapts to narrow windows via the viewport hook', async () => {
    installAgenticoMock();
    render(<Harness initial={unreadySnapshot()} />);
    const wizard = screen.getByLabelText(/first-launch setup/i);
    expect(wizard).not.toHaveAttribute('data-narrow');
    dispatchMediaChange('(max-width: 480px)', true);
    await waitFor(() => expect(wizard).toHaveAttribute('data-narrow', 'true'));
  });

  it('renders under both themes with text+icon status cues (never color alone)', () => {
    installAgenticoMock();
    for (const theme of ['light', 'dark'] as const) {
      document.documentElement.dataset['theme'] = theme;
      const { unmount } = render(<Harness initial={unreadySnapshot()} />);
      const state = document.querySelector('.provider-row__state');
      expect(state).not.toBeNull();
      expect(state!.textContent).toMatch(/needs attention/i);
      expect(state!.querySelector('[aria-hidden="true"]')).not.toBeNull();
      unmount();
    }
  });

  it('collapses help text and persists only the presentation preference', async () => {
    const mock = installAgenticoMock();
    render(<Harness initial={unreadySnapshot()} />);

    expect(screen.getByText(/provider clis installed on this machine/i)).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: /hide help/i }));
    expect(screen.queryByText(/provider clis installed on this machine/i)).not.toBeInTheDocument();
    await waitFor(() =>
      expect(mock.api.updateSettings).toHaveBeenCalledWith({
        wizard: { collapsedHelp: true },
      }),
    );
  });

  it('tolerates corrupt or unavailable local preferences with defaults', async () => {
    const mock = installAgenticoMock();
    mock.api.getSettings.mockRejectedValue(new Error('E_INTERNAL: settings unavailable'));
    render(<Harness initial={unreadySnapshot()} />);
    // Wizard still renders from the authoritative snapshot with default help.
    expect(await screen.findByRole('heading', { name: /set up agentico/i })).toBeInTheDocument();
    expect(screen.getByText(/provider clis installed on this machine/i)).toBeInTheDocument();
  });

  it('surfaces an invalid configuration as a blocking banner with remediation', () => {
    installAgenticoMock();
    render(
      <Harness
        initial={readySnapshot({
          ready: false,
          configuration: {
            valid: false,
            issue: {
              code: 'invalid_configuration',
              message: 'config.yaml is unreadable.',
              remedy: 'Fix config.yaml in the runtime directory.',
            },
          },
        })}
      />,
    );
    const banner = screen.getByRole('alert');
    expect(banner).toHaveTextContent('invalid_configuration');
    expect(banner).toHaveTextContent(/config\.yaml is unreadable/i);
    expect(banner).toHaveTextContent(/fix config\.yaml/i);
  });
});
