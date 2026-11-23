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

package agent

import (
	"testing"
)

// stubSandboxUnavailable makes the cached probe report an unusable sandbox
// for the duration of the test.
func stubSandboxUnavailable(t *testing.T, reason string) {
	t.Helper()
	setSandboxProbeForTest(func() SandboxStatus { return SandboxStatus{Unavailable: true, Reason: reason} })
	t.Cleanup(func() { setSandboxProbeForTest(nil) })
}

func TestSandboxAvailabilityProbesOncePerProcess(t *testing.T) {
	calls := 0
	setSandboxProbeForTest(func() SandboxStatus { calls++; return SandboxStatus{Unavailable: true, Reason: "x"} })
	t.Cleanup(func() { setSandboxProbeForTest(nil) })
	for range 3 {
		if status := SandboxAvailability(); !status.Unavailable || status.Reason != "x" {
			t.Fatalf("SandboxAvailability() = %+v; want stubbed unavailable", status)
		}
	}
	if calls != 1 {
		t.Fatalf("probe calls = %d; want 1", calls)
	}
}

func TestRecordSandboxUnavailableKeepsFirstRunRecord(t *testing.T) {
	store, id := newTestStoreWithFeature(t, nil, nil)
	recordSandboxUnavailable(store, id, "first")
	recordSandboxUnavailable(store, id, "second")
	f, err := store.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Run().Sandbox; got == nil || got.Available || got.Reason != "first" {
		t.Fatalf("run sandbox = %+v; want {available:false reason:first}", got)
	}
}
