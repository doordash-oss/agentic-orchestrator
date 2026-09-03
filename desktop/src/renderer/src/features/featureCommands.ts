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
 * The single renderer funnel every feature command flows through, whatever
 * invoked it: the ⌘K palette, the native Feature menu, or a routed menu
 * click. The mounted cockpit registers itself as the executor for the feature
 * it is showing, so a funnel invocation runs *exactly* the cockpit's own flow
 * — the same confirmation dialogs, completion preflight modals, launchers,
 * and editors its buttons open — instead of a second, drifting copy of it.
 *
 * Every invocation re-validates against the live registration and its live
 * action catalogue, so a command that raced a selection change or is no
 * longer enabled is a safe no-op rather than a mis-targeted dispatch. Exactly
 * one cockpit is mounted at a time (the shell renders one content pane), so a
 * module-level registration is the whole registry.
 */
import {
  featureCommandState,
  type FeatureActionLike,
  type FeatureCommandId,
} from '../../../shared/commands';

export interface FeatureCommandExecutor {
  /** The feature this executor acts on; a mismatched target is a no-op. */
  featureId: string;
  /** Read at invocation time, never captured — staleness is the whole risk. */
  actions(): readonly FeatureActionLike[] | null;
  /** Runs the cockpit's own flow for the command. */
  run(id: FeatureCommandId): void;
  /** Flips the cockpit's inspector (wide split-view pane or narrow drawer). */
  toggleInspector(): void;
}

let executor: FeatureCommandExecutor | null = null;

/** Registers the mounted cockpit; the returned function unregisters it. */
export function registerFeatureCommandExecutor(next: FeatureCommandExecutor): () => void {
  executor = next;
  return () => {
    if (executor === next) {
      executor = null;
    }
  };
}

export type FeatureCommandOutcome = 'executed' | 'no-target' | 'not-enabled';

/**
 * Runs `id` against the live registration. `featureId`, when supplied, must
 * match it — that is how a menu click that raced a selection change resolves
 * to a no-op instead of acting on the feature the user just left.
 */
export function runFeatureCommand(
  id: FeatureCommandId,
  options: { featureId?: string } = {},
): FeatureCommandOutcome {
  const target = executor;
  if (target === null) return 'no-target';
  if (options.featureId !== undefined && options.featureId !== target.featureId) {
    return 'no-target';
  }
  const state = featureCommandState(id, target.actions(), { hasSelection: true });
  if (!state.enabled) return 'not-enabled';
  target.run(id);
  return 'executed';
}

/**
 * The mounted cockpit's feature and its catalogue as of right now, or null when
 * none is mounted. The palette reads this when it opens so its Feature group is
 * right on the first frame, rather than a round trip later.
 */
export function activeFeatureCommandTarget(): {
  featureId: string;
  actions: readonly FeatureActionLike[] | null;
} | null {
  return executor === null ? null : { featureId: executor.featureId, actions: executor.actions() };
}

/** Flips the mounted cockpit's inspector; a no-op when none is mounted. */
export function toggleActiveInspector(): boolean {
  if (executor === null) return false;
  executor.toggleInspector();
  return true;
}
