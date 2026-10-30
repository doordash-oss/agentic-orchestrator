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
)

type multiAxisReviewResult struct {
	Axis     string
	Status   reviewStatus
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

func strictMultiAxisReviewStatus(results []multiAxisReviewResult, selectedCount int) reviewStatus {
	approved := 0
	if len(results) < selectedCount {
		return reviewChangesRequested
	}
	for _, result := range results {
		if result.Error != nil {
			return reviewChangesRequested
		}
		switch result.Status {
		case ReviewApproved:
			approved++
		case reviewChangesRequested:
			return reviewChangesRequested
		default:
			return reviewChangesRequested
		}
	}
	if approved != selectedCount {
		return reviewChangesRequested
	}
	return ReviewApproved
}

func firstMultiAxisReviewError(results []multiAxisReviewResult) error {
	// A different axis's generic failure must not hide a known quota reset.
	for _, result := range results {
		if isProviderUsageLimitError(result.Error) {
			return result.Error
		}
	}
	for _, result := range results {
		if result.Error != nil {
			return result.Error
		}
	}
	return nil
}

func validatorResultsToMultiAxisResults(results []validatorResult) []multiAxisReviewResult {
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

// Validator live states recorded alongside verdicts in ValidatorStatuses.
const (
	validatorStateRunning     = "running"
	validatorStateRetrying    = "retrying"
	validatorStateError       = "error"
	validatorStateInterrupted = "interrupted"
)

// maxValidatorErrorLen bounds the last-error text kept per validator.
const maxValidatorErrorLen = 500

func setMultiAxisValidatorStatuses(store FeatureStore, featureID string, axes []string) {
	if store == nil || featureID == "" {
		return
	}
	_ = store.Modify(featureID, func(f *feature.Feature) error {
		f.ValidatorStatuses = make(map[string]string, len(axes))
		f.ValidatorErrors = nil
		for _, axis := range axes {
			f.ValidatorStatuses[axis] = validatorStateRunning
		}
		return nil
	})
}

// updateMultiAxisValidatorStatus records a validator's terminal state: its
// verdict, "interrupted" for a stopped helper, or "error" plus the last error.
func updateMultiAxisValidatorStatus(store FeatureStore, featureID, axis string, status reviewStatus, err error) {
	switch {
	case err == nil:
		setMultiAxisValidatorState(store, featureID, axis, status.String(), "")
	case isHelperInterruptedError(err):
		setMultiAxisValidatorState(store, featureID, axis, validatorStateInterrupted, "")
	default:
		setMultiAxisValidatorState(store, featureID, axis, validatorStateError, err.Error())
	}
}

// markMultiAxisValidatorRetrying shows a validator as retrying after a
// failed attempt, keeping that attempt's error visible.
func markMultiAxisValidatorRetrying(store FeatureStore, featureID, axis string, err error) {
	detail := ""
	if err != nil {
		detail = err.Error()
	}
	setMultiAxisValidatorState(store, featureID, axis, validatorStateRetrying, detail)
}

func setMultiAxisValidatorState(store FeatureStore, featureID, axis, state, lastError string) {
	if store == nil || featureID == "" {
		return
	}
	_ = store.Modify(featureID, func(f *feature.Feature) error {
		if f.ValidatorStatuses == nil {
			f.ValidatorStatuses = make(map[string]string)
		}
		f.ValidatorStatuses[axis] = state
		if lastError == "" {
			delete(f.ValidatorErrors, axis)
			if len(f.ValidatorErrors) == 0 {
				f.ValidatorErrors = nil
			}
			return nil
		}
		if len(lastError) > maxValidatorErrorLen {
			lastError = lastError[:maxValidatorErrorLen] + "…"
		}
		if f.ValidatorErrors == nil {
			f.ValidatorErrors = make(map[string]string)
		}
		f.ValidatorErrors[axis] = lastError
		return nil
	})
}

func clearMultiAxisValidatorStatuses(store FeatureStore, featureID string) {
	if store == nil || featureID == "" {
		return
	}
	_ = store.Modify(featureID, func(f *feature.Feature) error {
		f.ValidatorStatuses = nil
		f.ValidatorErrors = nil
		return nil
	})
}
