export const BACKGROUND_REFRESH_DELAY_MS = 5_000;

export interface FeatureRefreshScheduler {
  invalidate(): void;
  setActive(active: boolean): void;
  setVisible(visible: boolean): void;
  dispose(): void;
}

export function createFeatureRefreshScheduler(
  refresh: () => Promise<void>,
  options: { active: boolean; visible: boolean; backgroundDelayMs?: number },
): FeatureRefreshScheduler {
  let active = options.active;
  let visible = options.visible;
  let dirty = false;
  let inFlight = false;
  let disposed = false;
  let timer: ReturnType<typeof setTimeout> | null = null;
  const delay = options.backgroundDelayMs ?? BACKGROUND_REFRESH_DELAY_MS;
  const cancelTimer = () => {
    if (timer !== null) clearTimeout(timer);
    timer = null;
  };
  const schedule = () => {
    if (disposed || active || !visible || !dirty || inFlight || timer !== null) return;
    timer = setTimeout(() => {
      timer = null;
      void flush();
    }, delay);
  };
  const flush = async () => {
    if (disposed || !visible || !dirty || inFlight) return;
    cancelTimer();
    dirty = false;
    inFlight = true;
    try {
      await refresh();
    } finally {
      inFlight = false;
      if (!disposed && dirty && visible) {
        if (active) void flush();
        else schedule();
      }
    }
  };
  return {
    invalidate() {
      if (disposed) return;
      dirty = true;
      if (active && visible) void flush();
      else schedule();
    },
    setActive(next) {
      active = next;
      if (active) {
        cancelTimer();
        if (visible) void flush();
      } else schedule();
    },
    setVisible(next) {
      visible = next;
      if (!visible) cancelTimer();
      else if (active) void flush();
      else schedule();
    },
    dispose() {
      disposed = true;
      cancelTimer();
    },
  };
}
