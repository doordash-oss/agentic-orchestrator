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

// --- Runtime schemas for the responses Phase 1 consumes -------------------
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

// --- Runtime config (GET /api/v1/config/runtime) — workspace-roots subset ---
// Only the fields the desktop setup flow consumes; the full response has
// many more, which z.object tolerates and strips.

export const RuntimeConfigWorkspaceSchema = z.object({
  api_version: z.string(),
  workspace_roots: z.array(z.string()).optional(),
});

export type RuntimeConfigWorkspace = z.output<typeof RuntimeConfigWorkspaceSchema>;

// --- Structured server error bodies (ErrorResponse) -------------------------
// Lenient on purpose: error paths must degrade gracefully, never fail open.

export const ServerErrorResponseSchema = z.object({
  error: z.object({
    code: z.string(),
    message: z.string(),
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
