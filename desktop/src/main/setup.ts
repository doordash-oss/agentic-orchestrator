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
import { assertLocalConnection, alwaysLocal, type LocalitySource } from './locality';
import {
  assertSameServer,
  captureServerIdentity,
  type ServerIdentity,
  type ServerIdentitySource,
} from './serverFence';

/** The authenticated transport surface the gateway provides. */
export type SetupTransport = ServerTransport;

export interface SetupDialogs {
  /** Native directory picker; resolves null when the user cancels. */
  pickDirectory(): Promise<string | null>;
}

export interface SetupServiceDeps {
  transport: SetupTransport;
  dialogs: SetupDialogs;
  locality?: LocalitySource;
  /** Captures the current server identity for root-mutation fencing. */
  identity?: ServerIdentitySource;
}

export class SetupService {
  private readonly locality: LocalitySource;
  constructor(private readonly deps: SetupServiceDeps) {
    this.locality = deps.locality ?? alwaysLocal;
  }

  async getReadiness(): Promise<ReadinessSnapshot> {
    const body = await this.api('/api/v1/readiness');
    return toReadinessSnapshot(validateWithSchema(body, ReadinessResponseSchema));
  }

  async refreshReadiness(): Promise<ReadinessSnapshot> {
    const body = await this.api('/api/v1/readiness/refresh', { method: 'POST', body: {} });
    return toReadinessSnapshot(validateWithSchema(body, ReadinessResponseSchema));
  }

  async pickWorkspaceDirectory(): Promise<PickedDirectory> {
    assertLocalConnection(this.locality);
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
   * returns the fresh authoritative readiness snapshot. The whole
   * read-modify-write sequence is local-only (a remote server's roots are
   * administrator-owned) and fenced by server identity and connection
   * generation: an already dispatched mutation can affect only its
   * original server, and a switch mid-sequence aborts before the write.
   * A refresh failure right after a successful save is reconciled once
   * against the same server before giving up.
   */
  async addWorkspaceRoot(path: string): Promise<ReadinessSnapshot> {
    assertLocalConnection(this.locality);
    const validated = validateWithSchema(path, AbsolutePathSchema);
    const before = this.captureIdentity();
    const configBody = await this.api('/api/v1/config/runtime');
    this.assertSameServer(before);
    const config = validateWithSchema(configBody, RuntimeConfigWorkspaceSchema);
    const roots = config.workspace_roots ?? [];
    if (!roots.includes(validated)) {
      await this.api('/api/v1/config/runtime', {
        method: 'PATCH',
        body: { workspace_roots: [...roots, validated] },
      });
      this.assertSameServer(before);
    }
    return this.refreshAfterRootMutation(before);
  }

  /**
   * Removes a workspace root through the server's runtime-config mutation.
   * Existing features remain intact; discovery refreshes from the server.
   */
  async removeWorkspaceRoot(path: string): Promise<ReadinessSnapshot> {
    assertLocalConnection(this.locality);
    const validated = validateWithSchema(path, AbsolutePathSchema);
    const before = this.captureIdentity();
    const configBody = await this.api('/api/v1/config/runtime');
    this.assertSameServer(before);
    const config = validateWithSchema(configBody, RuntimeConfigWorkspaceSchema);
    const roots = config.workspace_roots ?? [];
    const next = roots.filter((r) => r !== validated);
    if (next.length !== roots.length) {
      await this.api('/api/v1/config/runtime', {
        method: 'PATCH',
        body: { workspace_roots: next },
      });
      this.assertSameServer(before);
    }
    return this.refreshAfterRootMutation(before);
  }

  /**
   * Reorders workspace roots by replacing the full array through the
   * server's runtime-config mutation. The set of roots must be identical;
   * only the order changes.
   */
  async reorderWorkspaceRoots(paths: string[]): Promise<ReadinessSnapshot> {
    assertLocalConnection(this.locality);
    const validated = paths.map((p) => validateWithSchema(p, AbsolutePathSchema));
    const before = this.captureIdentity();
    const configBody = await this.api('/api/v1/config/runtime');
    this.assertSameServer(before);
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
    this.assertSameServer(before);
    return this.refreshAfterRootMutation(before);
  }

  /**
   * Refreshes the authoritative readiness after a root mutation, fenced to
   * the server the mutation targeted. A failure right after a successful
   * save is retried once against that same server before surfacing: the
   * renderer must never conclude the save itself failed from a refresh
   * failure.
   */
  private async refreshAfterRootMutation(before: ServerIdentity): Promise<ReadinessSnapshot> {
    try {
      const snapshot = await this.getReadiness();
      this.assertSameServer(before);
      return snapshot;
    } catch (err) {
      if (err instanceof CanonicalErrorException && err.canonical.code === 'E_SERVER_SWITCHED') {
        throw err;
      }
      // One reconcile attempt against the same server.
      const snapshot = await this.getReadiness();
      this.assertSameServer(before);
      return snapshot;
    }
  }

  /**
   * Server-owned repository initialization. Consent is enforced by the IPC
   * request schema already; this recheck is defense in depth. On success the
   * fresh discovery snapshot is returned so the renderer never infers state.
   */
  async initRepository(request: InitRepositoryRequest): Promise<ReadinessSnapshot> {
    assertLocalConnection(this.locality);
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

  private captureIdentity(): ServerIdentity {
    return captureServerIdentity(this.deps.identity);
  }

  /**
   * Aborts a root-mutation sequence whose server changed mid-flight: an
   * already dispatched mutation can affect only its original server, and
   * stale results must never authorize or select a root on the new one.
   */
  private assertSameServer(before: ServerIdentity): void {
    assertSameServer(before, this.deps.identity);
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
      cloneEligible: root.clone_eligible,
      ...(root.clone_issue === undefined ? {} : { cloneIssue: root.clone_issue }),
    })),
    repositories: server.workspace.repositories.map((repository) => ({
      name: repository.name,
      path: repository.path,
      valid: repository.valid,
      featureReady: repository.feature_ready,
      ...(repository.issue === undefined ? {} : { issue: repository.issue }),
      ...(repository.identity === undefined
        ? {}
        : {
            identity: {
              path: repository.identity.path,
              commonDir: repository.identity.common_dir,
              device: repository.identity.device,
              inode: repository.identity.inode,
            },
          }),
    })),
    issues: server.issues ?? [],
  };
}
