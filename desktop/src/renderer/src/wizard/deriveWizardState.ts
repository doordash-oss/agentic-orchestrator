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
 * Pure projection from the authoritative server readiness snapshot to the
 * wizard's presentation state. There is deliberately no local "completed
 * step" memory anywhere: reload, reconnect, and cancellation all re-derive
 * the current step from the latest snapshot, so the wizard can never drift
 * from the server or trust stale renderer state.
 */
import type {
  ReadinessIssue,
  ReadinessIssueCode,
  RuntimeReadinessSnapshot,
} from '../../../shared/ipc';

export const WIZARD_STEPS = ['providers', 'models', 'ready'] as const;

export type WizardStepId = (typeof WIZARD_STEPS)[number];

/** The mandatory gates, in wizard order. */
export type WizardGateId = Exclude<WizardStepId, 'ready'>;

const GATE_ORDER: readonly WizardGateId[] = ['providers', 'models'];

/** Which flattened issue codes belong to which step's blocker list. Codes
 * outside the readiness vocabulary (the canonical error code is a plain
 * catalog code string) map to no step. */
const ISSUE_STEP: Partial<Record<ReadinessIssueCode, WizardStepId | null>> = {
  missing_executable: 'providers',
  unsupported_version: 'providers',
  unauthenticated: 'providers',
  models_unavailable: 'models',
  // Workspace shape is not a setup gate: repositories are chosen (and added)
  // where the work is defined, and their issues are reported there and in
  // Settings. Configuration is cross-cutting, reported via configurationIssue.
  invalid_workspace_root: null,
  invalid_repository: null,
  invalid_configuration: null,
};

const stepForIssueCode = (code: string): WizardStepId | null =>
  ISSUE_STEP[code as ReadinessIssueCode] ?? null;

export interface WizardState {
  steps: readonly WizardStepId[];
  /** The first unsatisfied gate, or 'ready' when all gates pass. */
  activeStep: WizardStepId;
  activeIndex: number;
  /** True only when every mandatory gate AND the server's own ready flag pass. */
  complete: boolean;
  gates: Record<WizardGateId, boolean>;
  /** Invalid runtime configuration blocks everything; shown as a banner. */
  configurationIssue: ReadinessIssue | null;
  /** Outstanding issues that belong to the active step. */
  blockers: readonly ReadinessIssue[];
}

export function deriveWizardState(snapshot: RuntimeReadinessSnapshot): WizardState {
  const gates: Record<WizardGateId, boolean> = {
    providers: snapshot.providers.some((provider) => provider.ready),
    models: snapshot.models.available,
  };

  const firstBlocked = GATE_ORDER.find((gate) => !gates[gate]);
  const activeStep: WizardStepId = firstBlocked ?? 'ready';
  const activeIndex = WIZARD_STEPS.indexOf(activeStep);

  const configurationIssue = snapshot.configuration.valid
    ? null
    : (snapshot.configuration.issue ?? {
        // Mirrors the catalog's authored invalid_configuration entry.
        code: 'invalid_configuration' as const,
        class: 'blocking' as const,
        title: 'Invalid configuration',
        summary: 'The runtime configuration is unusable.',
      });

  // Completion is server-authoritative: the snapshot's own ready flag decides,
  // and the per-gate projection only explains which part of it is outstanding.
  const complete =
    snapshot.ready && configurationIssue === null && GATE_ORDER.every((gate) => gates[gate]);

  const blockers = snapshot.issues.filter((issue) => stepForIssueCode(issue.code) === activeStep);

  return {
    steps: WIZARD_STEPS,
    activeStep,
    activeIndex,
    complete,
    gates,
    configurationIssue,
    blockers,
  };
}

/**
 * How the readiness gate presents a snapshot. With no ready provider the
 * runtime cannot do anything, so the full-page wizard owns the window. Once
 * one provider is ready the shell mounts even while other gates are still
 * outstanding (`partial`), carrying a banner and an on-demand wizard sheet;
 * `complete` is the shell as it has always been.
 */
export type ReadinessGateMode = 'wizard' | 'partial' | 'complete';

export function readinessGateMode(snapshot: RuntimeReadinessSnapshot): ReadinessGateMode {
  const derived = deriveWizardState(snapshot);
  if (derived.complete) return 'complete';
  return derived.gates.providers ? 'partial' : 'wizard';
}

/** Mirrors the catalog's authored not_ready entry, for the no-detail case. */
const NOT_READY_ISSUE: ReadinessIssue = {
  code: 'not_ready',
  class: 'needs_action',
  title: 'Runtime not ready',
  summary: 'The runtime is not ready to create features.',
};

/**
 * The single issue the partial-readiness banner names: the configuration
 * issue first (it blocks everything), then the active step's first blocker,
 * then the models gate's own issue, then whatever the server listed, and
 * finally the catalog's generic not-ready entry.
 */
export function setupBannerIssue(snapshot: RuntimeReadinessSnapshot): ReadinessIssue {
  const derived = deriveWizardState(snapshot);
  return (
    derived.configurationIssue ??
    derived.blockers[0] ??
    (derived.activeStep === 'models' ? snapshot.models.issue : undefined) ??
    snapshot.issues[0] ??
    NOT_READY_ISSUE
  );
}
