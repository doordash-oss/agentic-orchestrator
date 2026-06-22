// Copyright 2026 DoorDash, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package opencode

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// Diagnostic redaction for the OpenCode provider.
//
// Every user-facing or persisted OpenCode diagnostic — readiness detail, command
// error/output, ACP handshake error, terminal ACP error result, and any text
// that lands in a transcript or behavioral evidence file — flows through
// sanitizeDiagnostic before it leaves the provider. OpenCode's readiness probe
// and ACP error objects can echo provider configuration, auth tokens, API keys,
// and environment-derived secrets, none of which may be surfaced or written to
// disk. The sanitizer redacts (or, for JSON-RPC `data`, omits) those classes
// while leaving clean remediation text intact.

// redactedPlaceholder replaces any credential-like value removed from a
// diagnostic before it is surfaced to the user or persisted.
const redactedPlaceholder = "[redacted]"

// configOmittedPlaceholder replaces an entire provider-configuration object
// removed from a diagnostic. Provider config is omitted wholesale rather than
// value-redacted: the structure itself (provider names, option keys, endpoints)
// is config content the plan requires absent from surfaced or persisted
// diagnostics, not just the credential values nested inside it.
const configOmittedPlaceholder = "[provider config omitted]"

// minEnvSecretLen is the shortest environment value scrubbed by exact match.
// Short values (flags, booleans, small numbers, common words) would cause noisy
// false-positive replacements across unrelated diagnostic text, so only longer
// values — the shape real tokens and keys take — are treated as secrets.
const minEnvSecretLen = 8

// environFunc returns the process environment. It is a variable so tests can
// inject a deterministic environment without mutating the real process env.
var environFunc = os.Environ

// secretEnvNamePattern matches environment variable NAMES that conventionally
// hold credentials. Any matching variable's value is scrubbed by exact match
// from diagnostics, which is how environment-derived secrets are removed even
// when they appear with no surrounding key/value framing.
var secretEnvNamePattern = regexp.MustCompile(`(?i)(API[_-]?KEY|ACCESS[_-]?KEY|SECRET|TOKEN|PASSWORD|PASSWD|CREDENTIAL|PRIVATE[_-]?KEY|SESSION[_-]?KEY|AUTH)`)

// bearerPattern matches an HTTP "Bearer <token>" authorization value. It runs
// before kvSecretPattern so the opaque token after the scheme is redacted even
// when it is the value of an Authorization header.
var bearerPattern = regexp.MustCompile(`(?i)(bearer\s+)([A-Za-z0-9._~+/=\-]+)`)

// kvSecretPattern matches inline "<credential-key>: <value>" / "...=<value>"
// pairs. The credential key and separator (groups 1 and 2) are preserved; the
// value (group 3) is dropped, so API keys, tokens, and provider-config option
// contents embedded in command output or ACP messages are removed while the
// surrounding diagnostic still reads sensibly.
var kvSecretPattern = regexp.MustCompile(`(?i)((?:api[_-]?key|access[_-]?key|secret(?:[_-]?access[_-]?key)?|token|password|passwd|credential|authorization|private[_-]?key|session[_-]?key))(["']?\s*[:=]\s*["']?)([^\s"',;}{)]+)`)

// tokenPrefixPattern matches standalone API-key / token literals carrying a
// well-known vendor prefix, even when they appear with no surrounding key name
// (for example in a free-text error message).
var tokenPrefixPattern = regexp.MustCompile(`\b(sk|pk|rk|ghp|gho|ghu|ghs|ghr|github_pat|xox[baprs]|glpat|AKIA|ASIA|AIza|ya29)[-_][A-Za-z0-9_\-./]{6,}`)

// providerConfigMarker identifies the structural keys of an OpenCode
// provider-configuration object. A JSON object carrying any of these keys is
// omitted wholesale rather than value-redacted, so the config structure —
// provider names, option keys, and non-credential option values such as a
// backend endpoint — never reaches a surfaced or persisted diagnostic.
var providerConfigMarker = regexp.MustCompile(`(?i)"(providers?|options|api[_-]?key|base[_-]?url)"\s*:`)

// sanitizeDiagnostic redacts credential-like content from a diagnostic string
// before it is surfaced to the user or persisted. It removes, in order:
//   - whole provider-configuration objects (the config structure itself, not
//     just the credentials nested inside it), so neither provider names, option
//     keys, nor non-credential option values are surfaced;
//   - the values of credential-bearing environment variables (exact match), so
//     an environment-derived secret echoed verbatim in CLI/ACP output is gone;
//   - "Bearer <token>" authorization values;
//   - inline "<credential-key>: <value>" / "<credential-key>=<value>" pairs,
//     covering API keys, tokens, and any provider-config option contents that
//     appeared outside a recognizable config object;
//   - standalone token literals carrying a known vendor prefix.
//
// Clean diagnostics with no credential-like content are returned unchanged so
// real remediation text still reaches the user.
func sanitizeDiagnostic(s string) string {
	if s == "" {
		return s
	}
	out := omitProviderConfig(s)
	for _, v := range secretEnvValues() {
		out = strings.ReplaceAll(out, v, redactedPlaceholder)
	}
	out = bearerPattern.ReplaceAllString(out, "${1}"+redactedPlaceholder)
	out = kvSecretPattern.ReplaceAllString(out, "${1}${2}"+redactedPlaceholder)
	out = tokenPrefixPattern.ReplaceAllString(out, redactedPlaceholder)
	return out
}

// omitProviderConfig replaces JSON object blobs that look like OpenCode provider
// configuration with configOmittedPlaceholder, so provider config contents — not
// just the credential values nested inside them — never reach a surfaced or
// persisted diagnostic. It scans for balanced "{...}" objects (respecting JSON
// string escaping) and omits any whose body carries a provider-config marker;
// objects without a marker, and text outside any object, are left untouched.
func omitProviderConfig(s string) string {
	if !strings.ContainsRune(s, '{') {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '{' {
			b.WriteByte(s[i])
			i++
			continue
		}
		end, ok := matchBrace(s, i)
		if !ok {
			// The object never closes (truncated input) — itself a corruption
			// signal. If the unclosed remainder looks like provider config, omit
			// it wholesale so partial config structure cannot leak; otherwise
			// emit the remainder verbatim. Either way nothing past an unclosed
			// brace can be a balanced object, so stop scanning.
			rest := s[i:]
			if providerConfigMarker.MatchString(rest) {
				b.WriteString(configOmittedPlaceholder)
			} else {
				b.WriteString(rest)
			}
			break
		}
		obj := s[i:end]
		if providerConfigMarker.MatchString(obj) {
			b.WriteString(configOmittedPlaceholder)
		} else {
			b.WriteString(obj)
		}
		i = end
	}
	return b.String()
}

// matchBrace returns the index just past the '}' that closes the '{' at start,
// tracking nesting depth and ignoring braces that appear inside JSON string
// literals (so a '{' or '}' within a quoted value never skews the balance). ok
// is false when the object is never closed.
func matchBrace(s string, start int) (end int, ok bool) {
	depth := 0
	inStr := false
	escaped := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inStr {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i + 1, true
			}
		}
	}
	return 0, false
}

// secretEnvValues returns the values of environment variables whose names look
// credential-bearing and whose values are long enough to scrub safely.
func secretEnvValues() []string {
	var vals []string
	for _, kv := range environFunc() {
		name, val, ok := strings.Cut(kv, "=")
		if !ok || len(val) < minEnvSecretLen {
			continue
		}
		if secretEnvNamePattern.MatchString(name) {
			vals = append(vals, val)
		}
	}
	return vals
}

// rpcErrorDetail renders a JSON-RPC error object as a sanitized, human-readable
// string. The error's structured `data` member — the most likely place for
// provider config or credentials to appear — is omitted entirely by decoding
// only into the code/message struct, and the remaining message is sanitized.
// A payload that is not a recognizable error object falls back to a sanitized
// rendering of the raw bytes rather than the structured original.
func rpcErrorDetail(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var e RPCError
	if err := json.Unmarshal(raw, &e); err != nil {
		return sanitizeDiagnostic(string(raw))
	}
	msg := sanitizeDiagnostic(e.Message)
	if msg == "" {
		return fmt.Sprintf("code %d", e.Code)
	}
	return fmt.Sprintf("code %d: %s", e.Code, msg)
}
