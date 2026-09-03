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
 * Main-process first-launch setup service. Every operation uses the gateway's
 * bearer transport or the native directory picker; creation-file concerns
 * live in CreationFilesService.
 */
import { buildCanonicalError, CanonicalErrorException } from '../shared/errors';
import {
  ReadinessResponseSchema,
  RuntimeConfigWorkspaceSchema,
  validateWithSchema,
  type ReadinessResponse,
} from '../shared/api/parse';
import {
  AbsolutePathSchema,
  type InitRepositoryRequest,
  type PickedDirectory,
  type ReadinessSnapshot,
  type RepositoryState,
} from '../shared/ipc';
import type { ApiRequestInit } from './gateway/runtimeGateway';
import { serverRequest, type ServerTransport } from './serverClient';

/** The authenticated transport surface the gateway provides. */
export type SetupTransport = ServerTransport;

export interface SetupDialogs {
  /** Native directory picker; resolves null when the user cancels. */
  pickDirectory(): Promise<string | null>;
}

export interface SetupServiceDeps {
  transport: SetupTransport;
  dialogs: SetupDialogs;
}

export class SetupService {
  constructor(private readonly deps: SetupServiceDeps) {}

  async getReadiness(): Promise<ReadinessSnapshot> {
    const body = await this.api('/api/v1/readiness');
    return toReadinessSnapshot(validateWithSchema(body, ReadinessResponseSchema));
  }

  async refreshReadiness(): Promise<ReadinessSnapshot> {
    const body = await this.api('/api/v1/readiness/refresh', { method: 'POST', body: {} });
    return toReadinessSnapshot(validateWithSchema(body, ReadinessResponseSchema));
  }

  async pickWorkspaceDirectory(): Promise<PickedDirectory> {
    const picked = await this.deps.dialogs.pickDirectory();
    if (picked === null) {
      return { path: null };
    }
    const parsed = AbsolutePathSchema.safeParse(picked);
    if (!parsed.success) {
      // Never echo the rejected path back across the boundary.
      throw new CanonicalErrorException(buildCanonicalError('E_INVALID_PATH'));
    }
    return { path: parsed.data };
  }

  /**
   * Adds a workspace root through the server's runtime-config mutation
   * (which persists it and rediscovers repositories server-side), then
   * returns the fresh authoritative readiness snapshot.
   */
  async addWorkspaceRoot(path: string): Promise<ReadinessSnapshot> {
    const validated = validateWithSchema(path, AbsolutePathSchema);
    const configBody = await this.api('/api/v1/config/runtime');
    const config = validateWithSchema(configBody, RuntimeConfigWorkspaceSchema);
    const roots = config.workspace_roots ?? [];
    if (!roots.includes(validated)) {
      await this.api('/api/v1/config/runtime', {
        method: 'PATCH',
        body: { workspace_roots: [...roots, validated] },
      });
    }
    return this.getReadiness();
  }

  /**
   * Removes a workspace root through the server's runtime-config mutation.
   * Existing features remain intact; discovery refreshes from the server.
   */
  async removeWorkspaceRoot(path: string): Promise<ReadinessSnapshot> {
    const validated = validateWithSchema(path, AbsolutePathSchema);
    const configBody = await this.api('/api/v1/config/runtime');
    const config = validateWithSchema(configBody, RuntimeConfigWorkspaceSchema);
    const roots = config.workspace_roots ?? [];
    const next = roots.filter((r) => r !== validated);
    if (next.length !== roots.length) {
      await this.api('/api/v1/config/runtime', {
        method: 'PATCH',
        body: { workspace_roots: next },
      });
    }
    return this.getReadiness();
  }

  /**
   * Reorders workspace roots by replacing the full array through the
   * server's runtime-config mutation. The set of roots must be identical;
   * only the order changes.
   */
  async reorderWorkspaceRoots(paths: string[]): Promise<ReadinessSnapshot> {
    const validated = paths.map((p) => validateWithSchema(p, AbsolutePathSchema));
    const configBody = await this.api('/api/v1/config/runtime');
    const config = validateWithSchema(configBody, RuntimeConfigWorkspaceSchema);
    const current = (config.workspace_roots ?? []).slice().sort();
    const sorted = validated.slice().sort();
    if (current.length !== sorted.length || current.some((r, i) => r !== sorted[i])) {
      throw new CanonicalErrorException(buildCanonicalError('E_INVALID_REORDER'));
    }
    await this.api('/api/v1/config/runtime', {
      method: 'PATCH',
      body: { workspace_roots: validated },
    });
    return this.getReadiness();
  }

  /**
   * Server-owned repository initialization. Consent is enforced by the IPC
   * request schema already; this recheck is defense in depth. On success the
   * fresh discovery snapshot is returned so the renderer never infers state.
   */
  async initRepository(request: InitRepositoryRequest): Promise<ReadinessSnapshot> {
    if (request.consent !== true) {
      throw new CanonicalErrorException(buildCanonicalError('E_CONSENT_REQUIRED'));
    }
    const path = validateWithSchema(request.path, AbsolutePathSchema);
    await this.api('/api/v1/workspace/repositories/init', {
      method: 'POST',
      body: { path, consent: true },
    });
    return this.getReadiness();
  }

  /** Repository choices always come from fresh server discovery. */
  async listRepositories(): Promise<RepositoryState[]> {
    const snapshot = await this.getReadiness();
    return snapshot.repositories;
  }

  // --- transport helpers -----------------------------------------------------

  private api(path: string, init?: ApiRequestInit): Promise<unknown> {
    return serverRequest(this.deps.transport, path, init);
  }
}

/** Maps the validated server readiness response to the renderer-facing shape.
 * Readiness issues are already canonical catalog-rendered errors on the wire,
 * so they pass through untouched — the strict IPC schema revalidates them. */
export function toReadinessSnapshot(server: ReadinessResponse): ReadinessSnapshot {
  return {
    ready: server.ready,
    ...(server.probed_at === undefined ? {} : { probedAt: server.probed_at }),
    providers: server.providers.map((provider) => ({
      name: provider.name,
      installed: provider.installed,
      ...(provider.version === undefined ? {} : { version: provider.version }),
      ready: provider.ready,
      ...(provider.issue === undefined ? {} : { issue: provider.issue }),
    })),
    models: {
      available: server.models.available,
      ...(server.models.models === undefined ? {} : { models: server.models.models }),
      ...(server.models.issue === undefined ? {} : { issue: server.models.issue }),
    },
    configuration: {
      valid: server.configuration.valid,
      ...(server.configuration.issue === undefined ? {} : { issue: server.configuration.issue }),
    },
    workspaceRoots: server.workspace.roots.map((root) => ({
      path: root.path,
      valid: root.valid,
      ...(root.issue === undefined ? {} : { issue: root.issue }),
    })),
    repositories: server.workspace.repositories.map((repository) => ({
      name: repository.name,
      path: repository.path,
      valid: repository.valid,
      ...(repository.issue === undefined ? {} : { issue: repository.issue }),
    })),
    issues: server.issues ?? [],
  };
}
