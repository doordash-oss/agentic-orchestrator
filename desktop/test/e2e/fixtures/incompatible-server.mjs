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
