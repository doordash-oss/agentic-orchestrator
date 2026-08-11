/**
 * Centralized copy for local-only capabilities. While connected to a remote
 * server, remaining local-only affordances (repository file search) degrade
 * into explanations rather than silent no-ops or error toasts — and every
 * surface phrases the limitation with these exact strings, so the story
 * stays identical wherever it appears. Attach/import affordances are no
 * longer local-only: they stage uploads (see features/stagedItems.ts).
 */

/** The shared limitation stem every local-only explanation builds on. */
export const REQUIRES_LOCAL_SERVER = 'requires a local server';

/** The @-mention repository file search. */
export const FILE_SEARCH_REQUIRES_LOCAL_SERVER = `Repository file search ${REQUIRES_LOCAL_SERVER}; file search and upload support are coming in a later release.`;
