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
 * Main-process-only parser for the one-line server attach string:
 *
 *   agentico://<token>@<host>:<port>[?name=<urlencoded-name>]
 *
 * Mirrors internal/server/connectstring.go exactly: the bearer token lives in
 * URL userinfo, the port is always explicit, and wildcard bind addresses are
 * rejected as non-dialable. This module is imported by main-process code only
 * — the token and anything derived from it must never cross
 * desktop/src/shared/ IPC schemas.
 *
 * Parsing is strict and each rejection carries a distinct SafeError code with
 * an actionable remediation. Messages never echo the raw input: it may carry
 * the bearer token.
 */

import { createHash } from 'node:crypto';

import { SafeErrorException, safeError } from '../shared/errors';

export const E_CONNECTION_STRING_SCHEME = 'E_CONNECTION_STRING_SCHEME';
export const E_CONNECTION_STRING_TOKEN = 'E_CONNECTION_STRING_TOKEN';
export const E_CONNECTION_STRING_HOST = 'E_CONNECTION_STRING_HOST';
export const E_CONNECTION_STRING_HOST_INVALID = 'E_CONNECTION_STRING_HOST_INVALID';
export const E_CONNECTION_STRING_WILDCARD = 'E_CONNECTION_STRING_WILDCARD';
export const E_CONNECTION_STRING_PORT = 'E_CONNECTION_STRING_PORT';
export const E_CONNECTION_STRING_PORT_RANGE = 'E_CONNECTION_STRING_PORT_RANGE';

const GRAMMAR = 'agentico://<token>@<host>:<port>[?name=<name>]';

export interface ParsedConnectionString {
  /** Canonical dialable base URL, e.g. http://10.1.2.3:8080 or http://[fe80::1]:8080. */
  baseUrl: string;
  /** Bearer token from the URL userinfo. Main-process only. */
  token: string;
  /** Optional server display name (URL-decoded); absent when not provided. */
  name?: string;
}

const SCHEME_RE = /^([A-Za-z][A-Za-z0-9+.-]*):\/\//;

function fail(code: string, message: string, remediation: string): never {
  throw new SafeErrorException(safeError(code, message, remediation));
}

/** Mirrors Go's net.JoinHostPort: bracket-wrap the host when it is an IPv6 literal. */
function joinHostPort(host: string, port: number): string {
  const h = host.includes(':') ? `[${host}]` : host;
  return `${h}:${String(port)}`;
}

function isWildcardHost(host: string): boolean {
  return host === '0.0.0.0' || host === '::';
}

// Characters that cannot appear in a dialable host as the Go side advertises
// one; their presence means the result would not form a valid http:// base URL.
const INVALID_HOST_CHARS_RE = /[\s@/?#]/;

/**
 * Parses and validates a connection string, throwing a SafeErrorException with
 * a distinct code per failure mode.
 */
export function parseConnectionString(raw: string): ParsedConnectionString {
  const schemeMatch = SCHEME_RE.exec(raw);
  const scheme = schemeMatch?.[1];
  if (scheme === undefined || scheme.toLowerCase() !== 'agentico') {
    const got = scheme === undefined ? 'no recognizable scheme' : `${scheme}://`;
    fail(
      E_CONNECTION_STRING_SCHEME,
      `Connection string must use the agentico:// scheme, got ${got}.`,
      `Paste the full attach string exactly as the server printed it (${GRAMMAR}).`,
    );
  }

  // Scheme matched; parse the remainder manually (Node's URL does not parse
  // non-special schemes with the same authority rules as Go's net/url).
  let rest = raw.slice(scheme.length + '://'.length);
  const fragmentIndex = rest.indexOf('#');
  if (fragmentIndex >= 0) {
    rest = rest.slice(0, fragmentIndex);
  }
  let query = '';
  const queryIndex = rest.indexOf('?');
  if (queryIndex >= 0) {
    query = rest.slice(queryIndex + 1);
    rest = rest.slice(0, queryIndex);
  }
  const pathIndex = rest.indexOf('/');
  const authority = pathIndex >= 0 ? rest.slice(0, pathIndex) : rest;

  const atIndex = authority.lastIndexOf('@');
  const userinfo = atIndex >= 0 ? authority.slice(0, atIndex) : '';
  const hostport = atIndex >= 0 ? authority.slice(atIndex + 1) : authority;
  // The token is the userinfo username (base64url, so no escaping needed).
  const token = userinfo.split(':')[0] ?? '';
  if (token === '') {
    fail(
      E_CONNECTION_STRING_TOKEN,
      'Connection string is missing the bearer token before the @ sign.',
      `Expected ${GRAMMAR} — copy the full string; the token is the part before the @.`,
    );
  }

  let host: string;
  let portText: string | undefined;
  if (hostport.startsWith('[')) {
    const close = hostport.indexOf(']');
    if (close < 0) {
      host = '';
      portText = undefined;
    } else {
      host = hostport.slice(1, close);
      const after = hostport.slice(close + 1);
      portText = after.startsWith(':') ? after.slice(1) : undefined;
    }
  } else {
    const colon = hostport.lastIndexOf(':');
    if (colon < 0) {
      host = hostport;
      portText = undefined;
    } else {
      host = hostport.slice(0, colon);
      portText = hostport.slice(colon + 1);
    }
  }

  if (host === '') {
    fail(
      E_CONNECTION_STRING_HOST,
      'Connection string is missing a host.',
      `Expected ${GRAMMAR} with the server's advertised address between @ and the port.`,
    );
  }
  if (isWildcardHost(host)) {
    fail(
      E_CONNECTION_STRING_WILDCARD,
      `Connection string host ${host} is a wildcard bind address, not a dialable address.`,
      'Ask the server to advertise its primary interface address (or use 127.0.0.1 for same-machine servers).',
    );
  }
  if (INVALID_HOST_CHARS_RE.test(host)) {
    fail(
      E_CONNECTION_STRING_HOST_INVALID,
      'Connection string host contains characters that cannot form a valid http:// base URL.',
      `Expected ${GRAMMAR} — check the host portion for stray spaces or punctuation.`,
    );
  }
  if (portText === undefined || portText === '') {
    fail(
      E_CONNECTION_STRING_PORT,
      'Connection string is missing an explicit port.',
      'The server always prints host:port — recopy the string including the port after the colon.',
    );
  }
  if (!/^\d+$/.test(portText)) {
    fail(
      E_CONNECTION_STRING_PORT_RANGE,
      `Connection string port "${portText}" is not a number in the range 1-65535.`,
      'Recopy the string; the port is the digits after the final colon.',
    );
  }
  const port = Number.parseInt(portText, 10);
  if (port < 1 || port > 65535) {
    fail(
      E_CONNECTION_STRING_PORT_RANGE,
      `Connection string port ${String(port)} is out of range 1-65535.`,
      'Recopy the string; the server listens on a port between 1 and 65535.',
    );
  }

  const nameValue = new URLSearchParams(query).get('name');
  const parsed: ParsedConnectionString = {
    baseUrl: `http://${joinHostPort(host, port)}`,
    token,
    ...(nameValue === null || nameValue === '' ? {} : { name: nameValue }),
  };
  return parsed;
}

/**
 * Canonicalizes a base URL the way Go's ConnectionString.BaseURL() renders it
 * (lowercase host, IPv6 bracket-wrapped, explicit port) and returns the
 * registry-style key: first 32 lowercase hex chars of its sha256, mirroring
 * gateway/registry.ts's registryEntryKey.
 */
export function serverKeyForBaseUrl(baseUrl: string): string {
  const u = new URL(baseUrl);
  const host = u.hostname.replace(/^\[|\]$/g, '').toLowerCase();
  const port = Number.parseInt(u.port, 10);
  const canonical = `${u.protocol}//${joinHostPort(host, port)}`;
  return createHash('sha256').update(canonical).digest('hex').slice(0, 32);
}
