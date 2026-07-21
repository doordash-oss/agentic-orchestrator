/**
 * Per-journey isolated world: throwaway HOME, app userData, runtime parent
 * (config.yaml + state dir), stub provider CLIs, and workspace repositories.
 * Everything lives under one mkdtemp root inside the OS temp directory and
 * is deleted in teardown; the journeys never touch the real user profile.
 */
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { upsertYamlScalar } from './yaml';

export interface StubAuthState {
  loggedIn: boolean;
  authMethod?: string;
  email?: string;
}

export interface JourneyWorld {
  /** mkdtemp root; every other path lives underneath it. */
  root: string;
  home: string;
  userData: string;
  runtimeDir: string;
  stateDir: string;
  configPath: string;
  workspaceRoot: string;
  stubDir: string;
  /** The claude stub CLI (config providers.claude.cli points here). */
  claudeStub: string;
  /** Path of the stub's auth-state file. */
  authStatePath: string;
  /** Marker file: while present the stub sleeps before answering auth. */
  authDelayPath: string;
  /** One line per real stream-json provider session (catalog/auth probes excluded). */
  providerInvocationLog: string;
}

export interface WorldOptions {
  /** Initial stub auth state (default: signed out). */
  auth?: StubAuthState;
  /** Seconds the stub sleeps on auth while the delay marker exists. */
  authDelaySeconds?: number;
  /** Pre-configure the workspace root so the wizard is already satisfied. */
  presetWorkspaceRoot?: boolean;
  /** Emit deterministic Claude stream-json activity for a real workflow session. */
  workflowProvider?: boolean;
  /** Emit deterministic blocking-control requests for attention journeys. */
  attentionProvider?: boolean;
  /** Resolve the completion rebase journey's hermetic conflict and approve review gates. */
  rebaseProvider?: boolean;
}

const STUB_VERSION = '2.99.0 (Claude Code)';

/** Counts authoritative workflow provider sessions recorded by the stub CLI. */
export function providerInvocationCount(logPath: string): number {
  try {
    return fs
      .readFileSync(logPath, 'utf8')
      .split('\n')
      .filter((line) => line.trim() === 'session').length;
  } catch {
    return 0;
  }
}

export function createWorld(name: string, options: WorldOptions = {}): JourneyWorld {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), `agentico-e2e-${name}-`));
  const home = path.join(root, 'home');
  const userData = path.join(root, 'user-data');
  const runtimeDir = path.join(home, '.agentic-orchestrator');
  const stateDir = path.join(runtimeDir, 'features');
  const workspaceRoot = path.join(home, 'workspace');
  const stubDir = path.join(root, 'stubs');
  for (const dir of [home, userData, runtimeDir, workspaceRoot, stubDir]) {
    fs.mkdirSync(dir, { recursive: true });
  }

  const claudeStub = path.join(stubDir, 'claude-stub');
  const authStatePath = path.join(stubDir, 'claude-auth.json');
  const authDelayPath = path.join(stubDir, 'claude-auth-delay');
  const providerInvocationLog = path.join(stubDir, 'workflow-invocations.log');
  const delaySeconds = options.authDelaySeconds ?? 0;
  writeStubCli(
    claudeStub,
    authStatePath,
    authDelayPath,
    providerInvocationLog,
    delaySeconds,
    options.workflowProvider === true,
    options.attentionProvider === true,
    options.rebaseProvider === true,
  );
  writeAuthState(authStatePath, options.auth ?? { loggedIn: false });
  if (delaySeconds > 0) {
    // The journey deletes this marker once it has captured the connection
    // shell; later probes answer instantly.
    fs.writeFileSync(authDelayPath, '');
  }

  const world: JourneyWorld = {
    root,
    home,
    userData,
    runtimeDir,
    stateDir,
    configPath: path.join(runtimeDir, 'config.yaml'),
    workspaceRoot,
    stubDir,
    claudeStub,
    authStatePath,
    authDelayPath,
    providerInvocationLog,
  };
  writeRuntimeConfig(world, options.presetWorkspaceRoot === true);
  return world;
}

/**
 * The provider-CLI stub. Speaks exactly the surface the server probes:
 *   --version            → a supported version string
 *   auth status --json   → the JSON in the auth-state file
 * Everything else (e.g. model-catalog probes) fails fast; the server then
 * falls back to its curated model catalog for detected providers.
 */
function writeStubCli(
  stubPath: string,
  authStatePath: string,
  authDelayPath: string,
  providerInvocationLog: string,
  authDelaySeconds: number,
  workflowProvider: boolean,
  attentionProvider: boolean,
  rebaseProvider: boolean,
): void {
  const script = [
    '#!/bin/sh',
    '# Packaged-E2E provider stub (generated per journey; never committed).',
    'case "$1" in',
    '  --version)',
    `    echo "${STUB_VERSION}"`,
    '    exit 0',
    '    ;;',
    '  auth)',
    ...(authDelaySeconds > 0
      ? [`    if [ -e "${authDelayPath}" ]; then`, `      sleep ${authDelaySeconds}`, '    fi']
      : []),
    `    cat "${authStatePath}"`,
    '    exit 0',
    '    ;;',
    'esac',
    ...(rebaseProvider
      ? [
          'is_stream=0',
          'for arg in "$@"; do',
          '  if [ "$arg" = "--input-format" ]; then is_stream=1; fi',
          'done',
          'if [ "$is_stream" -ne 1 ]; then exit 1; fi',
          'IFS= read -r _agentico_init || exit 1',
          'IFS= read -r _agentico_prompt || exit 1',
          '_context=$(printf "%s\\n" "$@" "$_agentico_prompt")',
          `printf 'rebase-session\\n' >> "${providerInvocationLog}"`,
          'write_approved_review() {',
          '  _feedback=$(printf "%s\\n" "$_context" | grep -o \'/[^"\\\\ ]*review-feedback\\.md\' | head -n 1)',
          '  if [ -z "$_feedback" ]; then',
          '    _root=$(printf "%s\\n" "$_context" | sed -n "s#.*helper_dir.: \\(/[^ ]*\\).*#\\1#p" | head -n 1)',
          '    if [ -z "$_root" ]; then',
          '      _root=$(printf "%s\\n" "$_context" | sed -n "s#.*iteration_dir.: \\(/[^ ]*\\).*#\\1#p" | head -n 1)',
          '    fi',
          '    if [ -z "$_root" ]; then exit 1; fi',
          '    _feedback="$_root/review-feedback.md"',
          '  fi',
          '  _dir=$(dirname "$_feedback")',
          '  mkdir -p "$_dir"',
          '  {',
          "    echo '## Findings'",
          "    echo '- (none)'",
          "    echo ''",
          "    echo '## Suggestions'",
          "    echo '- (none)'",
          "    echo ''",
          "    echo '## Verdict'",
          "    echo 'APPROVED'",
          '  } > "$_feedback"',
          '  : > "$_dir/phase_complete"',
          '}',
          'write_rebase_artifacts() {',
          '  _plan=$(printf "%s\\n" "$_agentico_prompt" | grep -o \'/[^"\\\\ ]*rebase-plan\\.md\' | head -n 1)',
          '  if [ -z "$_plan" ]; then exit 1; fi',
          '  _artifact=$(dirname "$_plan")',
          '  _iter="$_artifact/iteration-01"',
          '  _progress="$_artifact/progress.md"',
          '  _report="$_iter/verification-report.yaml"',
          '  _contract="$_artifact/testing-contract.yaml"',
          '  mkdir -p "$_iter"',
          '  _gitlog="$_artifact/rebase-git.log"',
          '  _prev=""',
          '  for _dir in "$@"; do',
          '    if [ "$_prev" = "--add-dir" ] && git -C "$_dir" rev-parse --is-inside-work-tree >/dev/null 2>&1; then',
          '      if head -n 1 "$_dir/README.md" 2>/dev/null | grep -q "^# local-core$"; then',
          '        printf \'# local-core\\nconflicting change on main\\nmerged change from feature\\n\' > "$_dir/README.md"',
          '        git -C "$_dir" -c user.name="Agentico E2E" -c user.email=e2e@example.invalid add README.md </dev/null >>"$_gitlog" 2>&1',
          '        GIT_EDITOR=true git -C "$_dir" -c user.name="Agentico E2E" -c user.email=e2e@example.invalid rebase --continue </dev/null >>"$_gitlog" 2>&1 || git -C "$_dir" -c user.name="Agentico E2E" -c user.email=e2e@example.invalid commit --no-edit </dev/null >>"$_gitlog" 2>&1 || true',
          '      fi',
          '    fi',
          '    _prev="$_dir"',
          '  done',
          '  {',
          "    echo 'version: 2'",
          '    printf "contract_path: %s\\n" "$_contract"',
          "    echo 'contract_revision: 1'",
          "    echo 'results:'",
          '    if [ -f "$_contract" ]; then',
          '      awk \'/^[[:space:]]+- id:/ { printf "  - item_id: %s\\n    status: passed\\n    evidence:\\n      exit_code: 0\\n      summary: Rebase verification command passed.\\n", $3 }\' "$_contract"',
          '    fi',
          "    echo 'summary: Rebase fixture verification passed.'",
          '  } > "$_report"',
          '  {',
          "    echo '# Iteration Progress'",
          "    echo ''",
          "    echo '## Iteration Handoff'",
          "    echo ''",
          "    echo '### Completed this iteration'",
          "    echo '- Resolved the packaged rebase fixture conflict and continued the in-progress rebase.'",
          "    echo ''",
          "    echo '### Remaining from the plan'",
          "    echo '- None.'",
          "    echo ''",
          "    echo '### Where I stopped'",
          "    echo 'Complete'",
          "    echo ''",
          "    echo '### Gotchas / blockers / in-flight decisions'",
          "    echo '- None.'",
          "    echo ''",
          "    echo '## Deferrals'",
          "    echo ''",
          "    echo '```yaml'",
          "    echo 'deferrals: []'",
          "    echo 'closed_deferrals: []'",
          "    echo '```'",
          "    echo ''",
          "    echo '## Verification Report'",
          "    echo ''",
          '    printf -- "- **Path**: %s\\n" "$_report"',
          "    echo '- **Notes**: Rebase verification passed in the packaged fixture.'",
          "    echo ''",
          "    echo '## Iteration State'",
          "    echo ''",
          "    echo 'SUCCESS'",
          '  } > "$_progress"',
          '  : > "$_iter/phase_complete"',
          '}',
          'case "$_context" in',
          '  *"review-feedback.md"*|*"helper_dir"*)',
          `    echo '{"type":"system","subtype":"init","session_id":"e2e-rebase-review"}'`,
          `    echo '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Approving packaged rebase fixture review."}]}}'`,
          '    write_approved_review',
          `    echo '{"type":"result","subtype":"success","session_id":"e2e-rebase-review","total_cost_usd":0}'`,
          '    # drain: give the parent a moment to read stdout before exit closes the pipe',
          '    sleep 0.2',
          '    exit 0',
          '    ;;',
          '  *"iteration_dir"*|*"rebase-plan.md"*)',
          `    echo '{"type":"system","subtype":"init","session_id":"e2e-rebase-implement"}'`,
          `    echo '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Resolving packaged rebase fixture."}]}}'`,
          '    write_rebase_artifacts "$@"',
          `    echo '{"type":"result","subtype":"success","session_id":"e2e-rebase-implement","total_cost_usd":0}'`,
          '    # drain: give the parent a moment to read stdout before exit closes the pipe',
          '    sleep 0.2',
          '    exit 0',
          '    ;;',
          'esac',
          'exit 1',
        ]
      : []),
    ...(workflowProvider
      ? [
          'is_stream=0',
          'for arg in "$@"; do',
          '  if [ "$arg" = "--input-format" ]; then is_stream=1; fi',
          'done',
          'if [ "$is_stream" -ne 1 ]; then exit 1; fi',
          `printf 'session\\n' >> "${providerInvocationLog}"`,
          `echo '{"type":"system","subtype":"init","session_id":"e2e-workflow-session"}'`,
          `echo '{"type":"assistant","subtype":"partial","message":{"role":"assistant","content":[{"type":"text","text":"Backfill ready: inspecting the isolated workspace."}]}}'`,
          `echo '{"type":"assistant","subtype":"partial","message":{"role":"assistant","content":[{"type":"text","text":"Backfill ready: isolated workspace inspected; live plan follows."}]}}'`,
          `echo '{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"tool-e2e-1","name":"Read","input":{"file_path":"README.md"}},{"type":"tool_use","id":"tool-e2e-2","name":"TaskCreate","input":{"description":"Prove packaged live supervision"}}]}}'`,
          'trap "exit 0" TERM INT HUP',
          'i=1',
          'while [ "$i" -le 240 ]; do',
          `  printf '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Live semantic update %03d — provider fixture remains supervised."}]}}\\n' "$i"`,
          '  i=$((i + 1))',
          '  sleep 0.02',
          'done',
          `echo '{"type":"system","subtype":"task_started","task_id":"task-e2e-1","tool_use_id":"tool-e2e-2","description":"Inspect packaged reconnect coverage","task_type":"local_agent","prompt":"Verify the supervised journey."}'`,
          `echo '{"type":"system","subtype":"task_progress","task_id":"task-e2e-1","tool_use_id":"tool-e2e-2","description":"Checking replay-safe activity","last_tool_name":"Read"}'`,
          `printf '%s\\n' '{"type":"tool_progress","tool_use_id":"tool-e2e-3","tool_name":"Write","data":"File: README.md\\nStatus: in_progress"}'`,
          `echo '{"type":"system","subtype":"task_notification","task_id":"task-e2e-1","tool_use_id":"tool-e2e-2","status":"completed","summary":"Reconnect fixture ready"}'`,
          '# Stay alive until the real server Stop action sends the Claude interrupt request.',
          '# If the server crashes, its stdin pipe closes. Keep the orphan alive so the',
          '# replacement server can recover the persisted process group and stop it.',
          'while :; do',
          '  if IFS= read -r input; then',
          '    case "$input" in',
          `      *'"subtype":"interrupt"'*)`,
          `        echo '{"type":"result","subtype":"success","session_id":"e2e-workflow-session","total_cost_usd":0}'`,
          '        exit 0',
          '        ;;',
          '    esac',
          '  else',
          '    sleep 1',
          '  fi',
          'done',
        ]
      : []),
    ...(attentionProvider
      ? [
          'is_stream=0',
          'for arg in "$@"; do',
          '  if [ "$arg" = "--input-format" ]; then is_stream=1; fi',
          'done',
          'if [ "$is_stream" -ne 1 ]; then exit 1; fi',
          `printf 'attention-session\\n' >> "${providerInvocationLog}"`,
          'IFS= read -r _agentico_init || exit 1',
          'IFS= read -r _agentico_prompt || exit 1',
          `printf 'initial:%s\\n' "$_agentico_prompt" >> "${providerInvocationLog}"`,
          'emit_request() {',
          '  _json="$1"',
          '  _id="$2"',
          '  printf "%s\\n" "$_json"',
          `  printf 'pending:%s\\n' "$_id" >> "${providerInvocationLog}"`,
          '  if IFS= read -r _response; then',
          `    printf 'response:%s:%s\\n' "$_id" "$_response" >> "${providerInvocationLog}"`,
          '  else',
          '    exit 0',
          '  fi',
          '}',
          'case "$_agentico_prompt" in',
          '  *"attention chat help"*)',
          `    echo '{"type":"system","subtype":"init","session_id":"e2e-attention-chat"}'`,
          `    echo '{"type":"result","subtype":"success","session_id":"e2e-attention-chat","total_cost_usd":0}'`,
          `    printf 'chat-waiting\\n' >> "${providerInvocationLog}"`,
          '    while :; do',
          '      if IFS= read -r _help; then',
          `      printf 'help-response:%s\\n' "$_help" >> "${providerInvocationLog}"`,
          '        exit 0',
          '      fi',
          '      sleep 0.2',
          '    done',
          '    exit 0',
          '    ;;',
          'esac',
          `echo '{"type":"system","subtype":"init","session_id":"e2e-attention-session"}'`,
          `echo '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Attention fixture ready."}]}}'`,
          `emit_request '{"type":"control_request","request_id":"perm-allow-once","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"printf allow-once"}}}' "perm-allow-once"`,
          `emit_request '{"type":"control_request","request_id":"perm-stale","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"printf stale-resolution"}}}' "perm-stale"`,
          `emit_request '{"type":"control_request","request_id":"perm-deny","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"printf deny-me"}}}' "perm-deny"`,
          `emit_request '{"type":"control_request","request_id":"perm-remember","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"npm test -- --private-token=private-token"}}}' "perm-remember"`,
          `emit_request '{"type":"control_request","request_id":"perm-remember-followup","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"npm test -- --private-token=private-token"}}}' "perm-remember-followup"`,
          `emit_request '{"type":"control_request","request_id":"ask-bundle","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","input":{"questions":[{"question":"Which verification tracks should be included?","header":"Verification tracks","multi_select":true,"options":[{"label":"Unit tests","description":"Exercise renderer and server contracts.","confidence":0.5},{"label":"Packaged smoke","description":"Drive the shipped Electron app.","confidence":0.5},{"label":"Manual note","description":"Record a supplemental operator note.","confidence":0.2}]},{"question":"Which note should be attached to the evidence bundle?","header":"Evidence note","options":[]}]}}}' "ask-bundle"`,
          `echo '{"type":"result","subtype":"success","session_id":"e2e-attention-session","total_cost_usd":0}'`,
          'exit 0',
        ]
      : []),
    'exit 1',
    '',
  ].join('\n');
  fs.writeFileSync(stubPath, script, { mode: 0o755 });
}

export function writeAuthState(authStatePath: string, state: StubAuthState): void {
  fs.writeFileSync(authStatePath, `${JSON.stringify(state)}\n`);
}

export function setStubAuthenticated(world: JourneyWorld, loggedIn: boolean): void {
  writeAuthState(
    world.authStatePath,
    loggedIn
      ? { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' }
      : { loggedIn: false },
  );
}

/**
 * The runtime config the server loads: the claude provider is redirected to
 * the stub; codex/opencode are pointed at paths that cannot exist so a
 * provider installed on the host machine can never leak into a journey.
 */
function writeRuntimeConfig(world: JourneyWorld, presetWorkspaceRoot: boolean): void {
  const missing = path.join(world.stubDir, 'missing');
  fs.writeFileSync(
    world.configPath,
    [
      'providers:',
      '  claude:',
      `    cli: ${world.claudeStub}`,
      '  codex:',
      `    cli: ${path.join(missing, 'codex')}`,
      '  opencode:',
      `    cli: ${path.join(missing, 'opencode')}`,
      ...(presetWorkspaceRoot ? ['workspace_roots:', `  - ${world.workspaceRoot}`] : []),
      '',
    ].join('\n'),
  );
}

// --- git helpers -------------------------------------------------------------

const GIT_IDENTITY = ['-c', 'user.name=Agentico E2E', '-c', 'user.email=e2e@example.invalid'];

export function git(cwd: string, ...args: string[]): string {
  return execFileSync('git', [...GIT_IDENTITY, ...args], {
    cwd,
    encoding: 'utf8',
    env: { ...minimalEnv(), GIT_CONFIG_GLOBAL: '/dev/null', GIT_CONFIG_SYSTEM: '/dev/null' },
  });
}

/** Creates a git repository under the workspace root; optionally commits. */
export function createRepo(world: JourneyWorld, name: string, opts: { commit: boolean }): string {
  const dir = path.join(world.workspaceRoot, name);
  fs.mkdirSync(dir, { recursive: true });
  fs.writeFileSync(path.join(dir, 'README.md'), `# ${name}\n`);
  git(dir, 'init', '--initial-branch=main');
  if (opts.commit) {
    git(dir, 'add', '.');
    git(dir, 'commit', '-m', 'Initial commit');
  }
  return dir;
}

/**
 * Creates a plain (non-repo) folder under the workspace root. Left empty:
 * the server's consent-gated init endpoint initializes empty folders or
 * existing repositories only (`directory_not_empty` otherwise).
 */
export function createPlainFolder(world: JourneyWorld, name: string): string {
  const dir = path.join(world.workspaceRoot, name);
  fs.mkdirSync(dir, { recursive: true });
  return dir;
}

/**
 * Seeds a server-created feature with immutable, run-authentic history while
 * the bundled server is stopped.  The base run is produced by the normal
 * creation/setup path, then copied using the production runs/run-NNN layout;
 * this keeps packaged journeys deterministic without teaching the client a
 * private mutation API or relying on timing-sensitive provider sessions.
 */
export function seedRunHistory(
  world: JourneyWorld,
  featureId: string,
  runCount = 7,
  repoName = 'signal-lab',
): void {
  if (!Number.isSafeInteger(runCount) || runCount < 2) {
    throw new Error(`runCount must be at least two, got ${runCount}`);
  }
  const featurePath = path.join(world.stateDir, featureId, 'feature.yaml');
  const runsPath = path.join(world.stateDir, featureId, 'runs');
  const basePath = path.join(runsPath, 'run-001');
  const baseRun = path.join(basePath, 'run.yaml');
  if (!fs.existsSync(featurePath) || !fs.existsSync(baseRun)) {
    throw new Error('seedRunHistory requires a feature created by the bundled server');
  }
  const phaseOneAnchor = git(path.join(world.workspaceRoot, repoName), 'rev-parse', 'HEAD').trim();

  const stamp = (runNumber: number, sealed: boolean): string => {
    const artifactName = `history-run-${runNumber}.md`;
    const sealedFields = sealed
      ? [
          `sealed_at: 2026-01-${String(runNumber).padStart(2, '0')}T12:00:00Z`,
          'seal_reason: rewind',
          'rewind_target: 2',
        ]
      : [];
    return [
      `run_number: ${runNumber}`,
      ...sealedFields,
      'current_iteration: 2',
      'current_roadmap_phase: 2',
      'total_roadmap_phases: 3',
      'roadmap_phase_type: tdd-fill-in',
      'roadmap_phase_commit_anchors:',
      '  1:',
      `    ${repoName}: ${phaseOneAnchor}`,
      'pending_review_phase: 2',
      'artifacts:',
      `  history-${runNumber}: ${artifactName}`,
      'phase_timings:',
      '  implement: 42s',
      'phase_costs:',
      '  implement: 0.12',
      '',
    ].join('\n');
  };

  for (let runNumber = 1; runNumber <= runCount; runNumber += 1) {
    const runPath = path.join(runsPath, `run-${String(runNumber).padStart(3, '0')}`);
    if (runNumber !== 1) fs.cpSync(basePath, runPath, { recursive: true });
    fs.writeFileSync(path.join(runPath, 'run.yaml'), stamp(runNumber, runNumber < runCount));
    fs.writeFileSync(
      path.join(runPath, `history-run-${runNumber}.md`),
      `# Historical run ${runNumber}\n\nThis artifact belongs only to Run ${runNumber}.\n`,
    );
    fs.mkdirSync(path.join(runPath, 'logs'), { recursive: true });
    fs.writeFileSync(
      path.join(runPath, 'logs', 'session.log'),
      `sealed run ${runNumber}: session output retained for bounded inspection\n`,
    );
    fs.writeFileSync(
      path.join(runPath, 'logs', 'phase.log'),
      `sealed run ${runNumber}: implement phase completed\n`,
    );
  }

  let featureYaml = fs.readFileSync(featurePath, 'utf8');
  featureYaml = upsertYamlScalar(featureYaml, 'status', 'CodeReady');
  featureYaml = upsertYamlScalar(featureYaml, 'current_phase', '2');
  featureYaml = upsertYamlScalar(featureYaml, 'active_run', String(runCount));
  featureYaml = upsertYamlScalar(featureYaml, 'run_count', String(runCount));
  fs.writeFileSync(featurePath, featureYaml);
}

// --- discovery / processes -----------------------------------------------------

export interface DiscoveryRecord {
  schema_version: number;
  api_version: string;
  base_url: string;
  auth_token?: string;
  runtime: { runtime_dir: string; state_dir: string; config_path: string };
  pid: number;
  started_at?: string;
}

export function discoveryPath(world: JourneyWorld): string {
  return path.join(world.runtimeDir, '.agentico-server.json');
}

export function readDiscovery(world: JourneyWorld): DiscoveryRecord | null {
  try {
    return JSON.parse(fs.readFileSync(discoveryPath(world), 'utf8')) as DiscoveryRecord;
  } catch {
    return null;
  }
}

export function processAlive(pid: number): boolean {
  try {
    process.kill(pid, 0);
    return true;
  } catch (err) {
    return (err as NodeJS.ErrnoException).code === 'EPERM';
  }
}

export async function waitFor(
  condition: () => boolean | Promise<boolean>,
  what: string,
  timeoutMs = 30_000,
  intervalMs = 250,
): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    if (await condition()) {
      return;
    }
    if (Date.now() > deadline) {
      throw new Error(`timed out after ${timeoutMs}ms waiting for ${what}`);
    }
    await new Promise((resolve) => setTimeout(resolve, intervalMs));
  }
}

/**
 * The minimal environment every journey process (app or server) runs with:
 * throwaway profile dirs and a PATH of system directories only, so provider
 * CLIs installed on the host can never be discovered. Display/session
 * variables pass through on Linux for xvfb.
 */
export function minimalEnv(world?: JourneyWorld): Record<string, string> {
  const env: Record<string, string> = {
    PATH: '/usr/bin:/bin:/usr/sbin:/sbin',
    TMPDIR: process.env['TMPDIR'] ?? os.tmpdir(),
    LANG: process.env['LANG'] ?? 'en_US.UTF-8',
    AGENTICO_E2E_ALLOW_LARGE_WINDOW: '1',
  };
  if (world !== undefined) {
    env['HOME'] = world.home;
    env['AGENTICO_E2E_USER_DATA'] = world.userData;
    if (process.platform === 'linux') {
      env['XDG_CONFIG_HOME'] = path.join(world.home, '.config');
      env['XDG_CACHE_HOME'] = path.join(world.home, '.cache');
      env['XDG_DATA_HOME'] = path.join(world.home, '.local', 'share');
    }
  } else {
    env['HOME'] = process.env['HOME'] ?? os.homedir();
  }
  for (const passthrough of [
    'DISPLAY',
    'XAUTHORITY',
    'WAYLAND_DISPLAY',
    'XDG_RUNTIME_DIR',
    'DBUS_SESSION_BUS_ADDRESS',
  ]) {
    const value = process.env[passthrough];
    if (value !== undefined) {
      env[passthrough] = value;
    }
  }
  return env;
}

/** Recursively removes the world; never throws (teardown best effort). */
export function destroyWorld(world: JourneyWorld): void {
  try {
    fs.rmSync(world.root, { recursive: true, force: true, maxRetries: 3 });
  } catch {
    // Leftover temp files are acceptable; leftover processes are not.
  }
}
