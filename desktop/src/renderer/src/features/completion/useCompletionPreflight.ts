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

import { useState, useCallback, useEffect, useRef } from 'react';
import { parseIpcError } from '../../wizard/ipcError';
import type { CanonicalError, CompletionPreflightResult } from '../../../../shared/ipc';

export interface CompletionPreflightHandle {
  preflight: CompletionPreflightResult | null;
  loading: boolean;
  /** The parsed canonical error, rendered by the changes surface as a compact ErrorSurface. */
  error: CanonicalError | null;
  refresh: () => Promise<void>;
}

export function useCompletionPreflight(
  featureId: string,
  enabled: boolean,
  preflightCompletion: (featureId: string) => Promise<CompletionPreflightResult>,
): CompletionPreflightHandle {
  const [preflight, setPreflight] = useState<CompletionPreflightResult | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<CanonicalError | null>(null);
  const requestRef = useRef(0);

  const refresh = useCallback(async () => {
    const request = ++requestRef.current;
    setLoading(true);
    setError(null);
    try {
      const result = await preflightCompletion(featureId);
      if (request !== requestRef.current) return;
      setPreflight(result);
    } catch (err) {
      if (request !== requestRef.current) return;
      setError(parseIpcError(err));
    } finally {
      if (request === requestRef.current) setLoading(false);
    }
  }, [featureId, preflightCompletion]);

  useEffect(() => {
    if (!enabled) return;
    void refresh();
    return () => {
      requestRef.current += 1;
    };
  }, [enabled, refresh]);

  return { preflight, loading, error, refresh };
}
