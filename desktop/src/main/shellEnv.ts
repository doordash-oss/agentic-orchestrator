/**
 * Login-shell PATH resolution for GUI launches.
 *
 * Apps started from Spotlight/Finder/Dock (and via `open`, which the CLI's
 * desktop launcher uses) inherit launchd's minimal PATH, not the user's shell
 * PATH. The bundled Go server probes provider CLIs with exec.LookPath in the
 * environment it inherits from this process, so without the login-shell PATH
 * it cannot see CLIs installed under Homebrew, the npm prefix, ~/.local/bin,
 * or version managers. Resolve the login shell's PATH once at startup and
 * append its missing entries to process.env.PATH before the server first
 * spawns.
 *
 * Append-only on purpose: whatever the launch environment already put first
 * (dev overrides, E2E stub directories) keeps precedence.
 */
import { execFile } from 'node:child_process';

export interface ShellEnvOptions {
  env: NodeJS.ProcessEnv;
  platform?: NodeJS.Platform;
  runShell?: (shell: string, args: readonly string[]) => Promise<string>;
  timeoutMs?: number;
}

export interface ShellEnvOutcome {
  applied: boolean;
  /** Entries appended to PATH (empty when nothing was missing). */
  added: string[];
  /** Why nothing was applied (skipped platforms, failures, no additions). */
  reason?: string;
}

/** Bounded so a hanging shell profile cannot stall app startup forever. */
const DEFAULT_TIMEOUT_MS = 4000;
const MAX_OUTPUT_BYTES = 1024 * 1024;

/**
 * First line-anchored PATH= entry wins. Profiles are free to print noise
 * around `env` output, so anchor on line starts instead of trusting the whole
 * stream.
 */
export function parsePathFromEnvOutput(output: string): string | null {
  const match = /^PATH=(.*)$/m.exec(output);
  if (match === null) return null;
  const value = match[1]?.trim() ?? '';
  return value === '' ? null : value;
}

/** Append shell-only entries after the current ones, deduplicated. */
export function mergePathLists(currentPath: string | undefined, shellPath: string): string {
  const current = (currentPath ?? '').split(':').filter((entry) => entry !== '');
  const seen = new Set(current);
  const merged = [...current];
  for (const entry of shellPath.split(':')) {
    if (entry !== '' && !seen.has(entry)) {
      seen.add(entry);
      merged.push(entry);
    }
  }
  return merged.join(':');
}

function defaultRunShell(timeoutMs: number) {
  return (shell: string, args: readonly string[]): Promise<string> =>
    new Promise((resolve, reject) => {
      execFile(
        shell,
        [...args],
        { timeout: timeoutMs, maxBuffer: MAX_OUTPUT_BYTES, windowsHide: true },
        (error, stdout) => {
          if (error !== null) reject(error);
          else resolve(stdout);
        },
      );
    });
}

/**
 * Resolve the login shell's PATH and append its missing entries to env.PATH.
 * Never throws: on any failure the launch environment stays untouched and the
 * outcome says why, so the caller can surface it through diagnostics (the
 * first question behind "provider CLI was not found" is always "what PATH did
 * the server get").
 */
export async function applyLoginShellPath(options: ShellEnvOptions): Promise<ShellEnvOutcome> {
  const platform = options.platform ?? process.platform;
  const { env } = options;
  if (platform !== 'darwin' && platform !== 'linux') {
    return { applied: false, added: [], reason: `unsupported platform ${platform}` };
  }
  if (env['AGENTICO_DISABLE_SHELL_ENV'] === '1') {
    return { applied: false, added: [], reason: 'disabled by AGENTICO_DISABLE_SHELL_ENV' };
  }
  const shell = env['SHELL'] ?? (platform === 'darwin' ? '/bin/zsh' : '/bin/bash');
  const runShell = options.runShell ?? defaultRunShell(options.timeoutMs ?? DEFAULT_TIMEOUT_MS);
  let output: string;
  try {
    // -l -i: PATH additions commonly live in interactive rc files (.zshrc),
    // not just login profiles. /usr/bin/env sidesteps shell builtins and
    // works under zsh, bash, and fish alike.
    output = await runShell(shell, ['-l', '-i', '-c', '/usr/bin/env']);
  } catch (error) {
    const detail = error instanceof Error ? error.message : String(error);
    return { applied: false, added: [], reason: `${shell} failed: ${detail}` };
  }
  const shellPath = parsePathFromEnvOutput(output);
  if (shellPath === null) {
    return { applied: false, added: [], reason: `${shell} produced no PATH` };
  }
  const before = env['PATH'];
  const merged = mergePathLists(before, shellPath);
  if (merged === (before ?? '')) {
    return { applied: false, added: [], reason: 'login shell added no new entries' };
  }
  const added = merged
    .split(':')
    .filter((entry) => entry !== '' && !(before ?? '').split(':').includes(entry));
  env['PATH'] = merged;
  return { applied: true, added };
}
