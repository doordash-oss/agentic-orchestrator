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

/**
 * The creation sheet's nested create view. It embeds the same shared
 * create form as Settings, against the same destination-root control
 * (native chooser and root persistence on a local server). Creation is
 * synchronous: the owning sheet records the pending adoption before the
 * request flies and adopts the published repository into its own draft by
 * identity when the authoritative catalog shows it.
 *
 * Close only detaches the view: an in-flight creation completes on the
 * server and still adopts; the draft is never discarded. Escape is
 * handled here, never by the enclosing sheet.
 */
import { useRef } from 'react';
import type {
  ConnectionState,
  CreateRepositoryResult,
  ReadinessSnapshot,
  WorkspaceRootState,
} from '../../../shared/ipc';
import { useModalDismiss } from '../components/useModalDismiss';
import { serverDescriptor } from './cloneViews';
import { CreateRepositoryForm, type CreateRepositoryStartInput } from './createViews';

export interface PickerCreateDialogProps {
  connection: ConnectionState;
  workspaceRoots: readonly WorkspaceRootState[];
  /** Performs the server-owned creation; rejects with the canonical error. */
  onCreate(input: CreateRepositoryStartInput): Promise<CreateRepositoryResult>;
  /** Applies an authoritative readiness snapshot (e.g. after a root addition). */
  onReadinessChanged?(snapshot: ReadinessSnapshot): void;
  /** Close detaches the view; the draft and any pending adoption stay. */
  onClose(): void;
}

export function PickerCreateDialog({
  connection,
  workspaceRoots,
  onCreate,
  onReadinessChanged,
  onClose,
}: PickerCreateDialogProps) {
  const dialogRef = useRef<HTMLDivElement | null>(null);
  // The nested dialog owns its full focus lifecycle: initial focus, Tab
  // containment, Escape, and restoration to the invoking control on close.
  // useModalDismiss also suspends nothing here — it is the deepest modal,
  // so its trap is always active while this view exists.
  useModalDismiss(dialogRef, onClose);

  return (
    <div className="impact-dialog__backdrop creation-clone__backdrop">
      <div
        ref={dialogRef}
        className="impact-dialog creation-clone"
        role="dialog"
        aria-modal="true"
        aria-label="Create a repository"
        tabIndex={-1}
      >
        <h2>Create a repository</h2>
        <p className="creation-clone__desc">
          Create a new repository as a child of a workspace root on {serverDescriptor(connection)}.
          The finished repository is selected here once the server reports it.
        </p>

        <CreateRepositoryForm
          workspaceRoots={workspaceRoots}
          connection={connection}
          idPrefix="picker-create"
          onCreate={onCreate}
          onReadinessChanged={onReadinessChanged}
        />

        <p className="creation-clone__help">
          Closing this view keeps any in-flight creation running on {serverDescriptor(connection)};
          the repository is selected here when it finishes.
        </p>

        <div className="impact-dialog__actions">
          <button type="button" onClick={onClose}>
            Close
          </button>
        </div>
      </div>
    </div>
  );
}
