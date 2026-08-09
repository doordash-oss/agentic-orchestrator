import { useRef } from 'react';
import type { UpdateState } from '../../../shared/ipc';
import { canInstallInApp, hasActiveWork, installWhenIdleLabel } from '../../../shared/updateState';
import { UpdateIcon } from './icons';
import { ToolbarPopover, ToolbarPopoverAnchor } from './ToolbarPopover';

/**
 * Whether an update is worth telling the user about right now: a target version
 * in a ready/scheduled/available status that this session has not dismissed.
 * The toolbar trigger, the sidebar footer dot, and the popover all read this one
 * predicate, so a notice never appears in one place and not the other. It
 * narrows so callers that go on to render the notice get `targetVersion` as a
 * string without re-checking it.
 */
export function updateNoticePending(
  update: UpdateState | null,
  dismissedVersion: string | null,
): update is UpdateState & { targetVersion: string } {
  return (
    update !== null &&
    update.targetVersion !== undefined &&
    dismissedVersion !== update.targetVersion &&
    ['ready', 'scheduled', 'available'].includes(update.status)
  );
}

/**
 * The update notice as a transient toolbar popover: an icon button with a dot
 * appears beside the attention bell while an update is pending, and clicking it
 * opens the anchored popover carrying the version headline, the message, any
 * active-work summary, and the three actions the in-flow banner used to hold.
 * Nothing here occupies flow space, so the content pane never shifts when an
 * update arrives; the popover never opens on its own.
 */
export function UpdatePopover({
  update,
  dismissedVersion,
  scheduling,
  open,
  onOpenChange,
  onDismiss,
  onOpenSettings,
  onInstallWhenIdle,
}: {
  update: UpdateState | null;
  dismissedVersion: string | null;
  scheduling: boolean;
  open: boolean;
  onOpenChange(open: boolean): void;
  onDismiss(version: string): void;
  onOpenSettings(): void;
  onInstallWhenIdle(): Promise<void>;
}) {
  const trigger = useRef<HTMLButtonElement>(null);

  if (!updateNoticePending(update, dismissedVersion)) return null;
  const targetVersion = update.targetVersion;
  const updateHasActiveWork = hasActiveWork(update);
  const isScheduled = update.status === 'scheduled';

  return (
    <ToolbarPopoverAnchor>
      <button
        ref={trigger}
        type="button"
        className="update-trigger"
        aria-label="Show available update"
        aria-expanded={open}
        aria-controls="update-popover"
        onClick={() => {
          onOpenChange(!open);
          if (open) trigger.current?.focus();
        }}
      >
        <UpdateIcon />
        <span className="update-trigger__dot" aria-hidden="true" />
      </button>
      <ToolbarPopover
        open={open}
        id="update-popover"
        className="update-popover"
        label="Available update"
        anchorRef={trigger}
        onDismiss={() => onOpenChange(false)}
      >
        <h2 className="update-popover__headline">Agentico {targetVersion} is available</h2>
        <p className="update-popover__message">{update.message}</p>
        {updateHasActiveWork ? (
          <p className="update-popover__active-work">{update.activeWorkSummary}</p>
        ) : null}
        <div className="update-popover__actions">
          <button type="button" className="update-popover__action" onClick={onOpenSettings}>
            Updates
          </button>
          {canInstallInApp(update) && updateHasActiveWork ? (
            <button
              type="button"
              className="update-popover__action update-popover__action--primary"
              onClick={() => void onInstallWhenIdle()}
              disabled={scheduling || isScheduled}
            >
              {installWhenIdleLabel({ scheduling, scheduled: isScheduled })}
            </button>
          ) : null}
          <button
            type="button"
            className="update-popover__action"
            onClick={() => onDismiss(targetVersion)}
          >
            Dismiss
          </button>
        </div>
      </ToolbarPopover>
    </ToolbarPopoverAnchor>
  );
}
