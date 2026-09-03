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

import { describe, expect, it, vi } from 'vitest';
import { ThemeController, type NativeThemeLike } from '../theme';
import { AppEventSchema, type ThemePreference } from '../../shared/ipc';

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

  it('returns a payload that is exactly the cross-window theme fan-out event', () => {
    // The main process spreads setPreference's return into the 'theme' push
    // event so a theme picked in one window restyles the others. The fan-out
    // loop itself lives in a main-process closure; this pins the payload.
    const nativeTheme = makeNativeTheme(true);
    let stored: ThemePreference = 'system';
    const controller = new ThemeController(
      nativeTheme,
      () => stored,
      (p) => {
        stored = p;
      },
    );

    for (const preference of ['light', 'dark', 'system'] as const) {
      const info = controller.setPreference(preference);
      expect(AppEventSchema.parse({ type: 'theme', ...info })).toEqual({
        type: 'theme',
        preference: info.preference,
        resolved: info.resolved,
      });
    }
  });
});
