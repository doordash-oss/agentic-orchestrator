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
 * Main-process clone lifecycle service. Every operation goes through the
 * gateway's bearer transport and returns a fresh authoritative snapshot;
 * the renderer never infers state. Requests are fenced by server identity
 * and connection generation so a server switch discards late replies from
 * the previous server instead of applying them to the new one.
 */
import { randomUUID } from 'node:crypto';
import { buildCanonicalError, CanonicalErrorException } from '../shared/errors';
import {
  CloneActionResponseSchema,
  CloneOperationListResponseSchema,
  CloneOperationResponseSchema,
  validateWithSchema,
  type CloneActionResponse,
  type CloneOperationDTO,
  type CloneOperationListResponse,
} from '../shared/api/parse';
import {
  CloneStartRequestSchema,
  type CloneOperation,
  type CloneOperationsList,
  type CloneStartRequest,
} from '../shared/ipc';
import type { ApiRequestInit } from './gateway/runtimeGateway';
import { serverRequest, type ServerTransport } from './serverClient';

const CLONE_BASE = '/api/v1/workspace/repositories/clone';
const CLONE_LIST_LIMIT = 200;
const OPERATION_ID_PATTERN = /^[a-z0-9-]{1,64}$/;

/** Identity of the connected server, captured for request fencing. */
export interface ServerIdentity {
  serverKey: string | null;
  generation: number;
}

export type ServerIdentitySource = () => ServerIdentity;

export interface CloneServiceDeps {
  transport: ServerTransport;
  /** Captures the current server identity for request fencing. */
  identity?: ServerIdentitySource;
}

export class CloneService {
  constructor(private readonly deps: CloneServiceDeps) {}

  /** Generates the idempotency key binding one clone start request. */
  newIdempotencyKey(): string {
    return randomUUID();
  }

  /**
   * Starts a server-owned clone. The accepted (or retained) snapshot
   * returns promptly, independent of transfer duration; closing the
   * initiating view never cancels the work.
   */
  async startClone(request: CloneStartRequest): Promise<CloneOperation> {
    const validated = validateWithSchema(request, CloneStartRequestSchema);
    const body = await this.fencedCall(`${CLONE_BASE}`, {
      method: 'POST',
      body: {
        remote_url: validated.remoteUrl,
        root_path: validated.rootPath,
        destination: validated.destination,
        idempotency_key: validated.idempotencyKey,
      },
    });
    const parsed = validateWithSchema(body, CloneActionResponseSchema);
    return toCloneOperation(parsed.operation);
  }

  /** Reads one authoritative snapshot by operation ID. */
  async getCloneOperation(operationId: string): Promise<CloneOperation> {
    assertOperationId(operationId);
    const body = await this.fencedCall(`${CLONE_BASE}/${operationId}`);
    const parsed = validateWithSchema(body, CloneOperationResponseSchema);
    return toCloneOperation(parsed.operation);
  }

  /**
   * Lists the connected server's active and recent clones. The server owns
   * ordering, bounds and retention; a failed list never erases known work.
   */
  async listCloneOperations(): Promise<CloneOperationsList> {
    const body = await this.fencedCall(`${CLONE_BASE}?limit=${CLONE_LIST_LIMIT}`);
    const parsed = validateWithSchema(body, CloneOperationListResponseSchema);
    const list: CloneOperationsList = {
      operations: parsed.operations.map(toCloneOperation),
    };
    if (parsed.next_page_token !== undefined) {
      list.nextPageToken = parsed.next_page_token;
    }
    return list;
  }

  /** Requests explicit cancellation; returns the cancelling snapshot. */
  async cancelCloneOperation(operationId: string): Promise<CloneOperation> {
    return this.operationAction(operationId, 'cancel');
  }

  /** Retries cleanup for a cleanup-pending attempt. */
  async retryCloneCleanup(operationId: string): Promise<CloneOperation> {
    return this.operationAction(operationId, 'cleanup');
  }

  /** Starts a deliberate fresh retry of a terminal attempt. */
  async retryCloneOperation(operationId: string): Promise<CloneOperation> {
    return this.operationAction(operationId, 'retry');
  }

  private async operationAction(operationId: string, action: string): Promise<CloneOperation> {
    assertOperationId(operationId);
    const body = await this.fencedCall(`${CLONE_BASE}/${operationId}/${action}`, {
      method: 'POST',
      body: {},
    });
    const parsed = validateWithSchema(body, CloneActionResponseSchema);
    return toCloneOperation(parsed.operation);
  }

  private async api(path: string, init?: ApiRequestInit): Promise<unknown> {
    return serverRequest(this.deps.transport, path, init);
  }

  /**
   * Runs one transport call fenced by server identity and connection
   * generation: the identity is captured before the request and compared
   * after the response, so a switch A → B → A discards late replies from
   * the old connection rather than updating the new server's view.
   */
  private async fencedCall(path: string, init?: ApiRequestInit): Promise<unknown> {
    const before = this.captureIdentity();
    const body = await this.api(path, init);
    const after = this.captureIdentity();
    if (
      before.serverKey !== null &&
      after.serverKey !== null &&
      (before.serverKey !== after.serverKey || before.generation !== after.generation)
    ) {
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

function assertOperationId(operationId: string): void {
  if (!OPERATION_ID_PATTERN.test(operationId)) {
    // Never echo the rejected identifier back across the boundary.
    throw new CanonicalErrorException(buildCanonicalError('E_BAD_API_PATH'));
  }
}

/** Maps the snake_case server DTO onto the renderer-facing camelCase shape. */
export function toCloneOperation(dto: CloneOperationDTO): CloneOperation {
  const operation: CloneOperation = {
    id: dto.id,
    state: dto.state,
    remoteUrl: dto.remote_url,
    rootPath: dto.root_path,
    destination: dto.destination,
    destinationPath: dto.destination_path,
    idempotencyKey: dto.idempotency_key,
    cancelRequested: dto.cancel_requested,
    createdAt: dto.created_at,
    updatedAt: dto.updated_at,
  };
  if (dto.stage !== undefined) operation.stage = dto.stage;
  if (dto.progress !== undefined) operation.progress = dto.progress;
  if (dto.pending_outcome !== undefined) operation.pendingOutcome = dto.pending_outcome;
  if (dto.cancel_requested_at !== undefined) operation.cancelRequestedAt = dto.cancel_requested_at;
  if (dto.cleanup_issue !== undefined) operation.cleanupIssue = dto.cleanup_issue;
  if (dto.error !== undefined) operation.error = dto.error;
  if (dto.terminal_at !== undefined) operation.terminalAt = dto.terminal_at;
  if (dto.resolved_at !== undefined) operation.resolvedAt = dto.resolved_at;
  if (dto.published !== undefined) {
    operation.published = {
      repoKey: dto.published.repo_key,
      path: dto.published.path,
      hasHead: dto.published.has_head,
      publishedAt: dto.published.published_at,
    };
  }
  return operation;
}

export type { CloneActionResponse, CloneOperationListResponse };
