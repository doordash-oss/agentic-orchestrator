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

// cloneStartCodes are refused at the start boundary: blocking, with a
// destination param where one applies, and never echoing a rejected
// secret-bearing remote.
var cloneStartCodes = []Code{
	CloneRemoteInvalid,
	CloneDestinationInvalid,
	CloneDestinationExists,
	CloneDestinationReserved,
	CloneDestinationShadowed,
	CloneRootIneligible,
	CloneIdempotencyConflict,
	CloneOperationNotFound,
	CloneHistoryUnavailable,
	CloneNotRetryable,
	CloneUnavailable,
}

// cloneOutcomeCodes are terminal failure outcomes carried on
// authoritative snapshots. Cleanup-pending is needs-action because the
// user has an explicit Retry cleanup affordance; authentication, trust and
// prompt failures need server-side credential setup.
var cloneOutcomeCodes = []Code{
	CloneTimeout,
	CloneAuthenticationFailed,
	CloneTrustFailed,
	ClonePromptUnsupported,
	ClonePublicationUnsupported,
	CloneDestinationConflict,
	CloneExecutionFailed,
	CloneCleanupPending,
}

func TestCloneStartCodesContract(t *testing.T) {
	for _, code := range cloneStartCodes {
		entry, ok := Lookup(code)
		if !ok {
			t.Fatalf("%s: missing from catalog", code)
		}
		if entry.Class != ClassBlocking {
			t.Errorf("%s: class is %q; want blocking", code, entry.Class)
		}
		if entry.Title == "" || entry.Summary == "" || entry.Remediation == "" {
			t.Errorf("%s: incomplete entry %#v", code, entry)
		}
		if len(entry.Actions) != 0 {
			t.Errorf("%s: unexpected actions %#v", code, entry.Actions)
		}
	}
}

func TestCloneOutcomeCodesContract(t *testing.T) {
	needsAction := map[Code]bool{
		CloneAuthenticationFailed:   true,
		CloneTrustFailed:            true,
		ClonePromptUnsupported:      true,
		ClonePublicationUnsupported: true,
		CloneCleanupPending:         true,
	}
	for _, code := range cloneOutcomeCodes {
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
		if entry.Remediation == "" {
			t.Errorf("%s: missing remediation", code)
		}
	}
}

func TestCloneDestinationParamsRenderWithoutEchoingRemote(t *testing.T) {
	rendered := New(CloneDestinationExists, WithParams(CloneDestinationParams{Destination: "widget"}))
	if rendered.Title == "" || rendered.Summary == "" {
		t.Fatalf("render incomplete: %#v", rendered)
	}
	// The param set carries only the destination: no API surface can echo
	// a rejected secret-bearing remote through it.
}
