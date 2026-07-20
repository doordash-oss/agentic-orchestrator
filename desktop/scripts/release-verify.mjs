import { existsSync, readFileSync, readdirSync, writeFileSync, mkdirSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { execFileSync } from 'node:child_process';

const desktopDir = dirname(dirname(fileURLToPath(import.meta.url)));
const distDir = join(desktopDir, 'dist');
const strict = process.env.AGENTICO_RELEASE_STRICT === '1' || process.env.GITHUB_REF_TYPE === 'tag';
const failures = [];
const warnings = [];

function run(script) {
  execFileSync(process.execPath, [join(desktopDir, 'scripts', script)], {
    cwd: desktopDir,
    stdio: 'inherit',
  });
}

run('audit-release.mjs');

if (strict) {
  run('release-credentials-check.mjs');
  const files = existsSync(distDir) ? readdirSync(distDir) : [];
  for (const suffix of ['.dmg', '.AppImage', '.deb', '.sha256', '.sig']) {
    if (!files.some((file) => file.endsWith(suffix))) {
      failures.push(`strict release verification requires a ${suffix} artifact in dist/`);
    }
  }
} else {
  warnings.push('native signing/notarization/GPG verification skipped outside protected tag mode');
}

const builder = readFileSync(join(desktopDir, 'electron-builder.yml'), 'utf8');
if (!builder.includes('hardenedRuntime: true')) failures.push('hardened runtime is not enabled');
if (!builder.includes('protocols:')) failures.push('agentico protocol registration is missing');

mkdirSync(distDir, { recursive: true });
writeFileSync(
  join(distDir, 'release-verification.json'),
  `${JSON.stringify(
    {
      checkedAt: new Date().toISOString(),
      strict,
      warnings,
      failures,
    },
    null,
    2,
  )}\n`,
);

if (failures.length > 0) {
  console.error(`release verification failed:\n- ${failures.join('\n- ')}`);
  process.exit(1);
}
for (const warning of warnings) console.warn(`release verification warning: ${warning}`);
console.log('release verification passed');
