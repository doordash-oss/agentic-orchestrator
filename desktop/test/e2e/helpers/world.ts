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
  /** Detectable Codex CLI that records and fails app-server launches. */
  codexStub: string;
  /** Path of the stub's auth-state file. */
  authStatePath: string;
  /** Marker file: while present the stub sleeps before answering auth. */
  authDelayPath: string;
  /** One line per real stream-json provider session (catalog/auth probes excluded). */
  providerInvocationLog: string;
  /**
   * Sentinel file: while present, a supervisor-launched stub process exits
   * non-zero before printing anything, so the launch fails before its
   * handshake (supervisorProvider worlds only).
   */
  supervisorLaunchFailurePath: string;
  codexInvocationLog: string;
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
  /**
   * Serve the supervisor's long-lived interactive Claude session: stream
   * deterministic replies turn after turn and stay alive between turns.
   * Prompts carrying SUPERVISOR_E2E_MARKERS script permission, hold and
   * operate turns. Combined with `workflowProvider`, only the process the
   * supervisor launcher started (it alone carries AGENTICO_RUNTIME_DIR) is
   * the supervisor; every other stream session is a workflow session.
   */
  supervisorProvider?: boolean;
  /** Include a detectable Codex CLI whose app-server launch always fails. */
  unlaunchableCodex?: boolean;
}

/** Prompt markers the supervisorProvider stub reacts to. */
export const SUPERVISOR_E2E_MARKERS = {
  /** Blocks the turn on a Bash permission request until it is answered. */
  permission: 'SUPERVISOR_E2E_PERMISSION',
  /** Holds the turn until an interrupt arrives, then reports an interrupted result. */
  hold: 'SUPERVISOR_E2E_HOLD',
  /**
   * Commits one complete assistant text message (supervisorStubPartialReply),
   * then holds the turn like `hold` without ever reporting a result, so a
   * server restart cuts a turn that already has partial text.
   */
  partialHold: 'SUPERVISOR_E2E_PARTIAL_HOLD',
  /**
   * Operates Agentico through the real `"$AGENTICO_BIN" api` helper: creates
   * the feature named in `[...]` right after the marker (build it with
   * supervisorOperateCreateMarker), dispatches its setup (an API-created
   * feature waits in SettingUpWorktrees until a client does), posts its
   * config, and reads it back.
   */
  operateCreate: 'SUPERVISOR_E2E_OPERATE_CREATE',
  /** Starts the feature the last operate-create turn created, through the helper. */
  operateStart: 'SUPERVISOR_E2E_OPERATE_START',
  compact: 'SUPERVISOR_E2E_COMPACT',
  usageHigh: 'SUPERVISOR_E2E_USAGE_HIGH',
} as const;

/** Feature names an operate-create marker may carry (kept sh- and JSON-safe). */
const OPERATE_FEATURE_NAME = /^[A-Za-z0-9][A-Za-z0-9 ._-]{0,80}$/;

/** The operate-create marker text carrying the journey-chosen feature name. */
export function supervisorOperateCreateMarker(featureName: string): string {
  if (!OPERATE_FEATURE_NAME.test(featureName)) {
    throw new Error(`operate feature name ${JSON.stringify(featureName)} is not sh/JSON-safe`);
  }
  return `${SUPERVISOR_E2E_MARKERS.operateCreate}[${featureName}]`;
}

/**
 * The feature config the operate-create turn posts: every role on the
 * world's stub Claude provider, no inquiry and no review checkpoints, so a
 * started feature stays in its first running phase for the workflow stub.
 */
export const SUPERVISOR_E2E_OPERATE_CONFIG = {
  models: {
    inquiry: 'claude:haiku',
    research: 'claude:haiku',
    planning: 'claude:haiku',
    implementation: 'claude:haiku',
    review: 'claude:haiku',
    utilities: 'claude:haiku',
    kb_build: 'claude:haiku',
  },
  inquireness: 'none',
} as const;

/** The text reply that closes an operate-create turn. */
export function supervisorOperateCreatedReply(featureName: string): string {
  return `Created and configured ${featureName}.`;
}

/** The text reply that closes an operate-start turn. */
export const SUPERVISOR_E2E_OPERATE_STARTED_REPLY = 'Started the feature.';

/** Invocation-log prefix of every helper command the operate turns run. */
export const SUPERVISOR_E2E_HELPER_LOG_PREFIX = 'helper:';

/**
 * The heading the server's hidden error-context bundle opens with; the
 * supervisorProvider stub logs each turn whose wire text carries it.
 */
export const SUPERVISOR_E2E_HIDDEN_CONTEXT_HEADING = 'Chat context';

/** The deterministic reply the supervisorProvider stub commits for a turn (1-based). */
export function supervisorStubReply(turn: number): string {
  return `Supervisor reply ${turn}`;
}

/** The partial assistant text a partial-hold turn commits before holding (1-based turn). */
export function supervisorStubPartialReply(turn: number): string {
  return `Partial supervisor reply ${turn}`;
}

/**
 * The reply the supervisorProvider stub gives the first prompt of a process
 * launched with `--resume <id>`: the number of prior user prompts it found
 * in the rebuilt session file.
 */
export function supervisorStubResumedReply(priorMessages: number): string {
  return `Resumed with ${priorMessages} prior messages`;
}

/** The Bash command the supervisorProvider stub asks permission for. */
export const SUPERVISOR_E2E_PERMISSION_COMMAND = 'printf supervisor-permission';

const STUB_VERSION = '2.99.0 (Claude Code)';

// Initialization metadata is shared by every scenario, just as it is in Claude.
const STUB_MODEL_CATALOG = JSON.stringify({
  models: [
    {
      value: 'opus[1m]',
      resolvedModel: 'claude-opus-4-8',
      displayName: 'Opus',
      supportsEffort: true,
      supportedEffortLevels: ['low', 'medium', 'high', 'max'],
    },
    {
      value: 'claude-fable-5-1[1m]',
      resolvedModel: 'claude-fable-5-1',
      displayName: 'Fable',
      supportsEffort: true,
      supportedEffortLevels: ['low', 'medium', 'high', 'max'],
    },
    {
      value: 'sonnet',
      resolvedModel: 'claude-sonnet-4-6',
      displayName: 'Sonnet',
      supportsEffort: true,
      supportedEffortLevels: ['low', 'medium', 'high'],
    },
    {
      value: 'haiku',
      resolvedModel: 'claude-haiku-4-5',
      displayName: 'Haiku',
      supportsEffort: false,
    },
  ],
});

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
  const codexStub = path.join(stubDir, 'codex-stub');
  const codexInvocationLog = path.join(stubDir, 'codex-invocations.log');
  if (options.unlaunchableCodex === true) {
    fs.writeFileSync(
      codexStub,
      [
        '#!/bin/sh',
        'if [ "$1" = "--version" ]; then echo "codex-cli 0.156.0"; exit 0; fi',
        'if [ "$1" = "login" ] && [ "$2" = "status" ]; then echo "Logged in using ChatGPT"; exit 0; fi',
        `if [ "$1" = "debug" ] && [ "$2" = "models" ] && [ "$3" = "--bundled" ]; then echo '{"models":[{"slug":"gpt-5.4","display_name":"GPT-5.4","visibility":"list","supported_in_api":true,"context_window":272000},{"slug":"gpt-5.3-codex","display_name":"GPT-5.3 Codex","visibility":"list","supported_in_api":true,"context_window":272000}]}'; exit 0; fi`,
        'if [ "$1" = "debug" ] && [ "$2" = "models" ]; then exit 3; fi',
        `printf '%s\\n' "$*" >> "${codexInvocationLog}"`,
        'echo "Codex app-server unavailable" >&2',
        'exit 3',
        '',
      ].join('\n'),
      { mode: 0o755 },
    );
  }
  const authStatePath = path.join(stubDir, 'claude-auth.json');
  const authDelayPath = path.join(stubDir, 'claude-auth-delay');
  const providerInvocationLog = path.join(stubDir, 'workflow-invocations.log');
  const supervisorLaunchFailurePath = path.join(stubDir, 'supervisor-launch-failure');
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
    options.supervisorProvider === true,
    { home, launchFailurePath: supervisorLaunchFailurePath },
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
    codexStub,
    codexInvocationLog,
    authStatePath,
    authDelayPath,
    providerInvocationLog,
    supervisorLaunchFailurePath,
  };
  writeRuntimeConfig(
    world,
    options.presetWorkspaceRoot === true,
    options.unlaunchableCodex === true,
  );
  return world;
}

/**
 * The provider-CLI stub. Speaks exactly the surface the server probes:
 *   --version            → a supported version string
 *   auth status --json   → the JSON in the auth-state file
 *   stream initialize    → a model catalog, before any user prompt
 * Workflow activity starts only after the next input (the user prompt).
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
  supervisorProvider: boolean,
  supervisorPaths: { home: string; launchFailurePath: string },
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
    'is_stream=0',
    'for arg in "$@"; do',
    '  if [ "$arg" = "--input-format" ]; then is_stream=1; fi',
    'done',
    'if [ "$is_stream" -ne 1 ]; then exit 1; fi',
    // A supervisor launch (only its child carries AGENTICO_RUNTIME_DIR) dies
    // before printing anything while the failure sentinel exists.
    ...(supervisorProvider
      ? [
          `if [ -n "$AGENTICO_RUNTIME_DIR" ] && [ -e "${supervisorPaths.launchFailurePath}" ]; then`,
          `  printf 'launch-failed\\n' >> "${providerInvocationLog}"`,
          '  exit 3',
          'fi',
        ]
      : []),
    'IFS= read -r _agentico_init || exit 1',
    `request_id=$(printf '%s\\n' "$_agentico_init" | sed -n 's/.*"request_id" *: *"\\([^"]*\\)".*/\\1/p')`,
    '[ -n "$request_id" ] || exit 1',
    `printf '{"type":"control_response","response":{"subtype":"success","request_id":"%s","response":%s}}\\n' "$request_id" '${STUB_MODEL_CATALOG}'`,
    // With both supervisor and workflow scripts, only the supervisor
    // launcher's child carries AGENTICO_RUNTIME_DIR; alone, every stream
    // session is the supervisor.
    ...(supervisorProvider
      ? [
          workflowProvider
            ? 'if [ -n "$AGENTICO_RUNTIME_DIR" ]; then is_supervisor=1; else is_supervisor=0; fi'
            : 'is_supervisor=1',
        ]
      : []),
    // The supervisor launch completes its handshake on the first provider
    // output and only then sends the first user message, so its session
    // reports init before that message arrives (as an idle Claude does).
    // A resumed session reports the id it resumed, as Claude does.
    ...(supervisorProvider
      ? [
          'if [ "$is_supervisor" = 1 ]; then',
          ...supervisorResumeLines(providerInvocationLog, supervisorPaths.home),
          `  printf '{"type":"system","subtype":"init","session_id":"%s","model":"claude-haiku-4-5"}\\n' "\${_resume_id:-e2e-supervisor-session}"`,
          'fi',
        ]
      : []),
    // Discovery stops here; only real sessions send a user prompt.
    'IFS= read -r _agentico_prompt || exit 1',
    ...(supervisorProvider
      ? ['if [ "$is_supervisor" = 1 ]; then', ...supervisorStubLines(providerInvocationLog), 'fi']
      : []),
    ...(rebaseProvider
      ? [
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
          '  if [ -n "$_plan" ]; then',
          '    _artifact=$(dirname "$_plan")',
          '    _iter="$_artifact/iteration-01"',
          '  else',
          '    _artifact=$(printf "%s\\n" "$_context" | sed -n "s#.*phase_dir.: \\(/[^ ]*\\).*#\\1#p" | head -n 1)',
          '    _iter=$(printf "%s\\n" "$_context" | sed -n "s#.*iteration_dir.: \\(/[^ ]*\\).*#\\1#p" | head -n 1)',
          '    if [ -z "$_artifact" ] || [ -z "$_iter" ]; then exit 1; fi',
          '  fi',
          '  _progress="$_artifact/progress.md"',
          '  _report="$_iter/verification-report.yaml"',
          '  _contract="$_artifact/testing-contract.yaml"',
          '  mkdir -p "$_iter"',
          '  _gitlog="$_artifact/rebase-git.log"',
          '  _is_child=$( [ -z "$_plan" ] && echo 1 || echo 0 )',
          '  _prev=""',
          '  for _dir in "$@"; do',
          '    if [ "$_prev" = "--add-dir" ] && git -C "$_dir" rev-parse --is-inside-work-tree >/dev/null 2>&1; then',
          '      if head -n 1 "$_dir/README.md" 2>/dev/null | grep -q "^# local-core$"; then',
          '        if [ "$_is_child" = "1" ]; then',
          '          git -C "$_dir" -c user.name="Agentico E2E" -c user.email=e2e@example.invalid merge main --no-edit </dev/null >>"$_gitlog" 2>&1 || true',
          '        fi',
          '        printf \'# local-core\\nconflicting change on main\\nmerged change from feature\\n\' > "$_dir/README.md"',
          '        git -C "$_dir" -c user.name="Agentico E2E" -c user.email=e2e@example.invalid add README.md </dev/null >>"$_gitlog" 2>&1',
          '        GIT_EDITOR=true git -C "$_dir" -c user.name="Agentico E2E" -c user.email=e2e@example.invalid rebase --continue </dev/null >>"$_gitlog" 2>&1 || git -C "$_dir" -c user.name="Agentico E2E" -c user.email=e2e@example.invalid commit --no-edit </dev/null >>"$_gitlog" 2>&1 || true',
          '      elif [ "$_is_child" = "1" ]; then',
          '        if git -C "$_dir" rev-parse --verify origin/main >/dev/null 2>&1; then',
          '          git -C "$_dir" -c user.name="Agentico E2E" -c user.email=e2e@example.invalid merge origin/main --no-edit </dev/null >>"$_gitlog" 2>&1 || true',
          '        else',
          '          git -C "$_dir" -c user.name="Agentico E2E" -c user.email=e2e@example.invalid merge main --no-edit </dev/null >>"$_gitlog" 2>&1 || true',
          '        fi',
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
          'write_plan_artifacts() {',
          '  _artifact=$(printf "%s\\n" "$_context" | sed -n "s#.*artifact_dir.: \\(/[^ ]*\\).*#\\1#p" | head -n 1)',
          '  if [ -z "$_artifact" ]; then',
          '    _roadmap_path=$(printf "%s\\n" "$_agentico_prompt" | grep -o \'/[^"\\\\ ]*roadmap\\.md\' | head -n 1)',
          '    if [ -n "$_roadmap_path" ]; then _artifact=$(dirname "$_roadmap_path"); fi',
          '  fi',
          '  if [ -z "$_artifact" ]; then',
          '    _attempt=$(printf "%s\\n" "$_context" | sed -n "s#.*attempt_dir.: \\(/[^ ]*\\).*#\\1#p" | head -n 1)',
          '    if [ -n "$_attempt" ]; then _artifact=$(dirname "$_attempt"); fi',
          '  fi',
          '  if [ -z "$_artifact" ]; then exit 1; fi',
          '  mkdir -p "$_artifact"',
          '  case "$_artifact" in',
          '    */roadmap)',
          '      {',
          "        echo '## Phase 1: Rebase pass'",
          "        echo ''",
          "        echo '### Goal'",
          "        echo ''",
          "        echo 'Merge each behind repository target branch into the feature.'",
          "        echo ''",
          '      } > "$_artifact/roadmap.md"',
          '      ;;',
          '    *)',
          '      {',
          "        echo '# Phase Plan: Rebase Pass'",
          "        echo ''",
          "        echo '## Success Criteria'",
          "        echo ''",
          "        echo '### Automated Verification'",
          "        echo ''",
          "        echo '- [ ] None required: rebase child merge is verified by the integration gate.'",
          "        echo ''",
          "        echo '### Manual Verification'",
          "        echo ''",
          "        echo '- [ ] None required: automated-only verification for this feature.'",
          "        echo ''",
          "        echo '### Visual Evidence'",
          "        echo ''",
          "        echo '- [ ] None required: automated-only verification for this feature.'",
          "        echo ''",
          "        echo '### Behavioral Evidence'",
          "        echo ''",
          "        echo '- [ ] None required: automated-only verification for this feature.'",
          "        echo ''",
          '      } > "$_artifact/phase-plan.md"',
          '      ;;',
          '  esac',
          '}',
          'write_validator_feedback() {',
          '  _root=$(printf "%s\\n" "$_context" | sed -n "s#.*helper_dir.: \\(/[^ ]*\\).*#\\1#p" | head -n 1)',
          '  if [ -z "$_root" ]; then',
          '    _root=$(printf "%s\\n" "$_context" | sed -n "s#.*iteration_dir.: \\(/[^ ]*\\).*#\\1#p" | head -n 1)',
          '  fi',
          '  if [ -z "$_root" ]; then exit 1; fi',
          '  mkdir -p "$_root"',
          '  _domain=$(printf "%s" "$_root" | sed -n "s#.*/validate-\\([^/]*\\)$#\\1#p")',
          '  _feedback="$_root/review-feedback.md"',
          '  if [ -n "$_domain" ]; then',
          '    _feedback="$_root/validation-$_domain-feedback.md"',
          '  fi',
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
          '  : > "$_root/phase_complete"',
          '}',
          'case "$_context" in',
          '  *"review-feedback.md"*)',
          `    echo '{"type":"system","subtype":"init","session_id":"e2e-rebase-review"}'`,
          `    echo '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Approving packaged rebase fixture review. <agentico-outcome>{\\"status\\":\\"success\\",\\"summary\\":\\"review approved\\"}</agentico-outcome>"}]}}'`,
          '    write_approved_review',
          `    echo '{"type":"result","subtype":"success","session_id":"e2e-rebase-review","total_cost_usd":0}'`,
          '    # drain: give the parent a moment to read stdout before exit closes the pipe',
          '    sleep 0.2',
          '    exit 0',
          '    ;;',
          '  *"helper_dir"*)',
          `    echo '{"type":"system","subtype":"init","session_id":"e2e-rebase-validator"}'`,
          `    echo '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Validation approved. <agentico-outcome>{\\"status\\":\\"success\\",\\"summary\\":\\"validation approved\\"}</agentico-outcome>"}]}}'`,
          '    write_validator_feedback',
          `    echo '{"type":"result","subtype":"success","session_id":"e2e-rebase-validator","total_cost_usd":0}'`,
          '    sleep 0.2',
          '    exit 0',
          '    ;;',
          '  *"Planning Context"*|*"Revision Context"*|*"artifact_dir"*|*"attempt_dir"*)',
          `    echo '{"type":"system","subtype":"init","session_id":"e2e-rebase-plan"}'`,
          `    echo '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Created rebase plan. <agentico-outcome>{\\"status\\":\\"success\\",\\"summary\\":\\"plan created\\"}</agentico-outcome>"}]}}'`,
          '    write_plan_artifacts',
          `    echo '{"type":"result","subtype":"success","session_id":"e2e-rebase-plan","total_cost_usd":0}'`,
          '    sleep 0.2',
          '    exit 0',
          '    ;;',
          '  *"Implementation Context"*|*"iteration_dir"*|*"rebase-plan.md"*)',
          `    echo '{"type":"system","subtype":"init","session_id":"e2e-rebase-implement"}'`,
          `    echo '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Resolved packaged rebase fixture. <agentico-outcome>{\\"status\\":\\"success\\",\\"summary\\":\\"rebase complete\\"}</agentico-outcome>"}]}}'`,
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
          `printf 'attention-session\\n' >> "${providerInvocationLog}"`,
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
          `echo '{"type":"system","subtype":"init","session_id":"e2e-attention-session"}'`,
          `echo '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Attention fixture ready."}]}}'`,
          `emit_request '{"type":"control_request","request_id":"perm-allow-once","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"printf allow-once"}}}' "perm-allow-once"`,
          `emit_request '{"type":"control_request","request_id":"perm-stale","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"printf stale-resolution"}}}' "perm-stale"`,
          `emit_request '{"type":"control_request","request_id":"perm-deny","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"printf deny-me"}}}' "perm-deny"`,
          `emit_request '{"type":"control_request","request_id":"perm-remember","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"npm test -- --private-token=private-token"}}}' "perm-remember"`,
          `emit_request '{"type":"control_request","request_id":"perm-remember-followup","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"npm test -- --private-token=private-token"}}}' "perm-remember-followup"`,
          `emit_request '{"type":"control_request","request_id":"ask-bundle","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","input":{"questions":[{"question":"Which verification tracks should be included?","header":"Verification tracks","multiSelect":true,"options":[{"label":"Unit tests","description":"Exercise renderer and server contracts.","confidence":0.5},{"label":"Packaged smoke","description":"Drive the shipped Electron app.","confidence":0.5},{"label":"Manual note","description":"Record a supplemental operator note.","confidence":0.2}]},{"question":"Which note should be attached to the evidence bundle?","header":"Evidence note","options":[]}]}}}' "ask-bundle"`,
          `emit_request '{"type":"control_request","request_id":"ask-single","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","input":{"questions":[{"question":"How should existing tables behave?","header":"Existing tables","options":[{"label":"Keep current behavior","description":"Continue applying the existing settings."},{"label":"Disable until set"}]}]}}}' "ask-single"`,
          `echo '{"type":"result","subtype":"success","session_id":"e2e-attention-session","total_cost_usd":0}'`,
          'exit 0',
        ]
      : []),
    // Bounded utility helpers (e.g. Publish PR-description generation) reach
    // every world's stub. Serve them deterministically so publish flows can
    // generate a title/body without a real provider.
    'case "$_agentico_prompt" in',
    '  *"Generate PR Description"*)',
    `    echo '{"type":"system","subtype":"init","session_id":"e2e-publish-description"}'`,
    // printf %s: sh echo interprets \n escapes (splitting the JSON line), printf %s does not.
    `    printf '%s\\n' '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"TITLE: Packaged publish fixture change\\n\\nBODY:\\nDescribes the packaged E2E publish fixture change across the selected repositories."}]}}'`,
    `    echo '{"type":"result","subtype":"success","session_id":"e2e-publish-description","total_cost_usd":0}'`,
    '    # drain: give the parent a moment to read stdout before exit closes the pipe',
    '    sleep 0.2',
    '    exit 0',
    '    ;;',
    'esac',
    'exit 1',
    '',
  ].join('\n');
  fs.writeFileSync(stubPath, script, { mode: 0o755 });
}

/**
 * `--resume <id>` handling for the supervisor stub: reads the session file
 * the server rebuilt at `<claude config>/projects/<encoded physical cwd>/<id>.jsonl`
 * (config is CLAUDE_CONFIG_DIR, else $HOME/.claude; the encoding replaces
 * every `/` and `.` of `pwd -P` with `-`), counts its prior user prompts
 * (string `content`; tool results carry arrays), logs `resume:<id>` and
 * `history:<n>`, and arms the first reply to report the count.
 */
function supervisorResumeLines(providerInvocationLog: string, home: string): string[] {
  return [
    '  _resume_id=""',
    '  _resume_reply=""',
    '  _prev_arg=""',
    '  for _arg in "$@"; do',
    '    if [ "$_prev_arg" = "--resume" ]; then _resume_id="$_arg"; fi',
    `    if [ "$_prev_arg" = "--model" ]; then printf 'launch-model:%s\\n' "$_arg" >> "${providerInvocationLog}"; fi`,
    `    if [ "$_prev_arg" = "--effort" ]; then printf 'launch-effort:%s\\n' "$_arg" >> "${providerInvocationLog}"; fi`,
    '    _prev_arg="$_arg"',
    '  done',
    '  if [ -n "$_resume_id" ]; then',
    `    _claude_config="\${CLAUDE_CONFIG_DIR:-\${HOME:-${home}}/.claude}"`,
    `    _session_file="$_claude_config/projects/$(pwd -P | sed 's#[/.]#-#g')/$_resume_id.jsonl"`,
    `    printf 'resume:%s\\n' "$_resume_id" >> "${providerInvocationLog}"`,
    '    if [ -f "$_session_file" ]; then',
    `      _history=$(grep -c '"role":"user","content":"' "$_session_file")`,
    '    else',
    `      printf 'resume-file-missing:%s\\n' "$_session_file" >> "${providerInvocationLog}"`,
    '      _history=0',
    '    fi',
    `    printf 'history:%s\\n' "$_history" >> "${providerInvocationLog}"`,
    '    _resume_reply="Resumed with $_history prior messages"',
    '    if [ -f "$_session_file" ] && grep -q compact_boundary "$_session_file"; then',
    String.raw`      _history=$(awk '/compact_boundary/ { n=0; next } /"type":"user"/ { n++ } END { print n+0 }' "$_session_file")`,
    String.raw`      _summary=$(grep 'isCompactSummary' "$_session_file" | sed -n 's/.*"text":"\([^"]*\)".*/\1/p' | tail -n 1 | cut -c 1-80)`,
    String.raw`      printf 'resume-compacted:%s\n' "$_history" >> "${providerInvocationLog}"`,
    '      _resume_reply="Resumed after compaction with $_history prior messages: $_summary"',
    '    fi',
    '  fi',
  ];
}

/**
 * The supervisor's interactive session: one process serves every turn. A
 * plain prompt streams two text deltas, commits one assistant message with
 * the same message id, and reports success; the permission marker blocks on
 * a Bash request until its control response arrives; the hold marker emits
 * nothing until the interrupt control request, then reports an interrupted
 * result; the partial-hold marker first commits one complete assistant text
 * message, then holds the same way (a server restart cuts it with no result).
 * A process resumed with `--resume` answers its first plain prompt with the
 * resumed history count instead. The operate markers drive Agentico like a real model would: each
 * helper call is one assistant `tool_use` (Bash, mirroring the command), the
 * command run through `eval` exactly as written (so "$AGENTICO_BIN" expands
 * from the launch environment), one `tool_result` user record carrying its
 * output, and a `helper:<command>` / `helper-exit:<n>:<code>` pair in the
 * invocation log; a text reply and a success result close the turn. A turn
 * whose wire text carries a hidden error-context bundle logs
 * `hidden-context:<turn>`. The process keeps reading stdin and exits cleanly
 * on EOF.
 */
function supervisorStubLines(providerInvocationLog: string): string[] {
  const { permission, hold, partialHold, operateCreate, operateStart, compact, usageHigh } =
    SUPERVISOR_E2E_MARKERS;
  const permissionInput = JSON.stringify({ command: SUPERVISOR_E2E_PERMISSION_COMMAND });
  // Single-quoted inside the sh command text, so it must carry no single quote.
  const operateConfig = JSON.stringify(SUPERVISOR_E2E_OPERATE_CONFIG);
  if (operateConfig.includes("'")) throw new Error('operate config must not contain a quote');
  const helper = '\\"\\$AGENTICO_BIN\\" api';
  return [
    `printf 'session\\n' >> "${providerInvocationLog}"`,
    'turn=0',
    'tool=0',
    '_operate_feature=""',
    // JSON string escaping for arbitrary text: backslashes, then quotes,
    // then newlines joined as \n (helper bodies are JSON on one line, but a
    // usage error or a pretty-printed body would not be).
    'json_escape() {',
    String.raw`  printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g' | awk 'NR > 1 { printf "%s", "\\n" } { printf "%s", $0 }'`,
    '}',
    'operate_helper() {',
    '  tool=$((tool + 1))',
    `  printf '${SUPERVISOR_E2E_HELPER_LOG_PREFIX}%s\\n' "$1" >> "${providerInvocationLog}"`,
    `  printf '{"type":"assistant","message":{"id":"msg-e2e-supervisor-tool-%s","role":"assistant","content":[{"type":"tool_use","id":"toolu-e2e-supervisor-%s","name":"Bash","input":{"command":"%s"}}]}}\\n' "$tool" "$tool" "$(json_escape "$1")"`,
    // stdin stays the session's input pipe; the helper must never read it.
    '  _operate_out=$(eval "$1" </dev/null 2>&1)',
    '  _operate_code=$?',
    `  printf 'helper-exit:%s:%s\\n' "$tool" "$_operate_code" >> "${providerInvocationLog}"`,
    '  if [ "$_operate_code" = 0 ]; then _is_error=false; else _is_error=true; fi',
    `  printf '{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu-e2e-supervisor-%s","content":"%s","is_error":%s}]}}\\n' "$tool" "$(json_escape "$_operate_out")" "$_is_error"`,
    '}',
    'operate_reply() {',
    `  printf '{"type":"assistant","message":{"id":"msg-e2e-supervisor-%s","role":"assistant","content":[{"type":"text","text":"%s"}]}}\\n' "$turn" "$(json_escape "$1")"`,
    `  printf '%s\\n' '{"type":"result","subtype":"success","session_id":"e2e-supervisor-session","total_cost_usd":0}'`,
    '}',
    'operate_create() {',
    String.raw`  _name=$(printf '%s\n' "$1" | sed -n 's/.*${operateCreate}\[\([^]]*\)\].*/\1/p' | head -n 1)`,
    `  operate_helper "${helper} POST /api/v1/features '{\\"name\\":\\"$_name\\"}'"`,
    String.raw`  _operate_feature=$(printf '%s\n' "$_operate_out" | sed -n 's/.*"feature_id" *: *"\([^"]*\)".*/\1/p' | head -n 1)`,
    `  printf 'operate-feature:%s\\n' "$_operate_feature" >> "${providerInvocationLog}"`,
    `  operate_helper "${helper} POST /api/v1/features/$_operate_feature/actions/setup '{}'"`,
    `  operate_helper "${helper} POST /api/v1/features/$_operate_feature/config '${operateConfig.replaceAll('"', '\\"')}'"`,
    `  operate_helper "${helper} GET /api/v1/features/$_operate_feature"`,
    `  operate_reply "Created and configured $_name."`,
    '}',
    'operate_start() {',
    `  operate_helper "${helper} POST /api/v1/features/$_operate_feature/actions/start '{}'"`,
    `  operate_reply '${SUPERVISOR_E2E_OPERATE_STARTED_REPLY}'`,
    '}',
    // Two deltas split at the first space, then the committed message; the
    // first reply of a resumed process reports the history it found.
    'supervisor_reply() {',
    '  _reply="Supervisor reply $turn"',
    '  if [ -n "$_resume_reply" ]; then _reply="$_resume_reply"; _resume_reply=""; fi',
    '  _input_tokens=$((10000 + turn * 1000))',
    '  if [ "$_usage_high" = 1 ]; then _input_tokens=170000; fi',
    '  if [ "$_compacted" = 1 ]; then _input_tokens=10000; fi',
    `  printf '{"type":"stream_event","event":{"type":"message_start","message":{"id":"msg-e2e-supervisor-%s"}}}\\n' "$turn"`,
    `  printf '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"%s "}}}\\n' "\${_reply%% *}"`,
    `  printf '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"%s"}}}\\n' "\${_reply#* }"`,
    `  printf '{"type":"assistant","message":{"id":"msg-e2e-supervisor-%s","role":"assistant","content":[{"type":"text","text":"%s"}],"usage":{"input_tokens":%s,"output_tokens":100}}}\\n' "$turn" "$(json_escape "$_reply")" "$_input_tokens"`,
    `  printf '%s\\n' '{"type":"result","subtype":"success","session_id":"e2e-supervisor-session","total_cost_usd":0,"modelUsage":{"claude-haiku-4-5":{"contextWindow":200000}}}'`,
    '}',
    // Blocks until the interrupt control request; EOF ends the process.
    'hold_turn() {',
    '  _interrupted=0',
    '  while IFS= read -r _line; do',
    '    case "$_line" in',
    `      *'"subtype":"interrupt"'*) _interrupted=1; break ;;`,
    '    esac',
    '  done',
    '  [ "$_interrupted" = 1 ] || exit 0',
    `  printf 'interrupted:%s\\n' "$turn" >> "${providerInvocationLog}"`,
    `  printf '%s\\n' '{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"e2e-supervisor-session","total_cost_usd":0}'`,
    '}',
    'supervisor_turn() {',
    '  turn=$((turn + 1))',
    '  _usage_high=0',
    '  _compacted=0',
    `  case "$1" in *${usageHigh}*) _usage_high=1 ;; esac`,
    `  case "$1" in *${compact}*) _compacted=1 ;; esac`,
    '  if [ "$_compacted" = 1 ]; then',
    String.raw`    printf '%s\n' '{"type":"system","subtype":"compact_boundary","compact_metadata":{"trigger":"auto","pre_tokens":170000}}'`,
    String.raw`    printf '{"type":"user","isCompactSummary":true,"message":{"role":"user","content":"Summary: %s"}}\n' "$(json_escape "$1")"`,
    '  fi',
    `  printf 'turn:%s\\n' "$turn" >> "${providerInvocationLog}"`,
    '  case "$1" in',
    `    *'${SUPERVISOR_E2E_HIDDEN_CONTEXT_HEADING}'*)`,
    `      printf 'hidden-context:%s\\n' "$turn" >> "${providerInvocationLog}"`,
    '      ;;',
    '  esac',
    '  case "$1" in',
    `    *${permission}*)`,
    `      printf '{"type":"control_request","request_id":"supervisor-perm-%s","request":{"subtype":"can_use_tool","tool_name":"Bash","input":${permissionInput}}}\\n' "$turn"`,
    `      printf 'pending:supervisor-perm-%s\\n' "$turn" >> "${providerInvocationLog}"`,
    '      _answered=0',
    '      while IFS= read -r _line; do',
    '        case "$_line" in',
    `          *'"control_response"'*) _answered=1; break ;;`,
    '        esac',
    '      done',
    '      [ "$_answered" = 1 ] || exit 0',
    `      printf 'response:supervisor-perm-%s:%s\\n' "$turn" "$_line" >> "${providerInvocationLog}"`,
    '      supervisor_reply',
    '      ;;',
    `    *${partialHold}*)`,
    `      printf '{"type":"assistant","message":{"id":"msg-e2e-supervisor-partial-%s","role":"assistant","content":[{"type":"text","text":"Partial supervisor reply %s"}]}}\\n' "$turn" "$turn"`,
    `      printf 'partial-holding:%s\\n' "$turn" >> "${providerInvocationLog}"`,
    '      hold_turn',
    '      ;;',
    `    *${hold}*)`,
    `      printf 'holding:%s\\n' "$turn" >> "${providerInvocationLog}"`,
    '      hold_turn',
    '      ;;',
    `    *${operateCreate}*)`,
    `      printf 'operate-create:%s\\n' "$turn" >> "${providerInvocationLog}"`,
    '      operate_create "$1"',
    '      ;;',
    `    *${operateStart}*)`,
    `      printf 'operate-start:%s\\n' "$turn" >> "${providerInvocationLog}"`,
    '      operate_start',
    '      ;;',
    '    *)',
    '      supervisor_reply',
    '      ;;',
    '  esac',
    '}',
    `case "$_agentico_prompt" in`,
    `  *'"type":"user"'*) supervisor_turn "$_agentico_prompt" ;;`,
    'esac',
    'while IFS= read -r _line; do',
    '  case "$_line" in',
    `    *'"type":"user"'*) supervisor_turn "$_line" ;;`,
    '  esac',
    'done',
    'exit 0',
  ];
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
function writeRuntimeConfig(
  world: JourneyWorld,
  presetWorkspaceRoot: boolean,
  unlaunchableCodex: boolean,
): void {
  const missing = path.join(world.stubDir, 'missing');
  fs.writeFileSync(
    world.configPath,
    [
      'providers:',
      '  claude:',
      `    cli: ${world.claudeStub}`,
      '  codex:',
      `    cli: ${unlaunchableCodex ? world.codexStub : path.join(missing, 'codex')}`,
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
 * Creates a bare remote and adds it as the `origin` remote for the given repo
 * path, so the feature manager records the repo as publishable. Mirrors the Go
 * e2e journey's `reviewFeedbackJourneyBareRemote`.
 */
export function addBareRemote(world: JourneyWorld, repoPath: string): string {
  const remote = path.join(world.workspaceRoot, `${path.basename(repoPath)}.git`);
  fs.mkdirSync(remote, { recursive: true });
  git(remote, 'init', '--bare');
  git(repoPath, 'remote', 'add', 'origin', remote);
  return remote;
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
