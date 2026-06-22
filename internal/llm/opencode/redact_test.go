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
