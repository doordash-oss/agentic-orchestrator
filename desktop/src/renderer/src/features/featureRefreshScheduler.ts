export const BACKGROUND_REFRESH_DELAY_MS = 5_000;

export interface FeatureRefreshOptions {
  silent?: boolean;
}

export interface FeatureRefreshScheduler {
  /** Run an immediate refresh through the same single-flight coordinator. */
  refresh(options?: FeatureRefreshOptions): Promise<void>;
  invalidate(): void;
  setActive(active: boolean): void;
  setVisible(visible: boolean): void;
  dispose(): void;
}

export function createFeatureRefreshScheduler(
  runRefresh: (options?: FeatureRefreshOptions) => Promise<void>,
  options: { active: boolean; visible: boolean; backgroundDelayMs?: number },
): FeatureRefreshScheduler {
  let active = options.active;
  let visible = options.visible;
  let dirty = false;
  let inFlight: Promise<void> | null = null;
  let disposed = false;
  let timer: ReturnType<typeof setTimeout> | null = null;
  let queuedSilent = true;
  let queuedRequests: {
    resolve(): void;
    reject(error: unknown): void;
  }[] = [];
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
  const start = (refreshOptions: FeatureRefreshOptions = {}): Promise<void> => {
    let resolveResult!: () => void;
    let rejectResult!: (error: unknown) => void;
    const result = new Promise<void>((resolve, reject) => {
      resolveResult = resolve;
      rejectResult = reject;
    });
    const flight = result.finally(() => {
      if (inFlight === flight) inFlight = null;
      if (disposed) {
        for (const request of queuedRequests) request.resolve();
        queuedRequests = [];
        return;
      }
      if (queuedRequests.length > 0) {
        const requests = queuedRequests;
        const nextOptions = queuedSilent ? { silent: true } : {};
        queuedRequests = [];
        queuedSilent = true;
        dirty = false;
        cancelTimer();
        void start(nextOptions).then(
          () => requests.forEach((request) => request.resolve()),
          (error: unknown) => requests.forEach((request) => request.reject(error)),
        );
        return;
      }
      if (dirty && visible) {
        if (active) void flush();
        else schedule();
      }
    });
    // Publish the flight before crossing into the refresh callback. The
    // callback may synchronously trigger an invalidation at an IPC boundary.
    inFlight = flight;
    try {
      void runRefresh(refreshOptions).then(resolveResult, rejectResult);
    } catch (error) {
      rejectResult(error);
    }
    return flight;
  };
  const flush = async () => {
    if (disposed || !visible || !dirty || inFlight) return;
    cancelTimer();
    dirty = false;
    await start({ silent: true });
  };
  return {
    refresh(refreshOptions = {}) {
      if (disposed) return Promise.resolve();
      cancelTimer();
      if (inFlight === null) {
        dirty = false;
        return start(refreshOptions);
      }
      if (refreshOptions.silent !== true) queuedSilent = false;
      return new Promise<void>((resolve, reject) => {
        queuedRequests.push({ resolve, reject });
      });
    },
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
      dirty = false;
      cancelTimer();
      for (const request of queuedRequests) request.resolve();
      queuedRequests = [];
    },
  };
}
