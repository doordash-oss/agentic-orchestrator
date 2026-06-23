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
	"strings"
	"testing"
)

// withEnv swaps the redactor's environment source for a deterministic one and
// restores it when the test ends. It lets env-derived secret redaction be
// exercised without mutating the real process environment.
func withEnv(t *testing.T, environ []string) {
	t.Helper()
	prev := environFunc
	environFunc = func() []string { return environ }
	t.Cleanup(func() { environFunc = prev })
}

// TestSanitizeDiagnostic_RedactsCredentialLikeValues proves the shared sanitizer
// removes the four credential classes the plan requires (auth tokens, API keys,
// provider config contents, and environment-derived secrets) from a diagnostic
// string, while leaving clean diagnostics untouched.
func TestSanitizeDiagnostic_RedactsCredentialLikeValues(t *testing.T) {
	withEnv(t, []string{"PATH=/usr/bin", "HOME=/Users/dev"})

	cases := []struct {
		name   string
		in     string
		leaked string // credential substring that must NOT survive sanitization
	}{
		{
			name:   "vendor-prefixed API key standalone",
			in:     "provider rejected key sk-ant-api03-LeAkEdSecretValue1234567890",
			leaked: "sk-ant-api03-LeAkEdSecretValue1234567890",
		},
		{
			name:   "json apiKey field",
			in:     `auth failed: {"apiKey":"super-secret-key-value-abcdef123456"}`,
			leaked: "super-secret-key-value-abcdef123456",
		},
		{
			name:   "env-style token assignment",
			in:     "could not authenticate: GITHUB_TOKEN=ghp_AbCdEf0123456789AbCdEf0123 rejected",
			leaked: "ghp_AbCdEf0123456789AbCdEf0123",
		},
		{
			name:   "bearer authorization header",
			in:     "request denied (Authorization: Bearer eyJhbGci.OpaqueTokenValue.signature-xyz789)",
			leaked: "eyJhbGci.OpaqueTokenValue.signature-xyz789",
		},
		{
			name:   "provider config contents with embedded credential",
			in:     `config rejected: {"provider":{"anthropic":{"options":{"apiKey":"sk-ant-CONFIGLEAK9876543210"}}}}`,
			leaked: "sk-ant-CONFIGLEAK9876543210",
		},
		{
			name:   "password field",
			in:     `db error: {"password":"hunter2-very-secret-passphrase"}`,
			leaked: "hunter2-very-secret-passphrase",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := sanitizeDiagnostic(tc.in)
			if strings.Contains(out, tc.leaked) {
				t.Fatalf("sanitizeDiagnostic leaked %q in output: %q", tc.leaked, out)
			}
			// A credential is removed either by value redaction or, when it sat
			// inside a provider-config object, by omitting the whole object;
			// either marker proves the secret did not pass through silently.
			if !strings.Contains(out, redactedPlaceholder) && !strings.Contains(out, configOmittedPlaceholder) {
				t.Fatalf("sanitizeDiagnostic output %q has no redaction or omission marker", out)
			}
		})
	}
}

// TestSanitizeDiagnostic_OmitsProviderConfigContents proves the sanitizer omits
// an entire provider-configuration object, not merely the credential values
// nested inside it. Redacting only the apiKey value would still surface the
// config structure (provider names, option keys, endpoints), which the plan
// requires absent from surfaced and persisted diagnostics. After sanitization
// no config structure — keys, nesting braces, provider names, or non-credential
// option values such as an internal endpoint — may remain.
func TestSanitizeDiagnostic_OmitsProviderConfigContents(t *testing.T) {
	withEnv(t, []string{"PATH=/usr/bin"})

	cases := []struct {
		name    string
		in      string
		framing string   // actionable text that must survive the omission
		gone    []string // config structure/content that must NOT survive
	}{
		{
			name:    "readiness config dump with embedded api key",
			in:      `could not list OpenCode models: provider auth failed; config {"provider":{"anthropic":{"options":{"apiKey":"sk-ant-CONFIGSHAPE1234567890"}}}}`,
			framing: "could not list OpenCode models",
			gone:    []string{`"provider"`, `"options"`, "anthropic", "sk-ant-CONFIGSHAPE1234567890", "{", "}"},
		},
		{
			name:    "config with non-credential endpoint and plain secret",
			in:      `error: {"provider":{"openai":{"options":{"baseURL":"https://internal.acme.example/v1","apiKey":"plain-secret-value-7777"}}}}`,
			framing: "error:",
			gone:    []string{`"provider"`, `"options"`, "openai", "internal.acme.example", "baseURL", "plain-secret-value-7777", "{", "}"},
		},
		{
			// A truncated config dump (unclosed braces) is itself a corruption
			// signal; its partial structure must still be omitted, not emitted
			// verbatim with the credential value merely value-redacted.
			name:    "truncated unclosed config object",
			in:      `could not list OpenCode models: config {"provider":{"anthropic":{"options":{"apiKey":"sk-ant-TRUNCATEDLEAK1234567890`,
			framing: "could not list OpenCode models",
			gone:    []string{`"provider"`, `"options"`, "anthropic", "sk-ant-TRUNCATEDLEAK1234567890", "{"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := sanitizeDiagnostic(tc.in)
			for _, leak := range tc.gone {
				if strings.Contains(out, leak) {
					t.Fatalf("provider config content %q survived sanitization: %q", leak, out)
				}
			}
			if !strings.Contains(out, configOmittedPlaceholder) {
				t.Fatalf("output %q is missing the config-omitted marker %q", out, configOmittedPlaceholder)
			}
			if !strings.Contains(out, tc.framing) {
				t.Fatalf("output %q dropped its actionable framing %q", out, tc.framing)
			}
		})
	}
}

// TestSanitizeDiagnostic_RedactsEnvironmentDerivedSecret proves a secret read
// from a credential-bearing environment variable is scrubbed by exact match
// even when it carries no recognizable key/value framing and no vendor prefix —
// the only signal is that the value came from the environment.
func TestSanitizeDiagnostic_RedactsEnvironmentDerivedSecret(t *testing.T) {
	const secret = "Zx9Qw7Vt2Lp8Rn4Kd6Mb1aXcVbNmQwErTy"
	withEnv(t, []string{
		"ANTHROPIC_API_KEY=" + secret,
		"PATH=/usr/bin",
		"HOME=/Users/dev",
	})

	in := "could not list OpenCode models: backend rejected credential " + secret + " for provider"
	out := sanitizeDiagnostic(in)
	if strings.Contains(out, secret) {
		t.Fatalf("environment-derived secret leaked in %q", out)
	}
	if !strings.Contains(out, redactedPlaceholder) {
		t.Fatalf("output %q has no redaction marker", out)
	}
}

// TestSanitizeDiagnostic_PreservesCleanDiagnostics proves the sanitizer does not
// mangle ordinary diagnostics that contain no credential-like content, so real
// remediation text still reaches the user. It also proves non-secret
// environment values (PATH, HOME) are never scrubbed.
func TestSanitizeDiagnostic_PreservesCleanDiagnostics(t *testing.T) {
	withEnv(t, []string{"PATH=/usr/local/bin:/usr/bin", "HOME=/Users/dev"})

	clean := []string{
		"could not list OpenCode models: exit status 1",
		"no OpenCode models available; no provider is configured",
		"OpenCode prompt ended without completing (stop reason \"max_tokens\")",
		"OpenCode refused to complete the request",
		"Run 'opencode auth login' to configure a provider",
		"timed out listing OpenCode models",
	}
	for _, in := range clean {
		if out := sanitizeDiagnostic(in); out != in {
			t.Errorf("sanitizeDiagnostic(%q) = %q, want unchanged", in, out)
		}
	}

	// A non-secret environment value appearing verbatim in a diagnostic must
	// survive — only credential-bearing variables are scrubbed.
	withPath := "resolved opencode at /usr/local/bin/opencode"
	if out := sanitizeDiagnostic(withPath); out != withPath {
		t.Errorf("sanitizeDiagnostic scrubbed a non-secret env value: %q", out)
	}
}

// TestSanitizeDiagnostic_EmptyInput proves the sanitizer is a no-op on empty
// input so callers can pass it through unconditionally.
func TestSanitizeDiagnostic_EmptyInput(t *testing.T) {
	if out := sanitizeDiagnostic(""); out != "" {
		t.Fatalf("sanitizeDiagnostic(\"\") = %q, want empty", out)
	}
}

// TestStripTerminalControls_RemovesEscapesAndControlBytes proves ANSI escape
// sequences and C0/C1 control bytes are removed before any catalog entry,
// progress update, warning, log line, cache artifact, or transcript can carry
// them. Structural whitespace (space, tab, newline, carriage return) is
// preserved so multi-line diagnostics stay readable.
func TestStripTerminalControls_RemovesEscapesAndControlBytes(t *testing.T) {
	cases := map[string]string{
		"plain text":                     "plain text",
		"":                               "",
		"red \x1b[31mDANGER\x1b[0m text": "red DANGER text",
		"clear\x1b[2J\x1b[Hscreen":       "clearscreen",
		"osc \x1b]0;evil title\x07done":  "osc done",
		"bell\x07and\x08backspace":       "bellandbackspace",
		"keep\ttab\nnewline\rcr":         "keep\ttab\nnewline\rcr",
		"del\x7fchar":                    "delchar",
		"c1\x9bcontrol":                  "c1control",
	}
	for in, want := range cases {
		if got := stripTerminalControls(in); got != want {
			t.Errorf("stripTerminalControls(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSanitizeCatalogText_SingleLineNoControls proves catalog-facing text
// (display names, aliases) is reduced to a clean single line: escapes and
// control bytes gone, all whitespace runs collapsed to a single space, and
// surrounding whitespace trimmed.
func TestSanitizeCatalogText_SingleLineNoControls(t *testing.T) {
	cases := map[string]string{
		"Claude Sonnet 4.5":                       "Claude Sonnet 4.5",
		"  GPT-5  ":                                "GPT-5",
		"line\x1b[31mone\ntwo\tthree":              "lineone two three",
		"name\x07with\x08controls":                 "namewithcontrols",
		"\x1b]0;title\x07Gemma 4":                  "Gemma 4",
		"multi   space":                            "multi space",
		"":                                         "",
		"\x1b[2K\r":                                "",
	}
	for in, want := range cases {
		if got := sanitizeCatalogText(in); got != want {
			t.Errorf("sanitizeCatalogText(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSanitizeDiagnostic_DropsTerminalControls proves diagnostics also lose
// terminal-control content (so a malicious CLI error cannot inject escape
// sequences into stderr, logs, the cache, or captured evidence) while the
// remaining human-readable text and existing credential redaction still apply.
func TestSanitizeDiagnostic_DropsTerminalControls(t *testing.T) {
	withEnv(t, []string{"PATH=/usr/bin"})
	in := "could not list OpenCode models: \x1b[31mboom\x1b[0m token=tok_live_CTRLLEAK0987654321\x07"
	out := sanitizeDiagnostic(in)
	if strings.ContainsRune(out, '\x1b') || strings.ContainsRune(out, '\x07') {
		t.Fatalf("sanitizeDiagnostic left terminal-control bytes: %q", out)
	}
	if strings.Contains(out, "tok_live_CTRLLEAK0987654321") {
		t.Fatalf("sanitizeDiagnostic leaked a credential: %q", out)
	}
	if !strings.Contains(out, "could not list OpenCode models") || !strings.Contains(out, "boom") {
		t.Fatalf("sanitizeDiagnostic lost readable text: %q", out)
	}
}

// TestRPCErrorDetail_OmitsDataAndRedactsMessage proves a JSON-RPC error object's
// structured `data` member (the usual home for provider config or credentials)
// is omitted entirely, while a credential embedded in the human-readable message
// is redacted. The code is preserved for diagnosis.
func TestRPCErrorDetail_OmitsDataAndRedactsMessage(t *testing.T) {
	withEnv(t, []string{"PATH=/usr/bin"})
	raw := []byte(`{"code":-32000,"message":"auth failed for apiKey sk-ant-MSGLEAK111222333","data":{"provider":{"anthropic":{"options":{"apiKey":"sk-ant-DATALEAK444555666"}}}}}`)
	got := rpcErrorDetail(raw)

	for _, leak := range []string{"sk-ant-MSGLEAK111222333", "sk-ant-DATALEAK444555666", "DATALEAK", "options"} {
		if strings.Contains(got, leak) {
			t.Fatalf("rpcErrorDetail leaked %q in %q", leak, got)
		}
	}
	if !strings.Contains(got, "-32000") {
		t.Fatalf("rpcErrorDetail dropped the error code: %q", got)
	}
}
