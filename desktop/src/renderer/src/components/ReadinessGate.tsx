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
 * Decides what a ready connection shows: nothing but a loading state until
 * the first authoritative readiness snapshot arrives (so an already-ready
 * runtime never flashes the wizard), then one of three modes. With no ready
 * provider the full-page setup wizard owns the window. With at least one
 * ready provider but setup still incomplete, the main view mounts in
 * partial-readiness mode, carrying the snapshot so its banner and on-demand
 * wizard sheet read the same truth the gate does. Once everything passes the
 * main view mounts as-is. Partial and complete render the same shell element,
 * so a "Check again" that completes setup never remounts it. Mounted fresh on
 * every reconnect, so resume always starts from the server truth.
 */
import { useCallback, useEffect, useRef, type Dispatch, type SetStateAction } from 'react';
import type { AttentionItem, RoutedRequest, UpdateState } from '../../../shared/ipc';
import { WorkspaceShell } from '../features/WorkspaceShell';
import type { AttentionDrafts } from '../features/AttentionInbox';
import { readinessGateMode } from '../wizard/deriveWizardState';
import { retryAction, useIpcLoad } from '../hooks';
import { SetupWizard } from './wizard/SetupWizard';
import { ErrorSurface } from './ErrorSurface';

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
  onOpenPalette = () => {},
  onSetupIncompleteChange = () => {},
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
  /** Owned by App: dispatches the same 'palette' routeRequest ⌘K resolves to. */
  onOpenPalette?(): void;
  /**
   * Owned by App: true while the shell is mounted in partial-readiness mode,
   * so the palette can offer Setup…; false otherwise and on unmount.
   */
  onSetupIncompleteChange?(incomplete: boolean): void;
}) {
  const load = useCallback(() => window.agentico.getRuntimeReadiness(), []);
  const { state, reload, replace } = useIpcLoad(load, []);
  const mode = state.phase === 'loaded' ? readinessGateMode(state.data) : null;
  const setupIncomplete = mode === 'partial';

  const onSetupIncompleteChangeRef = useRef(onSetupIncompleteChange);
  onSetupIncompleteChangeRef.current = onSetupIncompleteChange;
  useEffect(() => {
    onSetupIncompleteChangeRef.current(setupIncomplete);
  }, [setupIncomplete]);
  useEffect(() => () => onSetupIncompleteChangeRef.current(false), []);

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
        {/* The parsed canonical error owns the presentation — code, title,
         * summary, remediation — so no hand-written remediation sentence
         * rides along; Retry simply re-runs the fetch. */}
        <ErrorSurface error={state.error} variant="compact" localAction={retryAction(reload)} />
      </section>
    );
  }

  const snapshot = state.data;
  if (mode !== 'wizard') {
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
        onOpenPalette={onOpenPalette}
        setupReadiness={setupIncomplete ? { snapshot, onSnapshot: replace } : null}
      />
    );
  }

  return <SetupWizard snapshot={snapshot} onSnapshot={replace} />;
}
