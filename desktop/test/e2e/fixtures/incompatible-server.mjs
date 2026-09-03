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

// Journey 4 fixture: a loopback HTTP process that answers the auth-exempt
// health probe with an EXPLICIT but unsupported compatibility declaration
// (wrong schema series + runtime policy). It stands in for a future/foreign
// Agentico runtime the desktop app must refuse to use — and must never stop.
//
// Usage: node incompatible-server.mjs --state-dir <dir> --log <request-log>
// Prints "PORT <n>" on stdout once listening. Runs until signalled.
import fs from 'node:fs';
import http from 'node:http';

function arg(name) {
  const index = process.argv.indexOf(name);
  return index >= 0 ? process.argv[index + 1] : undefined;
}

const stateDir = arg('--state-dir');
const logPath = arg('--log');
if (stateDir === undefined || logPath === undefined) {
  console.error('usage: incompatible-server.mjs --state-dir <dir> --log <file>');
  process.exit(2);
}

const health = {
  api_version: 'v1',
  status: 'ok',
  runtime: { runtime_dir: '', state_dir: stateDir, config_path: '' },
  started_at: new Date().toISOString(),
  server_time: new Date().toISOString(),
  compatibility: {
    api_version: 'v1',
    schema_version: 99,
    min_client_schema: 99,
    runtime_policy: 'quantum-entangled-v99',
    server_build: { version: 'v99.0.0', revision: 'f'.repeat(40) },
  },
};

const server = http.createServer((req, res) => {
  fs.appendFileSync(
    logPath,
    `${JSON.stringify({
      method: req.method,
      path: req.url,
      has_authorization: req.headers.authorization !== undefined,
    })}\n`,
  );
  if (req.method === 'GET' && req.url === '/api/v1/health') {
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify(health));
    return;
  }
  res.writeHead(404, { 'Content-Type': 'application/json' });
  res.end(JSON.stringify({ error: { code: 'not_found', message: 'not found' } }));
});

server.listen(0, '127.0.0.1', () => {
  console.log(`PORT ${server.address().port}`);
});
