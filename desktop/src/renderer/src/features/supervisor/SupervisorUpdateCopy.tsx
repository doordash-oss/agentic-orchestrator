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
 * The one sentence every install surface carries while a supervisor is live:
 * an update restarts it, so the user learns what survives and what does not
 * before they consent.
 */
import type { SupervisorLifecycle } from '../../../../shared/ipc';
import { useSupervisorStatus } from './supervisorStatus';

export const SUPERVISOR_UPDATE_COPY =
  'Updating restarts the supervisor; your conversation is saved. Sub-agents and background shells will be lost.';

/**
 * Whether an update would restart a live supervisor: any lifecycle but
 * `stopped` or `failed`. Not yet loaded reads as nothing to restart.
 */
export function supervisorUpdateCopyApplies(lifecycle: SupervisorLifecycle | null): boolean {
  return lifecycle !== null && lifecycle !== 'stopped' && lifecycle !== 'failed';
}

/** Renders the update copy from the status store, or nothing. */
export function SupervisorUpdateCopy({ className }: { className: string }) {
  const { lifecycle } = useSupervisorStatus();
  if (!supervisorUpdateCopyApplies(lifecycle)) return null;
  return <p className={className}>{SUPERVISOR_UPDATE_COPY}</p>;
}
