/**
 * Main-process first-launch setup service. Every operation talks to the
 * authoritative server through the runtime gateway's bearer transport and
 * returns the renderer-facing readiness snapshot; nothing here caches
 * server-domain data, stores tokens, or trusts renderer-provided paths
 * beyond the validated absolute-path schema.
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

/** Concrete, safe next steps per structured server error code. */
const REMEDY_BY_CODE: Record<string, string> = {
  consent_required: 'Confirm the initialization consent in the dialog, then try again.',
  invalid_repository_path: 'Choose an absolute folder located inside a configured workspace root.',
  path_outside_workspace_root:
    'Choose a folder inside one of the configured workspace roots, or add its parent as a root first.',
  already_repository:
    'This folder is already a git repository — select it directly instead of initializing.',
  directory_not_empty:
    'Choose an empty folder, a new folder name, or an existing git repository instead.',
  not_ready: 'Complete the outstanding setup steps shown in the wizard, then retry.',
};

export class SetupService {
  constructor(private readonly deps: SetupServiceDeps) {}

  async getReadiness(): Promise<ReadinessSnapshot> {
    const body = await this.api('/api/v1/readiness');
    return toSnapshot(validateWithSchema(body, ReadinessResponseSchema));
  }

  async refreshReadiness(): Promise<ReadinessSnapshot> {
    const body = await this.api('/api/v1/readiness/refresh', { method: 'POST', body: {} });
    return toSnapshot(validateWithSchema(body, ReadinessResponseSchema));
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
          REMEDY_BY_CODE['consent_required'] ?? '',
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
    return serverRequest(this.deps.transport, path, init, { remedyByCode: REMEDY_BY_CODE });
  }
}

/** Maps the validated server readiness response to the renderer-facing shape. */
function toSnapshot(server: ReadinessResponse): ReadinessSnapshot {
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
