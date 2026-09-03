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

import { execFileSync } from 'node:child_process';
import {
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  readdirSync,
  rmSync,
  writeFileSync,
} from 'node:fs';
import os from 'node:os';
import { dirname, join, posix } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

import { auditElectronBuilderFuseConfig } from './lib/fuse-policy.mjs';
import { expectedDesktopArtifacts } from './lib/release-artifacts.mjs';

const desktopDir = dirname(dirname(fileURLToPath(import.meta.url)));
const rootDir = dirname(desktopDir);
const distDir = join(desktopDir, 'dist');
const exceptionsPath = join(desktopDir, 'release-audit-exceptions.json');
const inventoryPath = join(distDir, 'third-party-license-inventory.json');
const MiB = 1024 * 1024;
const RELEASE_AUDIT_DESKTOP_ARTIFACTS = Object.freeze([
  ...expectedDesktopArtifacts('v0.0.0').map(({ name }) => name),
  'desktop-release.json',
  'desktop-release.json.sig',
]);

const SEVERITY_RANK = Object.freeze({
  info: 0,
  low: 1,
  moderate: 2,
  medium: 2,
  high: 3,
  critical: 4,
});

function loadJson(path, label) {
  try {
    return JSON.parse(readFileSync(path, 'utf8'));
  } catch (error) {
    throw new Error(`could not read ${label} at ${path}: ${error.message}`);
  }
}

function requireFile(path, label, failures) {
  if (!existsSync(path)) failures.push(`missing ${label}: ${path}`);
}

/**
 * Ensure that GoReleaser signs and uploads each desktop package. This keeps
 * the release configuration deliberately small and auditable without adding a
 * YAML parser to the release runtime: the two relevant blocks have an
 * unambiguous top-level section, `extra_files` child, and `- glob` entries.
 * Each required path is literal after POSIX normalization, rather than a
 * filename match, so an unrelated directory or broad wildcard cannot cause a
 * release package to bypass signing or upload.
 */
export function auditGoReleaserDesktopArtifacts(configText, expectedNames) {
  const failures = [];
  for (const [section, label] of [
    ['checksum', 'checksum.extra_files'],
    ['release', 'release.extra_files'],
  ]) {
    const globs = goreleaserExtraFileGlobs(configText, section);
    for (const name of expectedNames) {
      const expectedGlob = expectedGoReleaserDesktopGlob(name);
      const entries = globs.filter(
        (glob) => normalizeGoReleaserExtraFileGlob(glob) === expectedGlob,
      );
      if (entries.length === 0) {
        failures.push(`GoReleaser ${label} omits ${name}`);
      } else if (entries.length !== 1) {
        failures.push(
          `GoReleaser ${label} has ${entries.length} entries for ${name}, expected exactly 1`,
        );
      }
    }
  }
  return failures;
}

/** Keep releases pinned to the commit that the local operator actually tagged. */
export function auditGoReleaserReleaseTarget(configText) {
  const lines = releaseSectionLines(configText);
  const targets = lines
    .map((line) => /^  target_commitish:\s*(.+?)\s*$/.exec(line)?.[1])
    .filter((value) => value !== undefined);
  if (targets.length !== 1 || targets[0] !== '"{{ .Commit }}"') {
    return [
      'GoReleaser release.target_commitish must be exactly "{{ .Commit }}" to prevent publishing another commit',
    ];
  }
  return [];
}

/** Audit the publication ordering and prohibit raw GoReleaser flag injection. */
export function auditReleaseMakefile(makefileText) {
  const failures = [];
  const recipe = makeRecipeCommands(makefileText, 'release');
  const completeRecipe = ['node desktop/scripts/release-run.mjs'];
  if (JSON.stringify(recipe) !== JSON.stringify(completeRecipe)) {
    failures.push(
      'Makefile release recipe must contain only the complete audited release command structure',
    );
  }
  if (/RELEASE_TAG|RELEASE_COMMIT|GORELEASER_FLAGS/.test(recipe.join('\n'))) {
    failures.push('Makefile must not expose publication identity or arbitrary GoReleaser inputs');
  }
  return failures;
}

/** Require the wrapper's outward-facing operations in one fail-closed order. */
export function auditReleaseRunner(runnerText) {
  const start = runnerText.indexOf('evidence = preflight();');
  if (start === -1) return ['release runner does not begin with captured preflight evidence'];
  const resumeStart = runnerText.indexOf('if (pending !== null)');
  const resumeBody = resumeStart === -1 ? '' : runnerText.slice(resumeStart, start);
  const resumedRemoteChecks = [
    ...resumeBody.matchAll(/await verifyRemote\(\{ evidence, snapshot \}\)/g),
  ].map((match) => match.index);
  const resumedCasks = [...resumeBody.matchAll(/publishCask\(\{ evidence, snapshot \}\)/g)].map(
    (match) => match.index,
  );
  if (resumedCasks.length === 0) {
    return ['release runner must reverify remote publication before every resumed cask'];
  }
  let previousCask = -1;
  for (const resumedCask of resumedCasks) {
    if (!resumedRemoteChecks.some((remote) => remote > previousCask && remote < resumedCask)) {
      return ['release runner must reverify remote publication before every resumed cask'];
    }
    previousCask = resumedCask;
  }
  const body = runnerText.slice(start);
  const ordered = [
    "command('npm-ci'",
    "command('mac-package'",
    "command('linux-packages'",
    'verifyPackages({ evidence, desktopDist:',
    'prepareDesktopManifest({ evidence, desktopDist })',
    'verifyDesktopManifest({ evidence, desktopDist })',
    'snapshot = createSnapshot({',
    'verifySnapshot(snapshot)',
    'verifyPackages({ evidence, desktopDist: snapshot.path })',
    'verifyDesktopManifest({ evidence, desktopDist: snapshot.path })',
    'verifyProvenance(evidence)',
    "saveResume(evidence, snapshot, 'tag-reservation-started')",
    'await reserveTag({ evidence })',
    "saveResume(evidence, snapshot, 'goreleaser-started')",
    'publish({ evidence, snapshot, notesFile })',
    "saveResume(evidence, snapshot, 'goreleaser-published')",
    'verifyManifest({ evidence, snapshot })',
    'await verifyRemote({ evidence, snapshot })',
    "saveResume(evidence, snapshot, 'remote-verified')",
    'publishCask({ evidence, snapshot })',
  ];
  let cursor = -1;
  for (const step of ordered) {
    const index = body.indexOf(step, cursor + 1);
    if (index === -1) return [`release runner omits or reorders audited step: ${step}`];
    cursor = index;
  }
  const cleanup = body.indexOf('cleanup(evidence)', cursor);
  if (cleanup === -1) return ['release runner must clean the detached workspace in finally'];
  const removeResume = body.indexOf('removeResume(operatorRoot)', cleanup + 1);
  if (removeResume === -1) {
    return ['release runner must remove resume state only after detached-workspace cleanup'];
  }
  return [];
}

/** Prohibit cleanup from compiling or running source from the operator checkout. */
export function auditReleaseWorkspace(workspaceText) {
  if (
    /execFileSync\(\s*['"]go['"]\s*,\s*\[[\s\S]*?['"]run['"][\s\S]*?release-cleanup/.test(
      workspaceText,
    )
  ) {
    return ['release workspace cleanup must never execute ambient source with go run'];
  }
  return [];
}

/** Extract only tab-indented shell commands from one exact Make target's recipe. */
function makeRecipeCommands(makefileText, target) {
  const lines = makefileText.split(/\r?\n/);
  const targetIndex = lines.findIndex((line) => new RegExp(`^${target}:\\s*(?:#.*)?$`).test(line));
  if (targetIndex === -1) return [];
  const commands = [];
  for (let index = targetIndex + 1; index < lines.length; index += 1) {
    const line = lines[index];
    if (line.startsWith('\t')) {
      commands.push(
        line
          .slice(1)
          .replace(/^[@+\-]/, '')
          .trim(),
      );
      continue;
    }
    if (/^\S/.test(line) && !line.startsWith('#')) break;
  }
  return commands;
}

function releaseSectionLines(configText) {
  const lines = configText.split(/\r?\n/);
  const start = lines.findIndex((line) => /^release:\s*(?:#.*)?$/.test(line));
  if (start === -1) return [];
  const section = [];
  for (let index = start + 1; index < lines.length; index += 1) {
    if (/^\S/.test(lines[index])) break;
    section.push(lines[index]);
  }
  return section;
}

function goreleaserExtraFileGlobs(configText, section) {
  const lines = configText.split(/\r?\n/);
  const sectionIndex = lines.findIndex((line) =>
    new RegExp(`^${section}:\\s*(?:#.*)?$`).test(line),
  );
  if (sectionIndex === -1) return [];

  const globs = [];
  let inExtraFiles = false;
  for (let index = sectionIndex + 1; index < lines.length; index += 1) {
    const line = lines[index];
    if (/^\S/.test(line)) break;
    if (/^  extra_files:\s*(?:#.*)?$/.test(line)) {
      inExtraFiles = true;
      continue;
    }
    if (!inExtraFiles) continue;
    if (/^  \S/.test(line)) break;
    const match = /^    - glob:\s*(.+?)(?:\s+#.*)?$/.exec(line);
    if (match?.[1] !== undefined) globs.push(match[1].trim());
  }
  return globs;
}

function expectedGoReleaserDesktopGlob(artifactName) {
  const deb = /^agentico_\d+\.\d+\.\d+_(amd64|arm64)\.deb$/.exec(artifactName);
  if (deb?.[1] !== undefined) {
    return `desktop/dist/publication/agentico_{{ .Version }}_${deb[1]}.deb`;
  }
  return `desktop/dist/publication/${artifactName}`;
}

function normalizeGoReleaserExtraFileGlob(glob) {
  return posix.normalize(glob.replace(/^['"]|['"]$/g, ''));
}

function normalizeLicense(license) {
  return String(license ?? '')
    .trim()
    .replace(/^\((.*)\)$/, '$1')
    .replace(/\s+/g, ' ');
}

function packageNameFromLockKey(key) {
  if (key.includes('/node_modules/')) return key.split('/node_modules/').at(-1);
  if (key.startsWith('node_modules/')) return key.slice('node_modules/'.length);
  return key;
}

function resolveNpmPackageKey(lock, name, parentKey = '') {
  const direct = `node_modules/${name}`;
  if (parentKey !== '') {
    for (const ancestor of npmResolutionAncestors(parentKey)) {
      const nested = `${ancestor}/node_modules/${name}`;
      if (lock.packages?.[nested] !== undefined) return nested;
    }
  }
  if (lock.packages?.[direct] !== undefined) return direct;
  return direct;
}

function npmResolutionAncestors(parentKey) {
  const ancestors = [];
  let current = parentKey;
  while (current !== '') {
    ancestors.push(current);
    const index = current.lastIndexOf('/node_modules/');
    if (index < 0) break;
    current = current.slice(0, index);
  }
  return ancestors;
}

export function collectNpmRuntimeInventory(lock, desktopPackage, exceptions) {
  const roots = [
    ...Object.keys(desktopPackage.dependencies ?? {}),
    ...(exceptions.shippedDevDependencies ?? []),
  ];
  const seen = new Set();
  const inventory = [];
  const visit = (name, parentKey = '') => {
    const key = resolveNpmPackageKey(lock, name, parentKey);
    if (seen.has(key)) return;
    seen.add(key);
    const entry = lock.packages?.[key];
    if (entry === undefined) {
      inventory.push({
        source: 'npm',
        name,
        path: key,
        version: null,
        license: 'UNKNOWN',
        resolved: null,
        integrity: null,
        missing: true,
      });
      return;
    }
    inventory.push({
      source: 'npm',
      name: packageNameFromLockKey(key),
      path: key,
      version: entry.version ?? null,
      license: normalizeLicense(entry.license),
      resolved: entry.resolved ?? null,
      integrity: entry.integrity ?? null,
      devOnlyInLock: entry.dev === true,
    });
    for (const dependency of [
      ...Object.keys(entry.dependencies ?? {}),
      ...Object.keys(entry.optionalDependencies ?? {}),
    ]) {
      visit(dependency, key);
    }
  };
  for (const root of roots) visit(root);
  return inventory.sort((a, b) => a.name.localeCompare(b.name));
}

export function auditNpmLockfile(lock, desktopPackage, runtimeInventory) {
  const failures = [];
  if (lock.lockfileVersion !== 3) {
    failures.push(`package-lock.json lockfileVersion=${lock.lockfileVersion}, expected 3`);
  }
  if (
    !Array.isArray(lock.packages?.['']?.workspaces) ||
    !lock.packages[''].workspaces.includes('desktop')
  ) {
    failures.push('package-lock.json root workspaces do not include desktop');
  }
  const lockDesktop = lock.packages?.desktop;
  if (lockDesktop === undefined) {
    failures.push('package-lock.json is missing the desktop workspace package entry');
  } else {
    const lockDependencies = Object.keys(lockDesktop.dependencies ?? {}).sort();
    const manifestDependencies = Object.keys(desktopPackage.dependencies ?? {}).sort();
    if (JSON.stringify(lockDependencies) !== JSON.stringify(manifestDependencies)) {
      failures.push(
        `desktop package-lock dependencies ${lockDependencies.join(',')} do not match package.json ${manifestDependencies.join(',')}`,
      );
    }
  }
  for (const pkg of runtimeInventory) {
    if (pkg.missing === true) {
      failures.push(`runtime npm package missing from package-lock.json: ${pkg.path}`);
      continue;
    }
    if (pkg.version === null || pkg.version === '') {
      failures.push(`runtime npm package missing version: ${pkg.path}`);
    }
    if (pkg.license === '') {
      failures.push(`runtime npm package missing license metadata: ${pkg.path}`);
    }
    if (pkg.resolved === null || !pkg.resolved.startsWith('https://registry.npmjs.org/')) {
      failures.push(`runtime npm package has non-registry provenance: ${pkg.path}`);
    }
    if (pkg.integrity === null || !/^sha512-[A-Za-z0-9+/=]+$/.test(pkg.integrity)) {
      failures.push(`runtime npm package missing sha512 integrity: ${pkg.path}`);
    }
  }
  return failures;
}

export function parseGoModRequirements(goModText) {
  const requirements = [];
  let inRequireBlock = false;
  for (const rawLine of goModText.split(/\r?\n/)) {
    const line = rawLine.trim();
    if (line === '' || line.startsWith('//')) continue;
    if (line === 'require (') {
      inRequireBlock = true;
      continue;
    }
    if (inRequireBlock && line === ')') {
      inRequireBlock = false;
      continue;
    }
    if (line.startsWith('require ')) {
      const parts = line.split(/\s+/);
      if (parts.length >= 3) {
        requirements.push({
          path: parts[1],
          version: parts[2],
          indirect: rawLine.includes('// indirect'),
        });
      }
      continue;
    }
    if (inRequireBlock) {
      const parts = line.split(/\s+/);
      if (parts.length >= 2) {
        requirements.push({
          path: parts[0],
          version: parts[1],
          indirect: rawLine.includes('// indirect'),
        });
      }
    }
  }
  return requirements;
}

function auditGoModuleIntegrity(goModText, goSumText, requirements) {
  const failures = [];
  if (
    /^\s*(replace|exclude)\s+/m.test(goModText) ||
    /^\s*(replace|exclude)\s*\(/m.test(goModText)
  ) {
    failures.push(
      'go.mod contains replace/exclude directives; release provenance must be reviewed',
    );
  }
  for (const requirement of requirements) {
    const modulePattern = escapeRegExp(`${requirement.path} ${requirement.version}`);
    const moduleSum = new RegExp(`^${modulePattern} h1:[A-Za-z0-9+/=]+$`, 'm');
    const goModSum = new RegExp(`^${modulePattern}/go\\.mod h1:[A-Za-z0-9+/=]+$`, 'm');
    if (!moduleSum.test(goSumText)) {
      failures.push(
        `go.sum missing module checksum for ${requirement.path} ${requirement.version}`,
      );
    }
    if (!goModSum.test(goSumText)) {
      failures.push(
        `go.sum missing go.mod checksum for ${requirement.path} ${requirement.version}`,
      );
    }
  }
  return failures;
}

function escapeRegExp(value) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

function moduleCacheEscape(value) {
  return value.replace(/[A-Z]/g, (letter) => `!${letter.toLowerCase()}`);
}

function goModuleCacheDir(modulePath, version) {
  const cacheRoot = process.env.GOMODCACHE ?? join(os.homedir(), 'go', 'pkg', 'mod');
  return join(cacheRoot, `${moduleCacheEscape(modulePath)}@${moduleCacheEscape(version)}`);
}

function findLicenseFiles(dir) {
  try {
    return readdirSync(dir).filter((name) => /^(licen[sc]e|licence|copying)(\.|$)/i.test(name));
  } catch {
    return [];
  }
}

function classifyLicenseText(text) {
  const lower = text.toLowerCase();
  if (lower.includes('bsd zero clause license')) return '0BSD';
  if (lower.includes('apache license') && lower.includes('version 2.0')) return 'Apache-2.0';
  if (lower.includes('mozilla public license') && lower.includes('version 2.0')) return 'MPL-2.0';
  if (lower.includes('permission is hereby granted, free of charge')) return 'MIT';
  if (lower.includes('isc license')) return 'ISC';
  if (lower.includes('released into the public domain') && lower.includes('unencumbered')) {
    return 'Unlicense';
  }
  if (lower.includes('redistribution and use in source and binary forms')) {
    return lower.includes('neither the name') || lower.includes('nor the names')
      ? 'BSD-3-Clause'
      : 'BSD-2-Clause';
  }
  return 'UNKNOWN';
}

function collectGoLicenseInventory(requirements) {
  return requirements.map((requirement) => {
    const dir = goModuleCacheDir(requirement.path, requirement.version);
    const licenseFiles = findLicenseFiles(dir);
    let license = 'UNKNOWN';
    if (licenseFiles.length > 0) {
      const body = readFileSync(join(dir, licenseFiles[0]), 'utf8');
      license = classifyLicenseText(body.slice(0, 64 * 1024));
    }
    return {
      source: 'go',
      name: requirement.path,
      version: requirement.version,
      license,
      licenseFiles,
      indirect: requirement.indirect,
    };
  });
}

function isActiveException(exception, now = new Date()) {
  if (typeof exception.expires !== 'string' || Number.isNaN(Date.parse(exception.expires))) {
    return false;
  }
  return Date.parse(exception.expires) > now.getTime();
}

function matchingLicenseException(pkg, exceptions, now = new Date()) {
  return (exceptions.licenseExceptions ?? []).find(
    (exception) =>
      exception.source === pkg.source &&
      exception.name === pkg.name &&
      normalizeLicense(exception.license) === normalizeLicense(pkg.license) &&
      isActiveException(exception, now),
  );
}

export function auditLicenseInventory(inventory, exceptions, now = new Date()) {
  const allowed = new Set((exceptions.allowedLicenses ?? []).map(normalizeLicense));
  const failures = [];
  for (const pkg of inventory) {
    const license = normalizeLicense(pkg.license);
    if (license === '' || license === 'UNKNOWN') {
      if (matchingLicenseException(pkg, exceptions, now) === undefined) {
        failures.push(
          `${pkg.source} package ${pkg.name}@${pkg.version ?? '(unknown)'} has unknown license`,
        );
      }
      continue;
    }
    if (!allowed.has(license) && matchingLicenseException(pkg, exceptions, now) === undefined) {
      failures.push(
        `${pkg.source} package ${pkg.name}@${pkg.version ?? '(unknown)'} uses unreviewed license ${license}`,
      );
    }
  }
  for (const exception of exceptions.licenseExceptions ?? []) {
    if (!isActiveException(exception, now)) {
      failures.push(
        `license exception for ${exception.source}:${exception.name} ${exception.license} is expired or has no valid expires date`,
      );
    }
  }
  return failures;
}

function vulnerabilityExceptionMatches(vulnerability, exception, now = new Date()) {
  if (!isActiveException(exception, now)) return false;
  if (exception.source !== vulnerability.source) return false;
  if (exception.id !== undefined && exception.id !== vulnerability.id) return false;
  if (exception.name !== undefined && exception.name !== vulnerability.name) return false;
  return true;
}

function isExceptedVulnerability(vulnerability, exceptions) {
  return (exceptions.vulnerabilityExceptions ?? []).some((exception) =>
    vulnerabilityExceptionMatches(vulnerability, exception),
  );
}

function runCommand(command, args, cwd, timeout = 120_000, env = process.env) {
  try {
    const stdout = execFileSync(command, args, {
      cwd,
      encoding: 'utf8',
      env,
      maxBuffer: 20 * MiB,
      stdio: ['ignore', 'pipe', 'pipe'],
      timeout,
    });
    return { ok: true, exitCode: 0, stdout, stderr: '' };
  } catch (error) {
    if (error.code === 'ENOENT') {
      return { ok: false, unavailable: true, exitCode: null, stdout: '', stderr: error.message };
    }
    return {
      ok: false,
      exitCode: error.status ?? null,
      stdout: error.stdout?.toString() ?? '',
      stderr: error.stderr?.toString() ?? error.message,
    };
  }
}

function auditNpmVulnerabilities(exceptions) {
  const result = runCommand(
    'npm',
    ['audit', '--omit=dev', '--audit-level=high', '--json'],
    rootDir,
    120_000,
  );
  const tool = {
    name: 'npm audit',
    status: 'passed',
    command: 'npm audit --omit=dev --audit-level=high --json',
  };
  if (result.unavailable === true) {
    return {
      tool: { ...tool, status: 'unavailable', reason: result.stderr },
      failures: [],
      warnings: [],
    };
  }
  let parsed;
  try {
    parsed = JSON.parse(result.stdout);
  } catch {
    if (result.ok) {
      return {
        tool: { ...tool, status: 'failed' },
        failures: ['npm audit returned unparseable JSON'],
        warnings: [],
      };
    }
    return {
      tool: { ...tool, status: 'unavailable', exitCode: result.exitCode },
      failures: [],
      warnings: [
        `npm audit did not produce JSON; scanner unavailable (${trimOneLine(result.stderr)})`,
      ],
    };
  }
  if (parsed.error !== undefined && result.exitCode !== 1) {
    return {
      tool: {
        ...tool,
        status: 'unavailable',
        exitCode: result.exitCode,
        error: parsed.error.summary ?? parsed.error.code,
      },
      failures: [],
      warnings: [
        `npm audit unavailable: ${parsed.error.summary ?? parsed.error.code ?? 'unknown error'}`,
      ],
    };
  }
  const highCritical = [];
  for (const [name, value] of Object.entries(parsed.vulnerabilities ?? {})) {
    const severity = String(value.severity ?? '').toLowerCase();
    if ((SEVERITY_RANK[severity] ?? 0) >= SEVERITY_RANK.high) {
      highCritical.push({ source: 'npm', id: value.via?.[0]?.source?.toString(), name, severity });
    }
  }
  const unexcepted = highCritical.filter(
    (vulnerability) => !isExceptedVulnerability(vulnerability, exceptions),
  );
  return {
    tool: {
      ...tool,
      status: unexcepted.length === 0 ? 'passed' : 'failed',
      highCriticalCount: highCritical.length,
    },
    failures: unexcepted.map(
      (vulnerability) =>
        `npm ${vulnerability.severity} vulnerability requires exception: ${vulnerability.name}${vulnerability.id === undefined ? '' : ` (${vulnerability.id})`}`,
    ),
    warnings: [],
  };
}

function auditGoVulnerabilities(exceptions) {
  const tempRoot = mkdtempSync(join(os.tmpdir(), 'agentico-govulncheck-'));
  const attempts = [
    {
      command: 'go',
      args: ['run', 'golang.org/x/vuln/cmd/govulncheck@latest', '-json', './...'],
      label: 'go run golang.org/x/vuln/cmd/govulncheck@latest -json ./...',
      env: {
        ...process.env,
        GOPATH: join(tempRoot, 'gopath'),
        GOMODCACHE: join(tempRoot, 'gomodcache'),
        GOCACHE: join(tempRoot, 'gocache'),
      },
      cleanup: () =>
        rmSync(tempRoot, { recursive: true, force: true, maxRetries: 3, retryDelay: 100 }),
    },
  ];

  const unavailable = [];
  try {
    for (const attempt of attempts) {
      const result = runCommand(attempt.command, attempt.args, rootDir, 180_000, attempt.env);
      const parsed = parseGovulncheckRecords(result);
      if (parsed.records !== undefined) {
        return evaluateGoVulnerabilityRecords(parsed.records, exceptions, {
          name: 'govulncheck',
          status: 'passed',
          command: attempt.label,
        });
      }
      unavailable.push(`${attempt.label}: ${parsed.reason}`);
    }
  } finally {
    for (const attempt of attempts) {
      try {
        attempt.cleanup();
      } catch {
        // Cleanup failure should not hide the scanner result.
      }
    }
  }

  return {
    tool: {
      name: 'govulncheck',
      status: 'unavailable',
      command: attempts.map((attempt) => attempt.label).join(' || '),
    },
    failures: [],
    warnings: [`govulncheck unavailable: ${unavailable.join('; ')}`],
  };
}

function parseGovulncheckRecords(result) {
  const text = result.stdout
    .split(/\r?\n/)
    .filter((line) => !line.startsWith('go: '))
    .join('\n');
  const records = [];
  let start = -1;
  let depth = 0;
  let inString = false;
  let escaped = false;
  for (let index = 0; index < text.length; index += 1) {
    const char = text[index];
    if (start === -1) {
      if (/\s/.test(char)) continue;
      if (char !== '{') return { reason: `unexpected scanner output before JSON (${char})` };
      start = index;
      depth = 1;
      continue;
    }
    if (escaped) {
      escaped = false;
      continue;
    }
    if (char === '\\' && inString) {
      escaped = true;
      continue;
    }
    if (char === '"') {
      inString = !inString;
      continue;
    }
    if (inString) continue;
    if (char === '{') depth += 1;
    if (char === '}') depth -= 1;
    if (depth === 0) {
      const payload = text.slice(start, index + 1);
      start = -1;
      try {
        records.push(JSON.parse(payload));
      } catch {
        return { reason: `output was not parseable JSON (${trimOneLine(result.stderr)})` };
      }
    }
  }
  if (start !== -1) return { reason: 'scanner output ended inside a JSON object' };
  if (!result.ok && records.length === 0) {
    return { reason: trimOneLine(result.stderr) };
  }
  return { records };
}

function evaluateGoVulnerabilityRecords(records, exceptions, tool) {
  const osv = new Map();
  const findings = [];
  for (const record of records) {
    if (record.osv !== undefined) osv.set(record.osv.id, record.osv);
    if (record.finding !== undefined) {
      const id = record.finding.osv ?? record.finding.osv_id ?? record.finding.id;
      if (id !== undefined) {
        const meta = osv.get(id);
        const severity = highestGoSeverity(meta);
        findings.push({
          source: 'go',
          id,
          name:
            record.finding.symbol ??
            record.finding.package ??
            record.finding.trace?.[0]?.module ??
            id,
          severity,
        });
      }
    }
  }
  const highCritical = findings.filter(
    (finding) => (SEVERITY_RANK[finding.severity] ?? SEVERITY_RANK.high) >= SEVERITY_RANK.high,
  );
  const unexcepted = highCritical.filter(
    (vulnerability) => !isExceptedVulnerability(vulnerability, exceptions),
  );
  return {
    tool: {
      ...tool,
      status: unexcepted.length === 0 ? 'passed' : 'failed',
      highCriticalCount: highCritical.length,
    },
    failures: unexcepted.map(
      (vulnerability) =>
        `Go ${vulnerability.severity} vulnerability requires exception: ${vulnerability.id} (${vulnerability.name})`,
    ),
    warnings: [],
  };
}

export function highestGoSeverity(osv) {
  const severities = osv?.severity;
  if (!Array.isArray(severities) || severities.length === 0) return 'high';
  let highest = 'low';
  for (const severity of severities) {
    const label = classifyOsvSeverity(severity);
    if ((SEVERITY_RANK[label] ?? 0) > (SEVERITY_RANK[highest] ?? 0)) highest = label;
  }
  return highest;
}

function classifyOsvSeverity(severity) {
  if (typeof severity !== 'object' || severity === null) return 'high';
  const rawScore = severity.score;
  if (typeof rawScore === 'number' && Number.isFinite(rawScore)) {
    return classifyCvssScore(rawScore);
  }
  if (typeof rawScore === 'string') {
    const numeric = Number(rawScore);
    if (Number.isFinite(numeric)) {
      return classifyCvssScore(numeric);
    }
    const cvssV3 = cvssV3BaseScore(rawScore);
    if (cvssV3 !== null) {
      return classifyCvssScore(cvssV3);
    }
  }
  const rawType = typeof severity.type === 'string' ? severity.type.toLowerCase() : '';
  if (rawType.includes('critical')) return 'critical';
  if (rawType.includes('high')) return 'high';
  if (rawType.includes('medium') || rawType.includes('moderate')) return 'moderate';
  if (rawType.includes('low')) return 'low';
  return 'high';
}

function classifyCvssScore(score) {
  if (score >= 9) return 'critical';
  if (score >= 7) return 'high';
  if (score >= 4) return 'moderate';
  return 'low';
}

function cvssV3BaseScore(vector) {
  if (!/^CVSS:3\.[01]\//.test(vector)) return null;
  const metrics = Object.fromEntries(
    vector
      .split('/')
      .slice(1)
      .map((part) => {
        const [key, value] = part.split(':');
        return [key, value];
      }),
  );
  const scope = metrics.S;
  const av = { N: 0.85, A: 0.62, L: 0.55, P: 0.2 }[metrics.AV];
  const ac = { L: 0.77, H: 0.44 }[metrics.AC];
  const pr =
    scope === 'C'
      ? { N: 0.85, L: 0.68, H: 0.5 }[metrics.PR]
      : { N: 0.85, L: 0.62, H: 0.27 }[metrics.PR];
  const ui = { N: 0.85, R: 0.62 }[metrics.UI];
  const confidentiality = { H: 0.56, L: 0.22, N: 0 }[metrics.C];
  const integrity = { H: 0.56, L: 0.22, N: 0 }[metrics.I];
  const availability = { H: 0.56, L: 0.22, N: 0 }[metrics.A];
  if (
    scope === undefined ||
    av === undefined ||
    ac === undefined ||
    pr === undefined ||
    ui === undefined ||
    confidentiality === undefined ||
    integrity === undefined ||
    availability === undefined
  ) {
    return null;
  }
  const impactSubscore = 1 - (1 - confidentiality) * (1 - integrity) * (1 - availability);
  const impact =
    scope === 'U'
      ? 6.42 * impactSubscore
      : 7.52 * (impactSubscore - 0.029) - 3.25 * Math.pow(impactSubscore - 0.02, 15);
  if (impact <= 0) return 0;
  const exploitability = 8.22 * av * ac * pr * ui;
  const baseScore =
    scope === 'U'
      ? Math.min(impact + exploitability, 10)
      : Math.min(1.08 * (impact + exploitability), 10);
  return roundUpCvss(baseScore);
}

function roundUpCvss(score) {
  return Math.ceil(score * 10 - 1e-10) / 10;
}

export function hasElectronBuilderProtocolScheme(configText, scheme) {
  const lines = configText.split(/\r?\n/);
  for (let index = 0; index < lines.length; index += 1) {
    const start = /^(\s*)protocols:\s*(?:#.*)?$/.exec(lines[index]);
    if (start === null || start[1] === undefined) continue;
    const protocolIndent = start[1].length;
    for (let lineIndex = index + 1; lineIndex < lines.length; lineIndex += 1) {
      const line = lines[lineIndex];
      if (line.trim() === '') continue;
      const indent = leadingSpaces(line);
      if (indent <= protocolIndent) break;
      const schemes = /^(\s*)schemes:\s*(.*?)(?:\s+#.*)?$/.exec(line);
      if (schemes === null || schemes[1] === undefined || schemes[2] === undefined) continue;
      if (flowListIncludes(schemes[2], scheme)) return true;
      const schemesIndent = schemes[1].length;
      for (let itemIndex = lineIndex + 1; itemIndex < lines.length; itemIndex += 1) {
        const itemLine = lines[itemIndex];
        if (itemLine.trim() === '') continue;
        const itemIndent = leadingSpaces(itemLine);
        if (itemIndent <= schemesIndent) break;
        const item = /^\s*-\s*["']?([^"'\s#]+)["']?\s*(?:#.*)?$/.exec(itemLine);
        if (item?.[1] === scheme) return true;
      }
    }
  }
  return false;
}

function leadingSpaces(line) {
  return /^\s*/.exec(line)?.[0].length ?? 0;
}

function flowListIncludes(rawValue, expected) {
  const trimmed = rawValue.trim();
  if (!trimmed.startsWith('[') || !trimmed.endsWith(']')) return false;
  return trimmed
    .slice(1, -1)
    .split(',')
    .map((item) => item.trim().replace(/^["']|["']$/g, ''))
    .includes(expected);
}

function trimOneLine(value) {
  return (
    String(value ?? '')
      .split(/\r?\n/)
      .map((line) => line.trim())
      .filter(Boolean)[0] ?? 'no detail'
  );
}

function writeLicenseInventory(inventory) {
  const summary = inventory.reduce((acc, pkg) => {
    const key = `${pkg.source}:${normalizeLicense(pkg.license) || 'UNKNOWN'}`;
    acc[key] = (acc[key] ?? 0) + 1;
    return acc;
  }, {});
  const report = {
    generatedAt: new Date().toISOString(),
    packageCount: inventory.length,
    summary,
    packages: inventory,
  };
  mkdirSync(distDir, { recursive: true });
  writeFileSync(inventoryPath, `${JSON.stringify(report, null, 2)}\n`);
  return report;
}

export function runReleaseAudit() {
  const failures = [];
  const warnings = [];
  const toolResults = [];

  requireFile(join(rootDir, 'LICENSE.txt'), 'project license', failures);
  requireFile(join(rootDir, 'NOTICE.txt'), 'project notice', failures);
  requireFile(join(rootDir, 'package-lock.json'), 'npm lockfile', failures);
  requireFile(join(rootDir, 'go.mod'), 'go module file', failures);
  requireFile(join(rootDir, 'go.sum'), 'go checksum file', failures);
  requireFile(exceptionsPath, 'release audit exception contract', failures);

  const exceptions = existsSync(exceptionsPath)
    ? loadJson(exceptionsPath, 'release audit exception contract')
    : {
        allowedLicenses: [],
        shippedDevDependencies: [],
        licenseExceptions: [],
        vulnerabilityExceptions: [],
      };
  if (exceptions.schemaVersion !== 1) {
    failures.push(
      `release-audit-exceptions.json schemaVersion=${exceptions.schemaVersion}, expected 1`,
    );
  }

  const builder = readFileSync(join(desktopDir, 'electron-builder.yml'), 'utf8');
  for (const required of [
    'appId: com.doordash.agentico',
    'hardenedRuntime: true',
    'LICENSE.txt',
    'NOTICE.txt',
    'publish: null',
  ]) {
    if (!builder.includes(required)) failures.push(`electron-builder.yml is missing ${required}`);
  }
  if (!hasElectronBuilderProtocolScheme(builder, 'agentico')) {
    failures.push('electron-builder.yml is missing protocol scheme agentico');
  }
  if (!/^asar:\s*true$/m.test(builder)) failures.push('electron-builder.yml must package app.asar');
  failures.push(...auditElectronBuilderFuseConfig(builder));

  const goreleaserConfig = readFileSync(join(rootDir, '.goreleaser.yaml'), 'utf8');
  failures.push(
    ...auditGoReleaserDesktopArtifacts(goreleaserConfig, RELEASE_AUDIT_DESKTOP_ARTIFACTS),
  );
  failures.push(...auditGoReleaserReleaseTarget(goreleaserConfig));
  failures.push(...auditReleaseMakefile(readFileSync(join(rootDir, 'Makefile'), 'utf8')));
  failures.push(
    ...auditReleaseRunner(readFileSync(join(desktopDir, 'scripts', 'release-run.mjs'), 'utf8')),
  );
  failures.push(
    ...auditReleaseWorkspace(
      readFileSync(join(desktopDir, 'scripts', 'release-workspace.mjs'), 'utf8'),
    ),
  );

  const goMod = readFileSync(join(rootDir, 'go.mod'), 'utf8');
  const goSum = readFileSync(join(rootDir, 'go.sum'), 'utf8');
  const lock = loadJson(join(rootDir, 'package-lock.json'), 'npm lockfile');
  const desktopPackage = loadJson(join(desktopDir, 'package.json'), 'desktop package manifest');
  const npmInventory = collectNpmRuntimeInventory(lock, desktopPackage, exceptions);
  failures.push(...auditNpmLockfile(lock, desktopPackage, npmInventory));

  const goRequirements = parseGoModRequirements(goMod);
  failures.push(...auditGoModuleIntegrity(goMod, goSum, goRequirements));
  const goInventory = collectGoLicenseInventory(goRequirements);
  const licenseInventory = [...npmInventory, ...goInventory].sort((a, b) =>
    `${a.source}:${a.name}`.localeCompare(`${b.source}:${b.name}`),
  );
  failures.push(...auditLicenseInventory(licenseInventory, exceptions));
  const licenseReport = writeLicenseInventory(licenseInventory);

  const npmAudit = auditNpmVulnerabilities(exceptions);
  toolResults.push(npmAudit.tool);
  failures.push(...npmAudit.failures);
  warnings.push(...npmAudit.warnings);

  const goAudit = auditGoVulnerabilities(exceptions);
  toolResults.push(goAudit.tool);
  failures.push(...goAudit.failures);
  warnings.push(...goAudit.warnings);

  mkdirSync(distDir, { recursive: true });
  const report = {
    checkedAt: new Date().toISOString(),
    npmRuntimePackageCount: npmInventory.length,
    goModuleCount: goRequirements.length,
    licenseInventoryPath: 'dist/third-party-license-inventory.json',
    licenseSummary: licenseReport.summary,
    tools: toolResults,
    warnings,
    failures,
  };
  writeFileSync(join(distDir, 'release-audit.json'), `${JSON.stringify(report, null, 2)}\n`);
  return report;
}

function main() {
  const report = runReleaseAudit();
  for (const warning of report.warnings) console.warn(`release audit warning: ${warning}`);
  if (report.failures.length > 0) {
    console.error(`release audit failed:\n- ${report.failures.join('\n- ')}`);
    process.exit(1);
  }
  console.log(
    `release audit passed (${report.npmRuntimePackageCount} npm runtime packages, ${report.goModuleCount} Go modules)`,
  );
}

if (process.argv[1] !== undefined && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main();
}
