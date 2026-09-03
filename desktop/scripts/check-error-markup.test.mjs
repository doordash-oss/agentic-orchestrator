import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { describe, expect, it } from 'vitest';

import { checkErrorMarkup } from './check-error-markup.mjs';

function writeTree(files) {
  const root = mkdtempSync(join(tmpdir(), 'agentico-error-markup-'));
  const rendererDir = join(root, 'renderer');
  const sourceDir = join(root, 'src');
  const errorSurfacePath = join(rendererDir, 'components', 'ErrorSurface.tsx');
  const defaults = {
    'renderer/components/ErrorSurface.tsx': '<div role="alert">canonical surface</div>\n',
    'renderer/components/Good.tsx': '<div className="error-surface" />\n',
    'renderer/styles/app.css': '.error-surface { color: var(--bad); }\n',
    'src/shared/ipc.ts': 'export const ok = true;\n',
    'src/main/good.ts': 'export const ok = true;\n',
  };
  for (const [path, contents] of Object.entries({ ...defaults, ...files })) {
    const full = join(root, path);
    mkdirSync(dirname(full), { recursive: true });
    writeFileSync(full, contents);
  }
  return {
    root,
    rendererDir,
    sourceDir,
    stylesheet: join(rendererDir, 'styles', 'app.css'),
    errorSurfacePath,
  };
}

function optionsFor(tree) {
  return {
    rendererDir: tree.rendererDir,
    sourceDir: tree.sourceDir,
    stylesheet: tree.stylesheet,
    errorSurfacePath: tree.errorSurfacePath,
  };
}

describe('error-markup static check', () => {
  it('passes a clean tree whose only alert role is ErrorSurface itself', () => {
    const tree = writeTree({
      'renderer/styles/app.css': '.error-surface { color: var(--bad); }\n',
    });
    try {
      expect(checkErrorMarkup(optionsFor(tree))).toEqual([]);
    } finally {
      rmSync(tree.root, { recursive: true, force: true });
    }
  });

  it('fails a fixture carrying a legacy class name, reporting the file and line', () => {
    const tree = writeTree({
      'renderer/components/Legacy.tsx':
        'const a = 1;\nconst b = <p className="shell-card__error-message">boom</p>;\n',
    });
    try {
      const violations = checkErrorMarkup(optionsFor(tree));
      expect(violations).toHaveLength(1);
      expect(violations[0]).toMatchObject({
        file: join(tree.rendererDir, 'components', 'Legacy.tsx'),
        line: 2,
        rule: 'legacy error class family "shell-card__error"',
      });
      expect(violations[0].snippet).toContain('shell-card__error-message');
    } finally {
      rmSync(tree.root, { recursive: true, force: true });
    }
  });

  it('fails a fixture with role="alert" outside ErrorSurface, and skips test files', () => {
    const tree = writeTree({
      'renderer/components/HandRolled.tsx': '<p role="alert">hand-rolled</p>\n',
      'renderer/components/HandRolled.test.tsx': '<p role="alert">test fixture is exempt</p>\n',
    });
    try {
      const violations = checkErrorMarkup(optionsFor(tree));
      expect(violations).toHaveLength(1);
      expect(violations[0]).toMatchObject({
        file: join(tree.rendererDir, 'components', 'HandRolled.tsx'),
        line: 1,
        rule: 'hand-rolled role="alert"',
      });
    } finally {
      rmSync(tree.root, { recursive: true, force: true });
    }
  });

  it('fails when the stylesheet still declares a deleted family selector', () => {
    const tree = writeTree({
      'renderer/styles/app.css': '.cool { color: red; }\n.form-field__error { color: red; }\n',
    });
    try {
      const violations = checkErrorMarkup(optionsFor(tree));
      expect(violations).toHaveLength(1);
      expect(violations[0]).toMatchObject({
        file: tree.stylesheet,
        line: 2,
        rule: 'stylesheet still declares the deleted "form-field__error" family',
      });
    } finally {
      rmSync(tree.root, { recursive: true, force: true });
    }
  });

  it('fails when a deleted identifier appears anywhere under desktop source', () => {
    const tree = writeTree({
      'src/main/legacy.ts': 'import { SafeErrorSchema } from "./gone";\n',
    });
    try {
      const violations = checkErrorMarkup(optionsFor(tree));
      expect(violations).toHaveLength(1);
      expect(violations[0]).toMatchObject({
        file: join(tree.sourceDir, 'main', 'legacy.ts'),
        line: 1,
        rule: 'deleted identifier "SafeErrorSchema"',
      });
    } finally {
      rmSync(tree.root, { recursive: true, force: true });
    }
  });

  it('fails when main passes a literal reason instead of a catalog-owned code', () => {
    const tree = writeTree({
      'src/main/bad.ts':
        "buildCanonicalError('E_INTERNAL', { params: { reason: 'hand-authored prose' } });\n",
    });
    try {
      const violations = checkErrorMarkup(optionsFor(tree));
      expect(violations).toHaveLength(1);
      expect(violations[0]).toMatchObject({
        file: join(tree.sourceDir, 'main', 'bad.ts'),
        line: 1,
        rule: 'user-facing reason authored outside the desktop error catalog',
      });
    } finally {
      rmSync(tree.root, { recursive: true, force: true });
    }
  });

  it('fails when main forwards a string variable through a reason parameter', () => {
    const tree = writeTree({
      'src/main/bad.ts':
        "function fail(reason: string) { return buildCanonicalError('E_INTERNAL', { params: { reason } }); }\n",
    });
    try {
      const violations = checkErrorMarkup(optionsFor(tree));
      expect(violations).toHaveLength(1);
      expect(violations[0]).toMatchObject({
        file: join(tree.sourceDir, 'main', 'bad.ts'),
        line: 1,
        rule: 'user-facing reason authored outside the desktop error catalog',
      });
    } finally {
      rmSync(tree.root, { recursive: true, force: true });
    }
  });
});
