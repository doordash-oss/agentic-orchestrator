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
 * Main-process completion operations. Proxies the server's completion
 * preflight and repository diff endpoints through the bearer transport,
 * and implements validated open-external and reveal-path native operations.
 * Nothing here caches server-domain data, exposes raw host paths, or lets
 * the renderer compose REST paths.
 */
import { realpath, stat } from 'node:fs/promises';
import path from 'node:path';
import { clipboard, shell } from 'electron';
import { z } from 'zod';
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
import { alwaysLocal, type LocalitySource } from './locality';

export interface RevealPathOutcome {
  ok: boolean;
  /** Server-reported absolute path; present when it can be copied (remote). */
  path?: string;
}

export interface CompletionServiceDeps {
  transport: ServerTransport;
  /**
   * Gateway-owned locality of the active connection. While remote, the
   * reveal service skips local realpath/stat validation (the path is not on
   * this filesystem) and returns the server-reported path for copy use.
   */
  locality?: LocalitySource;
}

export class CompletionService {
  private readonly locality: LocalitySource;

  constructor(private readonly deps: CompletionServiceDeps) {
    this.locality = deps.locality ?? alwaysLocal;
  }

  async preflightCompletion(
    request: CompletionPreflightRequest,
  ): Promise<CompletionPreflightResult> {
    const input = validateWithSchema(request, CompletionPreflightRequestSchema);
    const body = await serverRequest(
      this.deps.transport,
      `/api/v1/features/${input.featureId}/completion/preflight`,
      undefined,
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
        // The canonical object crosses IPC intact except diagnostics, which
        // pass through the same redaction as every other raw text the
        // renderer receives.
        ...(repo.error === undefined
          ? {}
          : {
              error: {
                ...repo.error,
                diagnostics:
                  repo.error.diagnostics === undefined
                    ? undefined
                    : redactText(repo.error.diagnostics),
              },
            }),
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

  /**
   * The renderer never holds a Chromium clipboard permission (security.ts's
   * deny-all policy), so the remote Copy Path affordance writes through the
   * main-process clipboard. Bounded text only; what the renderer cannot
   * write is anything but the string itself.
   */
  writeClipboardText(text: unknown): Promise<{ ok: boolean }> {
    const value = validateWithSchema(text, z.string().min(1).max(4096));
    clipboard.writeText(value);
    return Promise.resolve({ ok: true });
  }

  async revealPath(request: RevealPathRequest): Promise<RevealPathOutcome> {
    const { featureId, repo } = validateWithSchema(request, RevealPathRequestSchema);
    const worktreePath = await this.resolveWorktreePath(featureId, repo);
    if (!worktreePath) {
      return { ok: false };
    }
    if (this.locality() === 'remote') {
      // The path lives on the server's host: never open or validate it
      // locally — hand it back so the renderer can offer Copy Path.
      return { ok: true, path: worktreePath };
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
    );
    const response = validateWithSchema(body, RepositoryPathResponseSchema);
    if (response.feature_id !== id || response.repo !== repo) {
      return null;
    }
    if (!path.isAbsolute(response.path) || response.path.includes('\0')) {
      return null;
    }
    if (this.locality() === 'remote') {
      // Remote servers report paths on their own host; local realpath/stat
      // validation would falsely reject (or worse, resolve a same-named
      // local directory). Trust the server identity check above only.
      return response.path;
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
