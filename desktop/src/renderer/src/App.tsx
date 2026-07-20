import {
  isConnectionErrorState,
  type AppRouteEvent,
  type AttentionItem,
  type FeatureSummaryView,
  type RoutedRequest,
  type ThemePreference,
} from '../../shared/ipc';
import { ConnectionShell } from './components/ConnectionShell';
import { AmaDock } from './components/AmaDock';
import { CommandPalette } from './components/CommandPalette';
import { ReadinessGate } from './components/ReadinessGate';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  AttentionInbox,
  emptyAttentionDrafts,
  type AttentionDrafts,
} from './features/AttentionInbox';
import { useConnectionState, useTheme } from './hooks';

const THEME_OPTIONS: readonly { value: ThemePreference; label: string }[] = [
  { value: 'light', label: 'Light' },
  { value: 'dark', label: 'Dark' },
  { value: 'system', label: 'System' },
];

export default function App() {
  const { preference, setPreference } = useTheme();
  const connection = useConnectionState();
  const [serverAttentionItems, setServerAttentionItems] = useState<AttentionItem[]>([]);
  const [recoveryItems, setRecoveryItems] = useState<{
    liveCount: number;
    deadCount: number;
    firstSeenAt: string;
  } | null>(null);
  const [attentionDrafts, setAttentionDrafts] = useState<AttentionDrafts>(emptyAttentionDrafts);
  const [featureNames, setFeatureNames] = useState<Record<string, string>>({});
  const [attentionJump, setAttentionJump] = useState<string | null>(null);
  const [routeRequest, setRouteRequest] = useState<RoutedRequest | null>(null);
  const routeSequence = useRef(0);

  const requestRoute = useCallback((event: AppRouteEvent) => {
    routeSequence.current += 1;
    setRouteRequest({ id: routeSequence.current, event });
  }, []);

  useEffect(() => window.agentico.onRouteRequest(requestRoute), [requestRoute]);

  const refreshAttention = useCallback(async () => {
    const snapshot = await window.agentico.getAttention();
    setServerAttentionItems(snapshot.items);
    return snapshot.items;
  }, []);

  const refreshRecovery = useCallback(async () => {
    try {
      const snapshot = await window.agentico.scanRecovery();
      const live = snapshot.items.filter((i) => i.processAlive).length;
      const dead = snapshot.items.length - live;
      if (live > 0 || dead > 0) {
        setRecoveryItems((prev) =>
          prev === null
            ? { liveCount: live, deadCount: dead, firstSeenAt: new Date().toISOString() }
            : { ...prev, liveCount: live, deadCount: dead },
        );
      } else {
        setRecoveryItems(null);
      }
    } catch {
      setRecoveryItems(null);
    }
  }, []);

  useEffect(() => {
    void refreshAttention();
    void refreshRecovery();
    return window.agentico.onAppEvent((event) => {
      if (event.type === 'status') {
        void refreshAttention();
        return;
      }
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
  }, [refreshAttention, refreshRecovery]);

  const refreshFeatureNames = useCallback(async () => {
    const features = await window.agentico.listFeatures();
    setFeatureNames(namesById(features));
  }, []);

  useEffect(() => {
    void refreshFeatureNames();
    return window.agentico.onAppEvent((event) => {
      if (
        event.type === 'invalidated' &&
        (event.kind === 'resync' ||
          event.kind.startsWith('feature') ||
          event.kind.startsWith('lifecycle'))
      ) {
        void refreshFeatureNames();
      }
    });
  }, [refreshFeatureNames]);

  const runtimeLabel =
    connection.status === 'ready'
      ? 'Runtime ready'
      : isConnectionErrorState(connection)
        ? 'Runtime needs attention'
        : 'Connecting';
  const runtimeTone =
    connection.status === 'ready'
      ? 'ready'
      : isConnectionErrorState(connection)
        ? 'error'
        : 'progress';

  // Recovery items sort ahead of all other attention so recovery receives
  // contextual priority (Task 8 acceptance criterion 1).
  const attentionItems = useMemo<AttentionItem[]>(() => {
    if (recoveryItems === null) return serverAttentionItems;
    const recoveryAttention: AttentionItem = {
      kind: 'recovery',
      id: 'recovery-scan',
      waitingSince: recoveryItems.firstSeenAt,
      liveCount: recoveryItems.liveCount,
      deadCount: recoveryItems.deadCount,
    };
    return [recoveryAttention, ...serverAttentionItems];
  }, [recoveryItems, serverAttentionItems]);

  return (
    <div className="app-frame">
      <header className="global-bar">
        <div className="global-bar__brand">
          <span className="global-bar__mark" aria-hidden="true">
            A
          </span>
          <h1>Agentico</h1>
        </div>
        <p className="global-bar__runtime" role="status" data-tone={runtimeTone}>
          <span aria-hidden="true">●</span> {runtimeLabel}
        </p>
        <AttentionInbox
          items={attentionItems}
          refresh={refreshAttention}
          featureLabel={(featureId) =>
            featureId === undefined ? 'Runtime' : (featureNames[featureId] ?? 'Untitled feature')
          }
          drafts={attentionDrafts}
          setDrafts={setAttentionDrafts}
          onJump={setAttentionJump}
          openRequest={
            routeRequest?.event.target === 'attention'
              ? {
                  id: routeRequest.id,
                  attentionId: routeRequest.event.attentionId,
                }
              : null
          }
        />
        <fieldset className="theme-switcher" role="radiogroup" aria-label="Theme">
          <legend className="sr-only">Theme</legend>
          {THEME_OPTIONS.map((option) => (
            <label key={option.value} className="theme-switcher__option">
              <input
                type="radio"
                name="theme"
                value={option.value}
                checked={preference === option.value}
                onChange={() => setPreference(option.value)}
              />
              <span>{option.label}</span>
            </label>
          ))}
        </fieldset>
      </header>
      {connection.status === 'ready' ? (
        <>
          <ReadinessGate
            attentionItems={attentionItems}
            refreshAttention={refreshAttention}
            attentionDrafts={attentionDrafts}
            setAttentionDrafts={setAttentionDrafts}
            attentionJump={attentionJump}
            onAttentionJumpHandled={() => setAttentionJump(null)}
            routeRequest={routeRequest}
          />
          <AmaDock
            attentionItems={attentionItems}
            refreshAttention={refreshAttention}
            attentionDrafts={attentionDrafts}
            setAttentionDrafts={setAttentionDrafts}
            routeRequest={routeRequest}
          />
        </>
      ) : (
        <ConnectionShell />
      )}
      <CommandPalette
        ready={connection.status === 'ready'}
        routeRequest={routeRequest}
        onRoute={requestRoute}
      />
    </div>
  );
}

function namesById(features: readonly FeatureSummaryView[]): Record<string, string> {
  return Object.fromEntries(features.map((feature) => [feature.id, feature.name]));
}
