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

package orchestrator

import (
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/workadmission"
)

func TestFeatureAdmissionsAsyncTailWithoutDispatchReleases(t *testing.T) {
	coord := workadmission.New(workadmission.Options{})
	a := newFeatureAdmissions(coord)
	never := func() bool { return false }

	// Child setup: the continuation finishes without launching a phase.
	if err := a.beginAsync("child"); err != nil {
		t.Fatal(err)
	}
	a.settleIfQuiet("child", never)
	if n, _ := coord.Held(); n != 1 {
		t.Fatalf("settle must not release a pending continuation, held=%d", n)
	}
	a.endAsync("child", never)
	if n, held := coord.Held(); n != 0 {
		t.Fatalf("continuation credit leaked: held=%v", held)
	}
	if !coord.CloseIfQuiesced() {
		t.Fatal("admission should quiesce once the tail ends")
	}
}

func TestFeatureAdmissionsAsyncTailDispatchingPhaseKeepsUntilQuiet(t *testing.T) {
	coord := workadmission.New(workadmission.Options{})
	a := newFeatureAdmissions(coord)
	running := true
	owns := func() bool { return running }

	if err := a.beginAsync("f"); err != nil {
		t.Fatal(err)
	}
	if err := a.launch("f"); err != nil {
		t.Fatal(err)
	}
	a.endAsync("f", owns)
	if n, _ := coord.Held(); n != 1 {
		t.Fatalf("running phase must keep the reservation, held=%d", n)
	}
	running = false
	a.settleIfQuiet("f", owns)
	if n, _ := coord.Held(); n != 0 {
		t.Fatalf("reservation should release once quiet, held=%d", n)
	}
}

func TestFeatureAdmissionsLaunchDoesNotConsumeContinuation(t *testing.T) {
	coord := workadmission.New(workadmission.Options{})
	a := newFeatureAdmissions(coord)
	never := func() bool { return false }

	if err := a.beginAsync("f"); err != nil {
		t.Fatal(err)
	}
	if err := a.launch("f"); err != nil {
		t.Fatal(err)
	}
	// The launched phase completed but the continuation is still in flight.
	a.settleIfQuiet("f", never)
	if n, _ := coord.Held(); n != 1 {
		t.Fatalf("in-flight continuation must hold, held=%d", n)
	}
	a.endAsync("f", never)
	if n, _ := coord.Held(); n != 0 {
		t.Fatalf("held=%d after tail end", n)
	}
}
