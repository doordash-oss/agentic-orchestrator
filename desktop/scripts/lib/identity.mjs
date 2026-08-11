// Build-identity schema shared by packaging (prepare-server.mjs writes
// build-identity.json into extraResources) and verification
// (verify-package.mjs validates the shipped file and cross-checks it against
// the bundled server binary). Pure functions only: no fs, no child_process,
// so the rejection paths are unit-testable without rebuilding packages.

/** Ordered list of required identity fields (the closed schema). */
export const IDENTITY_FIELDS = Object.freeze([
  'desktop_version',
  'api_version',
  'schema_version',
  'server_version',
  'server_revision',
  'os',
  'arch',
  'built_at',
]);

const OS_ARCHES = Object.freeze({
  // macOS ships a single lipo'd universal DMG; linux ships per-arch artifacts.
  darwin: Object.freeze(['universal']),
  linux: Object.freeze(['x64', 'arm64']),
});

function isNonEmptyString(value) {
  return typeof value === 'string' && value.trim() !== '';
}

/**
 * Validate an arbitrary parsed value against the identity schema.
 * Returns { ok, errors } — never throws — so callers can aggregate a full
 * report before failing.
 */
export function validateBuildIdentity(value) {
  const errors = [];
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    return { ok: false, errors: ['build identity must be a JSON object'] };
  }
  for (const field of IDENTITY_FIELDS) {
    if (!(field in value)) {
      errors.push(`missing required field: ${field}`);
      continue;
    }
    if (field === 'schema_version') {
      const v = value[field];
      if (!Number.isInteger(v) || v < 1) {
        errors.push(`schema_version must be a positive integer, got ${JSON.stringify(v)}`);
      }
      continue;
    }
    if (!isNonEmptyString(value[field])) {
      errors.push(`${field} must be a non-empty string, got ${JSON.stringify(value[field])}`);
    }
  }
  for (const key of Object.keys(value)) {
    if (!IDENTITY_FIELDS.includes(key)) {
      errors.push(`unexpected field: ${key}`);
    }
  }
  if (isNonEmptyString(value.os)) {
    const allowed = OS_ARCHES[value.os];
    if (allowed === undefined) {
      errors.push(`os must be one of ${Object.keys(OS_ARCHES).join(', ')}, got ${value.os}`);
    } else if (isNonEmptyString(value.arch) && !allowed.includes(value.arch)) {
      errors.push(
        `arch for os=${value.os} must be one of ${allowed.join(', ')}, got ${value.arch}`,
      );
    }
  }
  if (isNonEmptyString(value.built_at) && Number.isNaN(Date.parse(value.built_at))) {
    errors.push(`built_at must be an ISO-8601 timestamp, got ${JSON.stringify(value.built_at)}`);
  }
  return { ok: errors.length === 0, errors };
}

/**
 * Build a validated, frozen identity object. Throws on any schema violation
 * so packaging fails loudly instead of shipping a hollow identity.
 */
export function createBuildIdentity(fields) {
  const result = validateBuildIdentity(fields);
  if (!result.ok) {
    throw new Error(`invalid build identity:\n  ${result.errors.join('\n  ')}`);
  }
  return Object.freeze({ ...fields });
}

/**
 * Parse `agentico --version` stdout, i.e. buildinfo.VersionLine():
 * "agentico v<injected-version>" or
 * "agentico v<injected-version> (revision <full-sha>)".
 * Packaging injects the raw `git describe` output (which itself starts with
 * "v" when tags do), so only the literal "agentico v" prefix is stripped.
 * Returns { version, revision } (revision null for unstamped builds), or
 * null when the output is unrecognizable.
 */
export function parseAgenticoVersionOutput(output) {
  const match = /^agentico v(\S+)(?: \(revision ([0-9a-f]+)\))?\s*$/m.exec(output);
  return match === null ? null : { version: match[1], revision: match[2] ?? null };
}

/**
 * Parse `go version -m <binary>` output for the toolchain-stamped VCS
 * revision and target GOOS/GOARCH. Works on cross-compiled binaries without
 * executing them.
 */
export function parseGoBuildInfo(output) {
  const grab = (key) => {
    const match = new RegExp(`^\\tbuild\\t${key}=(\\S+)$`, 'm').exec(output);
    return match === null ? null : match[1];
  };
  return { revision: grab('vcs\\.revision'), goos: grab('GOOS'), goarch: grab('GOARCH') };
}

/**
 * Cross-check the shipped build-identity.json against the bundled server
 * binary's actual identity. probe fields come from running the binary
 * (--version) and reading its Go build info (go version -m). Returns a list
 * of human-readable errors; empty means the package is internally consistent.
 */
export function crossCheckServerBinary(identity, probe) {
  const errors = [];
  if (probe.reportedVersion !== identity.server_version) {
    errors.push(
      `server binary version mismatch: identity server_version=${identity.server_version}, ` +
        `binary reported ${probe.reportedVersion ?? '(unparseable)'}`,
    );
  }
  if (probe.reportedRevision === null || probe.reportedRevision === undefined) {
    errors.push(
      `server binary carries no VCS stamp; expected server_revision=${identity.server_revision}`,
    );
  } else if (probe.reportedRevision !== identity.server_revision) {
    errors.push(
      `server binary revision mismatch: identity server_revision=${identity.server_revision}, ` +
        `binary stamped ${probe.reportedRevision}`,
    );
  }
  if (probe.reportedGoos !== identity.os) {
    errors.push(
      `server binary GOOS mismatch: identity os=${identity.os}, binary built for ` +
        `${probe.reportedGoos ?? '(unknown)'}`,
    );
  }
  const expectedGoarch = expectedGoarchForIdentity(identity);
  if (expectedGoarch !== null && probe.reportedGoarch !== expectedGoarch) {
    errors.push(
      `server binary GOARCH mismatch: package target ${identity.os}/${identity.arch} requires ` +
        `${expectedGoarch}, binary built for ${probe.reportedGoarch ?? '(unknown)'}`,
    );
  }
  return errors;
}

/** Map a package identity to the single GOARCH its bundled server must carry. */
export function expectedGoarchForIdentity(identity) {
  if (identity.os === 'linux') return identity.arch === 'x64' ? 'amd64' : 'arm64';
  // Each slice of the macOS universal binary is independently inspected below.
  return null;
}

/**
 * Extract info.version from the OpenAPI YAML without a YAML dependency:
 * scoped to the top-level `info:` block, tolerating quoted scalars.
 * Throws when absent so identity generation cannot silently drop the field.
 */
export function parseOpenApiInfoVersion(yamlText) {
  const infoMatch = /^info:\n((?:[ \t]+\S.*\n?)+)/m.exec(yamlText);
  if (infoMatch !== null) {
    const versionMatch = /^[ \t]+version:[ \t]*['"]?([^'"\n]+?)['"]?[ \t]*$/m.exec(infoMatch[1]);
    if (versionMatch !== null) {
      return versionMatch[1];
    }
  }
  throw new Error('could not locate info.version in api/openapi.yaml');
}

/**
 * Extract the CompatibilitySchemaVersion constant from
 * internal/server/compatibility.go. Throws when absent.
 */
export function parseCompatibilitySchemaVersion(goSource) {
  const match = /^\s*CompatibilitySchemaVersion\s*=\s*(\d+)\s*$/m.exec(goSource);
  if (match === null) {
    throw new Error(
      'could not locate the CompatibilitySchemaVersion constant in internal/server/compatibility.go',
    );
  }
  return Number.parseInt(match[1], 10);
}
