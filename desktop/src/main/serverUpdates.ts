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
 * Drives the connected server's own update endpoints (GET /api/v1/update,
 * POST check/install, DELETE install) and projects the wire snapshot onto the
 * renderer-safe ServerUpdateState. Distinct from updates.ts, which updates
 * this app.
 */
import {
  CanonicalErrorSchema,
  WireResponseSchema,
  type WireSnapshotSchema,
} from '../shared/api/parse';
import type { z } from 'zod';
import {
  ServerUpdateStateSchema,
  type ServerListRow,
  type ServerUpdateInstallRequest,
  type ServerUpdateState,
} from '../shared/ipc';
import { supervisorActivitySentence } from './quitCoordinator';
import { mapServerError, type ServerTransport } from './serverClient';
import { defined, definedNonEmpty } from '../shared/defined';

type WireSnapshot = z.output<typeof WireSnapshotSchema>;

export function describeActiveWork(
  summary: WireSnapshot['active_work_summary'],
): string | undefined {
  if (summary.detection_failed)
    return 'Activity detection failed; the server refuses to install now.';
  const parts: string[] = [];
  const plural = (n: number, noun: string) => `${n} ${noun}${n === 1 ? '' : 's'}`;
  if (summary.feature_count > 0) parts.push(plural(summary.feature_count, 'feature'));
  const repository = summary.clone_count + summary.upload_count + summary.origin_check_count;
  if (repository > 0) parts.push(plural(repository, 'repository operation'));
  const supervisor = supervisorActivitySentence({
    supervisorActive: summary.supervisor_active,
    supervisorWaiting: summary.supervisor_waiting,
  });
  const last = parts.pop();
  const list =
    last === undefined ? null : parts.length === 0 ? last : `${parts.join(', ')} and ${last}`;
  const sentences = [
    list === null ? null : `${list.charAt(0).toUpperCase()}${list.slice(1)} active on the server.`,
    supervisor,
  ].filter((sentence): sentence is string => sentence !== null);
  return sentences.length === 0 ? undefined : sentences.join(' ');
}

export function projectServerUpdate(raw: unknown): ServerUpdateState {
  const wire = WireResponseSchema.parse(raw).update;
  const error = CanonicalErrorSchema.safeParse(wire.error);
  return ServerUpdateStateSchema.parse({
    status: wire.status,
    policy: wire.policy,
    currentVersion: wire.current_version,
    installation: wire.installation,
    signature: wire.signature,
    ...definedNonEmpty('latestVersion', wire.latest_version ?? undefined),
    ...definedNonEmpty('targetVersion', wire.target_version ?? undefined),
    ...definedNonEmpty('releaseUrl', wire.latest_release_url ?? undefined),
    ...definedNonEmpty('remediation', wire.remediation ?? undefined),
    ...definedNonEmpty('method', wire.method ?? undefined),
    ...defined('stopActiveWork', wire.stop_active_work),
    ...definedNonEmpty('scheduledFor', wire.scheduled_for ?? undefined),
    ...definedNonEmpty('lastCheckAt', wire.last_check_at ?? undefined),
    ...definedNonEmpty('nextCheckAt', wire.next_check_at ?? undefined),
    ...definedNonEmpty('activeWorkSummary', describeActiveWork(wire.active_work_summary)),
    ...(error.success ? { error: error.data } : {}),
  });
}

export class ServerUpdateService {
  private last: ServerUpdateState | null = null;

  constructor(private readonly transport: ServerTransport) {}

  async get(): Promise<ServerUpdateState> {
    return this.request('/api/v1/update', undefined);
  }

  async check(): Promise<ServerUpdateState> {
    return this.request('/api/v1/update/check', { method: 'POST', body: {} });
  }

  async install(request: ServerUpdateInstallRequest): Promise<ServerUpdateState> {
    // The server refuses a method change on an active operation; an idle
    // wait promoted to "now" is cancelled first.
    if (
      request.when === 'now' &&
      this.last?.method === 'idle' &&
      this.last.status === 'scheduled'
    ) {
      await this.cancel();
    }
    return this.request('/api/v1/update/install', {
      method: 'POST',
      body: {
        consent: true,
        when: request.when,
        ...(request.when === 'now' && request.stopActiveWork === true
          ? { stop_active_work: true }
          : {}),
      },
    });
  }

  async cancel(): Promise<ServerUpdateState> {
    return this.request('/api/v1/update/install', { method: 'DELETE', body: {} });
  }

  /** Best-effort refresh after a server-side change; never throws. */
  async refresh(): Promise<void> {
    try {
      await this.get();
    } catch {
      this.last = null;
    }
  }

  /** The switcher badge for the connected server, from the last read. */
  switcherBadge(): ServerListRow['serverUpdate'] | undefined {
    const state = this.last;
    if (state === null || state.latestVersion === undefined) return undefined;
    const available = ['available', 'downloading', 'verified', 'scheduled'].includes(state.status);
    return { available, latest: state.latestVersion };
  }

  private async request(
    path: string,
    init: { method: 'POST' | 'DELETE'; body: unknown } | undefined,
  ): Promise<ServerUpdateState> {
    const result = await this.transport.apiRequest(path, init);
    if (result.status < 200 || result.status >= 300) {
      throw mapServerError(result);
    }
    const state = projectServerUpdate(result.body);
    this.last = state;
    return state;
  }
}
