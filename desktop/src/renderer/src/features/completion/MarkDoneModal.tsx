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

import { useCallback } from 'react';
import type { CompletionPreflightResult } from '../../../../shared/ipc';
import { ResultBox, useCompletionAction, type CompletionAction } from './completionShared';

export interface MarkDoneModalProps {
  featureId: string;
  preflight: CompletionPreflightResult;
  dispatchAction: (
    featureId: string,
    action: CompletionAction,
    body?: Record<string, unknown>,
  ) => Promise<{ result: string; [k: string]: unknown }>;
  onDispatched: () => void;
}

export function MarkDoneModalBody({
  featureId,
  preflight,
  dispatchAction,
  onDispatched,
}: MarkDoneModalProps): React.ReactElement {
  const doneAction = useCompletionAction();

  const handleMarkDone = useCallback(async () => {
    await doneAction.run(
      () =>
        dispatchAction(featureId, 'mark-done', {
          source_revision: preflight.sourceRevision,
        }).then((r) => r.result),
      async () => onDispatched(),
    );
  }, [featureId, preflight, dispatchAction, onDispatched, doneAction]);

  return (
    <div className="completion-workspace__done">
      <p className="completion-workspace__done-hint">
        Marking Done is a separate server-authorized action. It transitions the feature only after
        the server confirms success.
      </p>
      {preflight.canMarkDone ? (
        <button
          type="button"
          className="completion-workspace__action"
          disabled={doneAction.busy}
          onClick={() => void handleMarkDone()}
        >
          {doneAction.busy ? 'Marking Done…' : 'Mark Done'}
        </button>
      ) : (
        <div className="completion-workspace__blocked">
          {preflight.markDoneBlocker ?? 'Mark Done is not available'}
        </div>
      )}
      <ResultBox result={doneAction.result} />
    </div>
  );
}
