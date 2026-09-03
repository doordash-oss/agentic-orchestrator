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
 * Journey transcript recorder. Each journey appends real, machine-produced
 * facts (commands it ran, IPC results, file findings, log excerpts) as it
 * executes; on evidence runs the transcript is written to
 * AGENTICO_E2E_EVIDENCE_DIR/behaviors/<name>.md, otherwise next to the test
 * output. Nothing here is hand-written after the fact.
 */
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import type { TestInfo } from '@playwright/test';
import { evidenceDir } from './app';
import { readVerification } from './packaged';

export interface TranscriptOptions {
  /**
   * Append to an existing transcript of the same name (used when several
   * journeys contribute to one evidence document, e.g.
   * ownership-compatibility.md). The section becomes an H2 in that case.
   */
  append?: boolean;
}

export class Transcript {
  private readonly lines: string[] = [];
  private readonly startedAt = new Date();
  private readonly append: boolean;

  constructor(
    private readonly name: string,
    title: string,
    options: TranscriptOptions = {},
  ) {
    this.append = options.append === true;
    const verification = readVerification();
    this.lines.push(
      `${this.append ? '##' : '#'} ${title}`,
      '',
      `- Recorded: ${this.startedAt.toISOString()} on ${os.platform()} ${os.arch()} (${os.hostname()})`,
      `- Runner: \`npm run test:e2e:packaged\` (Playwright, packaged app + real bundled server)`,
      ...(verification === null
        ? []
        : [
            `- Package under test: \`${verification.unpacked_app}\``,
            `- Package identity: desktop ${verification.identity.desktop_version}, ` +
              `server ${verification.identity.server_version} ` +
              `(${verification.identity.server_revision.slice(0, 12)}), ` +
              `${verification.identity.os}/${verification.identity.arch}`,
          ]),
      '',
    );
  }

  section(heading: string): void {
    this.lines.push(`${this.append ? '###' : '##'} ${heading}`, '');
  }

  step(text: string): void {
    this.lines.push(`- ${new Date().toISOString().slice(11, 23)} ${text}`);
  }

  /** Records an executed command with its real output (bounded). */
  command(commandLine: string, output: string, exitCode = 0): void {
    this.lines.push(
      '',
      `\`\`\`console`,
      `$ ${commandLine}`,
      ...bounded(output, 40),
      `(exit ${exitCode})`,
      '```',
      '',
    );
  }

  /** Records a JSON value (IPC result, discovery record, …) verbatim. */
  json(label: string, value: unknown): void {
    this.lines.push('', `${label}:`, '', '```json', JSON.stringify(value, null, 2), '```', '');
  }

  codeBlock(label: string, content: string, maxLines = 60): void {
    this.lines.push('', `${label}:`, '', '```', ...bounded(content, maxLines), '```', '');
  }

  /** Writes the transcript; evidence runs land in behaviors/<name>.md. */
  write(testInfo: TestInfo): string {
    const body = `${this.lines.join('\n')}\n`;
    const dir = evidenceDir();
    const target =
      dir !== null
        ? path.join(dir, 'behaviors', `${this.name}.md`)
        : testInfo.outputPath(`${this.name}.md`);
    fs.mkdirSync(path.dirname(target), { recursive: true });
    if (this.append && fs.existsSync(target)) {
      fs.appendFileSync(target, `\n${body}`);
    } else {
      fs.writeFileSync(target, body);
    }
    return target;
  }
}

function bounded(text: string, maxLines: number): string[] {
  const all = text.replaceAll('\r\n', '\n').split('\n');
  if (all.length <= maxLines) {
    return all;
  }
  const head = all.slice(0, maxLines);
  head.push(`… (${all.length - maxLines} more lines)`);
  return head;
}
