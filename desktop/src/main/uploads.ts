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
 * Main-process upload staging for remote connections: reads local files and
 * streams their bytes to `POST /api/v1/uploads` through the authenticated
 * gateway transport, one request per file. The renderer asks for a batch and
 * receives a per-file result (never a wholesale failure) so its composer
 * chips can stage/fail/retry individually.
 *
 * Server-shape note: `StageUploadResponse` is validated with a locally
 * declared schema until `npm run generate:api` regenerates schema.gen.ts
 * from the updated openapi.yaml.
 */
import path from 'node:path';
import { z } from 'zod';
import { buildCanonicalError, CanonicalErrorException, toCanonicalError } from '../shared/errors';
import { validateWithSchema } from '../shared/api/parse';
import {
  AbsolutePathSchema,
  CREATION_IMAGE_FORMATS,
  type CreationFileKind,
  type CreationFileUploadResult,
  type StagedUpload,
  type UploadCreationFilesResult,
} from '../shared/ipc';
import type { HttpResult } from './gateway/runtimeGateway';
import { mapServerError } from './serverClient';

/** The authenticated binary-post surface the gateway provides. */
export interface UploadTransport {
  apiUpload?(path: string, body: Uint8Array): Promise<HttpResult>;
}

export interface UploadServiceDeps {
  transport: UploadTransport;
  readFile(path: string): Promise<Uint8Array>;
  statFile(path: string): Promise<{ size: number }>;
  /**
   * Identity of the connected server (the known-servers key) stamped onto
   * every staged upload, so the renderer can scope staged items to the
   * connection that produced them after a server switch.
   */
  serverKey(): string | null;
}

/** Server-side per-kind byte caps (internal/server/uploads.go). */
export const UPLOAD_BYTE_LIMITS: Record<CreationFileKind, number> = {
  image: 10 * 1024 * 1024,
  attachment: 25 * 1024 * 1024,
};

/** Locally declared until the StageUploadResponse model is regenerated. */
const StageUploadResponseSchema = z.object({
  api_version: z.string().optional(),
  reference: z.string().min(1).max(128),
  kind: z.enum(['image', 'attachment']),
  name: z.string().min(1).max(255),
  size: z.number().int().nonnegative(),
});

const IMAGE_EXTENSIONS: ReadonlySet<string> = new Set(
  CREATION_IMAGE_FORMATS.map((format) => format.extension),
);

export class UploadService {
  constructor(private readonly deps: UploadServiceDeps) {}

  /**
   * Stages every file in request order. Individual failures are reported in
   * place (canonical-error-shaped) so one bad file cannot take down the batch.
   */
  async stageFiles(
    kind: CreationFileKind,
    paths: readonly string[],
  ): Promise<UploadCreationFilesResult> {
    const results: CreationFileUploadResult[] = [];
    for (const filePath of paths) {
      const name = path.basename(filePath);
      try {
        const upload = await this.stageFile(kind, filePath);
        results.push({ ok: true, name: upload.name, upload });
      } catch (err) {
        results.push({
          ok: false,
          name: name === '' ? 'selected file' : name,
          error: toCanonicalError(err, 'E_UPLOAD'),
        });
      }
    }
    return { results };
  }

  /** Stages one local file: preflight size/kind checks, then one POST. */
  private async stageFile(kind: CreationFileKind, filePath: string): Promise<StagedUpload> {
    const validated = validateWithSchema(filePath, AbsolutePathSchema);
    const name = path.basename(validated);
    if (name === '' || name.length > 255) {
      throw new CanonicalErrorException(buildCanonicalError('E_UPLOAD_NAME'));
    }
    if (kind === 'image' && !isImageExtension(name)) {
      throw new CanonicalErrorException(buildCanonicalError('E_UPLOAD_TYPE_UNSUPPORTED'));
    }
    const { size } = await this.deps.statFile(validated);
    const limit = UPLOAD_BYTE_LIMITS[kind];
    if (size > limit) {
      throw new CanonicalErrorException(
        buildCanonicalError('E_UPLOAD_TOO_LARGE', {
          params: { limit: kind === 'image' ? '10 MiB image' : '25 MiB attachment' },
        }),
      );
    }
    const upload = this.deps.transport.apiUpload;
    if (upload === undefined) {
      throw new CanonicalErrorException(buildCanonicalError('E_UPLOAD_UNAVAILABLE'));
    }
    const body = await this.deps.readFile(validated);
    const query = new URLSearchParams({ kind, name });
    const result = await upload.call(
      this.deps.transport,
      `/api/v1/uploads?${query.toString()}`,
      body,
    );
    if (result.status < 200 || result.status >= 300) {
      throw mapServerError(result);
    }
    const staged = validateWithSchema(result.body, StageUploadResponseSchema);
    const serverKey = this.deps.serverKey();
    if (serverKey === null) {
      throw new CanonicalErrorException(buildCanonicalError('E_NOT_CONNECTED'));
    }
    return {
      reference: staged.reference,
      kind,
      name: staged.name,
      size: staged.size,
      serverKey,
    };
  }
}

function isImageExtension(name: string): boolean {
  const extension = name.split('.').at(-1)?.toLowerCase() ?? '';
  return IMAGE_EXTENSIONS.has(extension);
}
