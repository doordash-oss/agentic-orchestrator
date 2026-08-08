import { act, renderHook, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';
import { installAgenticoMock } from './test/agenticoMock';
import { useSystemAccentMirror, useTheme } from './hooks';

describe('useSystemAccentMirror', () => {
  afterEach(() => {
    document.documentElement.style.removeProperty('--accent');
  });

  it('mirrors a published accent event onto the root custom property', () => {
    const mock = installAgenticoMock();
    renderHook(() => useSystemAccentMirror());

    mock.emitAppEvent({ type: 'accent', color: '#3d7dff' });

    expect(document.documentElement.style.getPropertyValue('--accent')).toBe('#3d7dff');
  });

  it('ignores unrelated app events', () => {
    const mock = installAgenticoMock();
    renderHook(() => useSystemAccentMirror());

    mock.emitAppEvent({ type: 'status', stream: 'live' });

    expect(document.documentElement.style.getPropertyValue('--accent')).toBe('');
  });

  it('unsubscribes on unmount', () => {
    const mock = installAgenticoMock();
    const { unmount } = renderHook(() => useSystemAccentMirror());
    expect(mock.appEventListenerCount()).toBe(1);

    unmount();

    expect(mock.appEventListenerCount()).toBe(0);
  });
});

describe('useTheme cross-window sync', () => {
  afterEach(() => {
    delete document.documentElement.dataset['theme'];
  });

  it('applies the main process theme broadcast to its state and <html data-theme>', async () => {
    const mock = installAgenticoMock({ theme: { preference: 'system', resolved: 'dark' } });
    const { result } = renderHook(() => useTheme());
    await waitFor(() => expect(result.current.resolved).toBe('dark'));
    expect(document.documentElement.dataset['theme']).toBe('dark');

    // A same-document CustomEvent cannot cross a window boundary, so the
    // Settings window's switch reaches this window only as an app event.
    act(() => mock.emitAppEvent({ type: 'theme', preference: 'light', resolved: 'light' }));

    await waitFor(() => expect(document.documentElement.dataset['theme']).toBe('light'));
    expect(result.current.preference).toBe('light');
    expect(result.current.resolved).toBe('light');
    // Nothing was written back: the broadcast is already the persisted truth.
    expect(mock.api.setThemePreference).not.toHaveBeenCalled();
  });

  it('ignores app events that are not theme broadcasts', async () => {
    const mock = installAgenticoMock({ theme: { preference: 'dark', resolved: 'dark' } });
    const { result } = renderHook(() => useTheme());
    await waitFor(() => expect(result.current.preference).toBe('dark'));

    act(() => mock.emitAppEvent({ type: 'invalidated', kind: 'resync' }));

    expect(result.current.preference).toBe('dark');
    expect(document.documentElement.dataset['theme']).toBe('dark');
  });
});
