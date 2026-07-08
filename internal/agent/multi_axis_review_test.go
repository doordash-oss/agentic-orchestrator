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
	"errors"
	"testing"
)

func TestStrictMultiAxisReviewStatus(t *testing.T) {
	errBoom := errors.New("boom")
	tests := []struct {
		name          string
		results       []multiAxisReviewResult
		selectedCount int
		want          ReviewStatus
	}{
		{
			name: "all selected axes approved",
			results: []multiAxisReviewResult{
				{Axis: "Craft", Status: ReviewApproved},
				{Axis: "Functionality/Evidence", Status: ReviewApproved},
				{Axis: "Cleanliness", Status: ReviewApproved},
			},
			selectedCount: 3,
			want:          ReviewApproved,
		},
		{
			name: "any changes requested blocks",
			results: []multiAxisReviewResult{
				{Axis: "Craft", Status: ReviewApproved},
				{Axis: "Functionality/Evidence", Status: ReviewChangesRequested},
				{Axis: "Cleanliness", Status: ReviewApproved},
			},
			selectedCount: 3,
			want:          ReviewChangesRequested,
		},
		{
			name: "any error blocks",
			results: []multiAxisReviewResult{
				{Axis: "Craft", Status: ReviewApproved},
				{Axis: "Functionality/Evidence", Status: ReviewApproved, Error: errBoom},
				{Axis: "Cleanliness", Status: ReviewApproved},
			},
			selectedCount: 3,
			want:          ReviewChangesRequested,
		},
		{
			name: "fewer results than selected axes blocks",
			results: []multiAxisReviewResult{
				{Axis: "Craft", Status: ReviewApproved},
				{Axis: "Functionality/Evidence", Status: ReviewApproved},
			},
			selectedCount: 3,
			want:          ReviewChangesRequested,
		},
		{
			name: "unknown status blocks",
			results: []multiAxisReviewResult{
				{Axis: "Craft", Status: ReviewApproved},
				{Axis: "Functionality/Evidence", Status: ReviewStatus(99)},
				{Axis: "Cleanliness", Status: ReviewApproved},
			},
			selectedCount: 3,
			want:          ReviewChangesRequested,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := strictMultiAxisReviewStatus(tt.results, tt.selectedCount)
			if got != tt.want {
				t.Fatalf("strictMultiAxisReviewStatus(%v, %d) = %s, want %s", tt.results, tt.selectedCount, got, tt.want)
			}
		})
	}
}
