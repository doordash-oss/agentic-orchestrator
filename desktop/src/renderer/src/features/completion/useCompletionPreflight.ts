import { useState, useCallback, useEffect, useRef } from 'react';
import { parseIpcError } from '../../wizard/ipcError';
import type { CompletionPreflightResult } from '../../../shared/ipc';

export interface CompletionPreflightHandle {
  preflight: CompletionPreflightResult | null;
  loading: boolean;
  error: string | null;
  refresh: () => Promise<void>;
}

export function useCompletionPreflight(
  featureId: string,
  enabled: boolean,
  preflightCompletion: (featureId: string) => Promise<CompletionPreflightResult>,
): CompletionPreflightHandle {
  const [preflight, setPreflight] = useState<CompletionPreflightResult | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
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
      setError(parseIpcError(err).message);
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
