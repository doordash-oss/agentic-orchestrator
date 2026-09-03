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

export function LauncherFooter({
  onCancel,
  primaryLabel,
  primaryDisabled = false,
  busy = false,
  onPrimary,
}: {
  onCancel(): void;
  primaryLabel: string;
  primaryDisabled?: boolean;
  busy?: boolean;
  onPrimary(): void;
}): React.ReactElement {
  return (
    <footer className="launcher-modal__footer">
      <button type="button" className="launcher-modal__cancel" onClick={onCancel} disabled={busy}>
        Cancel
      </button>
      <button
        type="button"
        className="launcher-modal__primary"
        disabled={primaryDisabled || busy}
        onClick={onPrimary}
      >
        {primaryLabel}
      </button>
    </footer>
  );
}
