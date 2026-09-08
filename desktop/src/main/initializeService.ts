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
 * Main-process explicit-initialization service. Initialization is a
 * synchronous server-owned operation on an existing repository from the
 * server's current catalog: the server resolves the selector and
 * revalidates the expected identity itself, so the renderer's key, path
 * and identity fields are comparisons, never filesystem authority.
 * Requests are fenced by server identity and connection generation so a
 * server switch discards late replies from the previous server instead of
 * applying them to the new one.
 */
import { buildCanonicalError, CanonicalErrorException } from '../shared/errors';
import {
  InitializeRepositoryResponseSchema,
  validateWithSchema,
  type InitializeRepositoryResponse,
} from '../shared/api/parse';
import {
  InitializeRepositoryRequestSchema,
  type InitializeRepositoryRequest,
  type InitializeRepositoryResult,
} from '../shared/ipc';
import type { ApiRequestInit } from './gateway/runtimeGateway';
import { serverRequest, type ServerTransport } from './serverClient';
import type { ServerIdentity, ServerIdentitySource } from './cloneService';

const INITIALIZE_PATH = '/api/v1/workspace/repositories/initialize';

export interface InitializeServiceDeps {
  transport: ServerTransport;
  /** Captures the current server identity for request fencing. */
  identity?: ServerIdentitySource;
}

export class InitializeService {
  constructor(private readonly deps: InitializeServiceDeps) {}

  /**
   * Creates one empty local initial commit in an existing unborn clone.
   * Consent is enforced by the IPC request schema already; this recheck is
   * defense in depth. The server owns eligibility (unborn, no user
   * content, no active Git operation), the preserved branch and origin,
   * and the exactly-once commit; this service only transports the request
   * and maps the authoritative result. An `already_initialized` result is
   * a refresh-only success, never an error.
   */
  async initializeRepository(
    request: InitializeRepositoryRequest,
  ): Promise<InitializeRepositoryResult> {
    const validated = validateWithSchema(request, InitializeRepositoryRequestSchema);
    if (validated.consent !== true) {
      throw new CanonicalErrorException(buildCanonicalError('E_CONSENT_REQUIRED'));
    }
    const body = await this.fencedCall(INITIALIZE_PATH, {
      method: 'POST',
      body: {
        repo_key: validated.repoKey,
        identity: {
          path: validated.identity.path,
          common_dir: validated.identity.commonDir,
          device: validated.identity.device,
          inode: validated.identity.inode,
        },
        ...(validated.path !== undefined ? { path: validated.path } : {}),
        consent: true,
      },
    });
    const parsed = validateWithSchema(body, InitializeRepositoryResponseSchema);
    if (!parsed.repository.has_head) {
      // The server contract promises has_head on both result values; a
      // response without it is a broken contract, not a success to infer.
      throw new CanonicalErrorException(buildCanonicalError('E_BAD_API_RESPONSE'));
    }
    return toInitializeRepositoryResult(parsed);
  }

  private api(path: string, init?: ApiRequestInit): Promise<unknown> {
    return serverRequest(this.deps.transport, path, init);
  }

  /**
   * Runs one transport call fenced by server identity and connection
   * generation: the identity is captured before the request and compared
   * after the response, so a switch A → B → A discards late replies from
   * the old connection rather than applying them to the new server.
   */
  private async fencedCall(path: string, init?: ApiRequestInit): Promise<unknown> {
    const before = this.captureIdentity();
    const body = await this.api(path, init);
    const after = this.captureIdentity();
    if (before.serverKey !== after.serverKey || before.generation !== after.generation) {
      throw new CanonicalErrorException(buildCanonicalError('E_SERVER_SWITCHED'));
    }
    return body;
  }

  private captureIdentity(): ServerIdentity {
    if (this.deps.identity === undefined) {
      return { serverKey: null, generation: 0 };
    }
    return this.deps.identity();
  }
}

/** Maps the snake_case server DTO onto the renderer-facing camelCase shape. */
export function toInitializeRepositoryResult(
  dto: InitializeRepositoryResponse,
): InitializeRepositoryResult {
  const result: InitializeRepositoryResult = {
    result: dto.result,
    repoKey: dto.repository.repo_key,
    path: dto.repository.path,
    hasHead: dto.repository.has_head,
    root: dto.repository.root,
  };
  if (dto.repository.identity !== undefined) {
    result.identity = {
      path: dto.repository.identity.path,
      commonDir: dto.repository.identity.common_dir,
      device: dto.repository.identity.device,
      inode: dto.repository.identity.inode,
    };
  }
  return result;
}
