import { readFileSync } from 'node:fs';
import path from 'node:path';
import { describe, expect, it, vi } from 'vitest';
import {
  auditElectronBuilderFuseConfig,
  expectedElectronBuilderFuses,
  parseElectronBuilderFuses,
} from '../../scripts/lib/fuse-policy.mjs';
import {
  buildCsp,
  isAllowedNavigation,
  isSafeExternalUrl,
  mainWindowWebPreferences,
  openExternalSafely,
  permissionRequestPolicy,
  resolveWithinRoot,
  windowOpenPolicy,
} from '../../src/main/security';

describe('mainWindowWebPreferences', () => {
  it('locks down the renderer: sandbox, context isolation, no node integration', () => {
    const prefs = mainWindowWebPreferences('/app/out/preload/index.cjs');
    expect(prefs.sandbox).toBe(true);
    expect(prefs.contextIsolation).toBe(true);
    expect(prefs.nodeIntegration).toBe(false);
    expect(prefs.webSecurity).toBe(true);
    expect(prefs.allowRunningInsecureContent).toBe(false);
    expect(prefs.experimentalFeatures).toBe(false);
    expect(prefs.preload).toBe('/app/out/preload/index.cjs');
  });
});

describe('production Electron fuses', () => {
  it('keeps hardened fuse settings in electron-builder config', () => {
    const config = readFileSync(path.join(process.cwd(), 'electron-builder.yml'), 'utf8');
    expect(parseElectronBuilderFuses(config)).toEqual(expectedElectronBuilderFuses());
    expect(auditElectronBuilderFuseConfig(config)).toEqual([]);
  });
});

describe('buildCsp', () => {
  it('restricts every fetch directive to self and blocks eval and remote code', () => {
    const csp = buildCsp();
    expect(csp).toContain("default-src 'self'");
    expect(csp).toContain("script-src 'self'");
    expect(csp).toContain("object-src 'none'");
    expect(csp).toContain("base-uri 'none'");
    expect(csp).toContain("frame-src 'none'");
    expect(csp).not.toContain('unsafe-eval');
    expect(csp).not.toContain('http:');
    expect(csp).not.toContain('https:');
  });
});

describe('isAllowedNavigation', () => {
  const devOrigin = 'http://localhost:5173';

  it('allows navigation within the app origin only', () => {
    expect(isAllowedNavigation(`${devOrigin}/index.html`, devOrigin)).toBe(true);
    expect(isAllowedNavigation('file:///app/out/renderer/index.html', 'file://')).toBe(true);
  });

  it('denies external, scheme-swapped, and javascript navigations', () => {
    expect(isAllowedNavigation('https://evil.example.com', devOrigin)).toBe(false);
    expect(isAllowedNavigation('http://localhost:5174/x', devOrigin)).toBe(false);
    expect(isAllowedNavigation('javascript:alert(1)', devOrigin)).toBe(false);
    expect(isAllowedNavigation('file:///etc/passwd', devOrigin)).toBe(false);
    expect(isAllowedNavigation('not a url', devOrigin)).toBe(false);
  });
});

describe('windowOpenPolicy', () => {
  it('denies all window creation', () => {
    expect(windowOpenPolicy()).toEqual({ action: 'deny' });
  });
});

describe('permissionRequestPolicy', () => {
  it('denies every permission, including media and notifications', () => {
    for (const permission of ['media', 'notifications', 'geolocation', 'clipboard-read']) {
      expect(permissionRequestPolicy(permission)).toBe(false);
    }
  });
});

describe('isSafeExternalUrl', () => {
  it('allows only https URLs on allowlisted hosts', () => {
    expect(isSafeExternalUrl('https://github.com/doordash-oss/agentic-orchestrator')).toBe(true);
  });

  it('rejects http, unknown hosts, credentials, and dangerous schemes', () => {
    expect(isSafeExternalUrl('http://github.com/x')).toBe(false);
    expect(isSafeExternalUrl('https://evil.example.com/x')).toBe(false);
    expect(isSafeExternalUrl('https://user:pass@github.com/x')).toBe(false);
    expect(isSafeExternalUrl('javascript:alert(1)')).toBe(false);
    expect(isSafeExternalUrl('file:///etc/passwd')).toBe(false);
    expect(isSafeExternalUrl('https://github.com.evil.example.com/x')).toBe(false);
    expect(isSafeExternalUrl('garbage')).toBe(false);
  });
});

describe('openExternalSafely', () => {
  it('opens allowlisted URLs and reports acceptance', async () => {
    const openExternal = vi.fn(() => Promise.resolve());
    await expect(openExternalSafely('https://github.com/x', openExternal)).resolves.toBe(true);
    expect(openExternal).toHaveBeenCalledOnce();
    expect(openExternal).toHaveBeenCalledWith('https://github.com/x');
  });

  it('rejects non-allowlisted URLs without invoking the opener', async () => {
    const openExternal = vi.fn(() => Promise.resolve());
    await expect(openExternalSafely('https://evil.example.com/x', openExternal)).resolves.toBe(
      false,
    );
    expect(openExternal).not.toHaveBeenCalled();
  });

  it('propagates opener failures so callers can surface them', async () => {
    const openExternal = vi.fn(() => Promise.reject(new Error('boom')));
    await expect(openExternalSafely('https://github.com/x', openExternal)).rejects.toThrow('boom');
  });
});

describe('resolveWithinRoot', () => {
  it('resolves normal paths inside the root', () => {
    expect(resolveWithinRoot('/app/out/renderer', 'index.html')).toBe(
      '/app/out/renderer/index.html',
    );
    expect(resolveWithinRoot('/app/out/renderer', 'assets/font.woff2')).toBe(
      '/app/out/renderer/assets/font.woff2',
    );
  });

  it('rejects traversal, encoded traversal, and absolute escapes', () => {
    expect(resolveWithinRoot('/app/out/renderer', '../main/index.mjs')).toBeNull();
    expect(resolveWithinRoot('/app/out/renderer', '..%2f..%2fetc/passwd')).toBeNull();
    expect(resolveWithinRoot('/app/out/renderer', '%2e%2e/secret')).toBeNull();
    expect(resolveWithinRoot('/app/out/renderer', '/etc/passwd')).toBeNull();
    expect(resolveWithinRoot('/app/out/renderer', 'a/../../b')).toBeNull();
  });
});
