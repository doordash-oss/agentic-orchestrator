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
import { CanonicalErrorSchema } from '../shared/api/parse';
import { assertCompatibleApiVersion } from '../shared/apiVersion';
import { redactedCanonicalError } from '../shared/errors';
import {
  SlackSettingsDraftSchema,
  SlackSettingsSnapshotSchema,
  SlackValidationRequestSchema,
  SlackValidationResultSchema,
  type SlackIdentity,
  type SlackSettingsDraft,
  type SlackSettingsSnapshot,
  type SlackValidationRequest,
  type SlackValidationResult,
} from '../shared/ipc';
import { fencedServerRequest, type ServerIdentitySource } from './serverFence';
import type { ServerTransport } from './serverClient';

const WireSlackIdentitySchema = z.strictObject({
  team_id: z.string(),
  team_name: z.string(),
  user_id: z.string(),
  display_name: z.string(),
  bot_id: z.string().optional(),
});

const WireSlackStatusSchema = z.strictObject({
  state: z.enum(['not_configured', 'connected', 'warning']),
  last_error: CanonicalErrorSchema.nullable().optional(),
  last_checked_at: z.string().datetime().nullable().optional(),
});

const WireSlackSettingsSchema = z.strictObject({
  enabled: z.boolean(),
  token_set: z.boolean(),
  token_hint: z.string(),
  token_type: z.enum(['bot', 'user']).nullable().optional(),
  identity: WireSlackIdentitySchema.nullable().optional(),
  granted_scopes: z.array(z.string()),
  missing_scopes: z.array(z.string()),
  status: WireSlackStatusSchema,
  manifest: z.string(),
});

const RuntimeConfigResponseSchema = z.object({
  api_version: z.string(),
  slack: WireSlackSettingsSchema.optional(),
});

const ActionResponseSchema = z.object({ api_version: z.string() });

const SlackValidateResponseSchema = z.object({
  api_version: z.string(),
  token_type: z.enum(['bot', 'user']),
  identity: WireSlackIdentitySchema,
  granted_scopes: z.array(z.string()),
  missing_scopes: z.array(z.string()),
});

export interface SlackSettingsServiceDeps {
  transport: ServerTransport;
  identity?: ServerIdentitySource;
}

function mapIdentity(identity: z.output<typeof WireSlackIdentitySchema>): SlackIdentity {
  return {
    teamId: identity.team_id,
    teamName: identity.team_name,
    userId: identity.user_id,
    displayName: identity.display_name,
    botId: identity.bot_id ?? null,
  };
}

export class SlackSettingsService {
  constructor(private readonly deps: SlackSettingsServiceDeps) {}

  async get(): Promise<SlackSettingsSnapshot> {
    const body = await this.request('/api/v1/config/runtime');
    const response = RuntimeConfigResponseSchema.parse(body);
    assertCompatibleApiVersion(response.api_version);
    if (response.slack === undefined) {
      return { supported: false };
    }
    const slack = response.slack;
    return SlackSettingsSnapshotSchema.parse({
      supported: true,
      enabled: slack.enabled,
      tokenSet: slack.token_set,
      tokenHint: slack.token_hint,
      tokenType: slack.token_type ?? null,
      identity:
        slack.identity === undefined || slack.identity === null
          ? null
          : mapIdentity(slack.identity),
      grantedScopes: slack.granted_scopes,
      missingScopes: slack.missing_scopes,
      status: {
        state: slack.status.state,
        lastError:
          slack.status.last_error === undefined || slack.status.last_error === null
            ? null
            : redactedCanonicalError(slack.status.last_error),
        lastCheckedAt: slack.status.last_checked_at ?? null,
      },
      manifest: slack.manifest,
    });
  }

  async update(draft: SlackSettingsDraft): Promise<SlackSettingsSnapshot> {
    const input = SlackSettingsDraftSchema.parse(draft);
    const body = await this.request('/api/v1/config/runtime', {
      method: 'PATCH',
      body: {
        slack: {
          ...(input.enabled === undefined ? {} : { enabled: input.enabled }),
          ...(input.token === undefined ? {} : { token: input.token }),
          ...(input.clearToken === undefined ? {} : { clear_token: input.clearToken }),
        },
      },
    });
    const response = ActionResponseSchema.parse(body);
    assertCompatibleApiVersion(response.api_version);
    return this.get();
  }

  async validate(request: SlackValidationRequest): Promise<SlackValidationResult> {
    const input = SlackValidationRequestSchema.parse(request);
    const body = await this.request('/api/v1/integrations/slack/validate', {
      method: 'POST',
      body: input.token === undefined ? {} : { token: input.token },
    });
    const response = SlackValidateResponseSchema.parse(body);
    assertCompatibleApiVersion(response.api_version);
    return SlackValidationResultSchema.parse({
      tokenType: response.token_type,
      identity: mapIdentity(response.identity),
      grantedScopes: response.granted_scopes,
      missingScopes: response.missing_scopes,
    });
  }

  private request(
    path: string,
    init?: Parameters<typeof fencedServerRequest>[3],
  ): Promise<unknown> {
    return fencedServerRequest(this.deps.transport, this.deps.identity, path, init);
  }
}
