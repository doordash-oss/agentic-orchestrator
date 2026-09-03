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
 * The desktop side of the explicit compatibility handshake. The server
 * declares its contract on /api/v1/health (CompatibilityDeclaration in
 * api/openapi.yaml); this module decides whether THIS build of the desktop
 * app supports it. A shared API major alone is never sufficient — the
 * declared schema series and runtime policy must both be supported, and the
 * server must accept this client's schema series. Build identities are
 * informational only and never gate attachment.
 */
import { CompatibilityDeclarationSchema, type BuildIdentity } from '../../shared/api/parse';
import type { CompatibilityFailure } from '../../shared/errors';

export type { BuildIdentity };

/** The client schema series this desktop build implements. */
export const DESKTOP_SCHEMA_VERSION = 1;

/** Server REST API majors this desktop build speaks. */
export const SUPPORTED_SERVER_API_VERSIONS: readonly string[] = ['v1'];

/** Server schema contract series this desktop build supports. */
export const SUPPORTED_SERVER_SCHEMA_VERSIONS: readonly number[] = [1];

/**
 * Runtime security/ownership policy contracts this desktop build supports:
 * the loopback policy (loopback-only listener) and the network policy
 * (non-loopback listener with bearer auth and strict Origin enforcement).
 */
export const SUPPORTED_RUNTIME_POLICIES: readonly string[] = [
  'loopback-bearer-v1',
  'network-bearer-v1',
];

export type CompatibilityVerdict =
  { compatible: true; serverBuild: BuildIdentity } | CompatibilityFailure;

/**
 * Evaluates the server's declaration. Absent or unparseable declarations
 * fail closed: an old server that predates the declaration is treated as
 * incompatible rather than assumed compatible.
 */
export function evaluateCompatibility(declaration: unknown): CompatibilityVerdict {
  if (declaration === undefined || declaration === null) {
    return {
      compatible: false,
      code: 'missing_contract',
    };
  }
  const parsed = CompatibilityDeclarationSchema.safeParse(declaration);
  if (!parsed.success) {
    return {
      compatible: false,
      code: 'unrecognized_contract',
    };
  }
  const decl = parsed.data;
  if (!SUPPORTED_SERVER_API_VERSIONS.includes(decl.api_version)) {
    return {
      compatible: false,
      code: 'unsupported_api',
      apiVersion: decl.api_version,
    };
  }
  if (!SUPPORTED_SERVER_SCHEMA_VERSIONS.includes(decl.schema_version)) {
    return {
      compatible: false,
      code: 'unsupported_schema',
      schemaVersion: String(decl.schema_version),
    };
  }
  if (decl.min_client_schema > DESKTOP_SCHEMA_VERSION) {
    return {
      compatible: false,
      code: 'newer_client_schema_required',
      schemaVersion: String(decl.min_client_schema),
      clientSchemaVersion: String(DESKTOP_SCHEMA_VERSION),
    };
  }
  if (!SUPPORTED_RUNTIME_POLICIES.includes(decl.runtime_policy)) {
    return {
      compatible: false,
      code: 'unsupported_runtime_policy',
    };
  }
  const serverBuild: BuildIdentity =
    decl.server_build.revision === undefined
      ? { version: decl.server_build.version }
      : { version: decl.server_build.version, revision: decl.server_build.revision };
  return { compatible: true, serverBuild };
}
