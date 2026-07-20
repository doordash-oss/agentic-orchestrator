/**
 * Guarded test-only hooks. The packaged E2E journeys need every launch to be
 * hermetic, but Electron derives the app-local data directory (settings.json
 * etc.) from the OS user profile, which HOME cannot redirect on macOS. The
 * single hook here lets a test relocate that directory — and nothing else.
 *
 * Guard rails, deliberately strict:
 *  - The override must resolve (symlink-free) to a directory INSIDE the OS
 *    temp directory, so it can never point the app at another user's — or
 *    the real user's — data.
 *  - It only moves where this user's own presentation settings live; it
 *    grants an env-controlling caller nothing they do not already have
 *    (HOME relocation moves the same data on Linux). It never influences
 *    which binaries run or which runtime is trusted — the bundled-server
 *    resolution ignores env overrides when packaged (see gateway/resources).
 */

export interface TestUserDataDeps {
  /** Resolves symlinks; must throw when the path does not exist. */
  realpath(candidate: string): string;
  /** The OS temp directory (os.tmpdir()). */
  tmpdir(): string;
  /** path.isAbsolute for the host platform. */
  isAbsolute(candidate: string): boolean;
  /** The platform path separator. */
  sep: string;
}

/**
 * Validates the AGENTICO_E2E_USER_DATA override. Returns the canonical
 * directory to use, or null when the override is absent or fails any guard
 * (never throws — a bad override is silently inert).
 */
export function resolveTestUserDataDir(
  candidate: string | undefined,
  deps: TestUserDataDeps,
): string | null {
  if (candidate === undefined || candidate === '' || !deps.isAbsolute(candidate)) {
    return null;
  }
  let real: string;
  let tempRoot: string;
  try {
    real = deps.realpath(candidate);
    tempRoot = deps.realpath(deps.tmpdir());
  } catch {
    return null; // must already exist — the hook never creates directories
  }
  if (real === tempRoot || real.startsWith(tempRoot + deps.sep)) {
    return real;
  }
  return null;
}

export interface TestPackagedResourcesDeps {
  realpath(candidate: string): string;
  isAbsolute(candidate: string): boolean;
  exists(candidate: string): boolean;
  join(...parts: string[]): string;
}

export function resolveTestPackagedResourcesDir(
  candidate: string | undefined,
  testUserDataDir: string | null,
  deps: TestPackagedResourcesDeps,
): string | null {
  if (
    testUserDataDir === null ||
    candidate === undefined ||
    candidate === '' ||
    !deps.isAbsolute(candidate)
  ) {
    return null;
  }
  let real: string;
  try {
    real = deps.realpath(candidate);
  } catch {
    return null;
  }
  for (const required of [
    deps.join(real, 'app.asar'),
    deps.join(real, 'build-identity.json'),
    deps.join(real, 'bin', 'agentico'),
  ]) {
    if (!deps.exists(required)) {
      return null;
    }
  }
  return real;
}

export interface TestOutputFileDeps {
  realpath(candidate: string): string;
  dirname(candidate: string): string;
  tmpdir(): string;
  isAbsolute(candidate: string): boolean;
  sep: string;
}

export function resolveTestOutputFile(
  candidate: string | undefined,
  testUserDataDir: string | null,
  deps: TestOutputFileDeps,
): string | null {
  if (
    testUserDataDir === null ||
    candidate === undefined ||
    candidate === '' ||
    !deps.isAbsolute(candidate)
  ) {
    return null;
  }
  let parent: string;
  let tempRoot: string;
  try {
    parent = deps.realpath(deps.dirname(candidate));
    tempRoot = deps.realpath(deps.tmpdir());
  } catch {
    return null;
  }
  if (parent === tempRoot || parent.startsWith(tempRoot + deps.sep)) {
    return candidate;
  }
  return null;
}
