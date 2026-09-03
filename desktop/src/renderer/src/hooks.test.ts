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

import { act, renderHook, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { ConnectionState } from '../../shared/ipc';
import { installAgenticoMock } from './test/agenticoMock';
import { useConnectionState, useIpcLoad, useSystemAccentMirror, useTheme } from './hooks';

describe('useIpcLoad', () => {
  it('reloads through one request lane and ignores a stale completion', async () => {
    let resolveFirst!: (value: string) => void;
    let resolveSecond!: (value: string) => void;
    const load = vi
      .fn<() => Promise<string>>()
      .mockReturnValueOnce(new Promise((resolve) => (resolveFirst = resolve)))
      .mockReturnValueOnce(new Promise((resolve) => (resolveSecond = resolve)));
    const { result } = renderHook(() => useIpcLoad(load, []));
    await waitFor(() => expect(load).toHaveBeenCalledTimes(1));

    act(() => result.current.reload());
    await waitFor(() => expect(load).toHaveBeenCalledTimes(2));
    await act(async () => resolveSecond('new'));
    expect(result.current.state).toEqual({ phase: 'loaded', data: 'new' });

    await act(async () => resolveFirst('stale'));
    expect(result.current.state).toEqual({ phase: 'loaded', data: 'new' });
  });

  it('replaces loaded data and prevents an in-flight request from overwriting it', async () => {
    let resolveLoad!: (value: string) => void;
    const load = vi.fn<() => Promise<string>>(
      () => new Promise((resolve) => (resolveLoad = resolve)),
    );
    const { result } = renderHook(() => useIpcLoad(load, []));
    await waitFor(() => expect(load).toHaveBeenCalledOnce());

    act(() => result.current.replace('adopted'));
    expect(result.current.state).toEqual({ phase: 'loaded', data: 'adopted' });

    await act(async () => resolveLoad('stale'));
    expect(result.current.state).toEqual({ phase: 'loaded', data: 'adopted' });
  });
});

describe('useConnectionState', () => {
  it('does not let a stale initial snapshot overwrite a pushed state', async () => {
    const mock = installAgenticoMock();
    let resolveInitial!: (state: ConnectionState) => void;
    mock.api.getConnectionStatus.mockImplementationOnce(
      () => new Promise<ConnectionState>((resolve) => (resolveInitial = resolve)),
    );
    const { result } = renderHook(() => useConnectionState());
    const ready: ConnectionState = {
      status: 'ready',
      stage: 'ready',
      detail: 'Connected.',
      ownership: 'external',
      kind: 'local',
      serverKey: 'local:runtime',
      serverName: 'frothy-macchiato',
    };

    act(() => mock.emitConnection(ready));
    expect(result.current).toEqual(ready);

    await act(async () => {
      resolveInitial({
        status: 'connecting',
        stage: 'authenticate',
        detail: 'Authenticating.',
        ownership: 'external',
      });
      await Promise.resolve();
    });

    expect(result.current).toEqual(ready);
  });
});

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
