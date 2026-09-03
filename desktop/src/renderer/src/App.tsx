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

import { disabledMainWindowUiState } from '../../shared/ipc';
import type { AppRouteEvent, AttentionItem, RoutedRequest, UpdateState } from '../../shared/ipc';
import { ConnectionShell } from './components/ConnectionShell';
import { AmaPanel } from './components/AmaPanel';
import { CommandPalette } from './components/CommandPalette';
import { HelpOverlay } from './components/HelpOverlay';
import { ReadinessGate } from './components/ReadinessGate';
import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type Dispatch,
  type SetStateAction,
} from 'react';
import { emptyAttentionDrafts, type AttentionDrafts } from './features/AttentionInbox';
import { useConnectionState, useSystemAccentMirror, useTheme } from './hooks';
import { ExplainChatProvider } from './explainChat';

export default function App() {
  // Called purely for its side effect (mirroring the resolved theme onto
  // <html data-theme>, following OS changes); the switcher itself now lives
  // in the Settings window's Appearance pane, whose own useTheme() instance
  // reaches this one through the main process's theme broadcast.
  useTheme();
  useSystemAccentMirror();
  const connection = useConnectionState();
  const runtimeReady = connection.status === 'ready';
  const serverKey = connection.serverKey ?? null;
  const [serverAttentionItems, setServerAttentionItems] = useState<AttentionItem[]>([]);
  /**
   * Attention/Ama drafts are scoped to the connected server's identity: this
   * component deliberately does NOT unmount during a connection flip, so the
   * map keeps each server's in-progress drafts and nothing bleeds across.
   * Entries live for the app session only; nothing is persisted.
   */
  const [draftsByServer, setDraftsByServer] = useState<ReadonlyMap<string, AttentionDrafts>>(
    () => new Map(),
  );
  const attentionDrafts: AttentionDrafts =
    (serverKey !== null ? draftsByServer.get(serverKey) : undefined) ?? emptyAttentionDrafts();
  const setAttentionDrafts = useCallback<Dispatch<SetStateAction<AttentionDrafts>>>(
    (next) => {
      if (serverKey === null) {
        return;
      }
      setDraftsByServer((current) => {
        const previous = current.get(serverKey) ?? emptyAttentionDrafts();
        const resolved = typeof next === 'function' ? next(previous) : next;
        if (resolved === previous) {
          return current;
        }
        const updated = new Map(current);
        updated.set(serverKey, resolved);
        return updated;
      });
    },
    [serverKey],
  );
  const [attentionJump, setAttentionJump] = useState<{
    requestId: number;
    featureId: string;
    attentionId?: string;
  } | null>(null);
  const [routeRequest, setRouteRequest] = useState<RoutedRequest | null>(null);
  const [updateState, setUpdateState] = useState<UpdateState | null>(null);
  // A reply that landed while the panel was closed, echoed on the Ask chip.
  const [amaUnread, setAmaUnread] = useState(false);
  const [updateDismissedVersion, setUpdateDismissedVersion] = useState<string | null>(null);
  const [schedulingUpdate, setSchedulingUpdate] = useState(false);
  const routeSequence = useRef(0);

  /**
   * Settings lives in its own window, so a settings route is an ask to the
   * main process rather than shell state; every other target stays a local
   * route request the mounted shell handles.
   */
  const requestRoute = useCallback((event: AppRouteEvent) => {
    if (event.target === 'settings') {
      void window.agentico
        .openSettingsWindow(
          event.settingsSection === undefined ? {} : { section: event.settingsSection },
        )
        .catch(() => {
          // The window is a destination, not a mutation: a failed open never
          // needs to disturb the surface the user is looking at.
        });
      return;
    }
    routeSequence.current += 1;
    setRouteRequest({ id: routeSequence.current, event });
  }, []);

  useEffect(() => window.agentico.onRouteRequest(requestRoute), [requestRoute]);

  // While the runtime is down the shell — the native menu bar's only source of
  // selection and enablement — is not mounted at all, so readiness is pushed
  // from here: the menu goes dark rather than holding whatever it last knew.
  useEffect(() => {
    if (runtimeReady) return;
    void window.agentico.publishUiState(disabledMainWindowUiState()).catch(() => {
      // A menu that stays stale for a moment is never worth surfacing.
    });
  }, [runtimeReady]);

  const refreshUpdates = useCallback(async () => {
    try {
      setUpdateState(await window.agentico.getUpdates());
    } catch {
      setUpdateState(null);
    }
  }, []);

  useEffect(() => {
    void refreshUpdates();
    const interval = setInterval(() => void refreshUpdates(), 60000);
    return () => clearInterval(interval);
  }, [refreshUpdates]);

  useEffect(
    () =>
      window.agentico.onAppEvent((event) => {
        if (event.type === 'invalidated' && event.kind === 'updates.changed') {
          void refreshUpdates();
        }
      }),
    [refreshUpdates],
  );

  const refreshAttention = useCallback(async () => {
    if (!runtimeReady) {
      setServerAttentionItems([]);
      return [];
    }
    try {
      const snapshot = await window.agentico.getAttention();
      setServerAttentionItems(snapshot.items);
      return snapshot.items;
    } catch {
      setServerAttentionItems([]);
      return [];
    }
  }, [runtimeReady]);

  const refreshRecovery = useCallback(async () => {
    if (!runtimeReady) return;
    try {
      // The watchdog owns routine recovery. Its scan stays deliberately quiet;
      // only an exhausted lifecycle failure is promoted by the server.
      await window.agentico.scanRecovery();
    } catch {
      // A best-effort watchdog scan must not become user-visible attention.
    }
  }, [runtimeReady]);

  useEffect(() => {
    if (!runtimeReady) {
      setServerAttentionItems([]);
      return;
    }
    void refreshAttention();
    void refreshRecovery();
    return window.agentico.onAppEvent((event) => {
      if (event.type === 'status') {
        void refreshAttention();
        return;
      }
      if (event.type !== 'invalidated') return;
      if (event.kind === 'resync') {
        void refreshAttention();
        void refreshRecovery();
        return;
      }
      if (
        event.kind === 'permission.updated' ||
        event.kind === 'prompt.updated' ||
        event.kind.startsWith('feature') ||
        event.kind.startsWith('lifecycle')
      ) {
        void refreshAttention();
      }
    });
  }, [refreshAttention, refreshRecovery, runtimeReady]);

  const attentionItems: AttentionItem[] = serverAttentionItems;

  return (
    // The explain-in-chat provider rides at the renderer root so every
    // ErrorSurface — in the shell tree or the AMA panel — can route a
    // question without prop drilling the root requester through panels.
    <ExplainChatProvider requestRoute={requestRoute}>
      <div className="app-frame">
        {runtimeReady ? (
          <>
            <ReadinessGate
              attentionItems={attentionItems}
              refreshAttention={refreshAttention}
              attentionDrafts={attentionDrafts}
              setAttentionDrafts={setAttentionDrafts}
              attentionJump={attentionJump}
              onAttentionJumpHandled={() => setAttentionJump(null)}
              routeRequest={routeRequest}
              onAttentionJump={(featureId, attentionId) => {
                routeSequence.current += 1;
                setAttentionJump({
                  requestId: routeSequence.current,
                  featureId,
                  ...(attentionId === undefined ? {} : { attentionId }),
                });
              }}
              updateState={updateState}
              updateDismissedVersion={updateDismissedVersion}
              schedulingUpdate={schedulingUpdate}
              onDismissUpdate={(version) => setUpdateDismissedVersion(version)}
              onOpenUpdatesSettings={() =>
                requestRoute({ target: 'settings', settingsSection: 'updates' })
              }
              onOpenAma={() => requestRoute({ target: 'ama' })}
              onOpenPalette={() => requestRoute({ target: 'palette' })}
              amaUnread={amaUnread}
              onInstallUpdateWhenIdle={async () => {
                try {
                  setSchedulingUpdate(true);
                  setUpdateState(await window.agentico.installUpdateWhenIdle());
                } finally {
                  setSchedulingUpdate(false);
                }
              }}
            />
            <AmaPanel
              attentionItems={attentionItems}
              refreshAttention={refreshAttention}
              attentionDrafts={attentionDrafts}
              setAttentionDrafts={setAttentionDrafts}
              routeRequest={routeRequest}
              onUnreadChange={setAmaUnread}
            />
          </>
        ) : (
          <ConnectionShell />
        )}
        <CommandPalette ready={runtimeReady} routeRequest={routeRequest} onRoute={requestRoute} />
        <HelpOverlay routeRequest={routeRequest} />
      </div>
    </ExplainChatProvider>
  );
}
