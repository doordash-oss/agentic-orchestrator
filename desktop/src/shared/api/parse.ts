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
  label: z.string().max(200).optional(),
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
  pending: z.boolean(),
  time: z.string().max(100).optional(),
});
export const ServerNeedUserInputGateSchema = z.object({
  feature_id: AttentionIDSchema.optional(),
  open: z.boolean(),
  scope: z.string().max(100).optional(),
  repo_name: z.string().max(500).optional(),
  cycle_type: z.string().max(200).optional(),
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

export const ServerActionSchema = z.object({
  id: z.string(),
  enabled: z.boolean(),
  disabled_reasons: z.array(z.object({ code: z.string(), message: z.string() })).optional(),
});

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
  progress: z.object({ current_phase_status: z.string().optional() }),
  warnings: z
    .array(z.object({ code: z.string(), message: z.string() }))
    .max(100)
    .optional(),
});

export type ServerFeatureSummary = z.output<typeof ServerFeatureSummarySchema>;

export const ServerFeatureDetailSchema = ServerFeatureSummarySchema.extend({
  description: z.string().optional(),
  pipeline: z.string().optional(),
  active_run_detail: z
    .object({
      setup: ServerSetupSchema.optional(),
    })
    .optional(),
  actions: z.array(ServerActionSchema),
  failure: z.object({ type: z.string().optional(), message: z.string().optional() }).optional(),
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
  usage: ServerUsageSchema,
});
export type ServerSessionSummary = z.output<typeof ServerSessionSummarySchema>;

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

export const RuntimeConfigCreationSchema = z.object({
  api_version: z.string(),
  feature_defaults: z.object({
    models: ServerModelDefaultsSchema,
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
      })
      .optional(),
  }),
});

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
