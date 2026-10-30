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
 * Fail-closed parsing for anything that crosses a trust boundary: server HTTP
 * responses (parsed in the main process before use) and IPC payloads.
 *
 * Order matters: byte-size gate, JSON parse, prototype-pollution scan,
 * API-version gate, then zod schema validation. Errors are typed canonical
 * errors and never echo raw payload values.
 */
import { z } from 'zod';
import { assertCompatibleApiVersion } from '../apiVersion';
import { buildCanonicalError, CanonicalErrorException } from '../errors';
import { MAX_PAYLOAD_BYTES, assertNoPrototypePollution, assertWithinByteSize } from '../sanitize';
import type { components } from './schema.gen';

/** Parses and validates raw JSON text against `schema`, failing closed. */
export function parseServerJson<Schema extends z.ZodType>(
  raw: string,
  schema: Schema,
  maxBytes: number = MAX_PAYLOAD_BYTES,
): z.output<Schema> {
  assertWithinByteSize(raw, maxBytes);

  let data: unknown;
  try {
    data = JSON.parse(raw);
  } catch {
    throw new CanonicalErrorException(buildCanonicalError('E_MALFORMED_RESPONSE'));
  }

  assertNoPrototypePollution(data);

  if (typeof data === 'object' && data !== null && 'api_version' in data) {
    const version = (data as { api_version: unknown }).api_version;
    assertCompatibleApiVersion(typeof version === 'string' ? version : '');
  }

  return validateWithSchema(data, schema);
}

/** Validates an already-parsed value against `schema`, failing closed. */
export function validateWithSchema<Schema extends z.ZodType>(
  data: unknown,
  schema: Schema,
): z.output<Schema> {
  const result = schema.safeParse(data);
  if (!result.success) {
    // Report only issue paths and codes — never received values. The paths
    // ride in the summary (redacted at build time), not free-form text.
    const paths = [...new Set(result.error.issues.map((i) => i.path.join('.') || '(root)'))]
      .slice(0, 5)
      .join(', ');
    throw new CanonicalErrorException(
      buildCanonicalError('E_SCHEMA_MISMATCH', { params: { paths } }),
    );
  }
  return result.data;
}

export const ServerModelDefaultsSchema = z.object({
  inquiry: z.string().optional(),
  research: z.string().optional(),
  planning: z.string().optional(),
  implementation: z.string().optional(),
  review: z.string().optional(),
  utilities: z.string().optional(),
  kb_build: z.string().optional(),
  automatic_review: z.string().optional(),
});
export const ServerEffortDefaultsSchema = ServerModelDefaultsSchema.omit({
  automatic_review: true,
});
export const ServerCheckpointsSchema = z.object({
  inquiry_review: z.boolean().optional(),
  research_review: z.boolean().optional(),
  design_review: z.boolean().optional(),
  roadmap_review: z.boolean().optional(),
  phase_plan_review: z.boolean().optional(),
  manual_publish: z.boolean().optional(),
  draft_publish: z.boolean().optional(),
});
export const ServerFeatureConfigSchema = z.object({
  models: ServerModelDefaultsSchema,
  effort: ServerEffortDefaultsSchema.optional(),
  inquireness: z.string(),
  checkpoints: ServerCheckpointsSchema,
  pipeline: z.string().optional(),
  input_notifications: z.string().optional(),
  automatic_review_mode: z.string().optional(),
});
export const FeatureConfigResponseSchema = z.object({
  api_version: z.string(),
  feature_id: z.string(),
  current: ServerFeatureConfigSchema,
  defaults: ServerFeatureConfigSchema,
  publishability: z.object({ manual_publish: z.boolean() }),
});
export const RuntimeConfigResponseSchema = z.object({
  api_version: z.string(),
  workspace_roots: z.array(z.string()).optional(),
  feature_defaults: z.object({
    models: ServerModelDefaultsSchema,
    effort: ServerEffortDefaultsSchema.optional(),
    inquireness: z.string().optional(),
    checkpoints: ServerCheckpointsSchema.optional(),
    pipeline: z.string().optional(),
    automatic_review_enabled: z.boolean().optional(),
    dangerously_skip_permissions: z.boolean().optional(),
  }),
  notifications: z.object({ mute_feature_input: z.boolean() }),
});
const ServerModelInfoSchema = z.object({
  id: z.string(),
  display_name: z.string().optional(),
  aliases: z.array(z.string()).optional(),
  category: z.string().optional(),
  context_window: z.number().int().optional(),
  effort_capabilities: z.array(z.string()).optional(),
});
export const ModelCatalogResponseSchema = z.object({
  api_version: z.string(),
  provider_order: z.array(z.string()),
  provider_models: z.record(z.string(), z.array(ServerModelInfoSchema)),
  phase_defaults: ServerModelDefaultsSchema,
  phase_provider_models: z.record(z.string(), z.record(z.string(), z.array(z.string()))),
  chat_default_effort: z
    .record(z.string(), z.enum(['low', 'medium', 'high', 'xhigh', 'max']))
    .optional(),
});
export const ActionResponseSchema = z.object({ api_version: z.string() });
export const WireSnapshotSchema = z
  .object({
    status: z.string(),
    policy: z.string(),
    current_version: z.string(),
    latest_version: z.string().optional(),
    target_version: z.string().optional(),
    latest_release_url: z.string().optional(),
    installation: z.string(),
    remediation: z.string().optional(),
    signature: z.string(),
    method: z.string().optional(),
    stop_active_work: z.boolean().optional(),
    scheduled_for: z.string().nullable(),
    last_check_at: z.string().optional(),
    next_check_at: z.string().optional(),
    error: z.unknown().optional(),
    active_work_summary: z
      .object({
        feature_count: z.number().int().nonnegative(),
        supervisor_active: z.boolean(),
        supervisor_waiting: z.boolean(),
        clone_count: z.number().int().nonnegative(),
        upload_count: z.number().int().nonnegative(),
        origin_check_count: z.number().int().nonnegative(),
        pending_admissions: z.number().int().nonnegative(),
        detection_failed: z.boolean(),
      })
      .passthrough(),
  })
  .passthrough();
export const WireResponseSchema = z.object({ update: WireSnapshotSchema }).passthrough();
export const StageUploadResponseSchema = z.object({
  api_version: z.string(),
  reference: z.string().min(1).max(128),
  kind: z.enum(['image', 'attachment']),
  name: z.string().min(1).max(255),
  size: z.number().int().nonnegative(),
});
export const SseEventEnvelopeSchema = z.object({
  seq: z.number().int().nonnegative().optional(),
  epoch: z.string().max(200).optional(),
  kind: z.string().max(200),
  resource: z.object({
    type: z.string().max(200),
    id: z.string().max(500).optional(),
    feature_id: z.string().max(500).optional(),
    parent_id: z.string().max(500).optional(),
    child_id: z.string().max(500).optional(),
    relationship_deleted: z.boolean().optional(),
  }),
  snapshot_required: z.boolean(),
});

export type SseEventEnvelope = z.output<typeof SseEventEnvelopeSchema>;

// --- Runtime response schemas ---------------------------------------------
// Each schema is type-checked below against the generated OpenAPI types so it
// cannot drift from api/openapi.yaml without failing `npm run check`.

const BuildIdentitySchema = z.object({
  version: z.string(),
  revision: z.string().optional(),
});

export type BuildIdentity = z.output<typeof BuildIdentitySchema>;
export const CompatibilityDeclarationSchema = z.object({
  api_version: z.string(),
  schema_version: z.number().int(),
  min_client_schema: z.number().int(),
  runtime_policy: z.string(),
  server_build: BuildIdentitySchema,
});

type CompatibilityDeclaration = z.output<typeof CompatibilityDeclarationSchema>;

/** Lenient health subset for auth-exempt discovery probes. */
export const ProbeHealthSchema = z.object({
  status: z.string(),
  compatibility: z.unknown().optional(),
  runtime: z.object({ state_dir: z.string() }).optional(),
  name: z.string().max(64).optional().catch(undefined),
});
export type ProbeHealth = z.output<typeof ProbeHealthSchema>;

// --- Blocking attention (GET /api/v1/prompts, /api/v1/permissions) ---------
// These responses are deliberately bounded before they are translated to the
// renderer. Tool input remains data (never HTML or a navigable URL).

const AttentionTextSchema = z.string().max(64 * 1024);
const AttentionIDSchema = z.string().min(1).max(200);
const ServerAskUserOptionSchema = z.object({
  // Matches the server's SafeDisplayText budget for AskUser option labels.
  label: z.string().max(1000).optional(),
  description: AttentionTextSchema.optional(),
  confidence: z.number().min(0).max(1).optional(),
});
const ServerAskUserQuestionSchema = z.object({
  /** One-based position of the question within its prompt; answers are keyed by it. */
  index: z.number().int().min(1),
  question: AttentionTextSchema,
  header: z.string().max(500).optional(),
  multi_select: z.boolean().optional(),
  options: z.array(ServerAskUserOptionSchema).max(100).optional(),
});
const ServerRememberPreviewSchema = z.object({
  pattern: z.string().max(4096),
  scope: z.string().max(4096),
  scope_display: z.string().max(4096),
});
/** Who raised a permission or question: the session's own agent or one of its sub-agents. */
const ServerRequestOriginSchema = z.enum(['root', 'child']);
const ServerControlRequestSchema = z.object({
  request_id: AttentionIDSchema,
  session_id: AttentionIDSchema.optional(),
  feature_id: AttentionIDSchema.optional(),
  phase: z.string().max(200).optional(),
  tool_name: z.string().max(500),
  status: z.string().max(100),
  summary: AttentionTextSchema.optional(),
  input: z.record(z.string().max(200), z.unknown()).optional(),
  questions: z.array(ServerAskUserQuestionSchema).max(100).optional(),
  remember: ServerRememberPreviewSchema.optional(),
  auto_approve: z.object({ would_fast_path: z.boolean() }).optional(),
  automatic_review: z
    .object({
      provider: z.string().max(200),
      model: z.string().max(500),
      outcome: z.string().max(100),
      reason: z.string().max(1000),
      paused: z.boolean(),
      retry_at: z.string().max(100).optional(),
    })
    .optional(),
  waiting_since: z.string().max(100).optional(),
  origin: ServerRequestOriginSchema.optional(),
  child_session_id: AttentionIDSchema.optional(),
});
const ServerHelpQueueSchema = z.object({
  feature_id: AttentionIDSchema,
  session_id: AttentionIDSchema.optional(),
  question: AttentionTextSchema,
  /** 'question' for real help requests, 'coordinating' for a synthetic idle-session entry. */
  kind: z.string().max(100).optional(),
  pending: z.boolean(),
  time: z.string().max(100).optional(),
});
const ServerNeedUserInputVerificationBlockerSchema = z.object({
  item_id: AttentionIDSchema,
  name: AttentionTextSchema,
  repo_name: z.string().max(500).optional(),
  command: AttentionTextSchema,
  reason: AttentionTextSchema,
  capabilities: z.array(AttentionTextSchema).max(20),
  remediation: AttentionTextSchema,
});
const ServerNeedUserInputVerificationSchema = z.object({
  blockers: z.array(ServerNeedUserInputVerificationBlockerSchema).max(100),
  allowed_actions: z.array(z.string().max(50)).max(3),
});
const ServerNeedUserInputGateSchema = z.object({
  feature_id: AttentionIDSchema.optional(),
  open: z.boolean(),
  scope: z.string().max(100).optional(),
  repo_name: z.string().max(500).optional(),
  iteration: z.number().int().nonnegative().optional(),
  summary: AttentionTextSchema.optional(),
  questions: z
    .array(
      z.object({
        // The stored one-based gate index; drafts and answers are keyed by it.
        index: z.number().int().min(1),
        prompt: AttentionTextSchema.optional(),
        answer: AttentionTextSchema.optional(),
      }),
    )
    .max(100)
    .optional(),
  verification: ServerNeedUserInputVerificationSchema.optional(),
  waiting_since: z.string().max(100).optional(),
});
export const PromptSnapshotResponseSchema = z.object({
  api_version: z.string(),
  ask_user_questions: z.array(ServerControlRequestSchema).max(1000),
  help_queue: z.array(ServerHelpQueueSchema).max(1000),
  need_user_inputs: z.array(ServerNeedUserInputGateSchema).max(1000),
});
export const PermissionSnapshotResponseSchema = z.object({
  api_version: z.string(),
  requests: z.array(ServerControlRequestSchema).max(1000),
});
export type ServerPermissionSnapshot = z.output<typeof PermissionSnapshotResponseSchema>;

// --- Review sessions -------------------------------------------------------
// These are deliberately narrow, renderer-independent views of the review
// protocol. The main process maps them into its IPC contract after checking
// the generated OpenAPI shape below.
export const ReviewSessionResponseSchema = z.object({
  api_version: z.string(),
  feature_id: z.string().min(1),
  review_id: z.string().min(1),
  review_mode: z.string().min(1),
  target_phase: z.string().min(1),
  run_number: z.number().int().nonnegative(),
  artifact_id: z.string().min(1),
  text: z.string(),
  draft_revision: z.string().min(1),
  source_revision: z.string().min(1),
  can_iterate: z.boolean(),
});
export type ServerReviewSession = z.output<typeof ReviewSessionResponseSchema>;
export const ReviewDraftValidationResponseSchema = z.object({
  api_version: z.string(),
  feature_id: z.string().min(1),
  review_id: z.string().min(1),
  applicable: z.boolean(),
  valid: z.boolean(),
  revision: z.string().min(1),
  findings: z.array(z.object({ code: z.string().min(1), message: z.string().min(1) })).max(100),
});
export type ServerReviewDraftValidation = z.output<typeof ReviewDraftValidationResponseSchema>;
export const ReviewDecisionResponseSchema = z.object({
  api_version: z.string(),
  feature_id: z.string().min(1),
  review_id: z.string().min(1),
  decision: z.string().optional(),
  result: z.string().min(1),
});
export type ServerReviewDecision = z.output<typeof ReviewDecisionResponseSchema>;

// --- Runtime config (GET /api/v1/config/runtime) — workspace-roots subset ---
// Only the fields the desktop setup flow consumes; the full response has
// many more, which z.object tolerates and strips.

// --- Canonical server error body (ErrorResponse) -----------------------------
// Strict on purpose: the canonical shape is a real contract, so unknown
// properties, an unknown class, or the pre-canonical `{code,message,status}`
// body all fail parsing and degrade to the main-process transport error.

/**
 * Canonical catalog-rendered error, mirroring the generated Error component.
 * The code must be either a desktop-local `E_UPPER_SNAKE` code or a server
 * lowercase snake_case code — the two families are disjoint by construction.
 */
export const CanonicalErrorSchema = z.strictObject({
  code: z
    .string()
    .min(1)
    .regex(/^(?:E_[A-Z0-9_]+|[a-z][a-z0-9]+(?:_[a-z0-9]{2,})*)$/),
  class: z.enum(['blocking', 'needs_action', 'warning']),
  title: z.string().min(1),
  summary: z.string().min(1),
  remediation: z
    .strictObject({
      hint: z.string().optional(),
      actions: z.array(z.string().min(1)).max(16).optional(),
    })
    .optional(),
  context: z
    .strictObject({
      repositories: z
        .array(
          z.strictObject({
            name: z.string().min(1),
            branch: z.string().optional(),
            rebase_target: z.string().optional(),
            remote_only_commits: z.number().int().nonnegative().optional(),
            // Uncapped on purpose: the Go producers store conflict lists
            // unbounded, so any client-side cap would make one large
            // conflict unparse the whole payload.
            conflict_files: z.array(z.string()).optional(),
            dirty_files: z.array(z.string()).max(500).optional(),
            parent_anchor_sha: z.string().optional(),
            expected_ref_sha: z.string().optional(),
            child_head_sha: z.string().optional(),
            candidate_sha: z.string().optional(),
            merge_head: z.string().optional(),
            observed_sha: z.string().optional(),
          }),
        )
        .max(100)
        .optional(),
      phase: z
        .strictObject({
          name: z.string().min(1),
          iteration: z.number().int().nonnegative().optional(),
        })
        .optional(),
      command: z
        .strictObject({
          exit_code: z.number().int().optional(),
          log_paths: z.array(z.string()).max(50).optional(),
        })
        .optional(),
      setup_task: z
        .strictObject({
          key: z.string().min(1),
          kind: z.string().min(1),
          label: z.string().min(1),
        })
        .optional(),
    })
    .optional(),
  diagnostics: z.string().optional(),
  // Launch failures may include the settings that were rolled back. This
  // field is accepted on the error response as well as on the state read model.
  attempted_settings: z
    .strictObject({
      harness: z.string().max(100),
      model: z.string().max(200),
      effort: z.string().max(40),
    })
    .optional(),
});

export type CanonicalError = z.output<typeof CanonicalErrorSchema>;

/** The full non-2xx response envelope carrying one canonical error. */
export const CanonicalErrorResponseSchema = z.strictObject({
  api_version: z.string(),
  error: CanonicalErrorSchema,
});

type CanonicalErrorResponse = z.output<typeof CanonicalErrorResponseSchema>;

// --- Readiness (GET /api/v1/readiness, POST /api/v1/readiness/refresh) -----
// Readiness issues are the canonical catalog-rendered error for their code:
// the strict CanonicalErrorSchema accepts them and rejects the pre-canonical
// `{code, message, remedy}` shape.

const RepositoryIdentitySchema = z.strictObject({
  path: z.string().min(1).max(4096),
  common_dir: z.string().min(1).max(4096),
  device: z.string().regex(/^[0-9]{1,20}$/),
  inode: z.string().regex(/^[0-9]{1,20}$/),
  birth_time: z.string().max(64).optional(),
});
export const ReadinessResponseSchema = z.object({
  api_version: z.string(),
  ready: z.boolean(),
  probed_at: z.string().optional(),
  providers: z.array(
    z.object({
      name: z.string(),
      installed: z.boolean(),
      version: z.string().optional(),
      ready: z.boolean(),
      issue: CanonicalErrorSchema.optional(),
      // Advisory endpoint reachability; never affects `ready`.
      endpoint_reachable: z.boolean().optional(),
      warning: CanonicalErrorSchema.optional(),
    }),
  ),
  models: z.object({
    available: z.boolean(),
    models: z.array(z.string()).optional(),
    issue: CanonicalErrorSchema.optional(),
  }),
  configuration: z.object({
    valid: z.boolean(),
    issue: CanonicalErrorSchema.optional(),
  }),
  workspace: z.object({
    roots: z.array(
      z.object({
        path: z.string(),
        valid: z.boolean(),
        issue: CanonicalErrorSchema.optional(),
        clone_eligible: z.boolean(),
        clone_issue: CanonicalErrorSchema.optional(),
      }),
    ),
    repositories: z.array(
      z.object({
        name: z.string(),
        path: z.string(),
        valid: z.boolean(),
        issue: CanonicalErrorSchema.optional(),
        feature_ready: z.boolean(),
        // Server-resolved repository identity; malformed identity data is
        // rejected rather than half-parsed, and a missing identity simply
        // leaves the repository unselectable upstream.
        identity: RepositoryIdentitySchema.optional(),
      }),
    ),
  }),
  issues: z.array(CanonicalErrorSchema).optional(),
});

export type ReadinessResponse = z.output<typeof ReadinessResponseSchema>;
export const RuntimeReadinessResponseSchema = ReadinessResponseSchema.omit({ workspace: true });
export type RuntimeReadinessResponse = z.output<typeof RuntimeReadinessResponseSchema>;
export const ProviderModelRefreshResponseSchema = z.object({
  api_version: z.string(),
  readiness: ReadinessResponseSchema,
  catalog: ModelCatalogResponseSchema,
});
export const RepositorySourcesResponseSchema = z.object({
  api_version: z.string(),
  repositories: z
    .array(
      z.strictObject({
        repo_key: z.string().min(1),
        identity: RepositoryIdentitySchema,
        mode: z.enum(['default', 'current']),
        kind: z.enum(['branch', 'detached']),
        branch: z.string().optional(),
        observed_sha: z.string().regex(/^[0-9a-f]{40,64}$/),
      }),
    )
    .min(1)
    .max(32),
});
const OriginCommitShaSchema = z.string().regex(/^[0-9a-f]{40,64}$/);
const OriginComparisonWireSchema = z.strictObject({
  status: z.enum(['up_to_date', 'behind', 'ahead', 'diverged']),
  local_sha: OriginCommitShaSchema,
  fetched_sha: OriginCommitShaSchema,
  origin_branch: z.string().min(1),
  ahead_count: z.number().int().min(0),
  behind_count: z.number().int().min(0),
  checked_at: z.string().min(1),
});
const RepositoryOriginStatusWireSchema = z.strictObject({
  repo_key: z.string().min(1),
  identity: RepositoryIdentitySchema,
  mode: z.enum(['default', 'current']),
  kind: z.enum(['branch', 'detached']),
  branch: z.string().optional(),
  commit: OriginCommitShaSchema.optional(),
  local_sha: OriginCommitShaSchema.optional(),
  origin_branch: z.string().optional(),
  fetched_sha: OriginCommitShaSchema.optional(),
  checked_at: z.string().optional(),
  status: z.enum([
    'checking',
    'up_to_date',
    'behind',
    'ahead',
    'diverged',
    'no_origin',
    'remote_branch_missing',
    'other_upstream',
    'detached',
    'local_base_missing',
    'unknown',
  ]),
  ahead_count: z.number().int().min(0).optional(),
  behind_count: z.number().int().min(0).optional(),
  issue: CanonicalErrorSchema.optional(),
  stale_comparison: OriginComparisonWireSchema.optional(),
  update_eligible: z.boolean().optional(),
  update_blockers: z
    .array(
      z.enum([
        'local_not_behind',
        'dirty_target_checkout',
        'git_operation_in_progress',
        'branch_checked_out_in_worktree',
        'checkout_operation_in_progress',
        'ignored_path_collision',
        'checkout_uninspectable',
        'comparison_unavailable',
      ]),
    )
    .optional(),
  checkout_head_ref: z.string().min(1).max(512).optional(),
  checkout_head_sha: OriginCommitShaSchema.optional(),
});
export const RepositoryOriginStatusResponseSchema = z.object({
  api_version: z.string(),
  repositories: z.array(RepositoryOriginStatusWireSchema).min(1).max(32),
});

export type RepositoryOriginStatusWireResponse = z.output<
  typeof RepositoryOriginStatusResponseSchema
>;
export const RepositoryUpdateSourceResponseSchema = z.object({
  api_version: z.string(),
  result: z.enum(['updated', 'already_up_to_date', 'stale']),
  reason: z
    .enum([
      'checkout_changed',
      'source_changed',
      'mapping_changed',
      'local_tip_changed',
      'origin_tip_changed',
      'origin_branch_missing',
      'not_fast_forward',
      'branch_checked_out',
      'dirty_checkout',
      'checkout_operation_in_progress',
      'ignored_path_collision',
      'checkout_conflict',
    ])
    .optional(),
  repo_key: z.string().min(1),
  identity: RepositoryIdentitySchema,
  mode: z.enum(['default', 'current']),
  branch: z.string().min(1),
  origin_branch: z.string().min(1),
  previous_sha: OriginCommitShaSchema.optional(),
  local_sha: OriginCommitShaSchema.optional(),
  fetched_sha: OriginCommitShaSchema.optional(),
  status: RepositoryOriginStatusWireSchema.optional(),
});
const RepositorySourceReconcileCheckoutWireSchema = z.strictObject({
  state: z.enum(['clean', 'dirty', 'operation_in_progress', 'unobserved']),
  // The server sends an empty head_ref when the checkout HEAD could not be
  // read; only an absent head_sha (never an empty one) is malformed.
  head_ref: z.string().max(512).optional(),
  head_sha: z
    .string()
    .regex(/^[0-9a-f]{40,64}$/)
    .optional(),
});
export const RepositorySourceReconcileResponseSchema = z.object({
  api_version: z.string(),
  outcome: z.enum([
    'expected_target_present',
    'original_tip_remains',
    'local_state_changed',
    'branch_missing',
  ]),
  repo_key: z.string().min(1),
  identity: RepositoryIdentitySchema,
  mode: z.enum(['default', 'current']),
  branch: z.string().min(1),
  origin_branch: z.string().min(1),
  local_sha: OriginCommitShaSchema.optional(),
  selection: z
    .strictObject({
      repo_key: z.string().min(1),
      identity: RepositoryIdentitySchema,
      mode: z.enum(['default', 'current']),
      kind: z.enum(['branch', 'detached']),
      branch: z.string().optional(),
      observed_sha: z.string().regex(/^[0-9a-f]{40,64}$/),
    })
    .optional(),
  checkout: RepositorySourceReconcileCheckoutWireSchema.optional(),
});

// --- Clone operations (POST/GET /api/v1/workspace/repositories/clone) ------
// Authoritative snapshots from the server-owned clone lifecycle. State,
// pending outcome and progress come from the durable record, never from
// git text or transport completion.

const CloneOperationDTOSchema = z.object({
  id: z.string(),
  state: z.enum([
    'accepted',
    'running',
    'finalizing',
    'cancelling',
    'succeeded',
    'failed',
    'cancelled',
    'interrupted',
    'cleanup_pending',
  ]),
  stage: z.string().optional(),
  progress: z.string().optional(),
  remote_url: z.string(),
  root_path: z.string(),
  destination: z.string(),
  destination_path: z.string(),
  idempotency_key: z.string(),
  pending_outcome: z.enum(['failed', 'cancelled', 'interrupted']).optional(),
  cancel_requested: z.boolean(),
  cancel_requested_at: z.string().optional(),
  cleanup_issue: z.string().optional(),
  error: CanonicalErrorSchema.optional(),
  published: z
    .object({
      repo_key: z.string(),
      path: z.string(),
      has_head: z.boolean(),
      published_at: z.string(),
      // Server-resolved identity of the repository actually published;
      // absent for older records or an unprovable destination.
      identity: RepositoryIdentitySchema.optional(),
    })
    .optional(),
  created_at: z.string(),
  updated_at: z.string(),
  terminal_at: z.string().optional(),
  resolved_at: z.string().optional(),
});

export type CloneOperationDTO = z.output<typeof CloneOperationDTOSchema>;
export const CloneActionResponseSchema = z.object({
  api_version: z.string(),
  result: z.string(),
  operation: CloneOperationDTOSchema,
});

export type CloneActionResponse = z.output<typeof CloneActionResponseSchema>;
export const CloneOperationResponseSchema = z.object({
  api_version: z.string(),
  operation: CloneOperationDTOSchema,
});

type CloneOperationResponse = z.output<typeof CloneOperationResponseSchema>;
export const CloneOperationListResponseSchema = z.object({
  api_version: z.string(),
  operations: z.array(CloneOperationDTOSchema),
  next_page_token: z.string().optional(),
});

export type CloneOperationListResponse = z.output<typeof CloneOperationListResponseSchema>;

// --- Repository creation (POST /api/v1/workspace/repositories/create) -------
// The synchronous creation result: the actual collision-safe repository
// key from discovery and the server-resolved identity of the repository
// that was published, pinned by the durable publication marker.

export const CreateRepositoryResponseSchema = z.object({
  api_version: z.string(),
  result: z.string(),
  repository: z.object({
    repo_key: z.string(),
    path: z.string(),
    has_head: z.boolean(),
    root: z.string(),
    // Absent only for a replayed success whose destination no longer
    // matches the publication marker; such a result is never adopted.
    identity: RepositoryIdentitySchema.optional(),
  }),
});

export type CreateRepositoryResponse = z.output<typeof CreateRepositoryResponseSchema>;

// --- Repository initialization (POST /api/v1/workspace/repositories/initialize)
// The synchronous explicit-initialization result: the refreshed repository
// under its actual catalog key and server-resolved identity. Both result
// values are successes ("already_initialized" is a refresh-only success).

export const InitializeRepositoryResponseSchema = z.object({
  api_version: z.string(),
  result: z.enum(['initialized', 'already_initialized']),
  repository: z.object({
    repo_key: z.string(),
    path: z.string(),
    has_head: z.boolean(),
    root: z.string(),
    identity: RepositoryIdentitySchema.optional(),
  }),
});

export type InitializeRepositoryResponse = z.output<typeof InitializeRepositoryResponseSchema>;

// --- Features (GET/POST /api/v1/features, GET /api/v1/features/{id}) --------
// Lenient subsets: z.object tolerates and strips fields this view does not
// consume yet.

const ServerSetupTaskSchema = z.object({
  key: z.string(),
  kind: z.string(),
  label: z.string().optional(),
  repo: z.string().optional(),
  status: z.string(),
  branch: z.string().optional(),
  exact_sha: z
    .string()
    .regex(/^[0-9a-f]{40,64}$/)
    .optional(),
  attempt: z.number().int().optional(),
  // Canonical error rendering the task's stored failure record; absent when
  // the task has not failed.
  error: CanonicalErrorSchema.optional(),
  // The removed last-error string must never reappear on the wire.
  last_error: z.never().optional(),
});

type ServerSetupTask = z.output<typeof ServerSetupTaskSchema>;
const ServerSetupSchema = z.object({
  status: z.string(),
  attempt: z.number().int().optional(),
  tasks: z.record(z.string(), ServerSetupTaskSchema).optional(),
  task_order: z.array(z.string()).optional(),
  // The removed last-error string must never reappear on the wire.
  last_error: z.never().optional(),
});

export type ServerSetup = z.output<typeof ServerSetupSchema>;
const ServerActionImpactSchema = z.object({
  kind: z.enum(['child_discard', 'parent_cascade_delete']),
  subject: z.object({ id: z.string(), name: z.string() }),
  categories: z.array(z.object({ key: z.string(), label: z.string(), items: z.array(z.string()) })),
  retained: z.array(z.string()),
});
const ServerActionSchema = z.object({
  id: z.string(),
  enabled: z.boolean(),
  required_inputs: z
    .array(
      z.object({
        name: z.string(),
        options: z.array(z.string()).optional(),
      }),
    )
    .optional(),
  disabled_reasons: z.array(z.object({ code: z.string(), message: z.string() })).optional(),
  impact_preview: ServerActionImpactSchema.optional(),
});

/**
 * Per-entry cap on a preserved child diff summary, matching the server's own
 * budget (internal/feature/diff_summary.go DiffSummaryBudget), counted in
 * string length rather than UTF-8 bytes. Bounding the
 * field rejects one oversized summary instead of letting an unbounded history
 * grow the whole payload past the boundary limit.
 */
const DIFF_SUMMARY_MAX_BYTES = 256 * 1024;
const ServerRelationshipChildSchema = z.object({
  id: z.string(),
  name: z.string(),
  kind: z.string(),
  display_token: z.string(),
  display_state: z.string(),
  pipeline: z.string(),
  status: z.string(),
  setup_status: z.string().optional(),
  relationship_state: z.string().optional(),
  outcome: z.enum(['completed', 'discarded']).optional(),
  started_at: z.string(),
  closed_at: z.string().optional(),
  cost: z.object({ total_usd: z.number(), by_phase: z.record(z.string(), z.number()) }),
  integration_state: z.string(),
  // Canonical error rendering the child's stored integration attention
  // record; absent when integration is not parked. Strict canonical
  // contract, same as ErrorResponse.
  attention: CanonicalErrorSchema.optional(),
  // Canonical warning-class errors for this child's stored cleanup and
  // review-feedback tail records.
  warnings: z.array(CanonicalErrorSchema).max(100),
  // The removed cleanup-warning strings must never reappear on the wire.
  cleanup_warnings: z.never().optional(),
  // The removed last-error string must never reappear on the wire.
  last_error: z.never().optional(),
  diff_summary: z.string().max(DIFF_SUMMARY_MAX_BYTES).optional(),
  // Set on list projections, which omit the body itself; the detail route
  // carries diff_summary instead.
  has_diff_summary: z.boolean().optional(),
});
export type ServerRelationshipChild = z.output<typeof ServerRelationshipChildSchema>;

// --- Owned errors --------------------------------------------------------------
// Strict on purpose: the entry is a real contract between the server's
// projection and every presence surface, so an unknown key, a warning-class
// error, or a reference whose keys are missing for or foreign to its scope
// fails parsing at the trust boundary instead of reaching the renderer.

/** The keys each scope requires; `transaction` additionally allows repository. */
const SERVER_ERROR_REFERENCE_REQUIRED_KEYS = {
  run: ['feature_id'],
  transaction: ['feature_id'],
  repository: ['feature_id', 'repository'],
  setup: ['feature_id', 'task_key'],
  recovery: ['snapshot_id', 'key'],
} as const;

/** The keys each scope rejects even when present and well-formed. */
const SERVER_ERROR_REFERENCE_FORBIDDEN_KEYS = {
  run: ['repository', 'task_key', 'snapshot_id', 'key'],
  transaction: ['task_key', 'snapshot_id', 'key'],
  repository: ['task_key', 'snapshot_id', 'key'],
  setup: ['repository', 'snapshot_id', 'key'],
  recovery: ['feature_id', 'repository', 'task_key'],
} as const;

/** Owner reference mirroring the generated ErrorReference component (snake_case). */
const ServerErrorReferenceSchema = z
  .strictObject({
    scope: z.enum(['run', 'transaction', 'repository', 'setup', 'recovery']),
    code: z.string().min(1).max(128),
    feature_id: z.string().min(1).max(200).optional(),
    repository: z.string().min(1).max(200).optional(),
    task_key: z.string().min(1).max(200).optional(),
    snapshot_id: z.string().min(1).max(128).optional(),
    key: z.string().min(1).max(500).optional(),
  })
  .superRefine((reference, ctx) => {
    for (const field of SERVER_ERROR_REFERENCE_REQUIRED_KEYS[reference.scope]) {
      if (reference[field] === undefined) {
        ctx.addIssue({
          code: 'custom',
          path: [field],
          message: `A "${reference.scope}" error reference requires "${field}".`,
        });
      }
    }
    for (const field of SERVER_ERROR_REFERENCE_FORBIDDEN_KEYS[reference.scope]) {
      if (reference[field] !== undefined) {
        ctx.addIssue({
          code: 'custom',
          path: [field],
          message: `"${field}" does not belong to a "${reference.scope}" error reference.`,
        });
      }
    }
  });

/** One owned-error entry on the feature summary, mirroring the generated OwnedError component. */
const ServerOwnedErrorSchema = z
  .strictObject({
    ref: ServerErrorReferenceSchema,
    error: CanonicalErrorSchema,
  })
  .superRefine((entry, ctx) => {
    // Warnings never own a presence surface; the entry's type stays as wide
    // as the generated component so the compile-time drift guard holds, and
    // a warning-class entry is rejected here at the boundary instead.
    if (entry.error.class === 'warning') {
      ctx.addIssue({
        code: 'custom',
        path: ['error', 'class'],
        message: 'An owned error never carries the warning class.',
      });
    }
  });
export type ServerOwnedError = z.output<typeof ServerOwnedErrorSchema>;
const ServerFeatureSummarySchema = z.object({
  id: z.string(),
  name: z.string(),
  slug: z.string(),
  status: z.string(),
  current_phase: z.string(),
  repos: z.array(z.string()),
  created_at: z.string(),
  active_run: z.number().int().nonnegative(),
  run_count: z.number().int().nonnegative(),
  progress: z.object({
    current_phase_status: z.string().optional(),
    current_iteration: z.number().int().nonnegative().optional(),
    current_roadmap_phase: z.number().int().nonnegative().optional(),
    total_roadmap_phases: z.number().int().nonnegative().optional(),
  }),
  // Canonical warning-class errors for this feature; absent when there is
  // nothing to warn about.
  warnings: z.array(CanonicalErrorSchema).max(100).optional(),
  // Current non-warning errors this feature or its active child owns; absent
  // when there are none.
  errors: z.array(ServerOwnedErrorSchema).max(100).optional(),
  active_child: ServerRelationshipChildSchema.optional(),
  child_history: z.array(ServerRelationshipChildSchema).optional(),
  child_history_total: z.number().int().nonnegative().optional(),
  child_history_truncated: z.boolean().optional(),
});
const ServerRepoStatusSchema = z.object({
  name: z.string(),
  publishable: z.boolean(),
  touched: z.boolean().optional(),
  pr_url: z.string().optional(),
  freshness: z.string().optional(),
  // Canonical error rendering the repository's stored publish-failure
  // record; absent when the repository has not failed.
  error: CanonicalErrorSchema.optional(),
  // The removed last-error string must never reappear on the wire.
  last_error: z.never().optional(),
  rebase_status: z.string().optional(),
  rebase_target: z.string().optional(),
  conflict_files: z.array(z.string()).optional(),
});
type ServerRepoStatus = z.output<typeof ServerRepoStatusSchema>;

const ServerNeedUserInputGateDetailSchema = z.object({
  feature_id: z.string().optional(),
  open: z.boolean(),
  scope: z.string().optional(),
  repo_name: z.string().optional(),
  iteration: z.number().int().nonnegative().optional(),
  summary: z.string().optional(),
  questions: z
    .array(
      z.object({
        index: z.number().int().min(1),
        prompt: z.string().optional(),
        answer: z.string().optional(),
      }),
    )
    .optional(),
  verification: ServerNeedUserInputVerificationSchema.optional(),
});
type ServerNeedUserInputGateDetail = z.output<typeof ServerNeedUserInputGateDetailSchema>;

const ServerReviewFeedbackCommentSchema = z.object({
  repo: z.string(),
  id: z.number().int(),
  type: z.enum(['review', 'issue', 'review_body']),
  path: z.string().optional(),
  line: z.number().int().optional(),
  author: z.string().optional(),
  body: z.string().optional(),
  diff_hunk: z.string().optional(),
  in_reply_to_id: z.number().int().optional(),
  created_at: z.string().optional(),
});
export type ServerReviewFeedbackComment = z.output<typeof ServerReviewFeedbackCommentSchema>;

/** One comment inside the server-owned, revisioned review-feedback pending draft. */
const ServerReviewFeedbackDraftCommentSchema = ServerReviewFeedbackCommentSchema.extend({
  stable_ref: z.string(),
  selected: z.boolean(),
});
export type ServerReviewFeedbackDraftComment = z.output<
  typeof ServerReviewFeedbackDraftCommentSchema
>;

const ServerReviewFeedbackRepoCommentsSchema = z.object({
  repo: z.string(),
  pr_url: z.string(),
  comments: z.array(ServerReviewFeedbackDraftCommentSchema),
});

const ServerFeatureDetailSchema = ServerFeatureSummarySchema.extend({
  description: z.string().optional(),
  wait_reason: z.string().optional(),
  pipeline: z.string().optional(),
  risk_level: z.string().optional(),
  exit_criteria: z.string().optional(),
  active_run_detail: z
    .object({
      setup: ServerSetupSchema.optional(),
      roadmap_phase: z.number().int().nonnegative().optional(),
      roadmap_total: z.number().int().nonnegative().optional(),
      iteration: z.number().int().nonnegative().optional(),
      phase_status: z.string().optional(),
    })
    .optional(),
  actions: z.array(ServerActionSchema),
  automatic_review: z.object({
    mode: z.enum(['default', 'enabled', 'disabled', 'dangerously_skip_permissions']),
    enabled: z.boolean(),
    source: z.enum(['global', 'feature']),
  }),
  repo_status: z.array(ServerRepoStatusSchema).optional(),
  review_gate: z.object({
    reviewing_gate: z.boolean(),
    review_fixing: z.boolean(),
    validating_plan: z.boolean(),
    validator_statuses: z.record(z.string(), z.string()).optional(),
    validator_errors: z.record(z.string(), z.string()).optional(),
  }),
  need_user_input: ServerNeedUserInputGateDetailSchema.optional(),
  // The durable run failure, rendered by the server through the error
  // catalog at read time; strict canonical contract, same as ErrorResponse.
  failure: CanonicalErrorSchema.optional(),
  // Pending usage-limit auto-resume of a Failed feature; the server retries
  // the feature at resume_at unless pause-stop parks it first.
  auto_resume: z
    .object({
      provider: z.enum(['claude', 'codex']),
      reset_at: z.string(),
      resume_at: z.string(),
      scheduled_at: z.string(),
    })
    .optional(),
  verification_items: z.array(z.object({ name: z.string(), state: z.string() })).optional(),
  timing: z.object({ total_seconds: z.number().int().nonnegative() }).optional(),
  parent_id: z.string().optional(),
  parent_kind: z.string().optional(),
  active: z.boolean().optional(),
  setup_complete: z.boolean().optional(),
  close_outcome: z.string().optional(),
  closed_at: z.string().optional(),
  transaction: z
    .strictObject({
      phase: z.string().optional(),
      // Canonical error rendering the journal's single stored attention
      // record; absent when integration is not parked.
      attention: CanonicalErrorSchema.optional(),
      entries: z
        .array(
          // Strict: the entry block is a fully-specified trust-boundary
          // contract mirroring the generated RepoTransactionEntry, so the
          // deleted diagnostics, conflict-file, and dirty shapes fail
          // parsing instead of slipping through as unknown keys.
          z.strictObject({
            repo: z.string().optional(),
            parent_branch: z.string().optional(),
            parent_anchor_sha: z.string().optional(),
            expected_ref_sha: z.string().optional(),
            child_head_sha: z.string().optional(),
            candidate_sha: z.string().optional(),
            merge_head: z.string().optional(),
            prep_state: z.string().optional(),
            apply_state: z.string().optional(),
            observed_sha: z.string().optional(),
            pending_sync: z.boolean().optional(),
          }),
        )
        .optional(),
    })
    .optional(),
  relationship: ServerRelationshipChildSchema.optional(),
  review_feedback: z.array(ServerReviewFeedbackCommentSchema).optional(),
});

export type ServerFeatureDetail = z.output<typeof ServerFeatureDetailSchema>;

export const FeatureListResponseSchema = z.object({
  api_version: z.string(),
  features: z.array(ServerFeatureSummarySchema),
  // Canonical warning errors for feature files that could not be loaded at
  // list level; absent when every feature loaded.
  warnings: z.array(CanonicalErrorSchema).max(100).optional(),
});

type FeatureListResponse = z.output<typeof FeatureListResponseSchema>;

export const FeatureDetailResponseSchema = z.object({
  api_version: z.string(),
  feature: ServerFeatureDetailSchema,
});

type FeatureDetailResponse = z.output<typeof FeatureDetailResponseSchema>;

// --- Testing contract (GET /features/{id}/testing-contract) ----------------

const ServerTestingContractDispositionSchema = z.object({
  status: z.string().min(1).max(100),
  reason: z.string().max(2000).optional(),
  changed_by: z.string().max(200).optional(),
});
const ServerTestingContractItemSchema = z.object({
  item_id: AttentionIDSchema,
  source: z.string().min(1).max(100),
  owner: z.string().min(1).max(100),
  repo: z.string().max(500).optional(),
  name: AttentionTextSchema,
  command: AttentionTextSchema,
  required: z.boolean(),
  allow_substitution: z.boolean(),
  allow_blocked: z.boolean(),
  allow_waiver: z.boolean(),
  disposition: ServerTestingContractDispositionSchema.optional(),
  capabilities: z.array(z.string().max(500)).max(20),
});
export type ServerTestingContractItem = z.output<typeof ServerTestingContractItemSchema>;
export const TestingContractResponseSchema = z.object({
  api_version: z.string(),
  feature_id: AttentionIDSchema,
  active_run: z.number().int().positive(),
  roadmap_phase: z.number().int().positive(),
  revision: z.number().int().positive(),
  items: z.array(ServerTestingContractItemSchema).max(500),
});
type TestingContractResponse = z.output<typeof TestingContractResponseSchema>;

// --- Run listing (GET /runs, GET /runs/{n}, GET /runs/{n}/sessions) --------

const ServerRunSummarySchema = z.object({
  run_number: z.number().int().nonnegative(),
  started_at: z.string().optional(),
  sealed_at: z.string().optional(),
  seal_reason: z.string().optional(),
  current_phase: z.string().optional(),
  phase_status: z.string().optional(),
  iteration: z.number().int().nonnegative().optional(),
  roadmap_phase: z.number().int().nonnegative().optional(),
  roadmap_total: z.number().int().nonnegative().optional(),
  pending_review_phase: z.string().optional(),
  is_rewind: z.boolean().optional(),
  artifact_count: z.number().int().nonnegative(),
  has_need_user_gate: z.boolean().optional(),
});
export type ServerRunSummary = z.output<typeof ServerRunSummarySchema>;

const ServerRunDetailSchema = ServerRunSummarySchema.extend({
  rewind_target: z.string().optional(),
  rewind_roadmap_phase: z.number().int().nonnegative().optional(),
  carried_from_run: z.number().int().nonnegative().optional(),
  carried_phases: z.array(z.string()).optional(),
  backup_branch_repos: z.array(z.string()).optional(),
  committing: z.boolean().optional(),
  timing: z
    .object({ total_seconds: z.number().int(), by_phase: z.record(z.string(), z.number()) })
    .optional(),
  cost: z.object({ total_usd: z.number(), by_phase: z.record(z.string(), z.number()) }).optional(),
});
export type ServerRunDetail = z.output<typeof ServerRunDetailSchema>;

export const RunListResponseSchema = z.object({
  api_version: z.string(),
  runs: z.array(ServerRunSummarySchema),
  page: z.number().int().positive(),
  page_size: z.number().int().positive(),
  total: z.number().int().nonnegative(),
  total_pages: z.number().int().nonnegative(),
});
type RunListResponse = z.output<typeof RunListResponseSchema>;

export const RunDetailResponseSchema = z.object({
  api_version: z.string(),
  run: ServerRunDetailSchema,
});
type RunDetailResponse = z.output<typeof RunDetailResponseSchema>;

// RunSessionListResponse is declared after ServerSessionSummarySchema below.

// --- Artifacts + logs (run-scoped) ------------------------------------------

const ServerArtifactSchema = z.object({
  id: z.string(),
  type: z.string().optional(),
  category: z.string().optional(),
  run_number: z.number().int().nonnegative(),
  phase: z.string().optional(),
  size: z.number().int().nonnegative().optional(),
  modified_at: z.string().optional(),
  content_available: z.boolean().optional(),
});
export type ServerArtifact = z.output<typeof ServerArtifactSchema>;

export const ArtifactListResponseSchema = z.object({
  api_version: z.string(),
  artifacts: z.array(ServerArtifactSchema),
});
type ArtifactListResponse = z.output<typeof ArtifactListResponseSchema>;

const ServerRunLogSchema = z.object({
  id: z.string(),
  path: z.string(),
  size: z.number().int().nonnegative(),
  modified_at: z.string(),
});

export const RunLogListResponseSchema = z.object({
  api_version: z.string(),
  logs: z.array(ServerRunLogSchema),
});

export const TextContentResponseSchema = z.object({
  api_version: z.string(),
  id: z.string(),
  offset: z.number().int().nonnegative(),
  limit: z.number().int().positive(),
  size: z.number().int().nonnegative(),
  text: z.string(),
  truncated: z.boolean(),
});
type TextContentResponse = z.output<typeof TextContentResponseSchema>;

// --- Rewind preview + execution --------------------------------------------

const ServerRewindChoiceSchema = z.object({
  phase: z.string(),
  escalates_to: z.string().optional(),
  override_phase: z.string().optional(),
});

const ServerRewindPRConsequenceSchema = z.object({
  repo: z.string(),
  pr_url: z.string(),
});

const ServerRewindWorktreeConsequenceSchema = z.object({
  repo: z.string(),
  reset_kind: z.enum(['anchor', 'base', 'base-local', 'none']),
});

export const RewindPreviewResponseSchema = z.object({
  api_version: z.string(),
  eligible: z.boolean(),
  source_run_number: z.number().int().nonnegative(),
  source_revision: z.string(),
  target_phase: z.string(),
  effective_phase: z.string(),
  roadmap_phase: z.number().int().nonnegative().optional(),
  upgrade_pipeline: z.string().optional(),
  valid_phases: z.array(ServerRewindChoiceSchema).optional(),
  valid_roadmap_phases: z.array(z.number().int().positive()).optional(),
  upgrade_pipeline_options: z.array(z.string()).optional(),
  carried_phases: z.array(z.string()).optional(),
  carried_from_run: z.number().int().nonnegative().optional(),
  pr_consequences: z.array(ServerRewindPRConsequenceSchema).optional(),
  worktree_consequences: z.array(ServerRewindWorktreeConsequenceSchema).optional(),
  backup_branch_repos: z.array(z.string()).optional(),
  validation_findings: z.array(z.string()).optional(),
});
type RewindPreviewResponse = z.output<typeof RewindPreviewResponseSchema>;

export const RewindActionResponseSchema = z.object({
  api_version: z.string(),
  result: z.string(),
  feature_id: z.string(),
  target_phase: z.string().optional(),
  effective_phase: z.string().optional(),
  roadmap_phase: z.number().int().nonnegative().optional(),
  upgrade_pipeline: z.string().optional(),
  // The removed warning count must never reappear on the wire.
  warning_count: z.never().optional(),
  source_run_number: z.number().int().nonnegative().optional(),
  new_run_number: z.number().int().nonnegative().optional(),
  // Canonical warning-class errors for non-fatal rewind failures
  // (pull-request close, backup branch, worktree reset).
  warnings: z.array(CanonicalErrorSchema).max(100).optional(),
});
type RewindActionResponse = z.output<typeof RewindActionResponseSchema>;

/** Shared shape of create/setup action responses (FeatureActionResult). */
export const FeatureActionResponseSchema = z.object({
  api_version: z.string(),
  result: z.string(),
  feature_id: z.string(),
  warnings: z.array(CanonicalErrorSchema).max(32).optional(),
});

type FeatureActionResponse = z.output<typeof FeatureActionResponseSchema>;

export const ServerFeatureOperationalActionResponseSchema = FeatureActionResponseSchema;

// --- Sessions ---------------------------------------------------------------

const ServerUsageSchema = z.object({
  input_tokens: z.number().int().nonnegative().optional(),
  output_tokens: z.number().int().nonnegative().optional(),
  cost_usd: z.number().nonnegative().optional(),
});

const ServerTaskActivityUsageSchema = z.object({
  total_tokens: z.number().int().nonnegative().optional(),
  tool_uses: z.number().int().nonnegative().optional(),
  duration_ms: z.number().int().nonnegative().optional(),
});

const ServerTaskActivitySchema = z.object({
  task_id: z.string(),
  tool_use_id: z.string().optional(),
  child_session_id: z.string().optional(),
  description: z.string().optional(),
  state: z.enum(['running', 'completed', 'failed', 'cancelled']),
  last_tool_name: z.string().optional(),
  last_path: z.string().optional(),
  status: z.string().optional(),
  summary: z.string().optional(),
  output_file: z.string().optional(),
  usage: ServerTaskActivityUsageSchema.optional(),
  started_at: z.string(),
  updated_at: z.string(),
  finished_at: z.string().optional(),
});

const ServerSessionSummarySchema = z.object({
  id: z.string(),
  feature_id: z.string(),
  run_number: z.number().int().nonnegative(),
  phase: z.string(),
  repo: z.string().optional(),
  kind: z.string(),
  label: z.string().optional(),
  provider: z.string().optional(),
  model: z.string().optional(),
  status: z.string(),
  turn_state: z.string().optional(),
  started_at: z.string(),
  iteration: z.number().int().nonnegative().optional(),
  context_percentage: z.number().int().optional(),
  task_activities: z.array(ServerTaskActivitySchema).max(1000),
  running_task_count: z.number().int().nonnegative(),
  usage: ServerUsageSchema,
});
export type ServerSessionSummary = z.output<typeof ServerSessionSummarySchema>;

export const RunSessionListResponseSchema = z.object({
  api_version: z.string(),
  run_number: z.number().int().positive(),
  sessions: z.array(ServerSessionSummarySchema),
});
type RunSessionListResponse = z.output<typeof RunSessionListResponseSchema>;

const ServerTranscriptCursorSchema = z.object({
  total: z.number().int().nonnegative(),
  start: z.number().int().nonnegative(),
  end: z.number().int().nonnegative(),
});

const ServerFileChangeSchema = z.object({
  path: z.string().optional(),
  old_path: z.string().optional(),
  operation: z.string().optional(),
  detail: z.string().optional(),
  added_lines: z.number().int().nonnegative().optional(),
  removed_lines: z.number().int().nonnegative().optional(),
  has_diff_patch: z.boolean().optional(),
});

const ServerToolCallSchema = z.object({
  summary: z.string().optional(),
  prompt: z.string().optional(),
});

const ServerTaskSchema = z.object({
  id: z.string().optional(),
  tool_use_id: z.string().optional(),
  description: z.string().optional(),
  task_type: z.string().optional(),
  prompt: z.string().optional(),
  last_tool_name: z.string().optional(),
  status: z.string().optional(),
  summary: z.string().optional(),
  output_file: z.string().optional(),
});

const ServerTranscriptMessageSchema = z.object({
  index: z.number().int().nonnegative(),
  block_index: z.number().int().nonnegative().optional(),
  role: z.string(),
  type: z.string(),
  text: z
    .string()
    .max(1024 * 1024)
    .optional(),
  tool: z.string().optional(),
  status: z.string().optional(),
  redacted: z.boolean().optional(),
  locally_appended: z.boolean().optional(),
  auto_picked: z.boolean().optional(),
  auto_pick_question: z.string().optional(),
  auto_pick_confidence: z.number().optional(),
  file_change: ServerFileChangeSchema.optional(),
  tool_call: ServerToolCallSchema.optional(),
  task: ServerTaskSchema.optional(),
});
export type ServerTranscriptMessage = z.output<typeof ServerTranscriptMessageSchema>;

export const SessionListResponseSchema = z.object({
  api_version: z.string(),
  sessions: z.array(ServerSessionSummarySchema).max(1000),
});

export const SessionDetailResponseSchema = z.object({
  api_version: z.string(),
  session: ServerSessionSummarySchema.extend({
    transcript_cursor: ServerTranscriptCursorSchema,
    pending_controls: z.array(z.unknown()).max(1000),
    initial_prompt: z
      .string()
      .max(1024 * 1024)
      .optional(),
    can_attach: z.boolean(),
    log_available: z.boolean(),
  }),
});
export type ServerSessionDetail = z.output<typeof SessionDetailResponseSchema>['session'];

export const TranscriptResponseSchema = z.object({
  api_version: z.string(),
  cursor: ServerTranscriptCursorSchema,
  messages: z.array(ServerTranscriptMessageSchema).max(500),
});

export const SessionOutputChunkSchema = z.object({
  api_version: z.string(),
  session_id: z.string().optional(),
  index: z.number().int().nonnegative(),
  message: ServerTranscriptMessageSchema.optional(),
  done: z.boolean().optional(),
});

export const LivePreviewResponseSchema = z.object({
  api_version: z.string(),
  feature: ServerFeatureSummarySchema,
  session: ServerSessionSummarySchema.optional(),
  activity: z.string().max(1000),
  context: z.object({ percentage: z.number().int() }),
  timing: z.object({
    total_seconds: z.number().int().nonnegative(),
    by_phase: z.record(z.string(), z.number().int().nonnegative()),
  }),
  cost: z.object({
    total_usd: z.number().nonnegative(),
    by_phase: z.record(z.string(), z.number().nonnegative()),
  }),
  transcript: z.array(ServerTranscriptMessageSchema).max(500),
});

// --- Supervisor conversation (/api/v1/supervisor/*) ---------------------------
// Bounded wire shapes; the main process maps them to the camelCase IPC types.

const ServerSupervisorIdSchema = z.string().max(200);
const ServerSupervisorSeqSchema = z.number().int().nonnegative();

const ServerSupervisorSettingsSchema = z.object({
  harness: z.string().max(100),
  model: z.string().max(200),
  effort: z.string().max(40),
});

const ServerSupervisorPermissionModeSchema = z.object({
  requested: z.string().max(100),
  effective: z.string().max(100),
  restricted_by_policy: z.boolean(),
});

const ServerSupervisorBackgroundTaskSchema = z.object({
  activity: z
    .array(z.object({ at: z.string().max(100), detail: z.string().max(1000) }))
    .max(8)
    .optional(),
  id: z.string().max(500),
  provider_id: z.string().max(200),
  generation: z.number().int().nonnegative(),
  kind: z.enum(['scheduled', 'monitor', 'task']),
  title: z.string().max(500),
  state: z.enum(['watching', 'running', 'completed', 'failed', 'stopped', 'interrupted']),
  schedule: z.string().max(500),
  detail: z.string().max(1000),
  started_at: z.string().max(100),
  updated_at: z.string().max(100),
  expires_at: z.string().max(100),
});

const ServerSupervisorStateSchema = z.object({
  background_tasks: z.array(ServerSupervisorBackgroundTaskSchema).optional(),
  conversation_id: ServerSupervisorIdSchema,
  generation: ServerSupervisorSeqSchema,
  session_id: z.string().max(200),
  lifecycle: z.enum([
    'stopped',
    'starting',
    'idle',
    'running',
    'waiting_permission',
    'waiting_question',
    'failed',
  ]),
  starting_step: z.enum(['rebuilding', 'launching', 'handshake']).optional(),
  last_turn_outcome: z.enum(['none', 'completed', 'interrupted', 'failed']),
  interrupted_by: z.enum(['none', 'user', 'shutdown']),
  settings: ServerSupervisorSettingsSchema,
  pending_change: z
    .object({
      request_id: z.string().min(1).max(128),
      kind: z.enum(['model', 'effort', 'harness']),
      target: ServerSupervisorSettingsSchema,
      requested_at: z.string().max(100),
    })
    .optional(),
  effective_model: z.string().max(200),
  permission_mode: ServerSupervisorPermissionModeSchema,
  failure: CanonicalErrorSchema.extend({
    attempted_settings: ServerSupervisorSettingsSchema.optional(),
  }).optional(),
  persist_failure: CanonicalErrorSchema.optional(),
  pending_requests: z.array(ServerControlRequestSchema).max(100),
  context_usage: z
    .object({
      percent: z.number().min(0).max(100),
      used_tokens: z.number().int().nonnegative(),
      window_tokens: z.number().int().positive(),
    })
    .nullable(),
  head_seq: ServerSupervisorSeqSchema,
  stream_epoch: z.string().max(200),
});
export type ServerSupervisorState = z.output<typeof ServerSupervisorStateSchema>;

const ServerSupervisorRequestRecordSchema = z.object({
  request_id: AttentionIDSchema,
  tool_name: z.string().max(500),
  stage: z.enum(['requested', 'resolved']),
  outcome: z.enum(['pending', 'allowed', 'denied', 'answered', 'interrupted']),
  summary: AttentionTextSchema.optional(),
  origin: ServerRequestOriginSchema.optional(),
  child_session_id: AttentionIDSchema.optional(),
});

const ServerSupervisorMarkerRecordSchema = z.object({
  marker: z.enum([
    'interrupted',
    'error',
    'history_not_restored',
    'permission_restricted',
    'settings_changed',
    'harness_change',
    'settings_reverted',
    'compacted',
    'transcript_recovered',
  ]),
  text: AttentionTextSchema,
  code: z.string().max(200).optional(),
  summary: z
    .string()
    .max(16 * 1024)
    .optional(),
  truncated: z.boolean().optional(),
});

const ServerSupervisorCheckpointRecordSchema = z.object({
  covers_through_seq: ServerSupervisorSeqSchema,
  reason: z.literal('native_auto'),
  model: z.string().max(200),
  summary: z.string().max(16 * 1024),
  truncated: z.boolean(),
  has_native_baseline: z.boolean(),
});

/** A user record's attachment: the conversation copy the server made. */
const ServerSupervisorAttachmentSchema = z.object({
  path: z.string().min(1).max(4096),
  kind: z.enum(['image', 'file']),
  name: z.string().max(1024),
  size: z.number().int().nonnegative(),
});

const ServerSupervisorRecordSchema = z.object({
  seq: z.number().int().positive(),
  id: ServerSupervisorIdSchema,
  conversation_id: ServerSupervisorIdSchema,
  generation: ServerSupervisorSeqSchema,
  turn_id: ServerSupervisorIdSchema,
  kind: z.enum([
    'user',
    'assistant',
    'tool_use',
    'tool_result',
    'permission',
    'question',
    'marker',
    'note',
    'checkpoint',
  ]),
  visibility: z.enum(['content', 'model_only', 'display_only']),
  created_at: z.string().max(100),
  client_message_id: z.string().max(128).optional(),
  stream_message_id: ServerSupervisorIdSchema.optional(),
  // Paging limits records, not the projected rows within a record. A single
  // tool result can contain thousands of file changes; preserve every row.
  messages: z.array(ServerTranscriptMessageSchema),
  request: ServerSupervisorRequestRecordSchema.optional(),
  marker: ServerSupervisorMarkerRecordSchema.optional(),
  checkpoint: ServerSupervisorCheckpointRecordSchema.optional(),
  attachments: z.array(ServerSupervisorAttachmentSchema).max(36).optional(),
});
export type ServerSupervisorRecord = z.output<typeof ServerSupervisorRecordSchema>;

export const SupervisorStateResponseSchema = z.object({
  api_version: z.string(),
  state: ServerSupervisorStateSchema,
});
type SupervisorStateResponse = z.output<typeof SupervisorStateResponseSchema>;

export const SupervisorTranscriptResponseSchema = z.object({
  api_version: z.string(),
  conversation_id: ServerSupervisorIdSchema,
  items: z.array(ServerSupervisorRecordSchema).max(500),
  first_seq: ServerSupervisorSeqSchema,
  last_seq: ServerSupervisorSeqSchema,
  has_more_before: z.boolean(),
  has_more_after: z.boolean(),
  head_seq: ServerSupervisorSeqSchema,
});
export type SupervisorTranscriptResponse = z.output<typeof SupervisorTranscriptResponseSchema>;

export const SupervisorMessageResponseSchema = z.object({
  api_version: z.string(),
  record: ServerSupervisorRecordSchema,
  launched: z.boolean(),
  deduplicated: z.boolean(),
});
type SupervisorMessageResponse = z.output<typeof SupervisorMessageResponseSchema>;

export const SupervisorActionResponseSchema = z.object({
  api_version: z.string(),
  result: z.enum(['accepted', 'ended', 'not_active']),
  state: ServerSupervisorStateSchema,
});
type SupervisorActionResponse = z.output<typeof SupervisorActionResponseSchema>;

export const SupervisorResetResponseSchema = z.object({
  api_version: z.string(),
  result: z.enum(['reset', 'noop']),
  previous_conversation_id: ServerSupervisorIdSchema,
  state: ServerSupervisorStateSchema,
});
type SupervisorResetResponse = z.output<typeof SupervisorResetResponseSchema>;

const ServerSupervisorDeltaSchema = z.object({
  turn_id: ServerSupervisorIdSchema,
  stream_message_id: ServerSupervisorIdSchema,
  chunk_index: z.number().int().nonnegative(),
  text: z.string().max(1024 * 1024),
});

/** One `/api/v1/supervisor/events` SSE payload (the JSON of a `data:` block). */
export const ServerSupervisorStreamEventSchema = z.object({
  kind: z.enum(['record', 'delta', 'state', 'request', 'stream.reset', 'heartbeat']),
  conversation_id: ServerSupervisorIdSchema,
  generation: ServerSupervisorSeqSchema,
  stream_epoch: z.string().max(200),
  seq: ServerSupervisorSeqSchema.optional(),
  snapshot_required: z.boolean().optional(),
  record: ServerSupervisorRecordSchema.optional(),
  delta: ServerSupervisorDeltaSchema.optional(),
  state: ServerSupervisorStateSchema.optional(),
  request: ServerControlRequestSchema.optional(),
});
export type ServerSupervisorStreamEvent = z.output<typeof ServerSupervisorStreamEventSchema>;

// --- Runtime config (GET /api/v1/config/runtime) — creation-defaults subset --

// --- Recovery, cycle actions, preflight, and bulk preview -------------------

const ServerRecoveryItemSchema = z.object({
  key: z.string(),
  feature_id: z.string(),
  feature_name: z.string().optional(),
  repo_name: z.string().optional(),
  phase: z.string().optional(),
  iteration: z.number().int().nonnegative().optional(),
  pid: z.number().int().optional(),
  process_alive: z.boolean(),
  // Canonical needs_action error classifying this orphan session by
  // liveness; required on every item.
  error: CanonicalErrorSchema,
  log_available: z.boolean().optional(),
  allowed_actions: z.array(z.string()),
  default_action: z.string(),
});
type ServerRecoveryItem = z.output<typeof ServerRecoveryItemSchema>;

export const RecoverySnapshotResponseSchema = z.object({
  api_version: z.string(),
  snapshot_id: z.string(),
  items: z.array(ServerRecoveryItemSchema),
});
type RecoverySnapshotResponse = z.output<typeof RecoverySnapshotResponseSchema>;

export const RecoveryActionResponseSchema = z.object({
  api_version: z.string(),
  result: z.string(),
});

export const ChildLaunchResponseSchema = z.object({
  api_version: z.string(),
  feature_id: z.string(),
  parent_id: z.string(),
  result: z.string(),
});

export const ReviewFeedbackFetchResponseSchema = z.object({
  api_version: z.string(),
  revision: z.number().int().nonnegative(),
  snapshot_id: z.string(),
  repos: z.array(ServerReviewFeedbackRepoCommentsSchema),
});
type ReviewFeedbackFetchResponse = z.output<typeof ReviewFeedbackFetchResponseSchema>;

export const ReviewFeedbackSelectionResponseSchema = z.object({
  api_version: z.string(),
  revision: z.number().int().nonnegative(),
  repos: z.array(ServerReviewFeedbackRepoCommentsSchema),
});
type ReviewFeedbackSelectionResponse = z.output<typeof ReviewFeedbackSelectionResponseSchema>;

export const ReviewFeedbackFeatureResponseSchema = z.object({
  api_version: z.string(),
  feature_id: z.string(),
  parent_id: z.string(),
  result: z.string(),
  child_id: z.string().optional(),
  changed: z.number().int().nonnegative().optional(),
  omitted: z.number().int().nonnegative().optional(),
  deferred: z.number().int().nonnegative().optional(),
});
type ReviewFeedbackFeatureResponse = z.output<typeof ReviewFeedbackFeatureResponseSchema>;

export const DiscardChildResponseSchema = z.object({
  api_version: z.string(),
  feature_id: z.string(),
  result: z.string(),
});
export const DeleteFeatureResponseSchema = z.object({
  api_version: z.string(),
  feature_id: z.string(),
  operation_id: z.string(),
  status: z.enum(['completed', 'cleanup_pending', 'attention_required']),
  diagnostics: z
    .array(z.object({ code: z.string().optional(), message: z.string().optional() }))
    .optional(),
});

const CompletionPreflightRepoSchema = z.object({
  repo: z.string(),
  publishable: z.boolean(),
  touched: z.boolean(),
  status: z.string(),
  pr_url: z.string().optional(),
  blocker: z.string().optional(),
  freshness: z.string().optional(),
  // Canonical error rendering the repository's stored publish-failure
  // record; absent when the repository has not failed.
  error: CanonicalErrorSchema.optional(),
  // The removed last-error string must never reappear on the wire.
  last_error: z.never().optional(),
  base_branch: z.string().optional(),
  branch: z.string().optional(),
  pending_commits: z.number().optional(),
  pending_dirty: z.boolean().optional(),
  push_mode: z.string().optional(),
  pending_dirty_files: z.array(z.string()).optional(),
  pending_dirty_file_total: z.number().optional(),
});

export const CompletionPreflightResponseSchema = z.object({
  api_version: z.string(),
  feature_id: z.string(),
  source_revision: z.string(),
  can_mark_done: z.boolean().optional(),
  mark_done_blocker: z.string().optional(),
  repos: z.array(CompletionPreflightRepoSchema),
});
type CompletionPreflightResponse = z.output<typeof CompletionPreflightResponseSchema>;

const RepositoryDiffFileSchema = z.object({
  path: z.string(),
  old_path: z.string().optional(),
  operation: z.string(),
  added_lines: z.number().optional(),
  removed_lines: z.number().optional(),
  binary: z.boolean().optional(),
  fingerprint: z.string().optional(),
});

export const RepositoryDiffResponseSchema = z.object({
  api_version: z.string(),
  feature_id: z.string(),
  repo: z.string(),
  source_revision: z.string().optional(),
  truncated: z.boolean().optional(),
  files: z.array(RepositoryDiffFileSchema),
  file_diff: z.string().optional(),
  file_truncated: z.boolean().optional(),
  file_binary: z.boolean().optional(),
  file_unavailable: z.boolean().optional(),
  // The removed partial-failure string must never reappear on the wire.
  partial_failure: z.never().optional(),
  // Canonical warning-class error rendering the typed partial failure of
  // this repository's inspection; absent when it was fully inspected.
  error: CanonicalErrorSchema.optional(),
});
type RepositoryDiffResponse = z.output<typeof RepositoryDiffResponseSchema>;

export const RepositoryPathResponseSchema = z.object({
  api_version: z.string(),
  feature_id: z.string(),
  repo: z.string(),
  path: z.string(),
});
type RepositoryPathResponse = z.output<typeof RepositoryPathResponseSchema>;

export const PublishDescriptionResponseSchema = z.object({
  api_version: z.string(),
  feature_id: z.string(),
  result: z.string(),
  title: z.string(),
  body: z.string(),
});
type PublishDescriptionResponse = z.output<typeof PublishDescriptionResponseSchema>;

// Each alias proves one direction of the generated OpenAPI contract.
// Subset schemas use generated -> parsed; narrowed wire enums use parsed -> generated.
type DriftGuard<_Actual extends Expected, Expected> = true;
type Wire = components['schemas'];
type _canonicalErrorSubset = DriftGuard<components['schemas']['Error'], CanonicalError>;
type _canonicalErrorResponseSubset = DriftGuard<Wire['ErrorResponse'], CanonicalErrorResponse>;
type _healthSubset = DriftGuard<components['schemas']['HealthResponse'], ProbeHealth>;
type _compatibilityAssignable = DriftGuard<
  CompatibilityDeclaration,
  Wire['CompatibilityDeclaration']
>;
type _readinessSubset = DriftGuard<components['schemas']['ReadinessResponse'], ReadinessResponse>;
type _runtimeConfigSubset = DriftGuard<
  Wire['RuntimeConfigResponse'],
  Omit<z.output<typeof RuntimeConfigResponseSchema>, 'feature_defaults'>
>;
type _featureListSubset = DriftGuard<Wire['FeatureListResponse'], FeatureListResponse>;
type _testingContractSubset = DriftGuard<Wire['TestingContractResponse'], TestingContractResponse>;
type _featureDetailSubset = DriftGuard<Wire['FeatureDetailResponse'], FeatureDetailResponse>;
type _setupSubset = DriftGuard<components['schemas']['Setup'], ServerSetup>;
type _setupTaskSubset = DriftGuard<components['schemas']['SetupTask'], ServerSetupTask>;
type _createFeatureSubset = DriftGuard<Wire['FeatureActionResponse'], FeatureActionResponse>;
type _sessionListSubset = DriftGuard<
  Wire['SessionListResponse'],
  z.output<typeof SessionListResponseSchema>
>;
type _sessionDetailSubset = DriftGuard<
  Wire['SessionDetailResponse'],
  z.output<typeof SessionDetailResponseSchema>
>;
type _transcriptSubset = DriftGuard<
  Wire['TranscriptResponse'],
  z.output<typeof TranscriptResponseSchema>
>;
type _outputChunkSubset = DriftGuard<
  components['schemas']['SessionOutputChunk'],
  z.output<typeof SessionOutputChunkSchema>
>;
type _reviewSessionSubset = DriftGuard<Wire['ReviewSessionResponse'], ServerReviewSession>;
type _reviewValidationSubset = DriftGuard<
  components['schemas']['ReviewDraftValidationResponse'],
  ServerReviewDraftValidation
>;
type _reviewDecisionSubset = DriftGuard<
  components['schemas']['ReviewSessionDecisionResponse'],
  ServerReviewDecision
>;
type _runListSubset = DriftGuard<components['schemas']['RunListResponse'], RunListResponse>;
type _runDetailSubset = DriftGuard<components['schemas']['RunDetailResponse'], RunDetailResponse>;
type _runSessionListSubset = DriftGuard<
  components['schemas']['RunSessionListResponse'],
  RunSessionListResponse
>;
type _artifactListSubset = DriftGuard<
  components['schemas']['ArtifactListResponse'],
  ArtifactListResponse
>;
type _textContentSubset = DriftGuard<
  components['schemas']['TextContentResponse'],
  TextContentResponse
>;
type _recoverySnapshotSubset = DriftGuard<
  components['schemas']['RecoverySnapshotResponse'],
  RecoverySnapshotResponse
>;
type _recoveryItemSubset = DriftGuard<components['schemas']['RecoveryItem'], ServerRecoveryItem>;
type _childLaunch = DriftGuard<
  components['schemas']['ChildLaunchResponse'],
  z.output<typeof ChildLaunchResponseSchema>
>;
type _reviewFeedbackFetchSubset = DriftGuard<
  components['schemas']['ReviewFeedbackFetchResponse'],
  ReviewFeedbackFetchResponse
>;
type _reviewFeedbackCommentSubset = DriftGuard<
  components['schemas']['ReviewFeedbackComment'],
  ServerReviewFeedbackComment
>;
type _reviewFeedbackDraftCommentSubset = DriftGuard<
  components['schemas']['ReviewFeedbackDraftComment'],
  ServerReviewFeedbackDraftComment
>;
type _reviewFeedbackSelectionSubset = DriftGuard<
  components['schemas']['ReviewFeedbackSelectionResponse'],
  ReviewFeedbackSelectionResponse
>;
type _reviewFeedbackFeatureSubset = DriftGuard<
  components['schemas']['ReviewFeedbackFeatureResponse'],
  ReviewFeedbackFeatureResponse
>;
type _repoStatusSubset = DriftGuard<components['schemas']['RepoStatus'], ServerRepoStatus>;
type _needUserInputGateSubset = DriftGuard<
  components['schemas']['NeedUserInputGate'],
  ServerNeedUserInputGateDetail
>;
type _needUserInputVerificationSubset = DriftGuard<
  components['schemas']['NeedUserInputVerification'],
  z.output<typeof ServerNeedUserInputVerificationSchema>
>;
type _askUserQuestionSubset = DriftGuard<
  components['schemas']['AskUserQuestion'],
  z.output<typeof ServerAskUserQuestionSchema>
>;
type _askUserQuestionAssignable = DriftGuard<
  z.output<typeof ServerAskUserQuestionSchema>,
  components['schemas']['AskUserQuestion']
>;
type _needUserInputQuestionSubset = DriftGuard<
  components['schemas']['NeedUserInputQuestion'],
  NonNullable<z.output<typeof ServerNeedUserInputGateSchema>['questions']>[number]
>;
type _needUserInputQuestionAssignable = DriftGuard<
  NonNullable<z.output<typeof ServerNeedUserInputGateSchema>['questions']>[number],
  components['schemas']['NeedUserInputQuestion']
>;
type _rewindPreviewSubset = DriftGuard<
  components['schemas']['RewindPreviewResponse'],
  RewindPreviewResponse
>;
type _rewindActionSubset = DriftGuard<
  components['schemas']['RewindFeatureResponse'],
  RewindActionResponse
>;
type _repositoryPathSubset = DriftGuard<
  components['schemas']['RepositoryPathDTO'],
  RepositoryPathResponse
>;
type _publishDescriptionSubset = DriftGuard<
  components['schemas']['PublishDescriptionResponse'],
  PublishDescriptionResponse
>;
type _completionPreflightSubset = DriftGuard<
  components['schemas']['CompletionPreflightResponse'],
  CompletionPreflightResponse
>;
type _repositoryDiffSubset = DriftGuard<
  components['schemas']['RepositoryDiffResponse'],
  RepositoryDiffResponse
>;
type _cloneOperationSubset = DriftGuard<components['schemas']['CloneOperation'], CloneOperationDTO>;
type _cloneActionSubset = DriftGuard<
  components['schemas']['CloneActionResponse'],
  CloneActionResponse
>;
type _cloneOperationResponseSubset = DriftGuard<
  components['schemas']['CloneOperationResponse'],
  CloneOperationResponse
>;
type _cloneOperationListSubset = DriftGuard<
  components['schemas']['CloneOperationListResponse'],
  CloneOperationListResponse
>;
type _createRepositorySubset = DriftGuard<
  components['schemas']['CreateRepositoryResponse'],
  CreateRepositoryResponse
>;
type _supervisorStateSubset = DriftGuard<
  components['schemas']['SupervisorStateResponse'],
  SupervisorStateResponse
>;
type _supervisorTranscriptSubset = DriftGuard<
  components['schemas']['SupervisorTranscriptResponse'],
  SupervisorTranscriptResponse
>;
type _supervisorMessageSubset = DriftGuard<
  components['schemas']['SupervisorMessageResponse'],
  SupervisorMessageResponse
>;
type _supervisorActionSubset = DriftGuard<
  components['schemas']['SupervisorActionResponse'],
  SupervisorActionResponse
>;
type _supervisorResetSubset = DriftGuard<
  components['schemas']['SupervisorResetResponse'],
  SupervisorResetResponse
>;
type _supervisorStreamEventSubset = DriftGuard<
  components['schemas']['SupervisorStreamEvent'],
  ServerSupervisorStreamEvent
>;
// Wire schemas moved from their main-process consumers.
type _runtimeReadinessSubset = DriftGuard<
  components['schemas']['RuntimeReadinessResponse'],
  RuntimeReadinessResponse
>;
type _modelDefaults = DriftGuard<
  components['schemas']['ModelDefaults'],
  z.output<typeof ServerModelDefaultsSchema>
>;
type _effortDefaults = DriftGuard<
  components['schemas']['EffortConfig'],
  z.output<typeof ServerEffortDefaultsSchema>
>;
type _checkpoints = DriftGuard<
  components['schemas']['Checkpoints'],
  z.output<typeof ServerCheckpointsSchema>
>;
type _featureConfigPayload = DriftGuard<
  components['schemas']['FeatureConfig'],
  z.output<typeof ServerFeatureConfigSchema>
>;
type _featureDefaults = DriftGuard<
  components['schemas']['FeatureDefaults'],
  Omit<z.output<typeof RuntimeConfigResponseSchema>['feature_defaults'], 'checkpoints'>
>;
type _modelInfo = DriftGuard<
  components['schemas']['Model'],
  z.output<typeof ServerModelInfoSchema>
>;
type _configAction = DriftGuard<
  components['schemas']['RuntimeConfigUpdateResponse'],
  z.output<typeof ActionResponseSchema>
>;
type _sseEnvelope = DriftGuard<
  components['schemas']['SSEEvent'],
  z.output<typeof SseEventEnvelopeSchema>
>;
type _featureConfig = DriftGuard<
  components['schemas']['FeatureConfigResponse'],
  z.output<typeof FeatureConfigResponseSchema>
>;
type _modelCatalog = DriftGuard<
  components['schemas']['ModelCatalogResponse'],
  Omit<z.output<typeof ModelCatalogResponseSchema>, 'chat_default_effort'>
>;
type _providerRefresh = DriftGuard<
  components['schemas']['ProviderModelRefreshResponse'],
  Omit<z.output<typeof ProviderModelRefreshResponseSchema>, 'catalog'>
>;
type _catalogEffort = DriftGuard<
  NonNullable<z.output<typeof ModelCatalogResponseSchema>['chat_default_effort']>,
  components['schemas']['ModelCatalogResponse']['chat_default_effort']
>;
type _stageUpload = DriftGuard<
  z.output<typeof StageUploadResponseSchema>,
  components['schemas']['StageUploadResponse']
>;
type _updateSnapshot = DriftGuard<
  components['schemas']['UpdateSnapshot'],
  z.output<typeof WireSnapshotSchema>
>;
type _updateResponse = DriftGuard<
  components['schemas']['UpdateSnapshotResponse'],
  z.output<typeof WireResponseSchema>
>;
