/**
 * System accent color controller, mirroring `theme.ts`'s pattern: the
 * platform surface (Electron's `systemPreferences`) is injected so the
 * publish/fallback logic stays unit-testable without Electron. macOS-only —
 * every other platform, and any read failure on macOS, leaves `getCurrent()`
 * at `null` so the caller never broadcasts and the static per-appearance
 * blue tokens hold.
 */
export interface AccentColorSource {
  getAccentColor(): string;
  subscribeNotification(event: string, callback: () => void): number;
  unsubscribeNotification(id: number): void;
}

const ACCENT_CHANGE_NOTIFICATION = 'NSSystemColorsDidChangeNotification';

export class AccentController {
  private subscriptionId: number | null = null;
  private current: string | null = null;

  constructor(
    private readonly platform: NodeJS.Platform,
    private readonly source: AccentColorSource,
    private readonly onChange: (color: string) => void,
  ) {}

  /** Reads the current color and subscribes to future changes. No-op off macOS. */
  start(): void {
    if (this.platform !== 'darwin') return;
    this.refresh();
    this.subscriptionId = this.source.subscribeNotification(ACCENT_CHANGE_NOTIFICATION, () =>
      this.refresh(),
    );
  }

  stop(): void {
    if (this.subscriptionId !== null) {
      this.source.unsubscribeNotification(this.subscriptionId);
      this.subscriptionId = null;
    }
  }

  /** The last successfully read value, for delivering to a renderer that just finished loading. */
  getCurrent(): string | null {
    return this.current;
  }

  private refresh(): void {
    let normalized: string | null;
    try {
      normalized = normalizeAccentColor(this.source.getAccentColor());
    } catch {
      normalized = null;
    }
    if (normalized === null) return;
    this.current = normalized;
    this.onChange(normalized);
  }
}

/** Electron reports `RRGGBBAA`; the renderer only needs an opaque `#rrggbb`. */
export function normalizeAccentColor(raw: string): string | null {
  const hex = raw.replace(/^#/, '');
  if (!/^[0-9a-f]{6,8}$/i.test(hex)) return null;
  return `#${hex.slice(0, 6).toLowerCase()}`;
}
