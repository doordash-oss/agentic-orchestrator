/**
 * Main-process completion operations. Proxies the server's completion
 * preflight and repository diff endpoints through the bearer transport,
 * and implements validated open-external and reveal-path native operations.
 * Nothing here caches server-domain data, exposes raw host paths, or lets
 * the renderer compose REST paths.
 */
import { realpath, stat } from 'node:fs/promises';
import path from 'node:path';
import { shell } from 'electron';
import { redactText } from '../shared/errors';
import {
  validateWithSchema,
  CompletionPreflightResponseSchema,
  RepositoryDiffResponseSchema,
  RepositoryPathResponseSchema,
} from '../shared/api/parse';
import { serverRequest, type ServerTransport } from './serverClient';
import { openExternalSafely } from './security';
import {
  CompletionPreflightRequestSchema,
  RepositoryDiffRequestSchema,
  FeatureIdSchema,
  OpenExternalRequestSchema,
  RevealPathRequestSchema,
  type CompletionPreflightRequest,
  type CompletionPreflightResult,
  type RepositoryDiffRequest,
  type RepositoryDiffResult,
  type OpenExternalRequest,
  type RevealPathRequest,
} from '../shared/ipc';

const COMPLETION_REMEDIES: Readonly<Record<string, string>> = {
  not_found: 'The feature no longer exists on the server. Close its tab.',
  bad_request: 'The server rejected the request. Refresh and retry.',
  conflict: 'The server state has changed. Refresh the completion preview.',
};

export interface CompletionServiceDeps {
  transport: ServerTransport;
}

export class CompletionService {
  constructor(private readonly deps: CompletionServiceDeps) {}

  async preflightCompletion(
    request: CompletionPreflightRequest,
  ): Promise<CompletionPreflightResult> {
    const input = validateWithSchema(request, CompletionPreflightRequestSchema);
    const body = await serverRequest(
      this.deps.transport,
      `/api/v1/features/${input.featureId}/completion/preflight`,
      undefined,
      { remedyByCode: COMPLETION_REMEDIES },
    );
    const response = validateWithSchema(body, CompletionPreflightResponseSchema);
    return {
      featureId: validateWithSchema(response.feature_id, FeatureIdSchema),
      sourceRevision: response.source_revision,
      canMarkDone: response.can_mark_done ?? false,
      ...(response.mark_done_blocker ? { markDoneBlocker: response.mark_done_blocker } : {}),
      repos: (response.repos ?? []).map((repo) => ({
        repo: repo.repo,
        publishable: repo.publishable,
        touched: repo.touched,
        status: repo.status,
        ...(repo.pr_url ? { prUrl: repo.pr_url } : {}),
        ...(repo.blocker ? { blocker: repo.blocker } : {}),
        ...(repo.freshness ? { freshness: repo.freshness } : {}),
        ...(repo.last_error ? { lastError: repo.last_error } : {}),
        ...(repo.base_branch ? { baseBranch: repo.base_branch } : {}),
        ...(repo.branch ? { branch: repo.branch } : {}),
        ...(repo.pending_commits === undefined ? {} : { pendingCommits: repo.pending_commits }),
        ...(repo.pending_dirty === undefined ? {} : { pendingDirty: repo.pending_dirty }),
        ...(repo.push_mode === 'fast_forward' || repo.push_mode === 'rewrite'
          ? { pushMode: repo.push_mode }
          : {}),
        ...(repo.pending_dirty_files === undefined
          ? {}
          : { pendingDirtyFiles: repo.pending_dirty_files }),
        ...(repo.pending_dirty_file_total === undefined
          ? {}
          : { pendingDirtyFileTotal: repo.pending_dirty_file_total }),
      })),
    };
  }

  async getRepositoryDiff(request: RepositoryDiffRequest): Promise<RepositoryDiffResult> {
    const input = validateWithSchema(request, RepositoryDiffRequestSchema);
    const query = input.filePath ? `?file_path=${encodeURIComponent(input.filePath)}` : '';
    const body = await serverRequest(
      this.deps.transport,
      `/api/v1/features/${input.featureId}/repositories/${encodeURIComponent(input.repo)}/diff${query}`,
      undefined,
      { remedyByCode: COMPLETION_REMEDIES },
    );
    const response = validateWithSchema(body, RepositoryDiffResponseSchema);
    return {
      featureId: validateWithSchema(response.feature_id, FeatureIdSchema),
      repo: response.repo,
      ...(response.source_revision ? { sourceRevision: response.source_revision } : {}),
      ...(response.truncated ? { truncated: response.truncated } : {}),
      files: (response.files ?? []).map((file) => ({
        path: file.path,
        operation: file.operation,
        ...(file.old_path ? { oldPath: file.old_path } : {}),
        ...(file.added_lines !== undefined ? { addedLines: file.added_lines } : {}),
        ...(file.removed_lines !== undefined ? { removedLines: file.removed_lines } : {}),
        ...(file.binary ? { binary: file.binary } : {}),
        ...(file.fingerprint ? { fingerprint: file.fingerprint } : {}),
      })),
      ...(response.file_diff ? { fileDiff: redactText(response.file_diff) } : {}),
      ...(response.file_truncated ? { fileTruncated: response.file_truncated } : {}),
      ...(response.file_binary ? { fileBinary: response.file_binary } : {}),
      ...(response.file_unavailable ? { fileUnavailable: response.file_unavailable } : {}),
      ...(response.partial_failure ? { partialFailure: response.partial_failure } : {}),
    };
  }

  async openExternal(request: OpenExternalRequest): Promise<{ ok: boolean }> {
    const input = validateWithSchema(request, OpenExternalRequestSchema);
    const opened = await openExternalSafely(input.url, (url) => shell.openExternal(url));
    return { ok: opened };
  }

  async revealPath(request: RevealPathRequest): Promise<{ ok: boolean }> {
    const { featureId, repo } = validateWithSchema(request, RevealPathRequestSchema);
    const worktreePath = await this.resolveWorktreePath(featureId, repo);
    if (!worktreePath) {
      return { ok: false };
    }
    const result = await shell.openPath(worktreePath);
    return { ok: result === '' };
  }

  private async resolveWorktreePath(featureId: string, repo: string): Promise<string | null> {
    const id = validateWithSchema(featureId, FeatureIdSchema);
    const body = await serverRequest(
      this.deps.transport,
      `/api/v1/features/${id}/repositories/${encodeURIComponent(repo)}/path`,
      undefined,
      { remedyByCode: COMPLETION_REMEDIES },
    );
    const response = validateWithSchema(body, RepositoryPathResponseSchema);
    if (response.feature_id !== id || response.repo !== repo) {
      return null;
    }
    if (!path.isAbsolute(response.path) || response.path.includes('\0')) {
      return null;
    }
    try {
      const real = await realpath(response.path);
      if (!path.isAbsolute(real) || real.includes('\0')) {
        return null;
      }
      const info = await stat(real);
      if (!info.isDirectory()) {
        return null;
      }
      return real;
    } catch {
      return null;
    }
  }
}
