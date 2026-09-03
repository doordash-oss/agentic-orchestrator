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

import type { VerificationItemView } from '../../../shared/ipc';

export type VerificationTone = 'passed' | 'failed' | 'running' | 'pending' | 'neutral';

/**
 * Maps a harness verification state onto a display tone. Terminal failure
 * states (failed/blocked/inherited_failure) collapse to `failed`; states that
 * neither passed nor ran to a verdict (waived/not_run/pending_human) are
 * `neutral`.
 */
export function verificationTone(state: string): VerificationTone {
  const normalized = state.trim().toLocaleLowerCase();
  if (normalized === 'passed') return 'passed';
  if (normalized === 'running') return 'running';
  if (normalized === 'pending' || normalized === '') return 'pending';
  if (['failed', 'blocked', 'inherited_failure'].includes(normalized)) return 'failed';
  return 'neutral';
}

export function verificationSymbol(state: string): string {
  const symbols: Record<VerificationTone, string> = {
    passed: '✓',
    failed: '✕',
    running: '⟳',
    pending: '·',
    neutral: '•',
  };
  return symbols[verificationTone(state)];
}

export interface VerificationCounts {
  total: number;
  /** Items with a terminal verdict (not running or pending). */
  done: number;
  /** Items in a failure tone (failed/blocked/inherited_failure). */
  failed: number;
}

export function verificationCounts(items: readonly VerificationItemView[]): VerificationCounts {
  let done = 0;
  let failed = 0;
  for (const item of items) {
    const tone = verificationTone(item.state);
    if (tone !== 'running' && tone !== 'pending') done += 1;
    if (tone === 'failed') failed += 1;
  }
  return { total: items.length, done, failed };
}

/**
 * True when the harness is mid-verification: the server reports the
 * "verifying" phase status and has seeded at least one contract command.
 * The review gate takes precedence, so callers should treat an active
 * reviewing gate as non-verifying.
 */
export function isVerifyingPhase(
  phaseStatus: string | undefined,
  items: readonly VerificationItemView[] | undefined,
): boolean {
  return phaseStatus?.trim().toLocaleLowerCase() === 'verifying' && (items?.length ?? 0) > 0;
}
