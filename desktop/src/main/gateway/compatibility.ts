/**
 * The desktop side of the explicit compatibility handshake. The server
 * declares its contract on /api/v1/health (CompatibilityDeclaration in
 * api/openapi.yaml); this module decides whether THIS build of the desktop
 * app supports it. A shared API major alone is never sufficient — the
 * declared schema series and runtime policy must both be supported, and the
 * server must accept this client's schema series. Build identities are
 * informational only and never gate attachment.
 */
import { CompatibilityDeclarationSchema, type BuildIdentity } from '../../shared/api/parse';

export type { BuildIdentity };

/** The client schema series this desktop build implements. */
export const DESKTOP_SCHEMA_VERSION = 1;

/** Server REST API majors this desktop build speaks. */
export const SUPPORTED_SERVER_API_VERSIONS: readonly string[] = ['v1'];

/** Server schema contract series this desktop build supports. */
export const SUPPORTED_SERVER_SCHEMA_VERSIONS: readonly number[] = [1];

/** Runtime security/ownership policy contracts this desktop build supports. */
export const SUPPORTED_RUNTIME_POLICIES: readonly string[] = ['loopback-bearer-v1'];

export type CompatibilityVerdict =
  { compatible: true; serverBuild: BuildIdentity } | { compatible: false; reason: string };

/**
 * Evaluates the server's declaration. Absent or unparseable declarations
 * fail closed: an old server that predates the declaration is treated as
 * incompatible rather than assumed compatible.
 */
export function evaluateCompatibility(declaration: unknown): CompatibilityVerdict {
  if (declaration === undefined || declaration === null) {
    return {
      compatible: false,
      reason: 'The server does not declare a compatibility contract.',
    };
  }
  const parsed = CompatibilityDeclarationSchema.safeParse(declaration);
  if (!parsed.success) {
    return {
      compatible: false,
      reason: 'The server declares an unrecognized compatibility contract.',
    };
  }
  const decl = parsed.data;
  if (!SUPPORTED_SERVER_API_VERSIONS.includes(decl.api_version)) {
    return {
      compatible: false,
      reason: `The server serves API ${decl.api_version}, which this app does not support.`,
    };
  }
  if (!SUPPORTED_SERVER_SCHEMA_VERSIONS.includes(decl.schema_version)) {
    return {
      compatible: false,
      reason: `The server declares schema series ${decl.schema_version}, which this app does not support.`,
    };
  }
  if (decl.min_client_schema > DESKTOP_SCHEMA_VERSION) {
    return {
      compatible: false,
      reason:
        `The server requires client schema ${decl.min_client_schema}, ` +
        `but this desktop build implements ${DESKTOP_SCHEMA_VERSION}.`,
    };
  }
  if (!SUPPORTED_RUNTIME_POLICIES.includes(decl.runtime_policy)) {
    return {
      compatible: false,
      reason: 'The server enforces a runtime policy this app does not support.',
    };
  }
  const serverBuild: BuildIdentity =
    decl.server_build.revision === undefined
      ? { version: decl.server_build.version }
      : { version: decl.server_build.version, revision: decl.server_build.revision };
  return { compatible: true, serverBuild };
}
