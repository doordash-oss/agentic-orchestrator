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

/**
 * Local-only diagnostics retained by the Electron main process. The renderer
 * receives bounded, already-redacted records; it never receives paths, dumps,
 * transcripts, prompts, environment values, or arbitrary filesystem handles.
 */
import fs from 'node:fs';
import path from 'node:path';
import { redactText } from '../shared/errors';
import type {
  CrashMetadata,
  DiagnosticEntry,
  DiagnosticLevel,
  DiagnosticSource,
  DiagnosticsRetention,
  DiagnosticsSnapshot,
} from '../shared/ipc';

export const DIAGNOSTICS_RETENTION = Object.freeze({
  maxBytes: 25 * 1024 * 1024,
  maxAgeDays: 7,
  maxCrashRecords: 10,
});

const EVENTS_FILE = 'events.jsonl';
const CRASHES_FILE = 'crashes.json';
const MAX_ENTRY_COUNT = 200;
const MAX_MESSAGE_CHARS = 700;
const MAX_DETAIL_CHARS = 1200;

export interface DiagnosticsServiceOptions {
  userDataDir: string;
  now?: () => Date;
  platform?: NodeJS.Platform;
  arch?: string;
  version: string;
  revision?: string;
  readServerLines?: () => readonly string[];
}

export class DiagnosticsService {
  private readonly root: string;
  private readonly now: () => Date;
  private readonly platform: NodeJS.Platform;
  private readonly arch: string;
  private readonly version: string;
  private readonly revision?: string;
  private readonly readServerLines: () => readonly string[];
  private sequence = 0;

  constructor(options: DiagnosticsServiceOptions) {
    this.root = path.join(options.userDataDir, 'diagnostics');
    this.now = options.now ?? (() => new Date());
    this.platform = options.platform ?? process.platform;
    this.arch = options.arch ?? process.arch;
    this.version = options.version;
    this.revision = options.revision;
    this.readServerLines = options.readServerLines ?? (() => []);
    this.ensureRoot();
    this.prune();
  }

  rootPath(): string {
    return this.root;
  }

  record(
    source: DiagnosticSource,
    level: DiagnosticLevel,
    message: string,
    detail?: string,
  ): DiagnosticEntry {
    const entry: DiagnosticEntry = {
      id: this.nextId('evt'),
      time: this.now().toISOString(),
      source,
      level,
      message: truncate(redactText(message), MAX_MESSAGE_CHARS),
      ...(detail === undefined || detail.trim() === ''
        ? {}
        : { detail: truncate(redactText(detail), MAX_DETAIL_CHARS) }),
    };
    this.ensureRoot();
    fs.appendFileSync(this.eventsPath(), `${JSON.stringify(entry)}\n`, { mode: 0o600 });
    return entry;
  }

  recordCrash(input: {
    processRole: CrashMetadata['processRole'];
    category: string;
    context?: string;
  }): CrashMetadata {
    const crash: CrashMetadata = {
      id: this.nextId('crash'),
      time: this.now().toISOString(),
      version: this.version,
      ...(this.revision === undefined ? {} : { revision: this.revision }),
      platform: this.platform,
      architecture: this.arch,
      processRole: input.processRole,
      category: truncate(redactText(input.category), 80),
      ...(input.context === undefined || input.context.trim() === ''
        ? {}
        : { context: truncate(redactText(input.context), 700) }),
    };
    this.ensureRoot();
    const crashes = [crash, ...this.readCrashes()].slice(0, DIAGNOSTICS_RETENTION.maxCrashRecords);
    this.writeJsonAtomic(this.crashesPath(), crashes);
    this.record('crash', 'error', `${crash.processRole} crash recorded`, crash.category);
    this.prune();
    return crash;
  }

  snapshot(): DiagnosticsSnapshot {
    this.prune();
    const persisted = this.readEntries();
    const serverEntries = this.serverEntries();
    const entries = [...serverEntries, ...persisted]
      .sort((a, b) => Date.parse(b.time) - Date.parse(a.time))
      .slice(0, MAX_ENTRY_COUNT);
    const crashes = this.readCrashes().slice(0, DIAGNOSTICS_RETENTION.maxCrashRecords);
    return {
      retention: this.retention(entries.length, crashes.length),
      entries,
      crashes,
    };
  }

  clear(): DiagnosticsSnapshot {
    fs.rmSync(this.root, { recursive: true, force: true });
    this.ensureRoot();
    return this.snapshot();
  }

  private eventsPath(): string {
    return path.join(this.root, EVENTS_FILE);
  }

  private crashesPath(): string {
    return path.join(this.root, CRASHES_FILE);
  }

  private ensureRoot(): void {
    fs.mkdirSync(this.root, { recursive: true, mode: 0o700 });
    fs.chmodSync(this.root, 0o700);
  }

  private readEntries(): DiagnosticEntry[] {
    try {
      const raw = fs.readFileSync(this.eventsPath(), 'utf8');
      return raw
        .split('\n')
        .filter((line) => line.trim() !== '')
        .map((line) => JSON.parse(line) as DiagnosticEntry)
        .filter(isDiagnosticEntry)
        .slice(-MAX_ENTRY_COUNT);
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== 'ENOENT') {
        this.quarantine(EVENTS_FILE);
      }
      return [];
    }
  }

  private readCrashes(): CrashMetadata[] {
    try {
      const parsed = JSON.parse(fs.readFileSync(this.crashesPath(), 'utf8')) as unknown;
      return Array.isArray(parsed) ? parsed.filter(isCrashMetadata) : [];
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== 'ENOENT') {
        this.quarantine(CRASHES_FILE);
      }
      return [];
    }
  }

  private serverEntries(): DiagnosticEntry[] {
    const lines = this.readServerLines()
      .map((line) => line.trim())
      .filter((line) => line !== '')
      .slice(-20);
    const now = this.now().toISOString();
    return lines.map((line, index) => ({
      id: `server-${index}`,
      time: now,
      source: 'server' as const,
      level: inferLevel(line),
      message: truncate(redactText(line), MAX_MESSAGE_CHARS),
    }));
  }

  private prune(): void {
    this.ensureRoot();
    const cutoff = this.now().getTime() - DIAGNOSTICS_RETENTION.maxAgeDays * 24 * 60 * 60 * 1000;
    const entries = this.readEntries()
      .filter((entry) => Date.parse(entry.time) >= cutoff)
      .slice(-MAX_ENTRY_COUNT);
    this.writeEvents(entries);
    const crashes = this.readCrashes()
      .filter((crash) => Date.parse(crash.time) >= cutoff)
      .slice(0, DIAGNOSTICS_RETENTION.maxCrashRecords);
    this.writeJsonAtomic(this.crashesPath(), crashes);
    this.pruneBySize();
  }

  private pruneBySize(): void {
    let entries = this.readEntries();
    while (this.currentBytes() > DIAGNOSTICS_RETENTION.maxBytes && entries.length > 0) {
      entries = entries.slice(1);
      this.writeEvents(entries);
    }
  }

  private writeEvents(entries: readonly DiagnosticEntry[]): void {
    const text = entries.map((entry) => JSON.stringify(entry)).join('\n');
    this.writeTextAtomic(this.eventsPath(), text === '' ? '' : `${text}\n`);
  }

  private writeTextAtomic(filePath: string, value: string): void {
    this.ensureRoot();
    const tmp = `${filePath}.${process.pid}.${Date.now()}.tmp`;
    fs.writeFileSync(tmp, value, { mode: 0o600 });
    fs.renameSync(tmp, filePath);
    fs.chmodSync(filePath, 0o600);
  }

  private writeJsonAtomic(filePath: string, value: unknown): void {
    this.ensureRoot();
    const tmp = `${filePath}.${process.pid}.${Date.now()}.tmp`;
    fs.writeFileSync(tmp, `${JSON.stringify(value, null, 2)}\n`, { mode: 0o600 });
    fs.renameSync(tmp, filePath);
    fs.chmodSync(filePath, 0o600);
  }

  private retention(entryCount: number, crashCount: number): DiagnosticsRetention {
    return {
      ...DIAGNOSTICS_RETENTION,
      currentBytes: this.currentBytes(),
      entryCount,
      crashCount,
    };
  }

  private currentBytes(): number {
    try {
      return fs
        .readdirSync(this.root)
        .reduce((sum, name) => sum + fs.statSync(path.join(this.root, name)).size, 0);
    } catch {
      return 0;
    }
  }

  private quarantine(fileName: string): void {
    const source = path.join(this.root, fileName);
    const target = path.join(this.root, `${fileName}.bad-${Date.now()}`);
    try {
      fs.renameSync(source, target);
    } catch {
      // Best-effort protection against corrupt diagnostics; never block app use.
    }
  }

  private nextId(prefix: string): string {
    this.sequence += 1;
    return `${prefix}-${this.now().getTime().toString(36)}-${this.sequence.toString(36)}`;
  }
}

function truncate(value: string, max: number): string {
  return value.length <= max ? value : `${value.slice(0, max - 1)}...`;
}

function inferLevel(line: string): DiagnosticLevel {
  const lower = line.toLowerCase();
  if (lower.includes('error') || lower.includes('failed') || lower.includes('crash'))
    return 'error';
  if (lower.includes('warn') || lower.includes('retry')) return 'warn';
  return 'info';
}

function isDiagnosticEntry(value: unknown): value is DiagnosticEntry {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) return false;
  const v = value as Record<string, unknown>;
  return (
    isBoundedString(v.id, 1, 80) &&
    isIsoTime(v.time) &&
    ['electron', 'server', 'update', 'crash'].includes(String(v.source)) &&
    ['info', 'warn', 'error'].includes(String(v.level)) &&
    isBoundedString(v.message, 1, MAX_MESSAGE_CHARS) &&
    (v.detail === undefined || isBoundedString(v.detail, 0, MAX_DETAIL_CHARS))
  );
}

function isCrashMetadata(value: unknown): value is CrashMetadata {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) return false;
  const v = value as Record<string, unknown>;
  return (
    isBoundedString(v.id, 1, 80) &&
    isIsoTime(v.time) &&
    isBoundedString(v.version, 1, 80) &&
    (v.revision === undefined || isBoundedString(v.revision, 1, 80)) &&
    isBoundedString(v.platform, 1, 40) &&
    isBoundedString(v.architecture, 1, 40) &&
    ['main', 'renderer', 'server', 'utility'].includes(String(v.processRole)) &&
    isBoundedString(v.category, 1, 80) &&
    (v.context === undefined || isBoundedString(v.context, 0, 700))
  );
}

function isIsoTime(value: unknown): value is string {
  return typeof value === 'string' && value.length <= 40 && Number.isFinite(Date.parse(value));
}

function isBoundedString(value: unknown, min: number, max: number): value is string {
  return typeof value === 'string' && value.length >= min && value.length <= max;
}
