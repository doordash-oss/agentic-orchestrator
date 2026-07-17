import {
  isConnectionErrorState,
  type AttentionItem,
  type FeatureSummaryView,
  type ThemePreference,
} from '../../shared/ipc';
import { ConnectionShell } from './components/ConnectionShell';
import { ReadinessGate } from './components/ReadinessGate';
import { useCallback, useEffect, useState } from 'react';
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
  const [attentionItems, setAttentionItems] = useState<AttentionItem[]>([]);
  const [attentionDrafts, setAttentionDrafts] = useState<AttentionDrafts>(emptyAttentionDrafts);
  const [featureNames, setFeatureNames] = useState<Record<string, string>>({});
  const [attentionJump, setAttentionJump] = useState<string | null>(null);

  const refreshAttention = useCallback(async () => {
    const snapshot = await window.agentico.getAttention();
    setAttentionItems(snapshot.items);
    return snapshot.items;
  }, []);

  useEffect(() => {
    void refreshAttention();
    return window.agentico.onAppEvent((event) => {
      if (
        event.type === 'status' ||
        event.kind === 'resync' ||
        event.kind === 'permission.updated' ||
        event.kind === 'prompt.updated' ||
        event.kind.startsWith('feature') ||
        event.kind.startsWith('lifecycle')
      ) {
        void refreshAttention();
      }
    });
  }, [refreshAttention]);

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
        <ReadinessGate
          attentionItems={attentionItems}
          refreshAttention={refreshAttention}
          attentionDrafts={attentionDrafts}
          setAttentionDrafts={setAttentionDrafts}
          attentionJump={attentionJump}
          onAttentionJumpHandled={() => setAttentionJump(null)}
        />
      ) : (
        <ConnectionShell />
      )}
    </div>
  );
}

function namesById(features: readonly FeatureSummaryView[]): Record<string, string> {
  return Object.fromEntries(features.map((feature) => [feature.id, feature.name]));
}
