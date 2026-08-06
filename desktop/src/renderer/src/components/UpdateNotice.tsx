import type { UpdateState } from '../../../shared/ipc';
import { canInstallInApp, hasActiveWork, installWhenIdleLabel } from '../../../shared/updateState';

/**
 * The passive update banner, re-homed into the content-pane flow — above the
 * scrollable body, below the toolbar — instead of the deleted global header.
 * Behavior is unchanged; its popover conversion is a later phase.
 */
export function UpdateNotice({
  update,
  dismissedVersion,
  scheduling,
  onDismiss,
  onOpenSettings,
  onInstallWhenIdle,
}: {
  update: UpdateState | null;
  dismissedVersion: string | null;
  scheduling: boolean;
  onDismiss(version: string): void;
  onOpenSettings(): void;
  onInstallWhenIdle(): Promise<void>;
}) {
  if (
    update === null ||
    update.targetVersion === undefined ||
    dismissedVersion === update.targetVersion ||
    !['ready', 'scheduled', 'available'].includes(update.status)
  ) {
    return null;
  }
  const updateHasActiveWork = hasActiveWork(update);
  const isScheduled = update.status === 'scheduled';
  return (
    <section className="update-notice" aria-label="Update available">
      <div>
        <strong>Agentico {update.targetVersion} is available</strong>
        <span>{update.message}</span>
        {update.activeWorkSummary && <span>{update.activeWorkSummary}</span>}
      </div>
      <div className="update-notice__actions">
        <button type="button" className="setup-wizard__action" onClick={onOpenSettings}>
          Updates
        </button>
        {canInstallInApp(update) && updateHasActiveWork && (
          <button
            type="button"
            className="setup-wizard__action setup-wizard__action--primary"
            onClick={() => void onInstallWhenIdle()}
            disabled={scheduling || isScheduled}
          >
            {installWhenIdleLabel({ scheduling, scheduled: isScheduled })}
          </button>
        )}
        <button
          type="button"
          className="settings-panel__root-btn"
          aria-label="Dismiss update notice"
          onClick={() => onDismiss(update.targetVersion!)}
        >
          Dismiss
        </button>
      </div>
    </section>
  );
}
