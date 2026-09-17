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
 * this app; a server that predates the endpoints reads as unsupported.
 */
import { z } from 'zod';
import { CanonicalErrorSchema } from '../shared/api/parse';
import {
  ServerUpdateStateSchema,
  type ServerListRow,
  type ServerUpdateInstallRequest,
  type ServerUpdateState,
} from '../shared/ipc';
import { mapServerError, type ServerTransport } from './serverClient';

const WireSnapshotSchema = z
  .object({
    status: z.string(),
    policy: z.string(),
    current_version: z.string(),
    latest_version: z.string().optional(),
    target_version: z.string().optional(),
    latest_release_url: z.string().optional(),
    installation: z.string(),
    remediation: z.string().optional(),
    signature: z.string(),
    method: z.string().optional(),
    stop_active_work: z.boolean().optional(),
    scheduled_for: z.string().nullable().optional(),
    last_check_at: z.string().optional(),
    next_check_at: z.string().optional(),
    error: z.unknown().optional(),
    active_work_summary: z
      .object({
        feature_count: z.number().int().nonnegative(),
        chat_active: z.boolean(),
        clone_count: z.number().int().nonnegative(),
        upload_count: z.number().int().nonnegative(),
        origin_check_count: z.number().int().nonnegative(),
        pending_admissions: z.number().int().nonnegative(),
        detection_failed: z.boolean(),
      })
      .passthrough(),
  })
  .passthrough();

const WireResponseSchema = z.object({ update: WireSnapshotSchema }).passthrough();

type WireSnapshot = z.output<typeof WireSnapshotSchema>;

export function describeActiveWork(
  summary: WireSnapshot['active_work_summary'],
): string | undefined {
  if (summary.detection_failed)
    return 'Activity detection failed; the server refuses to install now.';
  const parts: string[] = [];
  const plural = (n: number, noun: string) => `${n} ${noun}${n === 1 ? '' : 's'}`;
  if (summary.feature_count > 0) parts.push(plural(summary.feature_count, 'feature'));
  if (summary.chat_active) parts.push('chat');
  const repository = summary.clone_count + summary.upload_count + summary.origin_check_count;
  if (repository > 0) parts.push(plural(repository, 'repository operation'));
  if (parts.length === 0) return undefined;
  const list =
    parts.length === 1
      ? parts[0]
      : `${parts.slice(0, -1).join(', ')} and ${parts[parts.length - 1]}`;
  return `${list} active on the server.`;
}

export function projectServerUpdate(raw: unknown): ServerUpdateState {
  const wire = WireResponseSchema.parse(raw).update;
  const error = CanonicalErrorSchema.safeParse(wire.error);
  const optional = <T>(key: string, value: T | null | undefined): Record<string, T> =>
    value === undefined || value === null || value === '' ? {} : { [key]: value };
  return ServerUpdateStateSchema.parse({
    status: wire.status,
    policy: wire.policy,
    currentVersion: wire.current_version,
    installation: wire.installation,
    signature: wire.signature,
    ...optional('latestVersion', wire.latest_version),
    ...optional('targetVersion', wire.target_version),
    ...optional('releaseUrl', wire.latest_release_url),
    ...optional('remediation', wire.remediation),
    ...optional('method', wire.method),
    ...(wire.stop_active_work === undefined ? {} : { stopActiveWork: wire.stop_active_work }),
    ...optional('scheduledFor', wire.scheduled_for),
    ...optional('lastCheckAt', wire.last_check_at),
    ...optional('nextCheckAt', wire.next_check_at),
    ...optional('activeWorkSummary', describeActiveWork(wire.active_work_summary)),
    ...(error.success ? { error: error.data } : {}),
  });
}

const PREDATES_UPDATES: ServerUpdateState = {
  status: 'unsupported',
  policy: 'off',
  currentVersion: 'unknown',
  installation: 'unknown',
  signature: 'unverified',
  remediation: 'This server predates in-place updates. Upgrade it once by hand to enable them.',
};

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
    if (result.status === 404) {
      this.last = PREDATES_UPDATES;
      return PREDATES_UPDATES;
    }
    if (result.status < 200 || result.status >= 300) {
      throw mapServerError(result);
    }
    const state = projectServerUpdate(result.body);
    this.last = state;
    return state;
  }
}
