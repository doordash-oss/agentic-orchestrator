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

import { describe, expect, it, vi } from 'vitest';
import type { HttpResult } from '../gateway/runtimeGateway';
import { SlackSettingsService } from '../slackSettingsService';

const projection = {
  enabled: false,
  token_set: true,
  token_hint: '1234',
  token_type: 'bot',
  identity: {
    team_id: 'T123',
    team_name: 'Acme',
    user_id: 'U123',
    display_name: 'Agentico',
    bot_id: 'B123',
  },
  granted_scopes: ['chat:write', 'users:read'],
  missing_scopes: [],
  status: {
    state: 'connected',
    last_error: null,
    last_checked_at: '2026-09-19T10:00:00Z',
  },
  manifest: '{"display_information":{"name":"Agentico"}}',
};

function response(body: unknown, status = 200): HttpResult {
  return { status, body };
}

function transport(...responses: HttpResult[]) {
  return {
    apiRequest: vi.fn(async () => {
      const next = responses.shift();
      if (next === undefined) throw new Error('unexpected request');
      return next;
    }),
  };
}

describe('SlackSettingsService', () => {
  it('maps the runtime Slack projection without exposing token material', async () => {
    const server = transport(response({ api_version: 'v1', slack: projection }));
    const service = new SlackSettingsService({ transport: server });

    const result = await service.get();

    expect(result).toStrictEqual({
      supported: true,
      enabled: false,
      tokenSet: true,
      tokenHint: '1234',
      tokenType: 'bot',
      identity: {
        teamId: 'T123',
        teamName: 'Acme',
        userId: 'U123',
        displayName: 'Agentico',
        botId: 'B123',
      },
      grantedScopes: ['chat:write', 'users:read'],
      missingScopes: [],
      status: {
        state: 'connected',
        lastError: null,
        lastCheckedAt: '2026-09-19T10:00:00Z',
      },
      manifest: '{"display_information":{"name":"Agentico"}}',
    });
    expect(JSON.stringify(result)).not.toContain('xox');
  });

  it('returns an explicit unsupported marker when the server omits Slack', async () => {
    const server = transport(response({ api_version: 'v1' }));
    const service = new SlackSettingsService({ transport: server });

    await expect(service.get()).resolves.toStrictEqual({ supported: false });
  });

  it('patches only Slack and omits a token when the draft does not carry one', async () => {
    const server = transport(
      response({ api_version: 'v1' }),
      response({ api_version: 'v1', slack: projection }),
    );
    const service = new SlackSettingsService({ transport: server });

    const result = await service.update({ enabled: false, clearToken: true });

    expect(server.apiRequest).toHaveBeenNthCalledWith(1, '/api/v1/config/runtime', {
      method: 'PATCH',
      body: { slack: { enabled: false, clear_token: true } },
    });
    expect(result.supported).toBe(true);
    expect(JSON.stringify(result)).not.toContain('token-should-never-return');
  });

  it('forwards a write-only token once and never returns it', async () => {
    const token = 'xoxb-111-222-secret';
    const server = transport(
      response({ api_version: 'v1' }),
      response({ api_version: 'v1', slack: projection }),
    );
    const service = new SlackSettingsService({ transport: server });

    const result = await service.update({ enabled: true, token });

    expect(server.apiRequest).toHaveBeenNthCalledWith(1, '/api/v1/config/runtime', {
      method: 'PATCH',
      body: { slack: { enabled: true, token } },
    });
    expect(JSON.stringify(result)).not.toContain(token);
  });

  it('validates with and without a draft token', async () => {
    const token = 'xoxp-111-222-secret';
    const validated = {
      api_version: 'v1',
      token_type: 'user',
      identity: {
        team_id: 'T123',
        team_name: 'Acme',
        user_id: 'U234',
        display_name: 'Ada',
      },
      granted_scopes: ['chat:write'],
      missing_scopes: [],
    };
    const server = transport(response(validated), response(validated));
    const service = new SlackSettingsService({ transport: server });

    const draftResult = await service.validate({ token });
    const storedResult = await service.validate({});

    expect(server.apiRequest).toHaveBeenNthCalledWith(1, '/api/v1/integrations/slack/validate', {
      method: 'POST',
      body: { token },
    });
    expect(server.apiRequest).toHaveBeenNthCalledWith(2, '/api/v1/integrations/slack/validate', {
      method: 'POST',
      body: {},
    });
    expect(draftResult).toStrictEqual(storedResult);
    expect(JSON.stringify(draftResult)).not.toContain(token);
  });

  it('discards a response when the connected server identity changes', async () => {
    let generation = 1;
    const server = {
      apiRequest: vi.fn(async () => {
        generation = 2;
        return response({ api_version: 'v1', slack: projection });
      }),
    };
    const service = new SlackSettingsService({
      transport: server,
      identity: () => ({ serverKey: 'server-a', generation }),
    });

    await expect(service.get()).rejects.toMatchObject({
      canonical: { code: 'E_SERVER_SWITCHED' },
    });
  });

  it('classifies malformed server responses as schema mismatches', async () => {
    const server = transport(
      response({ api_version: 'v1', slack: { ...projection, missing_scopes: null } }),
      response({ api_version: 'v1', token_type: 'bot' }),
    );
    const service = new SlackSettingsService({ transport: server });

    await expect(service.get()).rejects.toMatchObject({
      canonical: { code: 'E_SCHEMA_MISMATCH' },
    });
    await expect(service.validate({})).rejects.toMatchObject({
      canonical: { code: 'E_SCHEMA_MISMATCH' },
    });
  });
});
