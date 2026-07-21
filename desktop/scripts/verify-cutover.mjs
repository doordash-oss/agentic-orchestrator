import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { existsSync, readFileSync, statSync, writeFileSync } from 'node:fs';
import { dirname, join, relative } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const desktopDir = dirname(dirname(fileURLToPath(import.meta.url)));
const rootDir = dirname(desktopDir);
const matrixPath = join(rootDir, 'docs', 'desktop', 'parity-matrix.md');
const manifestPath = join(desktopDir, 'cutover-frozen.json');
const EXPECTED_COLUMNS = 7;
const auditCategoryPatterns = Object.freeze({
  navigation: /dashboard|detail|overview|tab|keyboard/i,
  creation:
    /wizard|creation|attach (?:images|files)|repo selection|pipeline profile|review step|skill picker/i,
  live_supervision: /live event|watch|transcript|live preview|logs|artifacts|view diff/i,
  intervention: /answer agent|permission|resume all|stop a running|restart current|retry a failed/i,
  review_editing: /checkpoint review|review comments|planning review|edit feature/i,
  configuration: /settings|theme|config|model override|workspace|provider readiness/i,
  history: /sealed-run history|rewind|history and immutable/i,
  lifecycle_recovery: /connection shell|server|setup|recover|recovery/i,
  completion_publication:
    /publish|rebase|merge|refactor|mark feature|clean worktree|delete feature/i,
  chat: /Ask Me Anything/i,
  notifications: /notification|attention/i,
  native_commands: /help|keyboard-first|app-local settings/i,
  shutdown: /shutdown|quit/i,
});

function digestFrozenCells(row) {
  return createHash('sha256')
    .update(JSON.stringify(row.slice(0, 5)))
    .digest('hex');
}

function rowId(capability) {
  const shortHash = createHash('sha256').update(capability).digest('hex').slice(0, 12);
  return `cap-${shortHash}`;
}

export function parseMatrix(text) {
  const rows = [];
  let section = '';
  for (const [index, line] of text.split(/\r?\n/).entries()) {
    if (line.startsWith('## ')) {
      section = line.slice(3).trim();
      continue;
    }
    if (!line.startsWith('|')) continue;
    const cells = splitMarkdownRow(line);
    if (cells[0] === 'Capability' || cells.every((cell) => /^:?-+:?$/.test(cell))) continue;
    rows.push({ cells, line: index + 1, section });
  }
  return rows;
}

function splitMarkdownRow(line) {
  const cells = [];
  let cell = '';
  let inCode = false;
  for (let index = 1; index < line.length - 1; index += 1) {
    const character = line[index];
    if (character === '`' && line[index - 1] !== '\\') inCode = !inCode;
    if (character === '|' && !inCode && line[index - 1] !== '\\') {
      cells.push(cell.trim());
      cell = '';
      continue;
    }
    cell += character;
  }
  cells.push(cell.trim());
  return cells;
}

export function buildFrozenManifest(matrixText, baselineRevision) {
  const rows = parseMatrix(matrixText);
  const frozenRows = rows.map(({ cells }) => ({
    id: rowId(cells[0] ?? ''),
    capability: cells[0] ?? '',
    frozenHash: digestFrozenCells(cells),
  }));
  return {
    schemaVersion: 1,
    baselineRevision,
    auditCoverage: Object.fromEntries(
      Object.entries(auditCategoryPatterns).map(([category, pattern]) => [
        category,
        frozenRows.filter((row) => pattern.test(row.capability)).map((row) => row.id),
      ]),
    ),
    rows: frozenRows,
  };
}

function executableEvidencePaths(evidence) {
  return [...evidence.matchAll(/`([^`]+)`/g)]
    .map((match) => match[1].split('::', 1)[0])
    .filter((path) =>
      /(?:_test\.go|\.test\.(?:ts|tsx|mjs)|\.spec\.(?:ts|tsx)|\.txtar|\.sh|\.ya?ml)$/.test(path),
    );
}

function isIncompleteValue(value) {
  return /^(?:pending|partial|waived|unsupported)(?:\b|\s|:)/i.test(value.trim());
}

function validateRow(row, baseline, evidenceExists) {
  const failures = [];
  const { cells, line } = row;
  const label = baseline?.id ?? `line-${line}`;
  if (cells.length !== EXPECTED_COLUMNS) {
    return [`${label} line ${line}: has ${cells.length} columns, expected ${EXPECTED_COLUMNS}`];
  }
  const [capability, prior, interaction, contract, platform, evidence, status] = cells;
  for (const [name, value] of [
    ['capability', capability],
    ['prior behavior', prior],
    ['desktop interaction', interaction],
    ['authoritative contract', contract],
    ['platform scope', platform],
    ['automated evidence', evidence],
    ['status', status],
  ]) {
    if (value === '' || value === '—') failures.push(`${label} line ${line}: ${name} is blank`);
  }
  if (isIncompleteValue(interaction)) {
    failures.push(`${label} line ${line}: desktop interaction is incomplete`);
  }
  if (/^contract pending\b/i.test(contract) || isIncompleteValue(contract)) {
    failures.push(`${label} line ${line}: authoritative contract is incomplete`);
  }
  if (
    !/(?:\bIPC\b|\blocal\b|\bdiscovery\b|\b(?:GET|POST|PUT|PATCH|DELETE)\b|\bSSE\b|api\/openapi\.yaml|guarded .* execution)/i.test(
      contract,
    )
  ) {
    failures.push(
      `${label} line ${line}: contract is not an authoritative API, IPC, discovery, or local presentation contract`,
    );
  }
  if (!/macOS/i.test(platform) || !/Linux/i.test(platform)) {
    failures.push(`${label} line ${line}: macOS and Linux results are required`);
  }
  // The ledger is zero-gap, so a delivered row's platform/status cells must
  // not admit an unexercised-architecture gap (e.g. "no native runner" or
  // "not natively exercised"). Without this, a row can pass while naming an
  // unsupported architecture gap, leaving the cutover artifact inconsistent.
  const archGapPattern = /no native runner|not natively exercised|pending a native runner/i;
  if (archGapPattern.test(platform)) {
    failures.push(
      `${label} line ${line}: platform scope names an unexercised architecture gap, inconsistent with a delivered status`,
    );
  }
  if (archGapPattern.test(status)) {
    failures.push(
      `${label} line ${line}: status names an unexercised architecture gap, inconsistent with a delivered status`,
    );
  }
  if (!/^delivered(?:\b|\s|\()/i.test(status)) {
    failures.push(`${label} line ${line}: status is not fully delivered`);
  }
  const paths = executableEvidencePaths(evidence);
  if (paths.length === 0) {
    failures.push(`${label} line ${line}: automated evidence names no executable test`);
  } else {
    for (const path of paths) {
      if (!evidenceExists(path))
        failures.push(`${label} line ${line}: evidence does not exist: ${path}`);
    }
  }
  if (baseline !== undefined && digestFrozenCells(cells) !== baseline.frozenHash) {
    failures.push(
      `${label} line ${line}: frozen capability, prior behavior, interaction, contract, or platform cells changed`,
    );
  }
  return failures;
}

function validateResidue(files) {
  const failures = [];
  const bannedPaths = [join('internal', 'tui'), join('docs', 'keybindings.md')];
  const bannedTokens = [
    'charm.land/bubbletea',
    'charm.land/bubbles',
    'charm.land/lipgloss',
    'terminal_bundle_id',
    'keyboard_layout',
    'collapsed_sections',
    'APIAppModel',
    'ApplyKeyboardLayout',
    'ToggleInputNotify',
    'TERM_PROGRAM',
  ];
  for (const file of files) {
    const normalized = file.path.replaceAll('\\', '/');
    if (bannedPaths.some((path) => normalized === path || normalized.startsWith(`${path}/`))) {
      failures.push(`residue ${normalized}: retired terminal-client path is present`);
    }
    for (const token of bannedTokens) {
      if (file.content.includes(token))
        failures.push(`residue ${normalized}: contains retired token ${token}`);
    }
  }
  return failures;
}

export function verifyCutover({ matrixText, manifest, evidenceExists, residueFiles }) {
  const failures = [];
  if (manifest.schemaVersion !== 1)
    failures.push(`manifest schemaVersion=${manifest.schemaVersion}, expected 1`);
  if (typeof manifest.baselineRevision !== 'string' || manifest.baselineRevision === '') {
    failures.push('manifest baselineRevision is blank');
  }
  const rows = parseMatrix(matrixText);
  const frozenIDs = new Set((manifest.rows ?? []).map((row) => row.id));
  for (const category of Object.keys(auditCategoryPatterns)) {
    const covered = manifest.auditCoverage?.[category];
    if (!Array.isArray(covered) || covered.length === 0) {
      failures.push(`audit category ${category} is missing`);
      continue;
    }
    for (const id of covered) {
      if (!frozenIDs.has(id))
        failures.push(`audit category ${category} references unknown row ${id}`);
    }
  }
  const byCapability = new Map();
  for (const row of rows) {
    const capability = row.cells[0] ?? '';
    if (byCapability.has(capability)) {
      failures.push(`line-${row.line}: duplicate capability ${JSON.stringify(capability)}`);
    } else {
      byCapability.set(capability, row);
    }
  }
  const frozenCapabilities = new Set();
  for (const baseline of manifest.rows ?? []) {
    if (frozenCapabilities.has(baseline.capability)) {
      failures.push(
        `${baseline.id}: duplicate frozen capability ${JSON.stringify(baseline.capability)}`,
      );
      continue;
    }
    frozenCapabilities.add(baseline.capability);
    const row = byCapability.get(baseline.capability);
    if (row === undefined) {
      failures.push(`${baseline.id}: frozen capability is missing: ${baseline.capability}`);
      continue;
    }
    failures.push(...validateRow(row, baseline, evidenceExists));
  }
  for (const row of rows) {
    if (!frozenCapabilities.has(row.cells[0] ?? '')) {
      failures.push(...validateRow(row, undefined, evidenceExists));
    }
  }
  failures.push(...validateResidue(residueFiles));
  return failures;
}

function trackedResidueFiles(names) {
  const ignored = new Set([
    'desktop/scripts/audit-release.mjs',
    'desktop/scripts/audit-release.test.mjs',
    'desktop/scripts/verify-cutover.mjs',
    'desktop/scripts/verify-cutover.test.mjs',
    'cmd/agentico/desktop_only_contract_test.go',
    'cmd/agentico/docs_contract_test.go',
  ]);
  const files = [];
  for (const path of names) {
    if (ignored.has(path)) continue;
    const absolute = join(rootDir, path);
    if (!existsSync(absolute) || statSync(absolute).size > 2 * 1024 * 1024) continue;
    let content;
    try {
      content = readFileSync(absolute, 'utf8');
    } catch {
      continue;
    }
    files.push({ path, content });
  }
  return files;
}

function run() {
  const matrixText = readFileSync(matrixPath, 'utf8');
  if (process.argv.includes('--write-baseline')) {
    const revisionIndex = process.argv.indexOf('--baseline-revision');
    const revision = revisionIndex >= 0 ? process.argv[revisionIndex + 1] : '';
    if (revision === '') throw new Error('--write-baseline requires --baseline-revision <commit>');
    writeFileSync(
      manifestPath,
      `${JSON.stringify(buildFrozenManifest(matrixText, revision), null, 2)}\n`,
    );
    console.log(`wrote ${relative(rootDir, manifestPath)}`);
    return;
  }
  const manifest = JSON.parse(readFileSync(manifestPath, 'utf8'));
  const trackedPaths = execFileSync('git', ['ls-files', '-z'], { cwd: rootDir, encoding: 'utf8' })
    .split('\0')
    .filter(Boolean);
  const failures = verifyCutover({
    matrixText,
    manifest,
    evidenceExists: (path) => {
      if (existsSync(join(rootDir, path))) return true;
      if (path.includes('/')) return false;
      return trackedPaths.filter((tracked) => tracked.endsWith(`/${path}`)).length === 1;
    },
    residueFiles: trackedResidueFiles(trackedPaths),
  });
  if (failures.length > 0) {
    for (const failure of failures) console.error(`cutover: ${failure}`);
    process.exitCode = 1;
    return;
  }
  console.log(`cutover verified: ${manifest.rows.length} frozen rows, zero gaps or residue`);
}

if (import.meta.url === pathToFileURL(process.argv[1] ?? '').href) run();
