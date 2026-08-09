export interface ProductionFusePolicy {
  option: number;
  name: string;
  configKey: string;
  expected: boolean;
  expectedWireValue: number;
}

export const FUSE_WIRE_VALUES: Readonly<{
  enabled: number;
  disabled: number;
}>;

export const PRODUCTION_FUSE_POLICY: readonly ProductionFusePolicy[];

export function expectedElectronBuilderFuses(): Record<string, boolean>;

export function parseElectronBuilderFuses(configText: string): Record<string, boolean>;

export function auditElectronBuilderFuseConfig(configText: string): string[];

export function auditFuseWire(fuses: Record<number, number>): string[];
