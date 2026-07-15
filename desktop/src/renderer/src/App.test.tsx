import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it } from 'vitest';
import App from './App';
import { installAgenticoMock } from './test/agenticoMock';
import { dispatchMediaChange, matchMediaState } from './test/setup';

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
