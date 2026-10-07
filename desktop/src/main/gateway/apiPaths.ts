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
 * Authorization of renderer-supplied `/api/v1/...` paths against the main
 * process's allowlist. Pure string/query matching with no connection state;
 * every rule fails closed.
 */
import {
  MAX_RUN_CONTENT_BYTES,
  SESSION_ID_SEGMENT_PATTERN,
  SUPERVISOR_TRANSCRIPT_MAX_LIMIT,
  SupervisorStreamEpochSchema,
} from '../../shared/ipc';

export const SESSION_ID_PATTERN = new RegExp(`^${SESSION_ID_SEGMENT_PATTERN}$`, 'i');
const QUERYLESS_API_PATH_PATTERN = /^\/api\/v1(\/[a-z0-9_-]+)*$/i;
const QUERYLESS_SESSION_API_PATH_PATTERN = new RegExp(
  `^/api/v1/sessions/${SESSION_ID_SEGMENT_PATTERN}(?:/[a-z0-9_-]+)*$`,
  'i',
);
const SESSION_TRANSCRIPT_PATH_PATTERN = new RegExp(
  `^/api/v1/sessions/${SESSION_ID_SEGMENT_PATTERN}/transcript$`,
  'i',
);
const SAFE_API_SEGMENT = '[a-z0-9_-]+';
const RUN_LIST_PATH_PATTERN = new RegExp(`^/api/v1/features/${SAFE_API_SEGMENT}/runs$`, 'i');
const RUN_CONTENT_PATH_PATTERN = new RegExp(
  `^/api/v1/features/${SAFE_API_SEGMENT}/runs/\\d+/(?:artifacts|logs)/${SAFE_API_SEGMENT}$`,
  'i',
);
const REWIND_PREVIEW_PATH_PATTERN = new RegExp(
  `^/api/v1/features/${SAFE_API_SEGMENT}/rewind/preview$`,
  'i',
);
const REPOSITORY_DIFF_PATH_PATTERN = new RegExp(
  `^/api/v1/features/${SAFE_API_SEGMENT}/repositories/${SAFE_API_SEGMENT}/diff$`,
  'i',
);
const CLONE_LIST_PATH_PATTERN = /^\/api\/v1\/workspace\/repositories\/clone$/i;
const UPLOADS_PATH_PATTERN = /^\/api\/v1\/uploads$/i;
const SUPERVISOR_NAMESPACE_PATTERN = /^\/api\/v1\/supervisor(?:\/|$)/i;

/** The upper bound accepted for the supervisor stream's `heartbeat_ms`. */
const MAX_SUPERVISOR_HEARTBEAT_MS = 10 * 60 * 1000;

/**
 * The supervisor namespace is an exact (method, path, query) allowlist: each
 * route answers exactly one verb, and anything else under
 * `/api/v1/supervisor` fails closed instead of falling through to the
 * generic queryless rule.
 */
const SUPERVISOR_ROUTES: Readonly<
  Record<string, { method: string; query?: (rawQuery: string) => boolean }>
> = {
  '/api/v1/supervisor/state': { method: 'GET' },
  '/api/v1/supervisor/settings': { method: 'PATCH' },
  '/api/v1/supervisor/transcript': { method: 'GET', query: hasSupervisorTranscriptQuery },
  '/api/v1/supervisor/messages': { method: 'POST' },
  '/api/v1/supervisor/interrupt': { method: 'POST' },
  '/api/v1/supervisor/end': { method: 'POST' },
  '/api/v1/supervisor/events': { method: 'GET', query: hasSupervisorEventsQuery },
};

/**
 * Authorizes one `/api/v1/...` request. `method` matters only inside the
 * supervisor namespace, where each route is bound to its verb; every other
 * route keeps its method-agnostic path/query rule.
 */
export function isAllowedApiPath(path: string, method: string = 'GET'): boolean {
  const parts = path.split('?');
  const pathname = parts[0] ?? '';
  if (pathname.split('/').some((segment) => segment === '.' || segment === '..')) return false;
  if (SUPERVISOR_NAMESPACE_PATTERN.test(pathname)) {
    return isAllowedSupervisorRequest(parts, method);
  }
  if (UPLOADS_PATH_PATTERN.test(pathname)) {
    // Uploads require the mutually-required kind/name query; a queryless
    // uploads path must not fall through to the generic queryless branch.
    return parts.length === 2 && hasUploadsQuery(parts[1] ?? '');
  }
  if (parts.length === 1) {
    return (
      QUERYLESS_API_PATH_PATTERN.test(pathname) || QUERYLESS_SESSION_API_PATH_PATTERN.test(pathname)
    );
  }
  if (parts.length === 2 && RUN_LIST_PATH_PATTERN.test(pathname)) {
    return hasBoundedIntegerQuery(parts[1] ?? '', {
      page: { min: 1 },
      page_size: { min: 1, max: 100 },
    });
  }
  if (parts.length === 2 && RUN_CONTENT_PATH_PATTERN.test(pathname)) {
    return hasBoundedIntegerQuery(parts[1] ?? '', {
      offset: { min: 0 },
      limit: { min: 1, max: MAX_RUN_CONTENT_BYTES },
    });
  }
  if (parts.length === 2 && REWIND_PREVIEW_PATH_PATTERN.test(pathname)) {
    return hasRewindPreviewQuery(parts[1] ?? '');
  }
  if (parts.length === 2 && REPOSITORY_DIFF_PATH_PATTERN.test(pathname)) {
    return hasRepositoryDiffQuery(parts[1] ?? '');
  }
  if (parts.length === 2 && CLONE_LIST_PATH_PATTERN.test(pathname)) {
    return hasCloneListQuery(parts[1] ?? '');
  }
  if (parts.length !== 2 || !SESSION_TRANSCRIPT_PATH_PATTERN.test(pathname)) {
    return false;
  }
  return hasBoundedIntegerQuery(parts[1] ?? '', {
    offset: { min: 0 },
    limit: { min: 1, max: 500 },
  });
}

// The clone listing query: optional bounded `limit` (1..200) and opaque
// continuation `after` token, each at most once.
function hasCloneListQuery(rawQuery: string): boolean {
  if (rawQuery === '') return false;
  const seen = new Set<string>();
  for (const [key, value] of new URLSearchParams(rawQuery)) {
    if (seen.has(key)) return false;
    if (key === 'limit') {
      if (!/^\d+$/.test(value)) return false;
      const parsed = Number(value);
      if (!Number.isSafeInteger(parsed) || parsed < 1 || parsed > 200) return false;
      seen.add(key);
      continue;
    }
    if (key === 'after') {
      if (value === '' || value.length > 256) return false;
      seen.add(key);
      continue;
    }
    return false;
  }
  return seen.size > 0;
}

function hasBoundedIntegerQuery(
  rawQuery: string,
  allowed: Record<string, { min: number; max?: number }>,
): boolean {
  if (rawQuery === '') return false;
  const seen = new Set<string>();
  for (const [key, value] of new URLSearchParams(rawQuery)) {
    const bounds = allowed[key];
    if (bounds === undefined || seen.has(key) || !/^\d+$/.test(value)) return false;
    const parsed = Number(value);
    if (!Number.isSafeInteger(parsed) || parsed < bounds.min) return false;
    if (bounds.max !== undefined && parsed > bounds.max) return false;
    seen.add(key);
  }
  return seen.size > 0;
}

function hasRewindPreviewQuery(rawQuery: string): boolean {
  if (rawQuery === '') return false;
  const seen = new Set<string>();
  for (const [key, value] of new URLSearchParams(rawQuery)) {
    if (seen.has(key)) return false;
    seen.add(key);
    if (key === 'target_phase' && /^[a-z][a-z0-9_-]{0,199}$/i.test(value)) continue;
    if (key === 'roadmap_phase' && /^\d+$/.test(value) && Number(value) >= 1) continue;
    if (key === 'upgrade_pipeline' && /^[a-z][a-z0-9_-]{0,199}$/i.test(value)) continue;
    return false;
  }
  return seen.has('target_phase');
}

function hasRepositoryDiffQuery(rawQuery: string): boolean {
  if (rawQuery === '') return false;
  const seen = new Set<string>();
  for (const [key, value] of new URLSearchParams(rawQuery)) {
    if (seen.has(key) || key !== 'file_path') return false;
    seen.add(key);
    if (value === '' || value.length > 4096) return false;
    if (value.startsWith('/') || value.startsWith('\\') || value.includes('\\')) return false;
    const segments = value.split('/');
    if (segments.some((segment) => segment === '' || segment === '.' || segment === '..')) {
      return false;
    }
  }
  return seen.has('file_path');
}

/**
 * The upload-staging query: exactly `kind` (image|attachment) and `name`
 * (a bounded original filename), each exactly once. Mirrors the server's
 * mutually-required query contract; anything else fails closed here.
 */
function hasUploadsQuery(rawQuery: string): boolean {
  if (rawQuery === '') return false;
  const seen = new Set<string>();
  for (const [key, value] of new URLSearchParams(rawQuery)) {
    if (seen.has(key)) return false;
    seen.add(key);
    if (key === 'kind' && (value === 'image' || value === 'attachment')) continue;
    if (key === 'name' && value.length > 0 && value.length <= 255) continue;
    return false;
  }
  return seen.has('kind') && seen.has('name');
}

function isAllowedSupervisorRequest(parts: string[], method: string): boolean {
  if (parts.length > 2) return false;
  const route = SUPERVISOR_ROUTES[parts[0] ?? ''];
  if (route === undefined || route.method !== method) return false;
  if (parts.length === 1) return true;
  // A present-but-empty query (`path?`) is malformed, never "no query".
  return route.query !== undefined && route.query(parts[1] ?? '');
}

/** A non-negative safe integer in canonical decimal form within bounds. */
function isBoundedInteger(value: string, min: number, max?: number): boolean {
  if (!/^\d+$/.test(value)) return false;
  const parsed = Number(value);
  if (!Number.isSafeInteger(parsed) || parsed < min) return false;
  return max === undefined || parsed <= max;
}

/**
 * The supervisor transcript page query: optional `before` (>= 1) or `after`
 * (>= 0) — never both — plus optional `limit` (1..500), each at most once.
 */
function hasSupervisorTranscriptQuery(rawQuery: string): boolean {
  if (rawQuery === '') return false;
  const seen = new Set<string>();
  for (const [key, value] of new URLSearchParams(rawQuery)) {
    if (seen.has(key)) return false;
    seen.add(key);
    if (key === 'before' && isBoundedInteger(value, 1)) continue;
    if (key === 'after' && isBoundedInteger(value, 0)) continue;
    if (key === 'limit' && isBoundedInteger(value, 1, SUPERVISOR_TRANSCRIPT_MAX_LIMIT)) continue;
    return false;
  }
  return seen.size > 0 && !(seen.has('before') && seen.has('after'));
}

/**
 * The supervisor event-stream query: optional `after` cursor (>= 0) with its
 * `epoch`, plus optional `heartbeat_ms`, each at most once. An epoch without
 * a cursor is meaningless and rejected.
 */
function hasSupervisorEventsQuery(rawQuery: string): boolean {
  if (rawQuery === '') return false;
  const seen = new Set<string>();
  for (const [key, value] of new URLSearchParams(rawQuery)) {
    if (seen.has(key)) return false;
    seen.add(key);
    if (key === 'after' && isBoundedInteger(value, 0)) continue;
    if (key === 'epoch' && value !== '' && SupervisorStreamEpochSchema.safeParse(value).success) {
      continue;
    }
    if (key === 'heartbeat_ms' && isBoundedInteger(value, 1, MAX_SUPERVISOR_HEARTBEAT_MS)) continue;
    return false;
  }
  return seen.size > 0 && (!seen.has('epoch') || seen.has('after'));
}
