/**
 * Decides what a ready connection shows: nothing but a loading state until
 * the first authoritative readiness snapshot arrives (so an already-ready
 * runtime never flashes the wizard), the mandatory setup wizard while any
 * gate is unsatisfied, and the main view once everything passes. Mounted
 * fresh on every reconnect, so resume always starts from the server truth.
 */
import { useCallback, useEffect, useState, type Dispatch, type SetStateAction } from 'react';
import type {
  AttentionItem,
  ReadinessSnapshot,
  RoutedRequest,
  UpdateState,
} from '../../../shared/ipc';
import { WorkspaceShell } from '../features/WorkspaceShell';
import type { AttentionDrafts } from '../features/AttentionInbox';
import { deriveWizardState } from '../wizard/deriveWizardState';
import { parseIpcError, type WizardError } from '../wizard/ipcError';
import { SetupWizard } from './wizard/SetupWizard';

type GateState =
  | { phase: 'loading' }
  | { phase: 'error'; error: WizardError }
  | { phase: 'loaded'; snapshot: ReadinessSnapshot };

export function ReadinessGate({
  attentionDrafts,
  setAttentionDrafts,
  attentionItems = [],
  refreshAttention = async () => [],
  attentionJump = null,
  onAttentionJumpHandled = () => {},
  onAttentionJump = () => {},
  routeRequest = null,
  updateState = null,
  updateDismissedVersion = null,
  schedulingUpdate = false,
  onDismissUpdate = () => {},
  onOpenUpdatesSettings = () => {},
  onInstallUpdateWhenIdle = async () => {},
  onOpenAma = () => {},
  onOpenPalette = () => {},
  amaSessionActive = false,
  amaUnread = false,
}: {
  attentionDrafts?: AttentionDrafts;
  setAttentionDrafts?: Dispatch<SetStateAction<AttentionDrafts>>;
  attentionItems?: AttentionItem[];
  refreshAttention?: () => Promise<AttentionItem[]>;
  attentionJump?: {
    requestId: number;
    featureId: string;
    attentionId?: string;
  } | null;
  onAttentionJumpHandled?: () => void;
  /** Owned by App: routes a bell/inbox jump into the shell's selection. */
  onAttentionJump?(featureId: string, attentionId?: string): void;
  routeRequest?: RoutedRequest | null;
  updateState?: UpdateState | null;
  updateDismissedVersion?: string | null;
  schedulingUpdate?: boolean;
  onDismissUpdate?(version: string): void;
  onOpenUpdatesSettings?(): void;
  onInstallUpdateWhenIdle?(): Promise<void>;
  onOpenAma?(): void;
  /** Owned by App: dispatches the same 'palette' routeRequest ⌘K resolves to. */
  onOpenPalette?(): void;
  amaSessionActive?: boolean;
  amaUnread?: boolean;
}) {
  const [state, setState] = useState<GateState>({ phase: 'loading' });

  const load = useCallback(() => {
    setState({ phase: 'loading' });
    window.agentico
      .getReadiness()
      .then((snapshot) => setState({ phase: 'loaded', snapshot }))
      .catch((err: unknown) => setState({ phase: 'error', error: parseIpcError(err) }));
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  if (state.phase === 'loading') {
    return (
      <section className="shell-card setup-gate" aria-label="Runtime readiness">
        <p className="setup-gate__loading" role="status" aria-live="polite">
          Checking runtime readiness…
        </p>
      </section>
    );
  }

  if (state.phase === 'error') {
    return (
      <section className="shell-card setup-gate" aria-label="Runtime readiness">
        <div className="shell-card__error">
          <div className="shell-card__error-head">
            <span className="shell-card__error-code">{state.error.code}</span>
          </div>
          <p className="shell-card__error-message">{state.error.message}</p>
          <p className="shell-card__error-remediation">
            The readiness check could not be completed. Retry once the runtime is reachable.
          </p>
          <button type="button" className="shell-card__retry" onClick={load}>
            Try again
          </button>
        </div>
      </section>
    );
  }

  const derived = deriveWizardState(state.snapshot);
  if (derived.complete) {
    return (
      <WorkspaceShell
        attentionItems={attentionItems}
        refreshAttention={refreshAttention}
        attentionDrafts={attentionDrafts}
        setAttentionDrafts={setAttentionDrafts}
        attentionJump={attentionJump}
        onAttentionJumpHandled={onAttentionJumpHandled}
        onAttentionJump={onAttentionJump}
        routeRequest={routeRequest}
        updateState={updateState}
        updateDismissedVersion={updateDismissedVersion}
        schedulingUpdate={schedulingUpdate}
        onDismissUpdate={onDismissUpdate}
        onOpenUpdatesSettings={onOpenUpdatesSettings}
        onInstallUpdateWhenIdle={onInstallUpdateWhenIdle}
        onOpenAma={onOpenAma}
        onOpenPalette={onOpenPalette}
        amaSessionActive={amaSessionActive}
        amaUnread={amaUnread}
      />
    );
  }

  return (
    <SetupWizard
      snapshot={state.snapshot}
      onSnapshot={(snapshot) => setState({ phase: 'loaded', snapshot })}
    />
  );
}
