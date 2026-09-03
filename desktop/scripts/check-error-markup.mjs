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

// Static guard for the canonical error-markup migration: the legacy error
// class families, hand-rolled alert roles, and deleted safe/wizard error
// identifiers must never come back. Run as part of `npm run check`; the
// exported function is unit-tested with fixture trees.
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { join } from 'node:path';

/**
 * Legacy class families deleted by the canonical error-surface migration.
 * A family string matches any class that contains it (BEM suffixes included);
 * classes that survived the migration on purpose (loading notices, quiet
 * state strips, per-row item state) are not listed.
 */
export const BANNED_CLASS_FAMILIES = [
  // Connection shell / readiness gate
  'shell-card__error',
  'shell-card__diagnostics',
  'shell-card__retry',
  // Setup wizard
  'setup-wizard__banner',
  'setup-wizard__error',
  'setup-wizard__code',
  'provider-row__issue',
  'provider-row__remedy',
  // Settings
  'settings-panel__error',
  'settings-panel__root-issue',
  'settings-panel__provider-cause',
  'settings-panel__provider-remedy',
  // Overview / cockpit / archive
  'overview-lanes__error',
  'cockpit__missing',
  'cockpit__run-switcher-error',
  'archive-mode__error',
  // Creation sheet
  'creation-sheet__alert',
  'creation-sheet__retry',
  'creation-sheet__field-error',
  // Config editor error notice (the loading notice survives)
  'config-editor__notice--error',
  // Shared field-error family
  'form-field__error',
  // Completion
  'completion-workspace__error',
  'completion-workspace__result--failure',
  'completion-workspace__repo-outcome',
  'completion-publish-sheet__field-error',
  // Review feedback recovery card family
  'create-form__error',
  'review-feedback-workspace__error',
  'review-feedback-recovery',
  // Rewind
  'rewind-journey__error',
  'rewind-journey__findings',
  // Recovery scan error
  'recovery-attention',
  'recovery-workspace__error-actions',
  // Error-specific families that have no remaining production owner
  'aftercare-workspace__action-error',
  'composer__notice',
  'refactor-pass__notice',
];

/** Deleted identifiers that must not reappear anywhere under desktop/src. */
export const BANNED_IDENTIFIERS = [
  'SafeErrorException',
  'SafeErrorSchema',
  'toSafeError',
  'toEnvelopeError',
  'WizardError',
  'canonicalFromWizardError',
  'addServerErrorTitle',
];

const PRODUCTION_EXTENSIONS = /\.(ts|tsx|mjs|css)$/;

function listFiles(dir) {
  const out = [];
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry);
    if (statSync(full).isDirectory()) {
      out.push(...listFiles(full));
    } else if (PRODUCTION_EXTENSIONS.test(entry)) {
      out.push(full);
    }
  }
  return out;
}

function isTestFile(path) {
  return /\.(test|spec)\.[jt]sx?$/.test(path) || path.includes('__tests__');
}

function lineOf(contents, needle) {
  const index = contents.findIndex(needle);
  return index < 0 ? -1 : index + 1;
}

/**
 * Scans one tree for violations. Returns one violation per file per rule
 * (with the first offending line), which keeps the report readable.
 */
/** @param {{rendererDir: string, sourceDir: string, stylesheet: string, errorSurfacePath: string}} options */
export function checkErrorMarkup(options) {
  const violations = [];
  const rendererFiles = listFiles(options.rendererDir).filter(
    (path) => !isTestFile(path) && path !== options.errorSurfacePath && path !== options.stylesheet,
  );

  for (const file of rendererFiles) {
    const contents = readFileSync(file, 'utf8').split('\n');
    for (const family of BANNED_CLASS_FAMILIES) {
      const line = lineOf(contents, (text) => text.includes(family));
      if (line >= 0) {
        violations.push({
          file,
          line,
          rule: `legacy error class family "${family}"`,
          snippet: contents[line - 1].trim(),
          fix: 'render the error through ErrorSurface (or FieldError for per-field validation) and delete the class',
        });
      }
    }
    const alertLine = lineOf(
      contents,
      (text) => /role=["']alert["']/.test(text) || /role=\{["']alert["']\}/.test(text),
    );
    if (alertLine >= 0) {
      violations.push({
        file,
        line: alertLine,
        rule: 'hand-rolled role="alert"',
        snippet: contents[alertLine - 1].trim(),
        fix: 'role="alert" belongs to ErrorSurface alone; render the canonical error through ErrorSurface',
      });
    }
  }

  const stylesheetContents = readFileSync(options.stylesheet, 'utf8').split('\n');
  for (const family of BANNED_CLASS_FAMILIES) {
    const line = lineOf(stylesheetContents, (text) => text.includes(`.${family}`));
    if (line >= 0) {
      violations.push({
        file: options.stylesheet,
        line,
        rule: `stylesheet still declares the deleted "${family}" family`,
        snippet: stylesheetContents[line - 1].trim(),
        fix: 'delete the rule; the surface that used it renders through ErrorSurface now',
      });
    }
  }

  for (const file of listFiles(options.sourceDir)) {
    const contents = readFileSync(file, 'utf8').split('\n');
    for (const identifier of BANNED_IDENTIFIERS) {
      const line = lineOf(contents, (text) => new RegExp(`\\b${identifier}\\b`).test(text));
      if (line >= 0) {
        violations.push({
          file,
          line,
          rule: `deleted identifier "${identifier}"`,
          snippet: contents[line - 1].trim(),
          fix: 'use CanonicalError/parseIpcError/buildCanonicalError; the safe-error and wizard-error shapes are gone',
        });
      }
    }
  }

  for (const file of listFiles(join(options.sourceDir, 'main')).filter(
    (path) => !isTestFile(path),
  )) {
    const source = readFileSync(file, 'utf8');
    const match = /params\s*:\s*\{\s*reason(?:\s*:|\s*[,}])/m.exec(source);
    if (match !== null) {
      const line = source.slice(0, match.index).split('\n').length;
      violations.push({
        file,
        line,
        rule: 'user-facing reason authored outside the desktop error catalog',
        snippet: source.split('\n')[line - 1].trim(),
        fix: 'give the condition its own E_ code and author its title, summary, and hint in shared/errors.ts; caught exception text belongs in diagnostics',
      });
    }
  }

  return violations;
}

function main() {
  const desktopDir = process.cwd();
  const violations = checkErrorMarkup({
    rendererDir: join(desktopDir, 'src', 'renderer', 'src'),
    sourceDir: join(desktopDir, 'src'),
    stylesheet: join(desktopDir, 'src', 'renderer', 'src', 'styles', 'app.css'),
    errorSurfacePath: join(desktopDir, 'src', 'renderer', 'src', 'components', 'ErrorSurface.tsx'),
  });
  if (violations.length > 0) {
    console.error(
      'Legacy error markup found — the canonical ErrorSurface owns every error presentation:',
    );
    for (const violation of violations) {
      console.error(
        `  ${violation.file}:${violation.line}: ${violation.rule}\n` +
          `    ${violation.snippet}\n` +
          `    fix: ${violation.fix}`,
      );
    }
    process.exit(1);
  }
  console.log('No legacy error markup: every error renders through the canonical primitives.');
}

if (process.argv[1] && process.argv[1].endsWith('check-error-markup.mjs')) {
  main();
}
