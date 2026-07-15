import { describe, expect, it, vi } from 'vitest';
import { ThemeController, type NativeThemeLike } from '../theme';
import type { ThemePreference } from '../../shared/ipc';

function makeNativeTheme(dark: boolean): NativeThemeLike & { themeSource: string } {
  return {
    themeSource: 'system',
    get shouldUseDarkColors() {
      return this.themeSource === 'dark' || (this.themeSource === 'system' && dark);
    },
  };
}

describe('ThemeController', () => {
  it('reports the persisted preference and the OS-resolved appearance', () => {
    const nativeTheme = makeNativeTheme(true);
    const controller = new ThemeController(nativeTheme, () => 'system', vi.fn());
    expect(controller.getInfo()).toEqual({ preference: 'system', resolved: 'dark' });
  });

  it('applies and persists an explicit preference', () => {
    const nativeTheme = makeNativeTheme(true);
    let stored: ThemePreference = 'system';
    const persist = vi.fn((p: ThemePreference) => {
      stored = p;
    });
    const controller = new ThemeController(nativeTheme, () => stored, persist);

    const info = controller.setPreference('light');
    expect(nativeTheme.themeSource).toBe('light');
    expect(persist).toHaveBeenCalledWith('light');
    expect(info).toEqual({ preference: 'light', resolved: 'light' });
  });

  it('syncs nativeTheme to the stored preference on applyStored', () => {
    const nativeTheme = makeNativeTheme(false);
    const controller = new ThemeController(nativeTheme, () => 'dark', vi.fn());
    controller.applyStored();
    expect(nativeTheme.themeSource).toBe('dark');
    expect(controller.getInfo()).toEqual({ preference: 'dark', resolved: 'dark' });
  });
});
