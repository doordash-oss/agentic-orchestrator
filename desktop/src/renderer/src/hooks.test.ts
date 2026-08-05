import { renderHook } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';
import { installAgenticoMock } from './test/agenticoMock';
import { useSystemAccentMirror } from './hooks';

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
