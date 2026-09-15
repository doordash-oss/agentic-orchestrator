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
 * Main-process repository-creation service. Creation is a synchronous
 * server-owned operation through the gateway's bearer transport: the
 * response is the authoritative result (actual repository key and
 * server-resolved identity), never inferred client-side. Requests are
 * fenced by server identity and connection generation so a server switch
 * discards late replies from the previous server instead of applying them
 * to the new one.
 */
import { buildCanonicalError, CanonicalErrorException } from '../shared/errors';
import {
  CreateRepositoryResponseSchema,
  validateWithSchema,
  type CreateRepositoryResponse,
} from '../shared/api/parse';
import {
  CreateRepositoryRequestSchema,
  type CreateRepositoryRequest,
  type CreateRepositoryResult,
} from '../shared/ipc';
import type { ApiRequestInit } from './gateway/runtimeGateway';
import { serverRequest, type ServerTransport } from './serverClient';
import { fencedServerRequest, type ServerIdentitySource } from './serverFence';

const CREATE_PATH = '/api/v1/workspace/repositories/create';

export interface CreateServiceDeps {
  transport: ServerTransport;
  /** Captures the current server identity for request fencing. */
  identity?: ServerIdentitySource;
}

export class CreateService {
  constructor(private readonly deps: CreateServiceDeps) {}

  /**
   * Creates a repository as a new child of a configured workspace root.
   * Consent is enforced by the IPC request schema already; this recheck is
   * defense in depth. The operation is server-validated end to end (root
   * eligibility, child name, reservation, atomic publication); this
   * service only transports the request and maps the authoritative result.
   */
  async createRepository(request: CreateRepositoryRequest): Promise<CreateRepositoryResult> {
    const validated = validateWithSchema(request, CreateRepositoryRequestSchema);
    if (validated.consent !== true) {
      throw new CanonicalErrorException(buildCanonicalError('E_CONSENT_REQUIRED'));
    }
    const body = await this.fencedCall(CREATE_PATH, {
      method: 'POST',
      body: {
        root_path: validated.rootPath,
        destination: validated.destination,
        idempotency_key: validated.idempotencyKey,
        consent: true,
      },
    });
    const parsed = validateWithSchema(body, CreateRepositoryResponseSchema);
    if (parsed.result !== 'created') {
      throw new CanonicalErrorException(buildCanonicalError('E_BAD_API_RESPONSE'));
    }
    return toCreateRepositoryResult(parsed);
  }

  private api(path: string, init?: ApiRequestInit): Promise<unknown> {
    return serverRequest(this.deps.transport, path, init);
  }

  /**
   * Runs one transport call fenced by the shared server-identity policy, so
   * a stale reply from a previous connection is discarded instead of applied
   * to the new server.
   */
  private fencedCall(path: string, init?: ApiRequestInit): Promise<unknown> {
    return fencedServerRequest(this.deps.transport, this.deps.identity, path, init);
  }
}

/** Maps the snake_case server DTO onto the renderer-facing camelCase shape. */
export function toCreateRepositoryResult(dto: CreateRepositoryResponse): CreateRepositoryResult {
  const result: CreateRepositoryResult = {
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
      birthTime: dto.repository.identity.birth_time,
    };
  }
  return result;
}

export type { CreateRepositoryResponse };
