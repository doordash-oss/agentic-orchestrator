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
import { SafeErrorException, safeError } from '../shared/errors';
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

/** Client-side consent pre-flight hint; server rejections carry catalog hints. */
const CONSENT_HINT = 'Confirm the initialization consent in the dialog, then try again.';

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
      throw new SafeErrorException(
        safeError(
          'E_INVALID_PATH',
          'The selected folder could not be used.',
          'Choose a regular folder with an absolute path.',
        ),
      );
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
      throw new SafeErrorException(
        safeError(
          'E_INVALID_REORDER',
          'The reordered root set must match the current set of workspace roots.',
          'Add or remove roots separately before reordering.',
        ),
      );
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
      throw new SafeErrorException(
        safeError(
          'consent_required',
          'Repository initialization requires explicit consent.',
          CONSENT_HINT,
        ),
      );
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

/** Maps the validated server readiness response to the renderer-facing shape. */
export function toReadinessSnapshot(server: ReadinessResponse): ReadinessSnapshot {
  return {
    ready: server.ready,
    ...(server.probed_at === undefined ? {} : { probedAt: server.probed_at }),
    providers: server.providers.map((provider) => ({
      name: provider.name,
      installed: provider.installed,
      ...(provider.version === undefined ? {} : { version: provider.version }),
      ready: provider.ready,
      ...(provider.issue === undefined ? {} : { issue: toIssue(provider.issue) }),
    })),
    models: {
      available: server.models.available,
      ...(server.models.models === undefined ? {} : { models: server.models.models }),
      ...(server.models.issue === undefined ? {} : { issue: toIssue(server.models.issue) }),
    },
    configuration: {
      valid: server.configuration.valid,
      ...(server.configuration.issue === undefined
        ? {}
        : { issue: toIssue(server.configuration.issue) }),
    },
    workspaceRoots: server.workspace.roots.map((root) => ({
      path: root.path,
      valid: root.valid,
      ...(root.issue === undefined ? {} : { issue: toIssue(root.issue) }),
    })),
    repositories: server.workspace.repositories.map((repository) => ({
      name: repository.name,
      path: repository.path,
      valid: repository.valid,
      ...(repository.issue === undefined ? {} : { issue: toIssue(repository.issue) }),
    })),
    issues: (server.issues ?? []).map(toIssue),
  };
}

function toIssue(issue: NonNullable<ReadinessResponse['issues']>[number]): {
  code: (typeof issue)['code'];
  message: string;
  remedy?: string;
} {
  return {
    code: issue.code,
    message: issue.message,
    ...(issue.remedy === undefined ? {} : { remedy: issue.remedy }),
  };
}
