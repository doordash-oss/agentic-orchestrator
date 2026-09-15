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
 * Settings repository-creation area: the shared create form against the
 * connected server's workspace roots. Success announces the created
 * repository and refreshes the authoritative readiness/catalog — it never
 * opens a feature or adopts into any creation draft.
 */
import { useState } from 'react';
import type { ConnectionState, ReadinessSnapshot } from '../../../shared/ipc';
import { serverDescriptor } from './cloneViews';
import { CreateRepositoryForm, type CreateRepositoryStartInput } from './createViews';

export function CreateRepositorySection({
  readiness,
  connection,
  onReadinessChanged,
}: {
  readiness: ReadinessSnapshot | null;
  connection: ConnectionState;
  /** Applies an authoritative readiness snapshot (e.g. after a root addition). */
  onReadinessChanged?(snapshot: ReadinessSnapshot): void;
}) {
  const workspaceRoots = readiness?.workspaceRoots ?? [];
  const [announcement, setAnnouncement] = useState<string | null>(null);

  const handleCreate = (input: CreateRepositoryStartInput) =>
    window.agentico.createRepository(input).then((result) => {
      setAnnouncement(
        `Created ${result.repoKey} at ${result.path} on ${serverDescriptor(connection)}. It has one empty initial commit and no remote.`,
      );
      // The authoritative catalog re-read reflects the new repository on
      // every surface; a failed refresh never erases the announcement.
      void window.agentico
        .getReadiness()
        .then((snapshot) => onReadinessChanged?.(snapshot))
        .catch(() => undefined);
      return result;
    });

  return (
    <section className="settings-panel__section" aria-label="Create repositories">
      <h2 className="settings-panel__section-title">Create a repository</h2>
      <p className="settings-panel__section-desc">
        Create a new repository as a child of a workspace root on {serverDescriptor(connection)}.
        The new repository is feature-ready immediately.
      </p>

      <CreateRepositoryForm
        workspaceRoots={workspaceRoots}
        connection={connection}
        idPrefix="create"
        onCreate={handleCreate}
        onReadinessChanged={onReadinessChanged}
      />

      {announcement !== null ? (
        <p className="settings-panel__clone-announcement" role="status" aria-live="polite">
          {announcement}
        </p>
      ) : null}
    </section>
  );
}
