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
 * The desktop-local error catalog plus typed, redaction-safe error plumbing
 * shared by the main process, preload, and renderer.
 *
 * Catalog conventions:
 *  - Every desktop-authored error is keyed by an `E_UPPER_SNAKE` code. The
 *    `E_` prefix is the desktop-local marker: server catalog codes are
 *    lowercase snake_case (`publish_pull_request_failed`), so the two
 *    families are disjoint by construction and a code's origin is readable
 *    at a glance.
 *  - Every entry owns its human text: a class, a title, a summary (static or
 *    interpolated from a small typed parameter set), and an optional
 *    remediation hint. Free-form context never goes in the summary; it goes
 *    in `diagnostics`, which is redacted before crossing any boundary.
 *  - Class policy: `blocking` when the surface cannot function (terminal
 *    connection states, load failures, internal errors, protocol errors);
 *    `needs_action` when the user must do one specific thing (re-paste a
 *    token, fix a connection string, grant consent, pick a supported file,
 *    rename a file, reorder roots, choose a valid folder); `warning` for
 *    informational or self-recovering conditions (a request timeout whose
 *    operation may still complete, a lost remote server while a background
 *    re-probe keeps trying).
 *
 * Messages must never contain raw payloads, tokens, or absolute user paths —
 * `buildCanonicalError` applies `redactText` to every parameter-derived
 * summary, remediation hint, and diagnostics string.
 */
import type { CanonicalError } from './api/parse';

// Type-only re-export (cycle-safe: erased at compile time — parse.ts imports
// values from this module): the canonical error type is part of this module's
// public surface for main-process and preload importers.
export type { CanonicalError } from './api/parse';

/**
 * A server-emitted canonical error. The parsed catalog-rendered object
 * crosses IPC unchanged: the catalog owns the human text, so the main
 * process never re-wraps or re-redacts it.
 */
export class CanonicalErrorException extends Error {
  readonly canonical: CanonicalError;

  constructor(canonical: CanonicalError) {
    super(`${canonical.code}: ${canonical.title} — ${canonical.summary}`);
    this.name = 'CanonicalErrorException';
    this.canonical = canonical;
  }
}

/**
 * Prefix marking an Error message that carries a full canonical error as
 * JSON. Custom Error properties do not survive the context bridge, so the
 * message itself is the only channel that reliably carries the canonical
 * object from preload to the renderer.
 */
export const CANONICAL_ERROR_MESSAGE_PREFIX = 'E_CANONICAL_ERROR ';

// --- Catalog -----------------------------------------------------------------

/** One catalog entry: class plus authored title/summary/hint text. */
export interface CatalogSpec<P extends Record<string, string>> {
  class: CanonicalError['class'];
  title: string;
  /** Authored summary; parameter-derived text is redacted at build time. */
  summary: (params: P) => string;
  /** Authored remediation hint, when the user has a next step. */
  remediationHint?: (params: P) => string;
}

function entry<P extends Record<string, string>>(spec: CatalogSpec<P>): CatalogSpec<P> {
  return spec;
}

const CONNECTION_STRING_GRAMMAR = 'agentico://<token>@<host>:<port>[?name=<name>]';

/**
 * The desktop-local catalog. One code per concept; where legacy sites varied
 * only by message, the variable part became a typed parameter.
 */
export const ERROR_CATALOG = {
  // --- Transport / IPC plumbing ---------------------------------------------
  E_INTERNAL: entry({
    class: 'blocking',
    title: 'Request failed',
    summary: (_params: { reason: string }) => 'The request failed unexpectedly.',
  }),
  E_UNTRUSTED_SENDER: entry({
    class: 'blocking',
    title: 'Request rejected',
    summary: () => 'The request did not originate from the application window.',
  }),
  E_IPC_PROTOCOL: entry({
    class: 'blocking',
    title: 'Unrecognized response',
    summary: () => 'The main process returned an unrecognized response.',
    remediationHint: () => 'Restart the app; if this persists, report the issue.',
  }),
  E_IPC_UNREACHABLE: entry({
    class: 'blocking',
    title: 'Application core unreachable',
    summary: () => 'The application core did not respond.',
    remediationHint: () => 'Restart the app, then retry.',
  }),
  /** A request outran its client-side bound; the server operation may still run. */
  E_REQUEST_TIMEOUT: entry({
    class: 'warning',
    title: 'Request timed out',
    summary: () =>
      'The runtime did not answer within the request bound; the operation may still be running.',
    remediationHint: () =>
      'Wait for the feature to refresh — retrying could repeat work that already succeeded.',
  }),
  /**
   * The distinct locality refusal: local-filesystem work (pickers, clipboard
   * capture, repository file walks, local path submission) is meaningless
   * while the active connection targets a remote server. Main-process guards
   * throw this before touching the filesystem or the network.
   */
  E_REQUIRES_LOCAL_SERVER: entry({
    class: 'needs_action',
    title: 'A local server is required',
    summary: () => 'This action requires a local server.',
    remediationHint: () => 'Connect to a locally running Agentico server, then retry.',
  }),

  // --- Connection lifecycle ---------------------------------------------------
  E_GATEWAY: entry({
    class: 'blocking',
    title: 'Connection failed',
    summary: ({ reason }: { reason: string }) =>
      `The connection attempt failed unexpectedly: ${reason}`,
  }),
  E_RECOVERY_DELAY: entry({
    class: 'blocking',
    title: 'Recovery delayed',
    summary: ({ reason }: { reason: string }) =>
      `An automatic recovery step could not proceed: ${reason}`,
  }),
  E_BAD_API_PATH: entry({
    class: 'blocking',
    title: 'Disallowed API path',
    summary: () => 'The requested API path is not allowed.',
  }),
  E_NOT_CONNECTED: entry({
    class: 'blocking',
    title: 'Not connected',
    summary: () => 'The app is not connected to an Agentico runtime.',
    remediationHint: () => 'Wait for the connection to become ready, then retry.',
  }),
  E_SWITCH_UNAVAILABLE: entry({
    class: 'blocking',
    title: 'The selected server is unavailable',
    summary: () => 'The selected Agentico server is no longer running.',
    remediationHint: () => 'Use Retry to try again, or go back to the previous server.',
  }),
  E_EXTERNAL_SERVER_LOST: entry({
    class: 'blocking',
    title: 'The server connection was lost',
    summary: ({ reason }: { reason: string }) => reason,
    remediationHint: () => 'Check that the server is running and reachable, then use Retry.',
  }),
  E_EXTERNAL_RUNTIME_UNRESPONSIVE: entry({
    class: 'blocking',
    title: 'The server connection was lost',
    summary: () => 'The externally managed Agentico runtime stopped responding.',
    remediationHint: () => 'Restart it from where it was started, then use Retry.',
  }),
  /** A lost remote while the bounded background re-probe keeps trying to re-attach. */
  E_REMOTE_SERVER_LOST_REPROBING: entry({
    class: 'warning',
    title: 'The remote server is not responding',
    summary: () => 'The remote Agentico server stopped responding.',
    remediationHint: () =>
      'Check that the remote server is running and reachable. The app keeps probing in the ' +
      'background for a few minutes and reconnects automatically; you can also use Retry.',
  }),
  E_RESOURCES_MISSING: entry({
    class: 'blocking',
    title: 'Bundled runtime missing',
    summary: () => 'The bundled agentico server binary was not found in the application resources.',
    remediationHint: () =>
      'Reinstall the application. In development, run "make build" or point ' +
      'AGENTICO_SERVER_BIN at a built agentico binary, then retry.',
  }),
  E_LAUNCH_FAILED: entry({
    class: 'blocking',
    title: 'The bundled runtime could not be started',
    summary: ({ reason }: { reason: string }) => reason,
    remediationHint: () =>
      'Check that the application files are intact and executable, then retry.',
  }),
  E_BUNDLED_INCOMPATIBLE: entry({
    class: 'blocking',
    title: 'The bundled runtime is incompatible',
    summary: ({ reason }: { reason: string }) => reason,
    remediationHint: () => 'Reinstall the application so the bundled runtime matches the app.',
  }),
  E_LAUNCH_NO_TOKEN: entry({
    class: 'blocking',
    title: 'No credentials published',
    summary: () => 'The launched runtime did not publish an auth token in its discovery record.',
    remediationHint: () => 'Retry. If this persists, reinstall the application.',
  }),
  E_LAUNCH_AUTH: entry({
    class: 'blocking',
    title: 'Authentication with the launched runtime failed',
    summary: () => 'The launched runtime rejected its own published credentials.',
    remediationHint: () => 'Retry. If this persists, reinstall the application.',
  }),
  E_SERVER_EXITED: entry({
    class: 'blocking',
    title: 'The runtime exited during startup',
    summary: () => 'The bundled runtime exited during startup.',
    remediationHint: () => 'Retry. Local diagnostics were recorded for this launch attempt.',
  }),
  E_LAUNCH_TIMEOUT: entry({
    class: 'blocking',
    title: 'The runtime did not become healthy in time',
    summary: () => 'The launched runtime did not become healthy within the startup bound.',
    remediationHint: () =>
      'Retry. If this persists, inspect the runtime log in the runtime directory.',
  }),
  E_SERVER_CRASHED: entry({
    class: 'blocking',
    title: 'The app-managed runtime crashed',
    summary: ({ outcome }: { outcome: string }) => `The app-managed Agentico runtime ${outcome}.`,
    remediationHint: () =>
      'Agentico retries the restart automatically within a bounded crash budget; use Retry ' +
      'to start a fresh supervised cycle.',
  }),
  E_SERVER_CRASH_LOOP: entry({
    class: 'blocking',
    title: 'The runtime stopped repeatedly',
    summary: () => 'Three automatic restart attempts failed within one minute.',
    remediationHint: () =>
      'Inspect the redacted local diagnostics, then use Retry to start a fresh cycle.',
  }),
  E_EVENT_STREAM: entry({
    class: 'blocking',
    title: 'Event stream failed',
    summary: ({ reason }: { reason: string }) => reason,
  }),
  E_STOP_FAILED: entry({
    class: 'blocking',
    title: 'Stop failed',
    summary: ({ reason }: { reason: string }) => reason,
  }),

  // --- Attach / remote-add ----------------------------------------------------
  E_CONNECTION_STRING_SCHEME: entry({
    class: 'needs_action',
    title: 'The connection string could not be parsed',
    summary: ({ got }: { got: string }) =>
      `Connection string must use the agentico:// scheme, got ${got}.`,
    remediationHint: () =>
      `Paste the full attach string exactly as the server printed it (${CONNECTION_STRING_GRAMMAR}).`,
  }),
  E_CONNECTION_STRING_TOKEN: entry({
    class: 'needs_action',
    title: 'The connection string is missing its token',
    summary: () => 'Connection string is missing the access token before the @ sign.',
    remediationHint: () =>
      `Expected ${CONNECTION_STRING_GRAMMAR} — copy the full string; the token is the part before the @.`,
  }),
  E_CONNECTION_STRING_HOST: entry({
    class: 'needs_action',
    title: 'The connection string is missing its host',
    summary: () => 'Connection string is missing a host.',
    remediationHint: () =>
      `Expected ${CONNECTION_STRING_GRAMMAR} with the server's advertised address between @ and the port.`,
  }),
  E_CONNECTION_STRING_HOST_INVALID: entry({
    class: 'needs_action',
    title: 'The connection string host is not usable',
    summary: () =>
      'Connection string host contains characters that cannot form a valid http:// base URL.',
    remediationHint: () =>
      `Expected ${CONNECTION_STRING_GRAMMAR} — check the host portion for stray spaces or punctuation.`,
  }),
  E_CONNECTION_STRING_WILDCARD: entry({
    class: 'needs_action',
    title: 'The connection string host is not dialable',
    summary: ({ host }: { host: string }) =>
      `Connection string host ${host} is a wildcard bind address, not a dialable address.`,
    remediationHint: () =>
      'Ask the server to advertise its primary interface address (or use 127.0.0.1 for same-machine servers).',
  }),
  E_CONNECTION_STRING_PORT: entry({
    class: 'needs_action',
    title: 'The connection string is missing its port',
    summary: () => 'Connection string is missing an explicit port.',
    remediationHint: () =>
      'The server always prints host:port — recopy the string including the port after the colon.',
  }),
  E_CONNECTION_STRING_PORT_RANGE: entry({
    class: 'needs_action',
    title: 'The connection string port is invalid',
    summary: ({ port }: { port: string }) =>
      `Connection string port "${port}" is not a number in the range 1-65535.`,
    remediationHint: () => 'Recopy the string; the server listens on a port between 1 and 65535.',
  }),
  E_REMOTE_UNREACHABLE: entry({
    class: 'needs_action',
    title: 'The server could not be reached',
    summary: () => 'Could not reach the server at the address in the connection string.',
    remediationHint: () =>
      'Check that the server is running and reachable from this machine (address, port, firewall), then retry.',
  }),
  E_REMOTE_INCOMPATIBLE: entry({
    class: 'blocking',
    title: 'The server is not compatible with this app',
    summary: ({ reason }: { reason: string }) =>
      `The server is not compatible with this app: ${reason}`,
    remediationHint: () =>
      'Update the Agentico desktop app and the agentico server to matching releases, then retry.',
  }),
  E_REMOTE_AUTH_REJECTED: entry({
    class: 'needs_action',
    title: 'The token was rejected',
    summary: () => 'The server rejected the token from the connection string.',
    remediationHint: () =>
      'Re-copy the FULL connection string the server printed — the token is the part before the @ sign — and paste it again.',
  }),
  E_INCOMPATIBLE_SERVER: entry({
    class: 'blocking',
    title: 'The server is not compatible with this app',
    summary: ({ reason }: { reason: string }) => reason,
    remediationHint: () =>
      'Update the Agentico desktop app and the agentico runtime to matching releases, then retry. ' +
      'This app never shuts down a runtime it does not own — close that runtime from wherever it ' +
      'was started if you want this app to manage its own.',
  }),
  E_ATTACH_UNREACHABLE: entry({
    class: 'blocking',
    title: 'The selected server is unreachable',
    summary: () => 'The selected Agentico server is no longer reachable.',
    remediationHint: () => 'Use Retry to rescan the running servers.',
  }),
  E_ATTACH_NO_TOKEN: entry({
    class: 'blocking',
    title: 'No credentials to attach with',
    summary: () => 'The discovery record for the running runtime carries no auth token.',
    remediationHint: () => 'Restart that runtime from where it was started, then retry.',
  }),
  E_ATTACH_AUTH: entry({
    class: 'blocking',
    title: 'Authentication with the running runtime failed',
    summary: () => 'The running runtime rejected the stored credentials.',
    remediationHint: () => 'Restart that runtime from where it was started, then retry.',
  }),
  E_REMOTE_TOKEN_REPASTE: entry({
    class: 'needs_action',
    title: 'Re-enter the remote server token',
    summary: ({ reason }: { reason: string }) => reason,
    remediationHint: () =>
      'Re-enter the remote server token in Settings (paste its connection string again), then use Retry.',
  }),
  E_REMOTE_TOKEN_UNREADABLE: entry({
    class: 'needs_action',
    title: 'Re-enter the remote server token',
    summary: () => 'The stored token for this remote server could not be decrypted.',
    remediationHint: () =>
      'Re-enter the remote server token in Settings (paste its connection string again), then use Retry.',
  }),
  E_REMOTE_TOKEN_MISSING: entry({
    class: 'needs_action',
    title: 'Re-enter the remote server token',
    summary: () => 'There is no stored token for this remote server.',
    remediationHint: () =>
      'Re-enter the remote server token in Settings (paste its connection string again), then use Retry.',
  }),
  E_REMOTE_HEALTH_UNANSWERED: entry({
    class: 'blocking',
    title: 'The server connection was lost',
    summary: () => 'The remote Agentico server did not answer its health probe.',
    remediationHint: () => 'Check that the server is running and reachable, then use Retry.',
  }),
  E_REMOTE_HEALTH_UNHEALTHY: entry({
    class: 'blocking',
    title: 'The server connection was lost',
    summary: () => 'The remote Agentico server answered with an unhealthy status.',
    remediationHint: () => 'Check the server health and logs, then use Retry.',
  }),
  E_REMOTE_STORED_TOKEN_REJECTED: entry({
    class: 'needs_action',
    title: 'Re-enter the remote server token',
    summary: () => 'The remote Agentico server rejected the stored token.',
    remediationHint: () =>
      'Re-enter the remote server token in Settings (paste its connection string again), then use Retry.',
  }),
  E_SERVER_UNKNOWN: entry({
    class: 'blocking',
    title: 'Unknown server',
    summary: () => 'The server is not in the servers list.',
    remediationHint: () =>
      'Refresh Settings and try again; the server may already have been removed.',
  }),
  E_LINK_ADD_FAILED: entry({
    class: 'blocking',
    title: 'The server could not be added',
    summary: ({ reason }: { reason: string }) => reason,
  }),

  // --- Sessions / streams ------------------------------------------------------
  E_SSE_UNAVAILABLE: entry({
    class: 'blocking',
    title: 'Event stream unavailable',
    summary: () => 'This build has no event-stream transport wired.',
  }),
  E_BAD_SESSION_ID: entry({
    class: 'blocking',
    title: 'Invalid session ID',
    summary: () => 'The session ID is not allowed.',
  }),
  E_BAD_TRANSCRIPT_CURSOR: entry({
    class: 'blocking',
    title: 'Invalid transcript cursor',
    summary: () => 'The transcript cursor is not allowed.',
  }),
  E_SESSION_STREAM_REJECTED: entry({
    class: 'blocking',
    title: 'The session stream was rejected',
    summary: ({ status }: { status: string }) =>
      `The runtime rejected the session output stream (HTTP ${status}).`,
  }),
  E_SESSION_STREAM: entry({
    class: 'blocking',
    title: 'The session stream failed',
    summary: ({ reason }: { reason: string }) => reason,
  }),
  E_STREAM_PROTOCOL: entry({
    class: 'blocking',
    title: 'Session stream protocol error',
    summary: ({ detail }: { detail: string }) => detail,
  }),
  E_STREAM_PROTOCOL_UNKNOWN_EVENT: entry({
    class: 'blocking',
    title: 'Session stream protocol error',
    summary: () => 'The session output stream contained an unknown event.',
  }),
  E_STREAM_PROTOCOL_MISSING_SESSION_ID: entry({
    class: 'blocking',
    title: 'Session stream protocol error',
    summary: () => 'The session output stream omitted its session ID.',
  }),
  E_STREAM_PROTOCOL_CURSOR_MISMATCH: entry({
    class: 'blocking',
    title: 'Session stream protocol error',
    summary: () => 'The session output row cursor did not match its message.',
  }),
  E_SESSION_ERROR: entry({
    class: 'blocking',
    title: 'The session ended with an error',
    summary: ({ reason }: { reason: string }) => reason,
  }),
  E_HTTP_REJECTED: entry({
    class: 'blocking',
    title: 'The request was rejected',
    summary: ({ status }: { status: string }) =>
      `The runtime rejected the request (HTTP ${status}).`,
    remediationHint: () => 'Retry; if this persists, restart the runtime and check its log.',
  }),

  // --- Uploads / creation files ------------------------------------------------
  E_UPLOAD: entry({
    class: 'blocking',
    title: 'Upload failed',
    summary: ({ reason }: { reason: string }) => reason,
  }),
  E_UPLOAD_UNAVAILABLE: entry({
    class: 'blocking',
    title: 'Uploads unavailable',
    summary: () => 'This build has no upload transport wired.',
  }),
  E_UPLOAD_NAME: entry({
    class: 'needs_action',
    title: 'The file name is not usable',
    summary: () => 'The file name is not usable for upload.',
    remediationHint: () => 'Rename the file and retry.',
  }),
  E_UPLOAD_TYPE_UNSUPPORTED: entry({
    class: 'needs_action',
    title: 'Unsupported image type',
    summary: () => 'Only PNG, JPEG, GIF, or WebP images can be attached.',
    remediationHint: () => 'Choose an image in a supported format.',
  }),
  E_UPLOAD_TOO_LARGE: entry({
    class: 'needs_action',
    title: 'The file is too large',
    summary: ({ limit }: { limit: string }) => `The file is larger than the ${limit} upload limit.`,
    remediationHint: () =>
      'Choose a smaller file: images are limited to 10 MiB and attachments to 25 MiB.',
  }),
  E_INVALID_REPOSITORY_FILE: entry({
    class: 'needs_action',
    title: 'The selected repository file is not eligible',
    summary: ({ reason }: { reason: string }) => reason,
    remediationHint: () => 'Re-pick the file from the repository file picker, then retry.',
  }),

  // --- Settings / setup / drafts ------------------------------------------------
  E_INVALID_SETTINGS_PATCH: entry({
    class: 'blocking',
    title: 'Invalid settings update',
    summary: () => 'The settings update was rejected because it did not match the settings schema.',
  }),
  E_INVALID_PATH: entry({
    class: 'needs_action',
    title: 'The selected folder could not be used',
    summary: () => 'The selected folder could not be used.',
    remediationHint: () => 'Choose a regular folder with an absolute path.',
  }),
  E_INVALID_REORDER: entry({
    class: 'needs_action',
    title: 'The root set must match',
    summary: () => 'The reordered root set must match the current set of workspace roots.',
    remediationHint: () => 'Add or remove roots separately before reordering.',
  }),
  E_CONSENT_REQUIRED: entry({
    class: 'needs_action',
    title: 'Consent required',
    summary: () => 'Repository initialization requires explicit consent.',
    remediationHint: () => 'Confirm the initialization consent in the dialog, then try again.',
  }),
  E_INVALID_LOCAL_DRAFT: entry({
    class: 'blocking',
    title: 'Invalid local draft',
    summary: () => 'The local draft did not match the supported schema.',
  }),
  E_LOCAL_DRAFT_LIMIT: entry({
    class: 'needs_action',
    title: 'Too many local drafts',
    summary: () =>
      'Too many recoverable drafts are stored locally. Discard an older draft before saving another.',
  }),

  // --- Boundary validation -------------------------------------------------------
  E_UNSAFE_PAYLOAD: entry({
    class: 'blocking',
    title: 'Unsafe payload',
    summary: () => 'Payload rejected: it contains a prototype-polluting key.',
    remediationHint: () =>
      'This indicates a malicious or corrupted source; do not retry with the same data.',
  }),
  E_PAYLOAD_TOO_LARGE: entry({
    class: 'blocking',
    title: 'Payload too large',
    summary: ({ bytes, limit }: { bytes: string; limit: string }) =>
      `Payload rejected: ${bytes} bytes exceeds the ${limit}-byte limit.`,
    remediationHint: () => 'Reduce the requested data size or report this as a server bug.',
  }),
  E_MALFORMED_RESPONSE: entry({
    class: 'blocking',
    title: 'Malformed response',
    summary: () => 'The response was not valid JSON.',
    remediationHint: () => 'Retry; if this persists the server or transport is misbehaving.',
  }),
  E_SCHEMA_MISMATCH: entry({
    class: 'blocking',
    title: 'Schema mismatch',
    summary: ({ paths }: { paths: string }) =>
      `The payload did not match the expected schema at: ${paths}.`,
    remediationHint: () =>
      'Update the Agentico desktop app and the agentico server to matching releases.',
  }),
  E_API_VERSION_INCOMPATIBLE: entry({
    class: 'blocking',
    title: 'Unsupported API version',
    summary: ({ supported }: { supported: string }) =>
      `The server speaks an unsupported API version; this app requires ${supported}.`,
    remediationHint: () =>
      'Update the Agentico desktop app and the agentico server to matching releases.',
  }),

  // --- Cockpit and pass dialogs ---------------------------------------------------
  /**
   * A destructive action's confirmation dialog (feature delete, pass discard)
   * whose server-authored impact projection never arrived or went stale: the
   * dialog fails closed — the confirm stays disarmed — and the warning card
   * tells the user how to recover.
   */
  E_IMPACT_PROJECTION_STALE: entry({
    class: 'warning',
    title: 'Impact projection is missing or stale',
    summary: () =>
      'A current impact projection for this action is missing or stale, so the blast radius cannot be shown.',
    remediationHint: () => 'Close this dialog, refresh, then try the action again.',
  }),

  // --- Update flow --------------------------------------------------------------
  E_UPDATE_NOT_READY: entry({
    class: 'blocking',
    title: 'Update is not ready',
    summary: () => 'No verified update is ready to install.',
  }),
  E_UPDATE_CONSENT_REQUIRED: entry({
    class: 'needs_action',
    title: 'Update consent required',
    summary: () => 'Installing an update requires explicit consent.',
  }),
  E_UPDATE_RELEASE_VERSION_INVALID: entry({
    class: 'blocking',
    title: 'Update version is invalid',
    summary: () => 'The release feed did not contain a compatible SemVer identity.',
  }),
  E_UPDATE_DOWNGRADE_REJECTED: entry({
    class: 'blocking',
    title: 'Update downgrade rejected',
    summary: () => 'The release feed offered an older version.',
  }),
  E_UPDATE_ASSET_UNAVAILABLE: entry({
    class: 'blocking',
    title: 'Update package unavailable',
    summary: () => 'No compatible update package is available for this platform.',
  }),
  E_REWIND_TARGETS_UNAVAILABLE: entry({
    class: 'blocking',
    title: 'Rewind targets unavailable',
    summary: () => 'Rewind targets are no longer available.',
    remediationHint: () => 'Refresh the feature and try again.',
  }),
  E_WORKTREE_PATH_MISSING: entry({
    class: 'blocking',
    title: 'Worktree path unavailable',
    summary: () => 'The server did not report a worktree path for this repository.',
    remediationHint: () => 'Refresh the completion preview and try again.',
  }),
  E_CLIPBOARD_WRITE_FAILED: entry({
    class: 'blocking',
    title: 'Could not copy the worktree path',
    summary: () => 'The clipboard write failed.',
    remediationHint: () => 'Copy from the path shown instead.',
  }),
  E_UPDATE_CHECK_FAILED: entry({
    class: 'blocking',
    title: 'Update check failed',
    summary: () => 'Agentico could not complete the update check.',
  }),
  E_UPDATE_DOWNLOAD_FAILED: entry({
    class: 'blocking',
    title: 'Update download failed',
    summary: () => 'Agentico could not download the selected update.',
  }),
  E_UPDATE_SIGNATURE_FAILED: entry({
    class: 'blocking',
    title: 'Update signature verification failed',
    summary: () => 'Agentico could not verify the downloaded update.',
  }),
  E_UPDATE_INSTALL_FAILED: entry({
    class: 'blocking',
    title: 'Update install failed',
    summary: () => 'Agentico could not install the verified update.',
  }),
} as const;

export type CatalogCode = keyof typeof ERROR_CATALOG;

/** The typed parameter set a catalog code's summary interpolates. */
export type CatalogParams<C extends CatalogCode> = Parameters<
  (typeof ERROR_CATALOG)[C]['summary']
>[0];

export interface BuildCanonicalErrorOptions<C extends CatalogCode> {
  params?: CatalogParams<C>;
  /** Free-form raw diagnostics; redacted before crossing any boundary. */
  diagnostics?: string;
  /** Overrides the catalog-authored remediation hint (redacted here). */
  remediationHint?: string;
  context?: CanonicalError['context'];
}

/**
 * Builds the canonical error for a catalog code. Every parameter-derived
 * summary, remediation hint, and diagnostics string passes through
 * `redactText`, so a path or bearer token in a parameter can never cross a
 * boundary unredacted.
 */
export function buildCanonicalError<C extends CatalogCode>(
  code: C,
  options: BuildCanonicalErrorOptions<C> = {},
): CanonicalError {
  const spec = ERROR_CATALOG[code] as CatalogSpec<Record<string, string>>;
  const params = (options.params ?? {}) as Record<string, string>;
  const summary = redactText(spec.summary(params));
  const hint =
    options.remediationHint !== undefined
      ? redactText(options.remediationHint)
      : spec.remediationHint !== undefined
        ? redactText(spec.remediationHint(params))
        : undefined;
  return {
    code,
    class: spec.class,
    title: spec.title,
    summary,
    ...(hint === undefined ? {} : { remediation: { hint } }),
    ...(options.context === undefined ? {} : { context: options.context }),
    ...(options.diagnostics === undefined ? {} : { diagnostics: redactText(options.diagnostics) }),
  };
}

// --- Helpers -------------------------------------------------------------------

/** The distinct typed timeout error (see the catalog entry). */
export const E_REQUEST_TIMEOUT = 'E_REQUEST_TIMEOUT';

/** The distinct locality refusal (see the catalog entry). */
export const E_REQUIRES_LOCAL_SERVER = 'E_REQUIRES_LOCAL_SERVER';

export function requestTimeoutError(): CanonicalError {
  return buildCanonicalError('E_REQUEST_TIMEOUT');
}

/**
 * The distinct locality refusal (see the catalog entry). Main-process guards
 * throw this before touching the filesystem or the network; the renderer
 * surfaces the summary verbatim.
 */
export function requiresLocalServerError(): CanonicalError {
  return buildCanonicalError('E_REQUIRES_LOCAL_SERVER');
}

export function isRequiresLocalServerError(err: unknown): boolean {
  return err instanceof CanonicalErrorException && err.canonical.code === 'E_REQUIRES_LOCAL_SERVER';
}

export function isRequestTimeout(err: unknown): boolean {
  return err instanceof CanonicalErrorException && err.canonical.code === 'E_REQUEST_TIMEOUT';
}

/** Catalog codes whose summary interpolates one redacted `reason` string. */
type ReasonCode = {
  [C in CatalogCode]: CatalogParams<C> extends { reason: string } ? C : never;
}[CatalogCode];

/** True for a fetch/DOM abort, whose raw message reads as an opaque failure. */
export function isAbortError(err: unknown): boolean {
  return err instanceof Error && (err.name === 'AbortError' || err.name === 'TimeoutError');
}

/**
 * Converts an arbitrary thrown value into a canonical error.
 * CanonicalErrorExceptions pass through untouched; a fetch/DOM abort becomes
 * the typed timeout; Error messages are redacted into the fallback code's
 * `reason` parameter. E_INTERNAL keeps its catalog-authored summary and puts
 * the caught message in diagnostics instead. Anything else (which could hold
 * raw payload data) is replaced with a generic reason.
 */
export function toCanonicalError(err: unknown, fallbackCode: ReasonCode): CanonicalError {
  if (err instanceof CanonicalErrorException) {
    return err.canonical;
  }
  if (isAbortError(err)) {
    return requestTimeoutError();
  }
  const reason =
    err instanceof Error && err.message !== '' ? err.message : 'An unexpected error occurred.';
  if (fallbackCode === 'E_INTERNAL') {
    return buildCanonicalError('E_INTERNAL', {
      params: { reason: 'An unexpected error occurred.' },
      ...(err instanceof Error && err.message !== '' ? { diagnostics: err.message } : {}),
    });
  }
  return buildCanonicalError(fallbackCode, { params: { reason } });
}

const BEARER_RE = /bearer\s+[a-z0-9._~+/=-]+/gi;
const TOKEN_PARAM_RE = /([?&](?:token|access_token|bearer|key|secret)=)[^\s&"']+/gi;
const USER_PATH_RE = /(?:\/Users|\/home)\/[^\s:"']+/g;

/** Strips token material and absolute user paths from free-form text. */
export function redactText(text: string): string {
  return text
    .replace(BEARER_RE, '[redacted]')
    .replace(TOKEN_PARAM_RE, '$1[redacted]')
    .replace(USER_PATH_RE, '[path]');
}

/**
 * The canonical object crosses IPC intact except diagnostics, which pass
 * through the same redaction as every other raw text the renderer receives.
 */
export function redactedCanonicalError(error: CanonicalError): CanonicalError {
  return {
    ...error,
    diagnostics: error.diagnostics === undefined ? undefined : redactText(error.diagnostics),
  };
}

/**
 * Removes exact secret occurrences from free-form text (split/join, never
 * regex). Used at the server-boundary: an untrusted server can echo a
 * presented bearer back in free-text fields like its display name, so every
 * server-controlled string that lands in IPC state or persisted settings
 * passes through this with the credential in scope.
 */
export function stripSecrets(text: string, secrets: readonly string[]): string {
  let out = text;
  for (const secret of secrets) {
    if (secret.length > 0) {
      out = out.split(secret).join('[redacted]');
    }
  }
  return out;
}
