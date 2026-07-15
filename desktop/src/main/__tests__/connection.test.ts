import { describe, expect, it, vi } from 'vitest';
import { StubConnectionSource } from '../connection';

describe('StubConnectionSource', () => {
  it('starts idle at the resolve-runtime stage', () => {
    const source = new StubConnectionSource();
    expect(source.getState()).toEqual({
      status: 'idle',
      stage: 'resolve-runtime',
      detail: expect.any(String),
    });
  });

  it('moves to awaiting-gateway on start and notifies subscribers', () => {
    const source = new StubConnectionSource();
    const listener = vi.fn();
    source.subscribe(listener);
    source.start();
    expect(source.getState().status).toBe('awaiting-gateway');
    expect(listener).toHaveBeenCalledWith(
      expect.objectContaining({ status: 'awaiting-gateway', stage: 'resolve-runtime' }),
    );
  });

  it('stops notifying after unsubscribe', () => {
    const source = new StubConnectionSource();
    const listener = vi.fn();
    const unsubscribe = source.subscribe(listener);
    unsubscribe();
    source.start();
    expect(listener).not.toHaveBeenCalled();
  });
});
