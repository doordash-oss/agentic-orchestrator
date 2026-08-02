import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { describe, expect, it } from 'vitest';
import { selectRuntime } from '../gateway/wiring';

describe('selectRuntime', () => {
  it('uses the current runtime parent when only the legacy parent exists', () => {
    const homeDir = fs.mkdtempSync(path.join(os.tmpdir(), 'agentico-runtime-'));
    fs.mkdirSync(path.join(homeDir, '.agentic-workflow'));

    try {
      expect(selectRuntime(null, homeDir)).toStrictEqual({
        runtimeDir: path.join(homeDir, '.agentic-orchestrator'),
        stateDir: path.join(homeDir, '.agentic-orchestrator', 'features'),
        configPath: path.join(homeDir, '.agentic-orchestrator', 'config.yaml'),
      });
    } finally {
      fs.rmSync(homeDir, { recursive: true, force: true });
    }
  });

  it('preserves an explicit legacy-named runtime selection', () => {
    const homeDir = path.join(path.sep, 'home', 'agentico-user');

    expect(selectRuntime('~/.agentic-workflow', homeDir)).toStrictEqual({
      runtimeDir: path.join(homeDir, '.agentic-workflow'),
      stateDir: path.join(homeDir, '.agentic-workflow', 'features'),
      configPath: path.join(homeDir, '.agentic-workflow', 'config.yaml'),
    });
  });
});
