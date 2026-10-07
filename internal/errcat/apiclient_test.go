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

package errcat

import (
	"strings"
	"testing"
)

// TestAPIClientCodesPinShape pins the authored contract of the `agentico
// api` helper codes: blocking CLI failures with no actions or context
// blocks and an authored remediation hint.
func TestAPIClientCodesPinShape(t *testing.T) {
	for _, code := range []Code{DiscoveryMissing, DiscoveryUntrusted, ServerUnreachable} {
		entry, ok := Lookup(code)
		if !ok {
			t.Fatalf("%s: missing from catalog", code)
		}
		if entry.Class != ClassBlocking {
			t.Errorf("%s: class is %q; want blocking", code, entry.Class)
		}
		if len(entry.Actions) != 0 || len(entry.Blocks) != 0 {
			t.Errorf("%s: actions %#v blocks %#v; want none", code, entry.Actions, entry.Blocks)
		}
		if entry.Remediation == "" {
			t.Errorf("%s: needs a remediation hint", code)
		}
	}
}

// TestAPIClientCodesNameRuntimeDir pins that summary and remediation name
// the runtime directory the helper searched, and that zero-value params
// degrade to the authored static text.
func TestAPIClientCodesNameRuntimeDir(t *testing.T) {
	const dir = "/home/me/.agentic-orchestrator"
	for _, code := range []Code{DiscoveryMissing, DiscoveryUntrusted, ServerUnreachable} {
		rendered := New(code, WithParams(RuntimeDirParams{RuntimeDir: dir}))
		if !strings.Contains(rendered.Summary, dir) {
			t.Errorf("%s: summary %q does not name %s", code, rendered.Summary, dir)
		}
		if rendered.Remediation == nil || !strings.Contains(rendered.Remediation.Hint, dir) {
			t.Errorf("%s: remediation %#v does not name %s", code, rendered.Remediation, dir)
		}

		entry, _ := Lookup(code)
		zero := New(code, WithParams(RuntimeDirParams{}))
		if zero.Summary != entry.Summary || zero.Remediation == nil || zero.Remediation.Hint != entry.Remediation {
			t.Errorf("%s: zero-value render = %#v; want the authored static text", code, zero)
		}

		override := New(code, WithParams(RuntimeDirParams{RuntimeDir: dir}), WithRemediationHint("caller hint"))
		if override.Remediation == nil || override.Remediation.Hint != "caller hint" {
			t.Errorf("%s: caller hint did not win over the templated remediation: %#v", code, override.Remediation)
		}
	}
}
