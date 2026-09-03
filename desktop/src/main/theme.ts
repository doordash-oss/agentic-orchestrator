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

/**
 * Theme controller bridging the persisted preference (settings) with
 * Electron's nativeTheme. The nativeTheme dependency is injected so the
 * controller stays unit-testable without Electron.
 */
import type { ThemeInfo, ThemePreference } from '../shared/ipc';

export interface NativeThemeLike {
  themeSource: string;
  readonly shouldUseDarkColors: boolean;
}

export class ThemeController {
  constructor(
    private readonly nativeTheme: NativeThemeLike,
    private readonly getStoredPreference: () => ThemePreference,
    private readonly persistPreference: (preference: ThemePreference) => void,
  ) {}

  /** Pushes the persisted preference into nativeTheme (call at startup). */
  applyStored(): void {
    this.nativeTheme.themeSource = this.getStoredPreference();
  }

  getInfo(): ThemeInfo {
    return {
      preference: this.getStoredPreference(),
      resolved: this.nativeTheme.shouldUseDarkColors ? 'dark' : 'light',
    };
  }

  setPreference(preference: ThemePreference): ThemeInfo {
    this.nativeTheme.themeSource = preference;
    this.persistPreference(preference);
    return this.getInfo();
  }
}
