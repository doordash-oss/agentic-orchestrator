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
