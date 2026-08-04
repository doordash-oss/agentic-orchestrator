import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  BACKGROUND_REFRESH_DELAY_MS,
  createFeatureRefreshScheduler,
} from './featureRefreshScheduler';

afterEach(() => vi.useRealTimers());

describe('feature refresh scheduler', () => {
  it('coalesces inactive invalidations into one five-second refresh', async () => {
    vi.useFakeTimers();
    const refresh = vi.fn(() => Promise.resolve());
    const scheduler = createFeatureRefreshScheduler(refresh, {
      active: false,
      visible: true,
    });
    scheduler.invalidate();
    scheduler.invalidate();
    await vi.advanceTimersByTimeAsync(BACKGROUND_REFRESH_DELAY_MS - 1);
    expect(refresh).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1);
    expect(refresh).toHaveBeenCalledTimes(1);
  });

  it('flushes a dirty inactive tab immediately when activated', async () => {
    vi.useFakeTimers();
    const refresh = vi.fn(() => Promise.resolve());
    const scheduler = createFeatureRefreshScheduler(refresh, {
      active: false,
      visible: true,
    });
    scheduler.invalidate();
    scheduler.setActive(true);
    await vi.runAllTicks();
    expect(refresh).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(BACKGROUND_REFRESH_DELAY_MS);
    expect(refresh).toHaveBeenCalledTimes(1);
  });

  it('defers hidden-window work until visibility returns', async () => {
    vi.useFakeTimers();
    const refresh = vi.fn(() => Promise.resolve());
    const scheduler = createFeatureRefreshScheduler(refresh, {
      active: false,
      visible: false,
    });
    scheduler.invalidate();
    await vi.advanceTimersByTimeAsync(BACKGROUND_REFRESH_DELAY_MS);
    expect(refresh).not.toHaveBeenCalled();
    scheduler.setVisible(true);
    await vi.advanceTimersByTimeAsync(BACKGROUND_REFRESH_DELAY_MS);
    expect(refresh).toHaveBeenCalledTimes(1);
  });

  it('runs one trailing refresh when invalidated during an active request', async () => {
    let finishFirst!: () => void;
    const refresh = vi
      .fn<() => Promise<void>>()
      .mockImplementationOnce(() => new Promise<void>((resolve) => (finishFirst = resolve)))
      .mockResolvedValue(undefined);
    const scheduler = createFeatureRefreshScheduler(refresh, {
      active: true,
      visible: true,
    });
    scheduler.invalidate();
    scheduler.invalidate();
    expect(refresh).toHaveBeenCalledTimes(1);
    finishFirst();
    await vi.waitFor(() => expect(refresh).toHaveBeenCalledTimes(2));
  });

  it('cancels pending refreshes when disposed', async () => {
    vi.useFakeTimers();
    const refresh = vi.fn(() => Promise.resolve());
    const scheduler = createFeatureRefreshScheduler(refresh, {
      active: false,
      visible: true,
    });
    scheduler.invalidate();
    scheduler.dispose();
    await vi.advanceTimersByTimeAsync(BACKGROUND_REFRESH_DELAY_MS);
    expect(refresh).not.toHaveBeenCalled();
  });
});
