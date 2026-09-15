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
 * Bounded creation-file selection, repository indexing, and path resolution.
 * Renderer input is always rechecked against fresh repository discovery.
 */
import { lstat, opendir, realpath } from 'node:fs/promises';
import path from 'node:path';
import { buildCanonicalError, CanonicalErrorException } from '../shared/errors';
import {
  AbsolutePathSchema,
  CREATION_ATTACHMENT_LIMIT,
  CREATION_FILE_SEARCH_RESULT_LIMIT,
  CREATION_IMAGE_LIMIT,
  sameRepositoryIdentity,
  type CreationFileKind,
  type CreationFileSearchRequest,
  type CreationFileSearchResult,
  type ReadinessSnapshot,
  type RepositoryFileRef,
  type RepositoryIdentity,
} from '../shared/ipc';
import { validateWithSchema } from '../shared/api/parse';
import { alwaysLocal, assertLocalConnection, type LocalitySource } from './locality';

export interface CreationFilesServiceDeps {
  pickFiles(kind: CreationFileKind): Promise<string[]>;
  readReadiness(): Promise<ReadinessSnapshot>;
  /** Gateway-owned locality; remotely connected the local walks refuse. */
  locality?: LocalitySource;
}

export class CreationFilesService {
  private readonly searches = new Map<string, AbortController>();
  private readonly locality: LocalitySource;

  constructor(private readonly deps: CreationFilesServiceDeps) {
    this.locality = deps.locality ?? alwaysLocal;
  }

  async pickFiles(kind: CreationFileKind): Promise<{ paths: string[] }> {
    // The native dialog runs under every connection kind: locally the paths
    // are submitted as-is; remotely the renderer stages them through the
    // upload channel. Only the repository walks below stay local-only.
    const limit = kind === 'image' ? CREATION_IMAGE_LIMIT : CREATION_ATTACHMENT_LIMIT;
    const picked = await this.deps.pickFiles(kind);
    return {
      paths: picked
        .slice(0, limit)
        .map((filePath) => validateWithSchema(filePath, AbsolutePathSchema)),
    };
  }

  async search(request: CreationFileSearchRequest): Promise<CreationFileSearchResult> {
    // No local repository walk against a remote server.
    assertLocalConnection(this.locality);
    const controller = new AbortController();
    this.searches.set(request.requestId, controller);
    try {
      const snapshot = await this.deps.readReadiness();
      // Each requested repository is bound to its expected identity, so the
      // search walks the repository the request was issued against — a
      // replacement that took over the key, or a repository on another
      // server, can never contribute files. Feature-inherited entries
      // without an identity fall back to the current key.
      const eligible = snapshot.repositories.filter((repo) => {
        if (!repo.valid) return false;
        return request.repositories.some((expected) =>
          expected.identity !== undefined && repo.identity !== undefined
            ? sameRepositoryIdentity(expected.identity, repo.identity)
            : expected.key === repo.name,
        );
      });
      const files: Array<RepositoryFileRef & { score: number }> = [];
      let scanned = 0;
      let truncated = false;
      for (const repo of eligible) {
        for await (const relative of walkEligibleFiles(repo.path, controller.signal)) {
          scanned += 1;
          if (scanned > 10_000) {
            truncated = true;
            break;
          }
          const score = fuzzyScore(relative, request.query);
          if (score >= 0)
            files.push({
              repoKey: repo.name,
              path: relative,
              ...(repo.identity === undefined ? {} : { identity: repo.identity }),
              score,
            });
        }
        if (truncated || controller.signal.aborted) break;
      }
      files.sort((a, b) => b.score - a.score || a.path.localeCompare(b.path));
      return {
        requestId: request.requestId,
        files: files
          .slice(0, CREATION_FILE_SEARCH_RESULT_LIMIT)
          .map(({ repoKey, path, identity }) => ({
            repoKey,
            path,
            ...(identity === undefined ? {} : { identity }),
          })),
        truncated: truncated || files.length > CREATION_FILE_SEARCH_RESULT_LIMIT,
        cancelled: controller.signal.aborted,
      };
    } finally {
      this.searches.delete(request.requestId);
    }
  }

  cancelSearch(requestId: string): boolean {
    const search = this.searches.get(requestId);
    search?.abort();
    return search !== undefined;
  }

  async resolve(refs: readonly RepositoryFileRef[]): Promise<string[]> {
    if (refs.length === 0) return [];
    // Refs resolve to local absolute paths; remote submission never sees any.
    assertLocalConnection(this.locality);
    const snapshot = await this.deps.readReadiness();
    const resolved: string[] = [];
    for (const ref of refs) {
      let root: string | undefined;
      if (ref.identity !== undefined) {
        // The reference must still point at the repository it was captured
        // from. A replacement at the same key or path cannot satisfy it, and
        // a missing repository surfaces as unresolved instead of silently
        // dropping or redirecting the attachment.
        const match = snapshot.repositories.find(
          (repo) =>
            repo.valid &&
            repo.identity !== undefined &&
            sameRepositoryIdentity(ref.identity as RepositoryIdentity, repo.identity),
        );
        if (match === undefined) {
          throw repositoryFileError('E_REPOSITORY_FILE_UNRESOLVED');
        }
        root = match.path;
      } else {
        // Feature-inherited references without identity resolve by key.
        const match = snapshot.repositories.find((repo) => repo.valid && repo.name === ref.repoKey);
        root = match?.path;
      }
      if (root === undefined || path.isAbsolute(ref.path) || ref.path.includes('\0')) {
        throw repositoryFileError('E_REPOSITORY_FILE_INELIGIBLE');
      }
      const rootReal = await realpath(root);
      const candidate = path.resolve(rootReal, ref.path);
      if (!isWithinRoot(rootReal, candidate)) {
        throw repositoryFileError('E_REPOSITORY_FILE_ESCAPED');
      }
      const candidateReal = await realpath(candidate);
      if (!isWithinRoot(rootReal, candidateReal)) {
        throw repositoryFileError('E_REPOSITORY_FILE_OUTSIDE');
      }
      const info = await lstat(candidate);
      if (!info.isFile() || info.isSymbolicLink()) {
        throw repositoryFileError('E_REPOSITORY_FILE_NOT_REGULAR');
      }
      resolved.push(candidateReal);
    }
    return resolved;
  }
}

function repositoryFileError(
  code:
    | 'E_REPOSITORY_FILE_INELIGIBLE'
    | 'E_REPOSITORY_FILE_ESCAPED'
    | 'E_REPOSITORY_FILE_OUTSIDE'
    | 'E_REPOSITORY_FILE_NOT_REGULAR'
    | 'E_REPOSITORY_FILE_UNRESOLVED',
): CanonicalErrorException {
  return new CanonicalErrorException(buildCanonicalError(code));
}

function isWithinRoot(root: string, candidate: string): boolean {
  return candidate === root || candidate.startsWith(root + path.sep);
}

const EXCLUDED_TREES = new Set([
  '.git',
  'node_modules',
  'dist',
  'build',
  'coverage',
  'vendor',
  '.cache',
]);

async function* walkEligibleFiles(
  root: string,
  signal: AbortSignal,
  relative = '',
): AsyncGenerator<string> {
  if (signal.aborted) return;
  let directory;
  try {
    directory = await opendir(path.join(root, relative));
  } catch {
    return;
  }
  for await (const entry of directory) {
    if (signal.aborted) return;
    if (entry.isSymbolicLink()) continue;
    const next = relative === '' ? entry.name : path.join(relative, entry.name);
    if (entry.isDirectory()) {
      if (!EXCLUDED_TREES.has(entry.name) && !entry.name.startsWith('.agentic')) {
        yield* walkEligibleFiles(root, signal, next);
      }
    } else if (entry.isFile()) {
      yield next.split(path.sep).join('/');
    }
  }
}

function fuzzyScore(candidate: string, rawQuery: string): number {
  const normalize = (value: string): string =>
    value
      .trim()
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, ' ');
  const query = normalize(rawQuery);
  if (query === '') return 0;
  const value = normalize(candidate);
  const exact = value.indexOf(query);
  if (exact >= 0) return 1000 - exact - value.length / 100;
  let cursor = 0;
  let gap = 0;
  for (const character of query) {
    const index = value.indexOf(character, cursor);
    if (index < 0) return -1;
    gap += index - cursor;
    cursor = index + 1;
  }
  return 500 - gap - value.length / 100;
}
