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
  type CreationFileKind,
  type CreationFileSearchRequest,
  type CreationFileSearchResult,
  type ReadinessSnapshot,
  type RepositoryFileRef,
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
      const eligible = snapshot.repositories.filter(
        (repo) => repo.valid && request.repoKeys.includes(repo.name),
      );
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
          if (score >= 0) files.push({ repoKey: repo.name, path: relative, score });
        }
        if (truncated || controller.signal.aborted) break;
      }
      files.sort((a, b) => b.score - a.score || a.path.localeCompare(b.path));
      return {
        requestId: request.requestId,
        files: files
          .slice(0, CREATION_FILE_SEARCH_RESULT_LIMIT)
          .map(({ repoKey, path: filePath }) => ({ repoKey, path: filePath })),
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
    const repositories = new Map(
      snapshot.repositories.filter((repo) => repo.valid).map((repo) => [repo.name, repo.path]),
    );
    const resolved: string[] = [];
    for (const ref of refs) {
      const root = repositories.get(ref.repoKey);
      if (root === undefined || path.isAbsolute(ref.path) || ref.path.includes('\0')) {
        throw invalidRepositoryFile('A selected repository file is no longer eligible.');
      }
      const rootReal = await realpath(root);
      const candidate = path.resolve(rootReal, ref.path);
      if (!isWithinRoot(rootReal, candidate)) {
        throw invalidRepositoryFile('A selected repository file escaped its repository.');
      }
      const candidateReal = await realpath(candidate);
      if (!isWithinRoot(rootReal, candidateReal)) {
        throw invalidRepositoryFile('A selected repository file resolved outside its repository.');
      }
      const info = await lstat(candidate);
      if (!info.isFile() || info.isSymbolicLink()) {
        throw invalidRepositoryFile('A selected repository file is not a regular file.');
      }
      resolved.push(candidateReal);
    }
    return resolved;
  }
}

function invalidRepositoryFile(reason: string): CanonicalErrorException {
  return new CanonicalErrorException(
    buildCanonicalError('E_INVALID_REPOSITORY_FILE', { params: { reason } }),
  );
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
