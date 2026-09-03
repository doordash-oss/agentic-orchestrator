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

import { useCallback, useRef } from 'react';
import type { AttentionActionResult } from '../../../shared/ipc';
import type { AttentionAction, AttentionSubmitOptions } from './AttentionInbox';

/**
 * useAttentionDraftSaves serializes per-id attention draft submissions.
 *
 * For each id, a new save waits for the previous one to settle (swallowing
 * its rejection) before running, so concurrent drafts on the same attention
 * item never clobber each other. The in-flight promise is tracked on a ref
 * and cleaned up in a finally callback, so an unmount or refresh never races
 * a stale notification onto the wrong id.
 *
 * Callers supply the notice setters and the already-resolved refresh handler
 * so the hook stays free of surface-specific UI state (the cockpit announces
 * via a live region; the inbox/dock use a local notice).
 */
export function useAttentionDraftSaves({
  notify,
  notifyError,
  onAlreadyResolved,
}: {
  notify(result: AttentionActionResult, options: AttentionSubmitOptions): void;
  notifyError(error: unknown): void;
  onAlreadyResolved(): Promise<void> | void;
}): (id: string, action: AttentionAction, options?: AttentionSubmitOptions) => Promise<void> {
  const saves = useRef(new Map<string, Promise<void>>());
  const notifyRef = useRef(notify);
  notifyRef.current = notify;
  const notifyErrorRef = useRef(notifyError);
  notifyErrorRef.current = notifyError;
  const onAlreadyResolvedRef = useRef(onAlreadyResolved);
  onAlreadyResolvedRef.current = onAlreadyResolved;

  return useCallback(
    (
      id: string,
      action: AttentionAction,
      options: AttentionSubmitOptions = { successNotice: 'Draft saved.' },
    ): Promise<void> => {
      const previous = saves.current.get(id) ?? Promise.resolve();
      const run = previous
        .catch(() => undefined)
        .then(async () => {
          try {
            const result = await action();
            notifyRef.current(result, options);
            if (result.alreadyResolved === true) {
              await onAlreadyResolvedRef.current();
            }
          } catch (error) {
            notifyErrorRef.current(error);
            throw error;
          }
        });
      const tracked = run.finally(() => {
        if (saves.current.get(id) === tracked) {
          saves.current.delete(id);
        }
      });
      saves.current.set(id, tracked);
      return tracked;
    },
    [],
  );
}
