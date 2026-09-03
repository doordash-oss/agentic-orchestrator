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

// Regenerates the OpenAPI TypeScript types into a temp file and either writes
// them with the required license header or diffs them against the committed
// src/shared/api/schema.gen.ts. Tolerant by design of concurrent spec edits:
// it always regenerates from the current spec.
import { execFileSync } from 'node:child_process';
import { createRequire } from 'node:module';
import { readFileSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const desktopDir = dirname(dirname(fileURLToPath(import.meta.url)));
const spec = join(desktopDir, '..', 'api', 'openapi.yaml');
const committed = join(desktopDir, 'src', 'shared', 'api', 'schema.gen.ts');
const writeGenerated = process.argv.includes('--write');
const licenseHeader = `/*
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

`;

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
  const expected = licenseHeader + readFileSync(regenerated, 'utf8');
  if (writeGenerated) {
    writeFileSync(committed, expected);
    console.log('Generated desktop/src/shared/api/schema.gen.ts.');
    process.exit(0);
  }
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
