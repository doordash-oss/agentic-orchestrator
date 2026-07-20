import { describe, expect, it } from 'vitest';

import {
  WORKLOAD_NAMES,
  electronExecutableForPlatform,
  failuresFor,
  memorySamplesSettled,
  packagedAsarLaunchConfig,
  packagedResourcesDirForExecutable,
  performanceBudgetsForPlatform,
} from './performance-check.mjs';

describe('performance-check workload contract', () => {
  it('covers every contracted native workload by name', () => {
    expect(WORKLOAD_NAMES).toEqual([
      'cold-shell-readiness',
      'authoritative-dashboard-render',
      'maximum-bounded-transcript-append-render',
      'repeated-tab-and-session-changes',
      'reconnect-storms',
      'first-monaco-lazy-load',
      'post-stress-process-memory',
    ]);
  });

  it('loads checked-in baselines with a budget for every workload', () => {
    const budgets = performanceBudgetsForPlatform(
      {
        schemaVersion: 1,
        regressionTolerance: 1.2,
        platforms: {
          default: Object.fromEntries(
            WORKLOAD_NAMES.map((name) => [
              name,
              name === 'post-stress-process-memory'
                ? { baselineBytes: 1, ceilingBytes: 2 }
                : { baselineMs: 1, ceilingMs: 2 },
            ]),
          ),
        },
      },
      'test-platform',
    );

    expect(Object.keys(budgets).sort()).toEqual([...WORKLOAD_NAMES].sort());
  });

  it('fails when a workload exceeds the 20 percent regression budget', () => {
    expect(
      failuresFor([
        {
          name: 'cold-shell-readiness',
          unit: 'ms',
          median: 121,
          samples: [121],
          baseline: 100,
          ceiling: 200,
          regressionLimit: 120,
        },
      ]),
    ).toEqual(['cold-shell-readiness: 121.0 ms > 20% regression budget 120.0 ms']);
  });

  it('fails when post-stress process memory exceeds its ceiling', () => {
    expect(
      failuresFor([
        {
          name: 'post-stress-process-memory',
          unit: 'bytes',
          median: 315 * 1024 * 1024,
          samples: [315 * 1024 * 1024],
          baseline: 150 * 1024 * 1024,
          ceiling: 300 * 1024 * 1024,
          regressionLimit: 180 * 1024 * 1024,
        },
      ]),
    ).toEqual(
      expect.arrayContaining([
        'post-stress-process-memory: 315 MiB > ceiling 300 MiB',
        'post-stress-process-memory: 315 MiB > 20% regression budget 180 MiB',
      ]),
    );
  });

  it('fails when post-stress process memory does not settle', () => {
    expect(memorySamplesSettled([100, 103, 106], 1)).toBe(false);
    expect(memorySamplesSettled([100, 103, 104], 1)).toBe(true);
    expect(
      failuresFor([
        {
          name: 'post-stress-process-memory',
          unit: 'bytes',
          median: 106,
          samples: [100, 103, 106],
          baseline: 100,
          ceiling: 200,
          regressionLimit: 120,
          settled: false,
        },
      ]),
    ).toContain('post-stress-process-memory: RSS samples did not settle after stress');
  });

  it('derives packaged ASAR launch paths from the verified unpacked executable', () => {
    const mac = packagedAsarLaunchConfig(
      {
        unpacked_app: '/tmp/Agentico.app/Contents/MacOS/Agentico',
      },
      'darwin',
    );
    expect(mac.resourcesPath).toBe('/tmp/Agentico.app/Contents/Resources');
    expect(mac.appAsar).toBe('/tmp/Agentico.app/Contents/Resources/app.asar');
    expect(mac.electronExecutable).toMatch(
      /node_modules\/electron\/dist\/Electron\.app\/Contents\/MacOS\/Electron$/,
    );

    expect(packagedResourcesDirForExecutable('/tmp/linux-unpacked/agentico', 'linux')).toBe(
      '/tmp/linux-unpacked/resources',
    );
    expect(electronExecutableForPlatform('/repo', 'win32')).toBe(
      '/repo/node_modules/electron/dist/electron.exe',
    );
  });
});
