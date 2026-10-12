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

package git

import (
	"strings"
	"testing"
)

func TestInjectPRSignature(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantSig  bool
		wantBody string
	}{
		{
			name:    "empty body gets signature",
			body:    "",
			wantSig: true,
		},
		{
			name:    "normal body gets signature appended",
			body:    "## Summary\n\nSome changes.",
			wantSig: true,
		},
		{
			name:    "body mentioning agenticURL but without signature still gets signed",
			body:    "Already has https://github.com/doordash-oss/agentic-orchestrator in it",
			wantSig: true,
		},
		{
			name:     "body already containing full signature is unchanged",
			body:     "Some PR body" + pRSignature,
			wantSig:  false,
			wantBody: "Some PR body" + pRSignature,
		},
		{
			name:    "body with multiple sections gets signature appended",
			body:    "## Summary\n\nChanges.\n\n## Test Plan\n\n- Ran the full suite.",
			wantSig: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := injectPRSignature(tt.body)

			if tt.wantSig {
				if !strings.Contains(got, agenticURL) {
					t.Errorf("expected body to contain agenticURL, got: %q", got)
				}
				if !strings.HasSuffix(got, pRSignature) {
					t.Errorf("expected body to end with pRSignature, got: %q", got)
				}
				// Verify it starts with the original body
				if !strings.HasPrefix(got, tt.body) {
					t.Errorf("expected body to start with original content %q, got: %q", tt.body, got)
				}
			} else {
				if tt.wantBody != "" && got != tt.wantBody {
					t.Errorf("expected body %q, got: %q", tt.wantBody, got)
				}
			}
		})
	}
}

func TestInjectPRSignature_Idempotent(t *testing.T) {
	body := "## Summary\n\nSome changes."
	first := injectPRSignature(body)
	second := injectPRSignature(first)

	if first != second {
		t.Errorf("injectPRSignature is not idempotent:\nfirst:  %q\nsecond: %q", first, second)
	}
}

func TestConstants(t *testing.T) {
	wantTrailer := "Co-authored-by: Agentico <noreply@doordash-oss.github.com>"
	if commitSignatureTrailer != wantTrailer {
		t.Errorf("commitSignatureTrailer = %q, want %q (Co-authored-by convention so GitHub renders Agentico as a co-author)", commitSignatureTrailer, wantTrailer)
	}
	if !strings.Contains(pRSignature, agenticURL) {
		t.Errorf("pRSignature should contain agenticURL")
	}
	if !strings.Contains(pRSignature, "Generated with [agentic orchestrator]") {
		t.Errorf("pRSignature should contain 'Generated with [agentic orchestrator]'")
	}
	if strings.Contains(pRSignature, "Generated with [Agentic]") {
		t.Errorf("pRSignature should not contain legacy 'Generated with [Agentic]' label")
	}
}
