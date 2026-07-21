import { FuseV1Options } from '@electron/fuses';

export const FUSE_WIRE_VALUES = Object.freeze({
  enabled: 49,
  disabled: 48,
});

export const PRODUCTION_FUSE_POLICY = Object.freeze([
  Object.freeze({
    option: FuseV1Options.RunAsNode,
    name: 'RunAsNode',
    configKey: 'runAsNode',
    expected: false,
    expectedWireValue: FUSE_WIRE_VALUES.disabled,
  }),
  Object.freeze({
    option: FuseV1Options.EnableCookieEncryption,
    name: 'EnableCookieEncryption',
    configKey: 'enableCookieEncryption',
    expected: true,
    expectedWireValue: FUSE_WIRE_VALUES.enabled,
  }),
  Object.freeze({
    option: FuseV1Options.EnableNodeOptionsEnvironmentVariable,
    name: 'EnableNodeOptionsEnvironmentVariable',
    configKey: 'enableNodeOptionsEnvironmentVariable',
    expected: false,
    expectedWireValue: FUSE_WIRE_VALUES.disabled,
  }),
  Object.freeze({
    option: FuseV1Options.EnableNodeCliInspectArguments,
    name: 'EnableNodeCliInspectArguments',
    configKey: 'enableNodeCliInspectArguments',
    expected: false,
    expectedWireValue: FUSE_WIRE_VALUES.disabled,
  }),
  Object.freeze({
    option: FuseV1Options.EnableEmbeddedAsarIntegrityValidation,
    name: 'EnableEmbeddedAsarIntegrityValidation',
    configKey: 'enableEmbeddedAsarIntegrityValidation',
    expected: true,
    expectedWireValue: FUSE_WIRE_VALUES.enabled,
  }),
  Object.freeze({
    option: FuseV1Options.OnlyLoadAppFromAsar,
    name: 'OnlyLoadAppFromAsar',
    configKey: 'onlyLoadAppFromAsar',
    expected: true,
    expectedWireValue: FUSE_WIRE_VALUES.enabled,
  }),
  Object.freeze({
    option: FuseV1Options.LoadBrowserProcessSpecificV8Snapshot,
    name: 'LoadBrowserProcessSpecificV8Snapshot',
    configKey: 'loadBrowserProcessSpecificV8Snapshot',
    // Electron's standard packages carry architecture-specific context
    // snapshots, not browser_v8_context_snapshot.bin. Enabling this fuse
    // without that custom artifact makes the browser process fatal at launch.
    expected: false,
    expectedWireValue: FUSE_WIRE_VALUES.disabled,
  }),
  Object.freeze({
    option: FuseV1Options.GrantFileProtocolExtraPrivileges,
    name: 'GrantFileProtocolExtraPrivileges',
    configKey: 'grantFileProtocolExtraPrivileges',
    expected: false,
    expectedWireValue: FUSE_WIRE_VALUES.disabled,
  }),
]);

export function expectedElectronBuilderFuses() {
  return Object.fromEntries(PRODUCTION_FUSE_POLICY.map((fuse) => [fuse.configKey, fuse.expected]));
}

export function parseElectronBuilderFuses(configText) {
  const fuses = {};
  const lines = configText.split(/\r?\n/);
  const start = lines.findIndex((line) => line.trim() === 'electronFuses:');
  if (start < 0) {
    return fuses;
  }
  for (let index = start + 1; index < lines.length; index += 1) {
    const line = lines[index];
    if (/^\S/.test(line) && line.trim() !== '') {
      break;
    }
    const match = /^\s{2}([A-Za-z0-9]+):\s*(true|false)\s*(?:#.*)?$/.exec(line);
    if (match !== null) {
      fuses[match[1]] = match[2] === 'true';
    }
  }
  return fuses;
}

export function auditElectronBuilderFuseConfig(configText) {
  const fuses = parseElectronBuilderFuses(configText);
  const failures = [];
  for (const fuse of PRODUCTION_FUSE_POLICY) {
    if (!(fuse.configKey in fuses)) {
      failures.push(`electron-builder.yml missing electronFuses.${fuse.configKey}`);
    } else if (fuses[fuse.configKey] !== fuse.expected) {
      failures.push(
        `electron-builder.yml electronFuses.${fuse.configKey}=${fuses[fuse.configKey]}, expected ${fuse.expected}`,
      );
    }
  }
  return failures;
}

export function auditFuseWire(fuses) {
  const mismatches = [];
  for (const fuse of PRODUCTION_FUSE_POLICY) {
    if (fuses[fuse.option] !== fuse.expectedWireValue) {
      mismatches.push(
        `${fuse.name}: expected ${fuse.expectedWireValue}, got ${fuses[fuse.option]}`,
      );
    }
  }
  return mismatches;
}
