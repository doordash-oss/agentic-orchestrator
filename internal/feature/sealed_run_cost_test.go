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

package feature

import (
	"testing"
	"time"
)

func TestStore_ModifySealedRunRecordsSessionCost(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		run     int
		wantErr bool
	}{
		{name: "sealed run accepts the row", run: 1},
		{name: "active run is refused", run: 2, wantErr: true},
		{name: "missing run is refused", run: 3, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := NewStore(t.TempDir())
			id := "feat-sealed-cost"
			_ = seedFeatureForSealFork(t, store, id)
			if _, err := store.SealAndForkRun(id,
				func(oldRun *Run) error {
					now := time.Now()
					oldRun.SealedAt = &now
					oldRun.SealReason = SealReasonRewind
					return nil
				},
				func(oldRun *Run) (*Run, error) {
					return &Run{RunNumber: 2, Committing: true}, nil
				},
				nil,
			); err != nil {
				t.Fatalf("SealAndForkRun: %v", err)
			}

			err := store.ModifySealedRun(id, tt.run, func(r *Run) error {
				r.RecordSessionCost(SessionCostRecord{SessionID: "s1", PhaseKey: "phase-1-impl", CostUSD: 0.25})
				return nil
			})
			if (err != nil) != tt.wantErr {
				t.Fatalf("ModifySealedRun err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			run, err := store.LoadRun(id, tt.run)
			if err != nil {
				t.Fatal(err)
			}
			if !run.IsSealed() || len(run.SessionCosts) != 1 || run.PhaseCosts["phase-1-impl"] != 0.25 {
				t.Fatalf("sealed run = sealed %v, rows %+v, costs %+v", run.IsSealed(), run.SessionCosts, run.PhaseCosts)
			}
		})
	}
}
