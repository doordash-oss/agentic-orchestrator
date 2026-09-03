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

import { describe, it, expect, vi } from 'vitest';
import { renderHook, waitFor, act } from '@testing-library/react';
import { useCompletionPreflight } from './useCompletionPreflight';
import type { CompletionPreflightResult } from '../../../../shared/ipc';

const pf: CompletionPreflightResult = {
  featureId: 'f1',
  sourceRevision: 'r1',
  canMarkDone: true,
  repos: [{ repo: 'a', publishable: true, touched: true, status: 'eligible' }],
};

describe('useCompletionPreflight', () => {
  it('does not fetch when disabled', () => {
    const fetchPf = vi.fn(() => Promise.resolve(pf));
    renderHook(() => useCompletionPreflight('f1', false, fetchPf));
    expect(fetchPf).not.toHaveBeenCalled();
  });

  it('fetches on mount when enabled and exposes the result', async () => {
    const fetchPf = vi.fn(() => Promise.resolve(pf));
    const { result } = renderHook(() => useCompletionPreflight('f1', true, fetchPf));
    await waitFor(() => expect(result.current.preflight).toEqual(pf));
    expect(result.current.loading).toBe(false);
    expect(result.current.error).toBeNull();
  });

  it('captures a parsed error message', async () => {
    const fetchPf = vi.fn(() => Promise.reject(new Error('boom')));
    const { result } = renderHook(() => useCompletionPreflight('f1', true, fetchPf));
    await waitFor(() => expect(result.current.error).toBe('boom'));
  });

  it('refresh re-fetches', async () => {
    const fetchPf = vi.fn(() => Promise.resolve(pf));
    const { result } = renderHook(() => useCompletionPreflight('f1', true, fetchPf));
    await waitFor(() => expect(result.current.preflight).toEqual(pf));
    await act(async () => {
      await result.current.refresh();
    });
    expect(fetchPf).toHaveBeenCalledTimes(2);
  });
});
