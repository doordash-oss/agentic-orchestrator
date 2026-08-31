// Regenerates the OpenAPI TypeScript types into a temp file and diffs them
// against the committed src/shared/api/schema.gen.ts. Tolerant by design of
// concurrent spec edits: it always regenerates from the current spec, so the
// fix for a failure is simply `npm run generate:api` and commit.
import { execFileSync } from 'node:child_process';
import { createRequire } from 'node:module';
import { readFileSync, mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const desktopDir = dirname(dirname(fileURLToPath(import.meta.url)));
const spec = join(desktopDir, '..', 'api', 'openapi.yaml');
const committed = join(desktopDir, 'src', 'shared', 'api', 'schema.gen.ts');

// Resolve the openapi-typescript CLI wherever npm hoisted it.
const require = createRequire(import.meta.url);
const pkgDir = dirname(require.resolve('openapi-typescript/package.json'));
const cli = join(pkgDir, 'bin', 'cli.js');

const tempDir = mkdtempSync(join(tmpdir(), 'agentico-api-drift-'));
const regenerated = join(tempDir, 'schema.gen.ts');

try {
  execFileSync(process.execPath, [cli, spec, '-o', regenerated], {
    stdio: ['ignore', 'ignore', 'inherit'],
  });
  const expected = readFileSync(regenerated, 'utf8');
  const actual = readFileSync(committed, 'utf8');
  if (expected !== actual) {
    console.error(
      'API type drift: desktop/src/shared/api/schema.gen.ts is out of date with api/openapi.yaml.\n' +
        'Run `npm run generate:api --workspace desktop` and commit the result.',
    );
    process.exit(1);
  }
  console.log('API types are in sync with api/openapi.yaml.');
} finally {
  rmSync(tempDir, { recursive: true, force: true });
}
