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
	"sync"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

type multiAxisReviewResult struct {
	Axis     string
	Status   ReviewStatus
	Feedback string
	Error    error
}

func runMultiAxisReviews(count int, run func(int)) {
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			run(i)
		}(i)
	}
	wg.Wait()
}

func strictMultiAxisReviewStatus(results []multiAxisReviewResult, selectedCount int) ReviewStatus {
	approved := 0
	if len(results) < selectedCount {
		return ReviewChangesRequested
	}
	for _, result := range results {
		if result.Error != nil {
			return ReviewChangesRequested
		}
		switch result.Status {
		case ReviewApproved:
			approved++
		case ReviewChangesRequested:
			return ReviewChangesRequested
		default:
			return ReviewChangesRequested
		}
	}
	if approved != selectedCount {
		return ReviewChangesRequested
	}
	return ReviewApproved
}

func firstMultiAxisReviewError(results []multiAxisReviewResult) error {
	for _, result := range results {
		if result.Error != nil {
			return result.Error
		}
	}
	return nil
}

func validatorResultsToMultiAxisResults(results []ValidatorResult) []multiAxisReviewResult {
	out := make([]multiAxisReviewResult, 0, len(results))
	for _, result := range results {
		out = append(out, multiAxisReviewResult{
			Axis:     result.Domain,
			Status:   result.Status,
			Feedback: result.Feedback,
			Error:    result.Error,
		})
	}
	return out
}

func setMultiAxisValidatorStatuses(store ports.FeatureStore, featureID string, axes []string) {
	if store == nil || featureID == "" {
		return
	}
	_ = store.Modify(featureID, func(f *feature.Feature) error {
		f.ValidatorStatuses = make(map[string]string, len(axes))
		for _, axis := range axes {
			f.ValidatorStatuses[axis] = "running"
		}
		return nil
	})
}

func updateMultiAxisValidatorStatus(store ports.FeatureStore, featureID, axis string, status ReviewStatus, err error) {
	if store == nil || featureID == "" {
		return
	}
	_ = store.Modify(featureID, func(f *feature.Feature) error {
		if f.ValidatorStatuses == nil {
			f.ValidatorStatuses = make(map[string]string)
		}
		if err != nil {
			f.ValidatorStatuses[axis] = "error"
		} else {
			f.ValidatorStatuses[axis] = status.String()
		}
		return nil
	})
}

func clearMultiAxisValidatorStatuses(store ports.FeatureStore, featureID string) {
	if store == nil || featureID == "" {
		return
	}
	_ = store.Modify(featureID, func(f *feature.Feature) error {
		f.ValidatorStatuses = nil
		return nil
	})
}
