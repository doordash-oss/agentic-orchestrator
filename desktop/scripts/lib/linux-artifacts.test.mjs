import { existsSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';

import { normalizeLinuxAppImage } from './linux-artifacts.mjs';

const cleanup = [];

afterEach(async () => {
  const { rmSync } = await import('node:fs');
  for (const dir of cleanup.splice(0)) rmSync(dir, { recursive: true, force: true });
});

describe('normalizeLinuxAppImage', () => {
  it('renames electron-builder x86_64 output to the x64 release contract name', () => {
    const distDir = mkdtempSync(join(tmpdir(), 'agentico-linux-artifacts-'));
    cleanup.push(distDir);
    writeFileSync(join(distDir, 'Agentico-x86_64.AppImage'), 'package');

    const result = normalizeLinuxAppImage(distDir, 'x64');

    expect(result).toBe(join(distDir, 'Agentico-x64.AppImage'));
    expect(readFileSync(result, 'utf8')).toBe('package');
  });

  it('leaves electron-builder arm64 output at its canonical release name', () => {
    const distDir = mkdtempSync(join(tmpdir(), 'agentico-linux-artifacts-'));
    cleanup.push(distDir);
    writeFileSync(join(distDir, 'Agentico-arm64.AppImage'), 'package');

    expect(normalizeLinuxAppImage(distDir, 'arm64')).toBe(join(distDir, 'Agentico-arm64.AppImage'));
  });

  it('refuses to overwrite an existing x64 release artifact', () => {
    const distDir = mkdtempSync(join(tmpdir(), 'agentico-linux-artifacts-'));
    cleanup.push(distDir);
    const source = join(distDir, 'Agentico-x86_64.AppImage');
    const destination = join(distDir, 'Agentico-x64.AppImage');
    writeFileSync(source, 'new package');
    writeFileSync(destination, 'existing release package');

    expect(() => normalizeLinuxAppImage(distDir, 'x64')).toThrow(/refusing to overwrite/);
    expect(readFileSync(source, 'utf8')).toBe('new package');
    expect(readFileSync(destination, 'utf8')).toBe('existing release package');
    expect(existsSync(source)).toBe(true);
  });
});
