import type { AppRouteEvent, AttentionItem, RoutedRequest, UpdateState } from '../../shared/ipc';
import { ConnectionShell } from './components/ConnectionShell';
import { AmaPanel } from './components/AmaPanel';
import { CommandPalette } from './components/CommandPalette';
import { HelpOverlay } from './components/HelpOverlay';
import { ReadinessGate } from './components/ReadinessGate';
import { useCallback, useEffect, useRef, useState } from 'react';
import { emptyAttentionDrafts, type AttentionDrafts } from './features/AttentionInbox';
import { useConnectionState, useSystemAccentMirror, useTheme } from './hooks';

export default function App() {
  // Called purely for its side effect (mirroring the resolved theme onto
  // <html data-theme>, following OS changes); the switcher itself now lives
  // in Settings ▸ Appearance, which owns its own useTheme() instance and
  // stays in sync through the hook's cross-instance sync event.
  useTheme();
  useSystemAccentMirror();
  const connection = useConnectionState();
  const runtimeReady = connection.status === 'ready';
  const [serverAttentionItems, setServerAttentionItems] = useState<AttentionItem[]>([]);
  const [attentionDrafts, setAttentionDrafts] = useState<AttentionDrafts>(emptyAttentionDrafts);
  const [attentionJump, setAttentionJump] = useState<{
    requestId: number;
    featureId: string;
    attentionId?: string;
  } | null>(null);
  const [routeRequest, setRouteRequest] = useState<RoutedRequest | null>(null);
  const [updateState, setUpdateState] = useState<UpdateState | null>(null);
  // Owned here so the sidebar footer can show the mock's active-session state
  // while the panel itself owns the singleton chat session.
  const [amaSessionActive, setAmaSessionActive] = useState(false);
  const [updateDismissedVersion, setUpdateDismissedVersion] = useState<string | null>(null);
  const [schedulingUpdate, setSchedulingUpdate] = useState(false);
  const routeSequence = useRef(0);

  const requestRoute = useCallback((event: AppRouteEvent) => {
    routeSequence.current += 1;
    setRouteRequest({ id: routeSequence.current, event });
  }, []);

  useEffect(() => window.agentico.onRouteRequest(requestRoute), [requestRoute]);

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
            amaSessionActive={amaSessionActive}
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
            onSessionActiveChange={setAmaSessionActive}
          />
        </>
      ) : (
        <ConnectionShell />
      )}
      <CommandPalette ready={runtimeReady} routeRequest={routeRequest} onRoute={requestRoute} />
      <HelpOverlay routeRequest={routeRequest} />
    </div>
  );
}
