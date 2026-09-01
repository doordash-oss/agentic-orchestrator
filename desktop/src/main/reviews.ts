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

import { z } from 'zod';
import { assertCompatibleApiVersion } from '../shared/apiVersion';
import {
  CanonicalErrorResponseSchema,
  ReviewDecisionResponseSchema,
  ReviewDraftValidationResponseSchema,
  ReviewSessionResponseSchema,
  validateWithSchema,
  type ServerReviewDecision,
  type ServerReviewDraftValidation,
  type ServerReviewSession,
} from '../shared/api/parse';
import { assertNoPrototypePollution, assertWithinByteSize } from '../shared/sanitize';
import type { ApiRequestInit } from './gateway/runtimeGateway';
import { serverRequest, mapServerError, type ServerTransport } from './serverClient';

const identifier = z.string().min(1).max(200);
const revision = z.string().min(1).max(200);
const text = z.string().max(2 * 1024 * 1024);

const ReviewOpenRequestSchema = z.strictObject({ featureId: identifier });
const ReviewSaveRequestSchema = z.strictObject({
  featureId: identifier,
  reviewId: identifier,
  baseRevision: revision,
  text,
});
const ReviewValidateRequestSchema = z.strictObject({
  featureId: identifier,
  reviewId: identifier,
  text,
});
const ReviewDecisionRequestSchema = z.strictObject({
  featureId: identifier,
  reviewId: identifier,
  baseRevision: revision,
  decision: z.enum(['proceed', 'iterate']),
});

export type ReviewOpenRequest = z.output<typeof ReviewOpenRequestSchema>;
export type ReviewSaveRequest = z.output<typeof ReviewSaveRequestSchema>;
export type ReviewValidateRequest = z.output<typeof ReviewValidateRequestSchema>;
export type ReviewDecisionRequest = z.output<typeof ReviewDecisionRequestSchema>;
export type ReviewMutationResult<T> = { type: 'saved'; session: T } | { type: 'conflict' };

/** Main-process-only adapter for the revisioned review-session REST contract. */
export class ReviewService {
  constructor(private readonly transport: ServerTransport) {}

  async read(featureId: string): Promise<ServerReviewSession> {
    const input = validateWithSchema({ featureId }, ReviewOpenRequestSchema);
    return this.getSession(`/api/v1/features/${input.featureId}/reviews`);
  }

  async open(featureId: string): Promise<ServerReviewSession> {
    const input = validateWithSchema({ featureId }, ReviewOpenRequestSchema);
    return this.getSession(`/api/v1/features/${input.featureId}/reviews`, {
      method: 'POST',
      body: {},
    });
  }

  async save(request: ReviewSaveRequest): Promise<ReviewMutationResult<ServerReviewSession>> {
    const input = validateWithSchema(request, ReviewSaveRequestSchema);
    const path = `/api/v1/features/${input.featureId}/reviews/${input.reviewId}/draft`;
    const result = await this.mutate(
      path,
      { base_revision: input.baseRevision, text: input.text },
      'PUT',
    );
    return result.type === 'conflict'
      ? result
      : { type: 'saved', session: validateWithSchema(result.body, ReviewSessionResponseSchema) };
  }

  async validate(request: ReviewValidateRequest): Promise<ServerReviewDraftValidation> {
    const input = validateWithSchema(request, ReviewValidateRequestSchema);
    const body = await serverRequest(
      this.transport,
      `/api/v1/features/${input.featureId}/reviews/${input.reviewId}/validate`,
      { method: 'POST', body: { text: input.text } },
    );
    return validateWithSchema(body, ReviewDraftValidationResponseSchema);
  }

  async decide(
    request: ReviewDecisionRequest,
  ): Promise<ReviewMutationResult<ServerReviewDecision>> {
    const input = validateWithSchema(request, ReviewDecisionRequestSchema);
    const path = `/api/v1/features/${input.featureId}/reviews/${input.reviewId}/decision`;
    const result = await this.mutate(path, {
      decision: input.decision,
      base_revision: input.baseRevision,
    });
    return result.type === 'conflict'
      ? result
      : { type: 'saved', session: validateWithSchema(result.body, ReviewDecisionResponseSchema) };
  }

  private async getSession(path: string, init?: ApiRequestInit): Promise<ServerReviewSession> {
    const body = await serverRequest(this.transport, path, init);
    return validateWithSchema(body, ReviewSessionResponseSchema);
  }

  /**
   * Runs one review mutation. A 409 is detected by its canonical code alone:
   * the draft revision no longer travels on the error body, so callers
   * refetch the session to pick up the current revision.
   */
  private async mutate(
    path: string,
    body: Record<string, unknown>,
    method: 'POST' | 'PUT' = 'POST',
  ): Promise<{ type: 'body'; body: unknown } | { type: 'conflict' }> {
    const response = await this.transport.apiRequest(path, {
      method,
      body,
    });
    if (response.status >= 200 && response.status < 300) {
      return { type: 'body', body: response.body };
    }
    if (response.status === 409) {
      assertNoPrototypePollution(response.body);
      assertWithinByteSize(JSON.stringify(response.body) ?? '');
      const conflict = CanonicalErrorResponseSchema.safeParse(response.body);
      if (conflict.success && conflict.data.error.code === 'conflict') {
        assertCompatibleApiVersion(conflict.data.api_version);
        return { type: 'conflict' };
      }
    }
    throw mapServerError(response);
  }
}
