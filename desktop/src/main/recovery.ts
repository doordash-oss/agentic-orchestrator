/**
 * Main-process recovery service: scans for orphan session processes and
 * executes batched Resume/Kill/Skip actions through the authoritative server.
 * Everything routes through the shared server client so structured errors,
 * redaction, and fail-closed validation stay identical across services.
 */
import {
  RecoverySnapshotResponseSchema,
  RecoveryActionResponseSchema,
  TextContentResponseSchema,
  validateWithSchema,
} from '../shared/api/parse';
import {
  FeatureIdSchema,
  RecoveryExecuteRequestSchema,
  RecoveryLogReadRequestSchema,
  type RecoveryExecuteRequest,
  type RecoveryExecuteResult,
  type RecoveryItemView,
  type RecoveryLogReadRequest,
  type RecoveryLogReadResult,
  type RecoverySnapshot,
} from '../shared/ipc';
import type { ApiRequestInit } from './gateway/runtimeGateway';
import { serverRequest, type ServerTransport } from './serverClient';

export type RecoveryTransport = ServerTransport;

const REMEDY_BY_CODE: Record<string, string> = {
  not_found: 'The recovery snapshot is stale. Refresh the scan and try again.',
  bad_request: 'The recovery action was rejected. Refresh and retry.',
  conflict: 'The recovery item state changed. Refresh the scan and try again.',
};

export class RecoveryService {
  constructor(private readonly transport: RecoveryTransport) {}

  async scan(): Promise<RecoverySnapshot> {
    const body = await this.api('/api/v1/recovery');
    const response = validateWithSchema(body, RecoverySnapshotResponseSchema);
    const items: RecoveryItemView[] = (response.items ?? []).map((item) => ({
      key: item.key,
      featureId: validateWithSchema(item.feature_id, FeatureIdSchema),
      ...(item.feature_name === undefined || item.feature_name === ''
        ? {}
        : { featureName: item.feature_name }),
      ...(item.repo_name === undefined || item.repo_name === ''
        ? {}
        : { repoName: item.repo_name }),
      ...(item.phase === undefined || item.phase === '' ? {} : { phase: item.phase }),
      ...(item.iteration === undefined ? {} : { iteration: item.iteration }),
      ...(item.pid === undefined ? {} : { pid: item.pid }),
      processAlive: item.process_alive,
      ...(item.log_available === undefined ? {} : { logAvailable: item.log_available }),
      allowedActions: item.allowed_actions ?? [],
      defaultAction: item.default_action,
    }));
    return {
      snapshotId: response.snapshot_id,
      items,
    };
  }

  async execute(request: RecoveryExecuteRequest): Promise<RecoveryExecuteResult> {
    const input = validateWithSchema(request, RecoveryExecuteRequestSchema);
    const body = await this.api('/api/v1/recovery/actions', {
      method: 'POST',
      body: {
        snapshot_id: input.snapshotId,
        actions: input.actions,
      },
    });
    const response = validateWithSchema(body, RecoveryActionResponseSchema);
    return { result: response.result };
  }

  async readLog(request: RecoveryLogReadRequest): Promise<RecoveryLogReadResult> {
    const input = validateWithSchema(request, RecoveryLogReadRequestSchema);
    const params = new URLSearchParams({
      snapshot_id: input.snapshotId,
      key: input.key,
    });
    if (input.offset !== undefined) params.set('offset', String(input.offset));
    if (input.limit !== undefined) params.set('limit', String(input.limit));
    const body = await this.api(`/api/v1/recovery/logs?${params.toString()}`);
    const response = validateWithSchema(body, TextContentResponseSchema);
    return {
      id: response.id,
      offset: response.offset,
      limit: response.limit,
      size: response.size,
      text: response.text,
      truncated: response.truncated,
    };
  }

  private api(path: string, init?: ApiRequestInit): Promise<unknown> {
    return serverRequest(this.transport, path, init, {
      remedyByCode: REMEDY_BY_CODE,
    });
  }
}
