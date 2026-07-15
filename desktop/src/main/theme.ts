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
