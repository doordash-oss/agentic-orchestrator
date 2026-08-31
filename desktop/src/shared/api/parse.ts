/**
 * Fail-closed parsing for anything that crosses a trust boundary: server HTTP
 * responses (parsed in the main process before use) and IPC payloads.
 *
 * Order matters: byte-size gate, JSON parse, prototype-pollution scan,
 * API-version gate, then zod schema validation. Errors are typed SafeErrors
 * and never echo raw payload values.
 */
import { z } from 'zod';
import { assertCompatibleApiVersion } from '../apiVersion';
import { SafeErrorException, safeError } from '../errors';
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
    throw new SafeErrorException(
      safeError(
        'E_MALFORMED_RESPONSE',
        'The response was not valid JSON.',
        'Retry; if this persists the server or transport is misbehaving.',
      ),
    );
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
    // Report only issue paths and codes — never received values.
    const paths = [...new Set(result.error.issues.map((i) => i.path.join('.') || '(root)'))]
      .slice(0, 5)
      .join(', ');
    throw new SafeErrorException(
      safeError(
        'E_SCHEMA_MISMATCH',
        `The payload did not match the expected schema at: ${paths}.`,
        'Update the Agentico desktop app and the agentico server to matching releases.',
      ),
    );
  }
  return result.data;
}

// --- Runtime response schemas ---------------------------------------------
// Each schema is type-checked below against the generated OpenAPI types so it
// cannot drift from api/openapi.yaml without failing `npm run check`.

export const RuntimeIdentitySchema = z.object({
  runtime_dir: z.string(),
  state_dir: z.string(),
  config_path: z.string(),
});

export const LaunchPolicySchema = z.object({
  resolved: z.boolean(),
  providers: z.array(z.string()),
  dangerously_skip_permissions: z.boolean(),
});

export const OwnerSchema = z.object({
  pid: z.number().int(),
  pgid: z.number().int().optional(),
  started_at: z.string(),
  version: z.string().optional(),
});

export const BuildIdentitySchema = z.object({
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

export type CompatibilityDeclaration = z.output<typeof CompatibilityDeclarationSchema>;

export const HealthResponseSchema = z.object({
  api_version: z.string(),
  status: z.string(),
  /**
   * Optional operator-assigned server display name (server cap:
   * MaxServerNameLength = 64). Informational only — never gates
   * compatibility; an oversized or malformed name is dropped, not fatal.
   */
  name: z.string().max(64).optional().catch(undefined),
  runtime: RuntimeIdentitySchema,
  launch_policy: LaunchPolicySchema,
  started_at: z.string(),
  owner: OwnerSchema,
  server_time: z.string(),
  compatibility: CompatibilityDeclarationSchema,
});

export type HealthResponse = z.output<typeof HealthResponseSchema>;

// --- Readiness (GET /api/v1/readiness, POST /api/v1/readiness/refresh) -----

export const ServerReadinessIssueSchema = z.object({
  code: z.enum([
    'missing_executable',
    'unsupported_version',
    'unauthenticated',
    'models_unavailable',
    'invalid_configuration',
    'invalid_workspace_root',
    'invalid_repository',
  ]),
  message: z.string(),
  remedy: z.string().optional(),
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
      issue: ServerReadinessIssueSchema.optional(),
    }),
  ),
  models: z.object({
    available: z.boolean(),
    models: z.array(z.string()).optional(),
    issue: ServerReadinessIssueSchema.optional(),
  }),
  configuration: z.object({
    valid: z.boolean(),
    issue: ServerReadinessIssueSchema.optional(),
  }),
  workspace: z.object({
    roots: z.array(
      z.object({
        path: z.string(),
        valid: z.boolean(),
        issue: ServerReadinessIssueSchema.optional(),
      }),
    ),
    repositories: z.array(
      z.object({
        name: z.string(),
        path: z.string(),
        valid: z.boolean(),
        issue: ServerReadinessIssueSchema.optional(),
      }),
    ),
  }),
  issues: z.array(ServerReadinessIssueSchema).optional(),
});

export type ReadinessResponse = z.output<typeof ReadinessResponseSchema>;

// --- Blocking attention (GET /api/v1/prompts, /api/v1/permissions) ---------
// These responses are deliberately bounded before they are translated to the
// renderer. Tool input remains data (never HTML or a navigable URL).

const AttentionTextSchema = z.string().max(64 * 1024);
const AttentionIDSchema = z.string().min(1).max(200);

export const ServerAskUserOptionSchema = z.object({
  // Matches the server's SafeDisplayText budget for AskUser option labels.
  label: z.string().max(1000).optional(),
  description: AttentionTextSchema.optional(),
  confidence: z.number().min(0).max(1).optional(),
});
export const ServerAskUserQuestionSchema = z.object({
  question: AttentionTextSchema.optional(),
  header: z.string().max(500).optional(),
  multi_select: z.boolean().optional(),
  options: z.array(ServerAskUserOptionSchema).max(100).optional(),
});
export const ServerRememberPreviewSchema = z.object({
  pattern: z.string().max(4096),
  scope: z.string().max(4096),
  scope_display: z.string().max(4096),
});
export const ServerControlRequestSchema = z.object({
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
  waiting_since: z.string().max(100).optional(),
});
export const ServerHelpQueueSchema = z.object({
  feature_id: AttentionIDSchema,
  session_id: AttentionIDSchema.optional(),
  question: AttentionTextSchema,
  /** 'question' for real help requests, 'input' for a synthetic idle-session entry. */
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
  allowed_actions: z.array(z.string().max(50)).max(2),
});
export const ServerNeedUserInputGateSchema = z.object({
  feature_id: AttentionIDSchema.optional(),
  open: z.boolean(),
  scope: z.string().max(100).optional(),
  repo_name: z.string().max(500).optional(),
  iteration: z.number().int().nonnegative().optional(),
  summary: AttentionTextSchema.optional(),
  questions: z
    .array(
      z.object({
        index: z.number().int().nonnegative().optional(),
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
export type ServerPromptSnapshot = z.output<typeof PromptSnapshotResponseSchema>;
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

export const ReviewConflictResponseSchema = z.object({
  api_version: z.string(),
  error: z.object({
    code: z.literal('conflict'),
    message: z.string(),
    target: z.object({
      review_id: z.string().min(1),
      current_revision: z.string().min(1),
      expected_revision: z.string().min(1),
    }),
  }),
});

// --- Runtime config (GET /api/v1/config/runtime) — workspace-roots subset ---
// Only the fields the desktop setup flow consumes; the full response has
// many more, which z.object tolerates and strips.

export const RuntimeConfigWorkspaceSchema = z.object({
  api_version: z.string(),
  workspace_roots: z.array(z.string()).optional(),
});

export type RuntimeConfigWorkspace = z.output<typeof RuntimeConfigWorkspaceSchema>;

// --- Features (GET/POST /api/v1/features, GET /api/v1/features/{id}) --------
// Lenient subsets: z.object tolerates and strips fields this view does not
// consume yet.

export const ServerSetupTaskSchema = z.object({
  key: z.string(),
  kind: z.string(),
  label: z.string().optional(),
  repo: z.string().optional(),
  status: z.string(),
  branch: z.string().optional(),
  attempt: z.number().int().optional(),
  last_error: z.string().optional(),
});

export type ServerSetupTask = z.output<typeof ServerSetupTaskSchema>;

export const ServerSetupSchema = z.object({
  status: z.string(),
  attempt: z.number().int().optional(),
  tasks: z.record(z.string(), ServerSetupTaskSchema).optional(),
  task_order: z.array(z.string()).optional(),
  last_error: z.string().optional(),
});

export type ServerSetup = z.output<typeof ServerSetupSchema>;

export const ServerActionImpactSchema = z.object({
  kind: z.enum(['child_discard', 'parent_cascade_delete']),
  subject: z.object({ id: z.string(), name: z.string() }),
  categories: z.array(z.object({ key: z.string(), label: z.string(), items: z.array(z.string()) })),
  retained: z.array(z.string()),
});

export const ServerActionSchema = z.object({
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
export const DIFF_SUMMARY_MAX_BYTES = 256 * 1024;

export const ServerRelationshipChildSchema = z.object({
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
  attention: z.array(
    z.object({ code: z.string(), message: z.string(), repo: z.string().optional() }),
  ),
  cleanup_warnings: z.array(z.object({ repo: z.string().optional(), message: z.string() })),
  last_error: z.string().optional(),
  diff_summary: z.string().max(DIFF_SUMMARY_MAX_BYTES).optional(),
  // Set on list projections, which omit the body itself; the detail route
  // carries diff_summary instead.
  has_diff_summary: z.boolean().optional(),
});
export type ServerRelationshipChild = z.output<typeof ServerRelationshipChildSchema>;

export const ServerFeatureSummarySchema = z.object({
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
  warnings: z
    .array(z.object({ code: z.string(), message: z.string() }))
    .max(100)
    .optional(),
  active_child: ServerRelationshipChildSchema.optional(),
  child_history: z.array(ServerRelationshipChildSchema).optional(),
  child_history_total: z.number().int().nonnegative().optional(),
  child_history_truncated: z.boolean().optional(),
});

export type ServerFeatureSummary = z.output<typeof ServerFeatureSummarySchema>;

export const ServerRepoStatusSchema = z.object({
  name: z.string(),
  publishable: z.boolean(),
  touched: z.boolean().optional(),
  pr_url: z.string().optional(),
  freshness: z.string().optional(),
  last_error: z.string().optional(),
  rebase_status: z.string().optional(),
  rebase_target: z.string().optional(),
  conflict_files: z.array(z.string()).optional(),
});
export type ServerRepoStatus = z.output<typeof ServerRepoStatusSchema>;

export const ServerNeedUserInputGateDetailSchema = z.object({
  feature_id: z.string().optional(),
  open: z.boolean(),
  scope: z.string().optional(),
  repo_name: z.string().optional(),
  iteration: z.number().int().nonnegative().optional(),
  summary: z.string().optional(),
  questions: z
    .array(
      z.object({
        index: z.number().int().nonnegative().optional(),
        prompt: z.string().optional(),
        answer: z.string().optional(),
      }),
    )
    .optional(),
  verification: ServerNeedUserInputVerificationSchema.optional(),
});
export type ServerNeedUserInputGateDetail = z.output<typeof ServerNeedUserInputGateDetailSchema>;

export const ServerReviewFeedbackCommentSchema = z.object({
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
export const ServerReviewFeedbackDraftCommentSchema = ServerReviewFeedbackCommentSchema.extend({
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

export const ServerFeatureDetailSchema = ServerFeatureSummarySchema.extend({
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
    mode: z.enum(['default', 'enabled', 'disabled']),
    enabled: z.boolean(),
    source: z.enum(['global', 'feature']),
  }),
  repo_status: z.array(ServerRepoStatusSchema).optional(),
  review_gate: z.object({
    reviewing_gate: z.boolean(),
    review_fixing: z.boolean(),
    validating_plan: z.boolean(),
    validator_statuses: z.record(z.string(), z.string()).optional(),
  }),
  need_user_input: ServerNeedUserInputGateDetailSchema.optional(),
  failure: z.object({ type: z.string().optional(), message: z.string().optional() }).optional(),
  verification_items: z.array(z.object({ name: z.string(), state: z.string() })).optional(),
  timing: z.object({ total_seconds: z.number().int().nonnegative() }).optional(),
  parent_id: z.string().optional(),
  parent_kind: z.string().optional(),
  active: z.boolean().optional(),
  setup_complete: z.boolean().optional(),
  close_outcome: z.string().optional(),
  closed_at: z.string().optional(),
  transaction: z
    .object({
      phase: z.string().optional(),
      attention: z.string().optional(),
      entries: z
        .array(
          z.object({
            repo: z.string().optional(),
            prep_state: z.string().optional(),
            apply_state: z.string().optional(),
            conflict_files: z.array(z.string()).optional(),
            dirty: z
              .array(
                z.object({
                  repo: z.string().optional(),
                  path: z.string().optional(),
                  staged: z.array(z.string()).optional(),
                  unstaged: z.array(z.string()).optional(),
                  untracked: z.array(z.string()).optional(),
                  staged_total: z.number().int().nonnegative().optional(),
                  unstaged_total: z.number().int().nonnegative().optional(),
                  untracked_total: z.number().int().nonnegative().optional(),
                }),
              )
              .optional(),
            cleanup_warning: z.string().optional(),
            diagnostics: z.string().optional(),
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
});

export type FeatureListResponse = z.output<typeof FeatureListResponseSchema>;

export const FeatureDetailResponseSchema = z.object({
  api_version: z.string(),
  feature: ServerFeatureDetailSchema,
});

export type FeatureDetailResponse = z.output<typeof FeatureDetailResponseSchema>;

// --- Run listing (GET /runs, GET /runs/{n}, GET /runs/{n}/sessions) --------

export const ServerRunSummarySchema = z.object({
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

export const ServerRunDetailSchema = ServerRunSummarySchema.extend({
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
export type RunListResponse = z.output<typeof RunListResponseSchema>;

export const RunDetailResponseSchema = z.object({
  api_version: z.string(),
  run: ServerRunDetailSchema,
});
export type RunDetailResponse = z.output<typeof RunDetailResponseSchema>;

// RunSessionListResponse is declared after ServerSessionSummarySchema below.

// --- Artifacts + logs (run-scoped) ------------------------------------------

export const ServerArtifactSchema = z.object({
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
export type ArtifactListResponse = z.output<typeof ArtifactListResponseSchema>;

export const ServerRunLogSchema = z.object({
  id: z.string(),
  path: z.string(),
  size: z.number().int().nonnegative(),
  modified_at: z.string(),
});
export type ServerRunLog = z.output<typeof ServerRunLogSchema>;

export const RunLogListResponseSchema = z.object({
  api_version: z.string(),
  logs: z.array(ServerRunLogSchema),
});
export type RunLogListResponse = z.output<typeof RunLogListResponseSchema>;

export const TextContentResponseSchema = z.object({
  api_version: z.string(),
  id: z.string(),
  offset: z.number().int().nonnegative(),
  limit: z.number().int().positive(),
  size: z.number().int().nonnegative(),
  text: z.string(),
  truncated: z.boolean(),
});
export type TextContentResponse = z.output<typeof TextContentResponseSchema>;

// --- Rewind preview + execution --------------------------------------------

export const ServerRewindChoiceSchema = z.object({
  phase: z.string(),
  escalates_to: z.string().optional(),
  override_phase: z.string().optional(),
});

export const ServerRewindPRConsequenceSchema = z.object({
  repo: z.string(),
  pr_url: z.string(),
});

export const ServerRewindWorktreeConsequenceSchema = z.object({
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
export type RewindPreviewResponse = z.output<typeof RewindPreviewResponseSchema>;

export const RewindActionResponseSchema = z.object({
  api_version: z.string(),
  result: z.string(),
  feature_id: z.string(),
  target_phase: z.string().optional(),
  effective_phase: z.string().optional(),
  roadmap_phase: z.number().int().nonnegative().optional(),
  upgrade_pipeline: z.string().optional(),
  warning_count: z.number().int().nonnegative().optional(),
  source_run_number: z.number().int().nonnegative().optional(),
  new_run_number: z.number().int().nonnegative().optional(),
  warnings: z.array(z.string()).optional(),
});
export type RewindActionResponse = z.output<typeof RewindActionResponseSchema>;

/** Shared shape of create/setup action responses (FeatureActionResult). */
export const FeatureActionResponseSchema = z.object({
  api_version: z.string(),
  result: z.string(),
  feature_id: z.string(),
});

export type FeatureActionResponse = z.output<typeof FeatureActionResponseSchema>;

export const ServerFeatureOperationalActionResponseSchema = FeatureActionResponseSchema.extend({
  phase: z.string().optional(),
  session_ids: z.array(z.string()).max(100).optional(),
});

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

export const ServerSessionSummarySchema = z.object({
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
export type RunSessionListResponse = z.output<typeof RunSessionListResponseSchema>;

export const ServerTranscriptCursorSchema = z.object({
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

export const ServerTranscriptMessageSchema = z.object({
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
    safe_error: z
      .string()
      .max(1024 * 1024)
      .optional(),
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

// --- Runtime config (GET /api/v1/config/runtime) — creation-defaults subset --

export const ServerModelDefaultsSchema = z.object({
  inquiry: z.string().optional(),
  research: z.string().optional(),
  planning: z.string().optional(),
  implementation: z.string().optional(),
  review: z.string().optional(),
  utilities: z.string().optional(),
  kb_build: z.string().optional(),
});

export const ServerEffortDefaultsSchema = z.object({
  inquiry: z.string().optional(),
  research: z.string().optional(),
  planning: z.string().optional(),
  implementation: z.string().optional(),
  review: z.string().optional(),
  utilities: z.string().optional(),
  kb_build: z.string().optional(),
});

export const RuntimeConfigCreationSchema = z.object({
  api_version: z.string(),
  feature_defaults: z.object({
    models: ServerModelDefaultsSchema,
    effort: ServerEffortDefaultsSchema.optional(),
    inquireness: z.string().optional(),
    pipeline: z.string().optional(),
  }),
});

export type RuntimeConfigCreation = z.output<typeof RuntimeConfigCreationSchema>;

// --- Structured server error bodies (ErrorResponse) -------------------------
// Lenient on purpose: error paths must degrade gracefully, never fail open.

export const ServerErrorResponseSchema = z.object({
  error: z.object({
    code: z.string(),
    message: z.string(),
  }),
});

/**
 * Error body including the structured `target` payload the server attaches
 * to 409 `not_ready` creation rejections (outstanding readiness issues).
 */
export const ServerErrorWithIssuesSchema = z.object({
  error: z.object({
    code: z.string(),
    message: z.string(),
    target: z
      .object({
        issues: z
          .array(z.object({ code: z.string(), message: z.string(), remedy: z.string().optional() }))
          .optional(),
        repos: z
          .array(
            z.object({
              repo: z.string().optional(),
              path: z.string().optional(),
              staged: z.array(z.string()).optional(),
              unstaged: z.array(z.string()).optional(),
              untracked: z.array(z.string()).optional(),
              staged_total: z.number().int().nonnegative().optional(),
              unstaged_total: z.number().int().nonnegative().optional(),
              untracked_total: z.number().int().nonnegative().optional(),
            }),
          )
          .optional(),
      })
      .optional(),
  }),
});

// --- Recovery, cycle actions, preflight, and bulk preview -------------------

export const ServerRecoveryItemSchema = z.object({
  key: z.string(),
  feature_id: z.string(),
  feature_name: z.string().optional(),
  repo_name: z.string().optional(),
  phase: z.string().optional(),
  iteration: z.number().int().nonnegative().optional(),
  pid: z.number().int().optional(),
  process_alive: z.boolean(),
  log_available: z.boolean().optional(),
  allowed_actions: z.array(z.string()),
  default_action: z.string(),
});
export type ServerRecoveryItem = z.output<typeof ServerRecoveryItemSchema>;

export const RecoverySnapshotResponseSchema = z.object({
  api_version: z.string(),
  snapshot_id: z.string(),
  items: z.array(ServerRecoveryItemSchema),
});
export type RecoverySnapshotResponse = z.output<typeof RecoverySnapshotResponseSchema>;

export const RecoveryActionResponseSchema = z.object({
  api_version: z.string(),
  result: z.string(),
});
export type RecoveryActionResponse = z.output<typeof RecoveryActionResponseSchema>;

export const RebaseFeatureResponseSchema = z.object({
  api_version: z.string(),
  feature_id: z.string(),
  parent_id: z.string(),
  result: z.string(),
});
export type RebaseFeatureResponse = z.output<typeof RebaseFeatureResponseSchema>;

export const RefactorFeatureResponseSchema = z.object({
  api_version: z.string(),
  feature_id: z.string(),
  parent_id: z.string(),
  result: z.string(),
});
export type RefactorFeatureResponse = z.output<typeof RefactorFeatureResponseSchema>;

export const ReviewFeedbackFetchResponseSchema = z.object({
  api_version: z.string(),
  revision: z.number().int().nonnegative(),
  snapshot_id: z.string(),
  repos: z.array(ServerReviewFeedbackRepoCommentsSchema),
});
export type ReviewFeedbackFetchResponse = z.output<typeof ReviewFeedbackFetchResponseSchema>;

export const ReviewFeedbackSelectionResponseSchema = z.object({
  api_version: z.string(),
  revision: z.number().int().nonnegative(),
  repos: z.array(ServerReviewFeedbackRepoCommentsSchema),
});
export type ReviewFeedbackSelectionResponse = z.output<
  typeof ReviewFeedbackSelectionResponseSchema
>;

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
export type ReviewFeedbackFeatureResponse = z.output<typeof ReviewFeedbackFeatureResponseSchema>;

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

export const CompletionPreflightRepoSchema = z.object({
  repo: z.string(),
  publishable: z.boolean(),
  touched: z.boolean(),
  status: z.string(),
  pr_url: z.string().optional(),
  blocker: z.string().optional(),
  freshness: z.string().optional(),
  last_error: z.string().optional(),
  base_branch: z.string().optional(),
  branch: z.string().optional(),
  pending_commits: z.number().optional(),
  pending_dirty: z.boolean().optional(),
  push_mode: z.string().optional(),
  pending_dirty_files: z.array(z.string()).optional(),
  pending_dirty_file_total: z.number().optional(),
});
export type CompletionPreflightRepo = z.output<typeof CompletionPreflightRepoSchema>;

export const CompletionPreflightResponseSchema = z.object({
  api_version: z.string(),
  feature_id: z.string(),
  source_revision: z.string(),
  can_mark_done: z.boolean().optional(),
  mark_done_blocker: z.string().optional(),
  repos: z.array(CompletionPreflightRepoSchema),
});
export type CompletionPreflightResponse = z.output<typeof CompletionPreflightResponseSchema>;

export const RepositoryDiffFileSchema = z.object({
  path: z.string(),
  old_path: z.string().optional(),
  operation: z.string(),
  added_lines: z.number().optional(),
  removed_lines: z.number().optional(),
  binary: z.boolean().optional(),
  fingerprint: z.string().optional(),
});
export type RepositoryDiffFile = z.output<typeof RepositoryDiffFileSchema>;

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
  partial_failure: z.string().optional(),
});
export type RepositoryDiffResponse = z.output<typeof RepositoryDiffResponseSchema>;

export const RepositoryPathResponseSchema = z.object({
  api_version: z.string(),
  feature_id: z.string(),
  repo: z.string(),
  path: z.string(),
});
export type RepositoryPathResponse = z.output<typeof RepositoryPathResponseSchema>;

export const PublishDescriptionResponseSchema = z.object({
  api_version: z.string(),
  feature_id: z.string(),
  result: z.string(),
  title: z.string(),
  body: z.string(),
});
export type PublishDescriptionResponse = z.output<typeof PublishDescriptionResponseSchema>;

// Compile-time drift guards: zod outputs must stay assignable to the
// generated OpenAPI component types.
type HealthDTO = components['schemas']['HealthResponse'];
const _healthAssignable = (value: HealthResponse): HealthDTO => value;
void _healthAssignable;
type CompatibilityDTO = components['schemas']['CompatibilityDeclaration'];
const _compatibilityAssignable = (value: CompatibilityDeclaration): CompatibilityDTO => value;
void _compatibilityAssignable;
type ReadinessDTO = components['schemas']['ReadinessResponse'];
const _readinessAssignable = (value: ReadinessResponse): ReadinessDTO => value;
void _readinessAssignable;
// Reverse guard for the subset schema: every field it parses must exist on
// the generated RuntimeConfigResponse with a compatible type.
type RuntimeConfigDTO = components['schemas']['RuntimeConfigResponse'];
const _runtimeConfigSubset = (value: RuntimeConfigDTO): RuntimeConfigWorkspace => value;
void _runtimeConfigSubset;
// Reverse guards for the feature subsets: every parsed field must exist on
// the generated types with a compatible shape.
type FeatureListDTO = components['schemas']['FeatureListResponse'];
const _featureListSubset = (value: FeatureListDTO): FeatureListResponse => value;
void _featureListSubset;
type FeatureDetailResponseDTO = components['schemas']['FeatureDetailResponse'];
const _featureDetailSubset = (value: FeatureDetailResponseDTO): FeatureDetailResponse => value;
void _featureDetailSubset;
type SetupDTO = components['schemas']['Setup'];
const _setupSubset = (value: SetupDTO): ServerSetup => value;
void _setupSubset;
type SetupTaskDTO = components['schemas']['SetupTask'];
const _setupTaskSubset = (value: SetupTaskDTO): ServerSetupTask => value;
void _setupTaskSubset;
type CreateFeatureResponseDTO = components['schemas']['CreateFeatureResponse'];
const _createFeatureSubset = (value: CreateFeatureResponseDTO): FeatureActionResponse => value;
void _createFeatureSubset;
type FeatureSetupResponseDTO = components['schemas']['FeatureSetupResponse'];
const _featureSetupSubset = (value: FeatureSetupResponseDTO): FeatureActionResponse => value;
void _featureSetupSubset;
type SessionListDTO = components['schemas']['SessionListResponse'];
const _sessionListSubset = (value: SessionListDTO): z.output<typeof SessionListResponseSchema> =>
  value;
void _sessionListSubset;
type SessionDetailDTO = components['schemas']['SessionDetailResponse'];
const _sessionDetailSubset = (
  value: SessionDetailDTO,
): z.output<typeof SessionDetailResponseSchema> => value;
void _sessionDetailSubset;
type TranscriptDTO = components['schemas']['TranscriptResponse'];
const _transcriptSubset = (value: TranscriptDTO): z.output<typeof TranscriptResponseSchema> =>
  value;
void _transcriptSubset;
type OutputChunkDTO = components['schemas']['SessionOutputChunk'];
const _outputChunkSubset = (value: OutputChunkDTO): z.output<typeof SessionOutputChunkSchema> =>
  value;
void _outputChunkSubset;
type ReviewSessionDTO = components['schemas']['ReviewSessionResponse'];
const _reviewSessionSubset = (value: ReviewSessionDTO): ServerReviewSession => value;
void _reviewSessionSubset;
type ReviewValidationDTO = components['schemas']['ReviewDraftValidationResponse'];
const _reviewValidationSubset = (value: ReviewValidationDTO): ServerReviewDraftValidation => value;
void _reviewValidationSubset;
type ReviewDecisionDTO = components['schemas']['ReviewSessionDecisionResponse'];
const _reviewDecisionSubset = (value: ReviewDecisionDTO): ServerReviewDecision => value;
void _reviewDecisionSubset;
const _runtimeConfigCreationSubset = (value: RuntimeConfigDTO): RuntimeConfigCreation => value;
void _runtimeConfigCreationSubset;
type RunListDTO = components['schemas']['RunListResponse'];
const _runListSubset = (value: RunListDTO): RunListResponse => value;
void _runListSubset;
type RunDetailDTO = components['schemas']['RunDetailResponse'];
const _runDetailSubset = (value: RunDetailDTO): RunDetailResponse => value;
void _runDetailSubset;
type RunSessionListDTO = components['schemas']['RunSessionListResponse'];
const _runSessionListSubset = (value: RunSessionListDTO): RunSessionListResponse => value;
void _runSessionListSubset;
type ArtifactListDTO = components['schemas']['ArtifactListResponse'];
const _artifactListSubset = (value: ArtifactListDTO): ArtifactListResponse => value;
void _artifactListSubset;
type TextContentDTO = components['schemas']['TextContentResponse'];
const _textContentSubset = (value: TextContentDTO): TextContentResponse => value;
void _textContentSubset;
type RecoverySnapshotDTO = components['schemas']['RecoverySnapshotResponse'];
const _recoverySnapshotSubset = (value: RecoverySnapshotDTO): RecoverySnapshotResponse => value;
void _recoverySnapshotSubset;
type RecoveryItemDTO = components['schemas']['RecoveryItem'];
const _recoveryItemSubset = (value: RecoveryItemDTO): ServerRecoveryItem => value;
void _recoveryItemSubset;
type RebaseFeatureDTO = components['schemas']['RebaseFeatureResponse'];
const _rebaseFeatureSubset = (value: RebaseFeatureDTO): RebaseFeatureResponse => value;
void _rebaseFeatureSubset;
type RefactorFeatureDTO = components['schemas']['RefactorFeatureResponse'];
const _refactorFeatureSubset = (value: RefactorFeatureDTO): RefactorFeatureResponse => value;
void _refactorFeatureSubset;
type ReviewFeedbackFetchDTO = components['schemas']['ReviewFeedbackFetchResponse'];
const _reviewFeedbackFetchSubset = (value: ReviewFeedbackFetchDTO): ReviewFeedbackFetchResponse =>
  value;
void _reviewFeedbackFetchSubset;
type ReviewFeedbackCommentDTO = components['schemas']['ReviewFeedbackComment'];
const _reviewFeedbackCommentSubset = (
  value: ReviewFeedbackCommentDTO,
): ServerReviewFeedbackComment => value;
void _reviewFeedbackCommentSubset;
type ReviewFeedbackDraftCommentDTO = components['schemas']['ReviewFeedbackDraftComment'];
const _reviewFeedbackDraftCommentSubset = (
  value: ReviewFeedbackDraftCommentDTO,
): ServerReviewFeedbackDraftComment => value;
void _reviewFeedbackDraftCommentSubset;
type ReviewFeedbackSelectionDTO = components['schemas']['ReviewFeedbackSelectionResponse'];
const _reviewFeedbackSelectionSubset = (
  value: ReviewFeedbackSelectionDTO,
): ReviewFeedbackSelectionResponse => value;
void _reviewFeedbackSelectionSubset;
type ReviewFeedbackFeatureDTO = components['schemas']['ReviewFeedbackFeatureResponse'];
const _reviewFeedbackFeatureSubset = (
  value: ReviewFeedbackFeatureDTO,
): ReviewFeedbackFeatureResponse => value;
void _reviewFeedbackFeatureSubset;
type RepoStatusDTO = components['schemas']['RepoStatus'];
const _repoStatusSubset = (value: RepoStatusDTO): ServerRepoStatus => value;
void _repoStatusSubset;
type NeedUserInputGateDTO = components['schemas']['NeedUserInputGate'];
const _needUserInputGateSubset = (value: NeedUserInputGateDTO): ServerNeedUserInputGateDetail =>
  value;
void _needUserInputGateSubset;
type NeedUserInputVerificationDTO = components['schemas']['NeedUserInputVerification'];
const _needUserInputVerificationSubset = (
  value: NeedUserInputVerificationDTO,
): z.output<typeof ServerNeedUserInputVerificationSchema> => value;
void _needUserInputVerificationSubset;
type RewindPreviewDTO = components['schemas']['RewindPreviewResponse'];
const _rewindPreviewSubset = (value: RewindPreviewDTO): RewindPreviewResponse => value;
void _rewindPreviewSubset;
type RewindActionDTO = components['schemas']['RewindFeatureResponse'];
const _rewindActionSubset = (value: RewindActionDTO): RewindActionResponse => value;
void _rewindActionSubset;
type RepositoryPathDTO = components['schemas']['RepositoryPathDTO'];
const _repositoryPathSubset = (value: RepositoryPathDTO): RepositoryPathResponse => value;
void _repositoryPathSubset;
type PublishDescriptionDTO = components['schemas']['PublishDescriptionResponse'];
const _publishDescriptionSubset = (value: PublishDescriptionDTO): PublishDescriptionResponse =>
  value;
void _publishDescriptionSubset;
type CompletionPreflightDTO = components['schemas']['CompletionPreflightResponse'];
const _completionPreflightSubset = (value: CompletionPreflightDTO): CompletionPreflightResponse =>
  value;
void _completionPreflightSubset;
type RepositoryDiffDTO = components['schemas']['RepositoryDiffResponse'];
const _repositoryDiffSubset = (value: RepositoryDiffDTO): RepositoryDiffResponse => value;
void _repositoryDiffSubset;
