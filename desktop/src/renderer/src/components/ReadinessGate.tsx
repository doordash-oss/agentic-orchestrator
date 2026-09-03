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
 * runtime never flashes the wizard), the mandatory setup wizard while any
 * gate is unsatisfied, and the main view once everything passes. Mounted
 * fresh on every reconnect, so resume always starts from the server truth.
 */
import { useCallback, useState, type Dispatch, type SetStateAction } from 'react';
import type {
  AttentionItem,
  ReadinessSnapshot,
  RoutedRequest,
  UpdateState,
} from '../../../shared/ipc';
import { WorkspaceShell } from '../features/WorkspaceShell';
import type { AttentionDrafts } from '../features/AttentionInbox';
import { deriveWizardState } from '../wizard/deriveWizardState';
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
  onOpenAma = () => {},
  onOpenPalette = () => {},
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
  amaUnread?: boolean;
}) {
  const load = useCallback(() => window.agentico.getReadiness(), []);
  const { state, reload } = useIpcLoad(load, []);
  const [adoptedSnapshot, setAdoptedSnapshot] = useState<ReadinessSnapshot | null>(null);

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

  const snapshot = adoptedSnapshot ?? state.data;
  const derived = deriveWizardState(snapshot);
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
        amaUnread={amaUnread}
      />
    );
  }

  return <SetupWizard snapshot={snapshot} onSnapshot={setAdoptedSnapshot} />;
}
