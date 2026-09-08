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
 * Settings repositories area: the connected server's current catalog with
 * per-repository readiness. An unborn row (a successful clone of an empty
 * remote) keeps the later entry point for the explicit initial commit —
 * the same shared operation and consent wording as the clone-success
 * offer. Success re-reads authoritative readiness and announces; Settings
 * never opens a feature or selects into any creation draft.
 */
import { useEffect, useRef, useState } from 'react';
import { parseIpcError } from '../wizard/ipcError';
import type { ConnectionState, ReadinessSnapshot, RepositoryState } from '../../../shared/ipc';
import { InitializeOffer, serverDescriptor } from './cloneViews';

export function WorkspaceRepositoriesSection({
  readiness,
  connection,
  onReadinessChanged,
}: {
  readiness: ReadinessSnapshot | null;
  connection: ConnectionState;
  /** Applies an authoritative readiness snapshot (e.g. after initialization). */
  onReadinessChanged?(snapshot: ReadinessSnapshot): void;
}) {
  const repositories = readiness?.repositories ?? [];
  const [announcement, setAnnouncement] = useState<string | null>(null);
  // One initialize attempt at a time: pending is scoped to the repository
  // action, and duplicate actions for the same repository are suppressed.
  const [pendingKey, setPendingKey] = useState<string | null>(null);
  const [initializeError, setInitializeError] = useState<{
    repoKey: string;
    error: ReturnType<typeof parseIpcError>;
  } | null>(null);
  // The row whose inline offer is expanded (later opt-in).
  const [expandedKey, setExpandedKey] = useState<string | null>(null);
  const serverKey = connection.status === 'ready' ? connection.serverKey : null;
  const previousServerKey = useRef<string | null | undefined>(undefined);

  useEffect(() => {
    if (previousServerKey.current === undefined) {
      previousServerKey.current = serverKey;
      return;
    }
    if (previousServerKey.current === serverKey) return;
    previousServerKey.current = serverKey;
    setPendingKey(null);
    setExpandedKey(null);
    setInitializeError(null);
    setAnnouncement(null);
  }, [serverKey]);

  const handleInitialize = (repo: RepositoryState): void => {
    if (pendingKey !== null || repo.identity === undefined) return;
    setPendingKey(repo.name);
    setInitializeError(null);
    void window.agentico
      .initializeRepository({
        repoKey: repo.name,
        identity: repo.identity,
        path: repo.path,
        consent: true,
      })
      .then((result) => {
        setPendingKey(null);
        setExpandedKey(null);
        setAnnouncement(
          result.result === 'initialized'
            ? `Initialized ${result.repoKey} at ${result.path} on ${serverDescriptor(connection)}. It has one empty initial commit; nothing was pushed.`
            : `${result.repoKey} already had an initial commit on ${serverDescriptor(connection)}; it is ready for feature work.`,
        );
        void window.agentico
          .getReadiness()
          .then(onReadinessChanged)
          .catch(() => undefined);
      })
      .catch((e: unknown) => {
        setPendingKey(null);
        const error = parseIpcError(e);
        if (error.code !== 'E_SERVER_SWITCHED') {
          setInitializeError({ repoKey: repo.name, error });
          // A rejected transport may hide a successful server-side commit.
          // Re-read authoritative state without repeating the mutation.
          void window.agentico
            .getReadiness()
            .then(onReadinessChanged)
            .catch(() => undefined);
        }
      });
  };

  return (
    <section className="settings-panel__section" aria-label="Repositories">
      <h2 className="settings-panel__section-title">Repositories</h2>
      <p className="settings-panel__section-desc">
        The current catalog on {serverDescriptor(connection)}. An empty-remote clone stays here
        until it has a commit; its row keeps the Create initial commit action.
      </p>
      {repositories.length === 0 ? (
        <p className="settings-panel__clone-empty">No repositories in the catalog yet.</p>
      ) : (
        <ul className="settings-panel__repositories">
          {repositories.map((repo) => (
            <li
              key={repo.name}
              className="settings-panel__repository"
              data-repo-key={repo.name}
              data-feature-ready={repo.valid && repo.featureReady}
            >
              <div className="settings-panel__repository-row">
                <span className="settings-panel__repository-body">
                  <b className="settings-panel__repository-name">{repo.name}</b>
                  <code className="settings-panel__repository-path">{repo.path}</code>
                  {!repo.valid ? (
                    <span className="settings-panel__repository-issue">
                      {repo.issue?.summary ?? 'Unavailable'}
                    </span>
                  ) : repo.featureReady ? (
                    <span className="settings-panel__repository-issue">
                      Ready for feature work.
                    </span>
                  ) : (
                    <span className="settings-panel__repository-issue">
                      No commits yet — an initial commit is required before feature work.
                    </span>
                  )}
                </span>
                {repo.valid && !repo.featureReady && repo.identity !== undefined ? (
                  <button
                    type="button"
                    className="settings-panel__clone-action"
                    aria-expanded={expandedKey === repo.name}
                    aria-controls={`repository-${encodeURIComponent(repo.name)}-initialize`}
                    disabled={pendingKey !== null}
                    onClick={() => {
                      setExpandedKey((current) => (current === repo.name ? null : repo.name));
                      if (initializeError?.repoKey !== repo.name) setInitializeError(null);
                    }}
                  >
                    Create initial commit…
                  </button>
                ) : null}
              </div>
              {repo.valid && !repo.featureReady && repo.identity !== undefined ? (
                <div id={`repository-${encodeURIComponent(repo.name)}-initialize`}>
                  {expandedKey === repo.name ? (
                    <InitializeOffer
                      idPrefix={`repository-${encodeURIComponent(repo.name)}`}
                      controller={{
                        pending: pendingKey === repo.name,
                        error:
                          initializeError?.repoKey === repo.name ? initializeError.error : null,
                        onInitialize: () => handleInitialize(repo),
                        onDecline: () => setExpandedKey(null),
                      }}
                    />
                  ) : null}
                </div>
              ) : null}
            </li>
          ))}
        </ul>
      )}
      {announcement !== null ? (
        <p className="settings-panel__clone-announcement" role="status" aria-live="polite">
          {announcement}
        </p>
      ) : null}
    </section>
  );
}
