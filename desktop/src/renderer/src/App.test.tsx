import { act, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it } from 'vitest';
import { ConnectionStateSchema, type ConnectionState } from '../../shared/ipc';
import App from './App';
import { installAgenticoMock, readySnapshot } from './test/agenticoMock';
import { dispatchMediaChange, matchMediaState } from './test/setup';

/** Builds a state through the schema, so tests can only emit valid variants. */
function connection(overrides: Record<string, unknown>): ConnectionState {
  return ConnectionStateSchema.parse({
    status: 'discovering',
    stage: 'discover',
    detail: 'Looking for a running Agentico runtime.',
    ownership: 'none',
    ...overrides,
  });
}

beforeEach(() => {
  matchMediaState.darkScheme = true;
  matchMediaState.reducedMotion = false;
  delete document.documentElement.dataset['theme'];
});

describe('App theming', () => {
  it('applies the resolved theme from the main process on startup', async () => {
    installAgenticoMock({ theme: { preference: 'system', resolved: 'dark' } });
    render(<App />);
    await waitFor(() => expect(document.documentElement.dataset['theme']).toBe('dark'));
  });

  it('switches to the light theme when the user selects it', async () => {
    const mock = installAgenticoMock({ theme: { preference: 'system', resolved: 'dark' } });
    render(<App />);
    await waitFor(() => expect(document.documentElement.dataset['theme']).toBe('dark'));

    await userEvent.click(screen.getByRole('radio', { name: /light/i }));
    expect(mock.api.setThemePreference).toHaveBeenCalledWith('light');
    await waitFor(() => expect(document.documentElement.dataset['theme']).toBe('light'));
  });

  it('offers light, dark, and system choices in an accessible radiogroup', async () => {
    installAgenticoMock();
    render(<App />);
    const group = await screen.findByRole('radiogroup', { name: /theme/i });
    expect(group).toBeInTheDocument();
    for (const name of [/light/i, /dark/i, /system/i]) {
      expect(screen.getByRole('radio', { name })).toBeInTheDocument();
    }
  });

  it('follows OS appearance changes while the preference is system', async () => {
    installAgenticoMock({ theme: { preference: 'system', resolved: 'dark' } });
    render(<App />);
    await waitFor(() => expect(document.documentElement.dataset['theme']).toBe('dark'));

    dispatchMediaChange('(prefers-color-scheme: dark)', false);
    await waitFor(() => expect(document.documentElement.dataset['theme']).toBe('light'));
  });

  it('ignores OS appearance changes when an explicit theme is chosen', async () => {
    installAgenticoMock({ theme: { preference: 'dark', resolved: 'dark' } });
    render(<App />);
    await waitFor(() => expect(document.documentElement.dataset['theme']).toBe('dark'));

    dispatchMediaChange('(prefers-color-scheme: dark)', false);
    await new Promise((resolve) => setTimeout(resolve, 10));
    expect(document.documentElement.dataset['theme']).toBe('dark');
  });
});

describe('App readiness gating', () => {
  it('shows the connection shell — never the wizard — before the gateway is ready', async () => {
    const mock = installAgenticoMock();
    render(<App />);
    await waitFor(() =>
      expect(screen.getByRole('heading', { name: /^agentico$/i })).toBeInTheDocument(),
    );
    expect(screen.queryByLabelText(/first-launch setup/i)).not.toBeInTheDocument();
    expect(mock.api.getReadiness).not.toHaveBeenCalled();
  });

  it('opens the mandatory wizard when the runtime is ready but setup is incomplete', async () => {
    const mock = installAgenticoMock({
      connection: connection({ status: 'ready', stage: 'ready', ownership: 'app-owned' }),
    });
    render(<App />);
    await waitFor(() =>
      expect(screen.getByRole('heading', { name: /set up agentico/i })).toBeInTheDocument(),
    );
    expect(mock.api.getReadiness).toHaveBeenCalled();
    // No path into feature creation exists while gates are unsatisfied.
    expect(screen.queryByRole('button', { name: /create|new feature/i })).not.toBeInTheDocument();
  });

  it('skips the wizard entirely for an already-ready runtime', async () => {
    installAgenticoMock({
      connection: connection({ status: 'ready', stage: 'ready', ownership: 'external' }),
      readiness: readySnapshot(),
    });
    render(<App />);
    expect(await screen.findByRole('tab', { name: 'Home' })).toBeInTheDocument();
    expect(screen.queryByLabelText(/first-launch setup/i)).not.toBeInTheDocument();
  });

  it('falls back to the connection shell on disconnect and re-derives on recovery', async () => {
    const mock = installAgenticoMock({
      connection: connection({ status: 'ready', stage: 'ready', ownership: 'app-owned' }),
    });
    render(<App />);
    await waitFor(() =>
      expect(screen.getByRole('heading', { name: /set up agentico/i })).toBeInTheDocument(),
    );
    const fetchesBeforeCrash = mock.api.getReadiness.mock.calls.length;

    act(() => {
      mock.emitConnection(
        connection({
          status: 'crashed',
          stage: 'connect',
          detail: 'The app-managed runtime exited unexpectedly.',
          error: { code: 'E_SERVER_CRASHED', message: 'exited', remediation: 'Retry.' },
        }),
      );
    });
    expect(screen.queryByLabelText(/first-launch setup/i)).not.toBeInTheDocument();
    expect(screen.getByLabelText(/agentico connection/i)).toBeInTheDocument();

    act(() => {
      mock.emitConnection(connection({ status: 'ready', stage: 'ready', ownership: 'app-owned' }));
    });
    // Recovery refetches the authoritative snapshot instead of trusting
    // anything remembered from before the crash.
    await waitFor(() =>
      expect(mock.api.getReadiness.mock.calls.length).toBeGreaterThan(fetchesBeforeCrash),
    );
    await waitFor(() =>
      expect(screen.getByRole('heading', { name: /set up agentico/i })).toBeInTheDocument(),
    );
  });
});
