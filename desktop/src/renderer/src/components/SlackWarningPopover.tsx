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

import { useRef } from 'react';
import type { SlackSettingsSnapshot } from '../../../shared/ipc';
import { SlackIcon } from './icons';
import { ToolbarPopover, ToolbarPopoverAnchor } from './ToolbarPopover';

export function slackCredentialWarningPending(
  snapshot: SlackSettingsSnapshot | null,
  dismissed: boolean,
): snapshot is Extract<SlackSettingsSnapshot, { supported: true }> {
  return snapshot?.supported === true && snapshot.status.state === 'credential_error' && !dismissed;
}

export function SlackWarningPopover({
  snapshot,
  dismissed,
  open,
  onOpenChange,
  onDismiss,
  onOpenSettings,
}: {
  snapshot: SlackSettingsSnapshot | null;
  dismissed: boolean;
  open: boolean;
  onOpenChange(open: boolean): void;
  onDismiss(): void;
  onOpenSettings(): void;
}) {
  const trigger = useRef<HTMLButtonElement>(null);

  if (!slackCredentialWarningPending(snapshot, dismissed)) return null;
  const error = snapshot.status.lastError;
  const title = error?.title ?? 'Slack credentials need attention';
  const summary =
    error?.summary ?? 'Slack rejected the saved credentials. Open Slack settings to reconnect.';

  return (
    <ToolbarPopoverAnchor>
      <button
        ref={trigger}
        type="button"
        className="slack-warning-trigger"
        aria-label="Show Slack credential warning"
        aria-expanded={open}
        aria-controls="slack-warning-popover"
        onClick={() => {
          onOpenChange(!open);
          if (open) trigger.current?.focus();
        }}
      >
        <SlackIcon />
        <span className="slack-warning-trigger__dot" aria-hidden="true" />
      </button>
      <ToolbarPopover
        open={open}
        id="slack-warning-popover"
        className="slack-warning-popover"
        label="Slack needs attention"
        anchorRef={trigger}
        onDismiss={() => onOpenChange(false)}
      >
        <h2 className="slack-warning-popover__headline">{title}</h2>
        <p className="slack-warning-popover__summary">{summary}</p>
        <div className="slack-warning-popover__actions">
          <button
            type="button"
            className="slack-warning-popover__action slack-warning-popover__action--primary"
            onClick={() => {
              onOpenChange(false);
              onOpenSettings();
            }}
          >
            Open Slack settings
          </button>
          <button
            type="button"
            className="slack-warning-popover__action"
            onClick={() => {
              onOpenChange(false);
              onDismiss();
            }}
          >
            Dismiss
          </button>
        </div>
      </ToolbarPopover>
    </ToolbarPopoverAnchor>
  );
}
