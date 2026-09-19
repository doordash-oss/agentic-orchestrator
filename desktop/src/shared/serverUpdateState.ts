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

import type { ServerUpdateState } from './ipc';

const INSTALLING = new Set(['downloading', 'verified', 'scheduled', 'draining', 'restarting']);

export function serverUpdateActive(state: ServerUpdateState | null): boolean {
  return state !== null && INSTALLING.has(state.status);
}

export function serverUpdateAvailable(state: ServerUpdateState | null): boolean {
  return state !== null && state.status === 'available';
}

export function serverUpdateStatusLabel(state: ServerUpdateState | null): string {
  if (state === null) return 'Unknown';
  switch (state.status) {
    case 'up_to_date':
      return 'Up to date';
    case 'draining':
      return 'Stopping work';
    case 'restarting':
      return 'Restarting…';
    case 'scheduled':
      return state.scheduledFor === undefined ? 'Waiting for idle' : 'Waiting for window';
    default:
      return state.status.charAt(0).toUpperCase() + state.status.slice(1);
  }
}

export function serverUpdateTone(state: ServerUpdateState | null): string {
  if (state === null) return 'neutral';
  if (state.status === 'failed') return 'error';
  if (state.status === 'available' || state.status === 'verified' || state.status === 'scheduled') {
    return 'ready';
  }
  if (['checking', 'downloading', 'draining', 'restarting'].includes(state.status)) {
    return 'progress';
  }
  return 'neutral';
}

function formatLocalTime(iso: string): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return iso;
  return date.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' });
}

/** One sentence describing where the server's update stands. */
export function serverUpdateSummary(state: ServerUpdateState | null): string {
  if (state === null) return 'Server update state has not loaded yet.';
  switch (state.status) {
    case 'disabled':
      return 'Updates are turned off by this server’s startup policy.';
    case 'unsupported':
      return state.remediation ?? 'This server cannot update itself in place.';
    case 'idle':
      return 'No release check has run yet.';
    case 'checking':
      return 'Checking GitHub Releases for a newer server.';
    case 'up_to_date':
      return 'The server is running the latest release.';
    case 'available':
      return state.policy === 'auto'
        ? `Version ${state.latestVersion ?? ''} will install automatically when the server is idle.`
        : `Version ${state.latestVersion ?? ''} is available to install.`;
    case 'downloading':
      return `Downloading and verifying version ${state.targetVersion ?? ''}.`;
    case 'verified':
      return `Version ${state.targetVersion ?? ''} is verified and about to install.`;
    case 'scheduled':
      return state.scheduledFor === undefined
        ? `Version ${state.targetVersion ?? ''} installs as soon as the server is idle.`
        : `Version ${state.targetVersion ?? ''} installs after ${formatLocalTime(state.scheduledFor)}, once the server is idle.`;
    case 'draining':
      return 'Stopping work before the server restarts.';
    case 'restarting':
      return 'The server is restarting into the new version. This window reconnects on its own.';
    case 'confirmed':
      return `The server updated to ${state.currentVersion} and confirmed healthy.`;
    case 'failed':
      return 'The last update attempt failed.';
  }
}

export function serverUpdatePolicyLabel(state: ServerUpdateState | null): string {
  if (state === null) return 'Unknown';
  switch (state.policy) {
    case 'auto':
      return 'Installs automatically when idle';
    case 'notify':
      return 'Notify only';
    case 'off':
      return 'Off';
  }
}

export interface ServerUpdateVerbs {
  check: boolean;
  installWhenIdle: boolean;
  installNow: boolean;
  /** Install now must stop live work first and needs a consent dialog. */
  installNowStopsWork: boolean;
  cancel: boolean;
}

export function serverUpdateVerbs(state: ServerUpdateState | null): ServerUpdateVerbs {
  const none: ServerUpdateVerbs = {
    check: false,
    installWhenIdle: false,
    installNow: false,
    installNowStopsWork: false,
    cancel: false,
  };
  if (state === null || state.status === 'disabled' || state.status === 'unsupported') return none;
  const busy =
    state.status === 'checking' || state.status === 'draining' || state.status === 'restarting';
  const available = state.status === 'available' || state.status === 'failed';
  const hasTarget =
    state.latestVersion !== undefined && state.latestVersion !== state.currentVersion;
  const installable = available && hasTarget;
  const active = serverUpdateActive(state);
  return {
    check: !busy && !active,
    installWhenIdle: installable && state.policy !== 'auto',
    installNow: installable || (active && state.method === 'idle' && state.status === 'scheduled'),
    installNowStopsWork: state.activeWorkSummary !== undefined,
    cancel: active && state.status !== 'draining' && state.status !== 'restarting',
  };
}
