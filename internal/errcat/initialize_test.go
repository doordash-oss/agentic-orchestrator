// Copyright 2026 DoorDash, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package errcat

import "testing"

// initializeCodes are the explicit-initialization refusal codes. Selector
// and identity failures are blocking (the request cannot proceed against
// the current catalog); content and in-progress-operation refusals are
// needs-action because the user can resolve them outside Agentico and
// retry; availability failures are blocking server-side conditions.
var initializeCodes = []Code{
	InitializeRepositoryNotFound,
	InitializeIdentityStale,
	InitializeContentPresent,
	InitializeOperationActive,
	InitializeUnavailable,
}

func TestInitializeCodesContract(t *testing.T) {
	needsAction := map[Code]bool{
		InitializeContentPresent:  true,
		InitializeOperationActive: true,
	}
	for _, code := range initializeCodes {
		entry, ok := Lookup(code)
		if !ok {
			t.Fatalf("%s: missing from catalog", code)
		}
		want := ClassBlocking
		if needsAction[code] {
			want = ClassNeedsAction
		}
		if entry.Class != want {
			t.Errorf("%s: class is %q; want %q", code, entry.Class, want)
		}
		if entry.Title == "" || entry.Summary == "" || entry.Remediation == "" {
			t.Errorf("%s: incomplete entry %#v", code, entry)
		}
		if len(entry.Actions) != 0 {
			t.Errorf("%s: unexpected actions %#v", code, entry.Actions)
		}
	}
}
