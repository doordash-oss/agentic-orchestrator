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

package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

func TestValidateEffortConfig(t *testing.T) {
	cases := []struct {
		name       string
		effort     config.EffortConfig
		models     config.ModelConfig
		reg        *llm.Registry
		wantOK     bool
		wantStatus int
	}{
		{
			name:       "auto-only model rejects explicit effort",
			effort:     config.EffortConfig{Implementation: "high"},
			models:     config.ModelConfig{Implementation: "auto-only-model"},
			reg:        testRegistryWithCaps("auto-only-model", nil),
			wantOK:     false,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "auto-only model accepts auto effort",
			effort:     config.EffortConfig{Implementation: "auto"},
			models:     config.ModelConfig{Implementation: "auto-only-model"},
			reg:        testRegistryWithCaps("auto-only-model", nil),
			wantOK:     true,
			wantStatus: http.StatusOK,
		},
		{
			name:       "unknown model rejects explicit effort",
			effort:     config.EffortConfig{Implementation: "high"},
			models:     config.ModelConfig{Implementation: "nonexistent-model"},
			reg:        testRegistryWithCaps("sonnet", []llm.EffortLevel{llm.EffortLow, llm.EffortMedium, llm.EffortHigh}),
			wantOK:     false,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "unknown model accepts auto effort",
			effort:     config.EffortConfig{Implementation: "auto"},
			models:     config.ModelConfig{Implementation: "nonexistent-model"},
			reg:        testRegistryWithCaps("sonnet", []llm.EffortLevel{llm.EffortLow, llm.EffortMedium, llm.EffortHigh}),
			wantOK:     true,
			wantStatus: http.StatusOK,
		},
		{
			name:       "capable model accepts supported effort",
			effort:     config.EffortConfig{Implementation: "high"},
			models:     config.ModelConfig{Implementation: "sonnet"},
			reg:        testRegistryWithCaps("sonnet", []llm.EffortLevel{llm.EffortLow, llm.EffortMedium, llm.EffortHigh}),
			wantOK:     true,
			wantStatus: http.StatusOK,
		},
		{
			name:       "capable model rejects unsupported effort",
			effort:     config.EffortConfig{Implementation: "max"},
			models:     config.ModelConfig{Implementation: "sonnet"},
			reg:        testRegistryWithCaps("sonnet", []llm.EffortLevel{llm.EffortLow, llm.EffortMedium, llm.EffortHigh}),
			wantOK:     false,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "nil registry accepts explicit effort",
			effort:     config.EffortConfig{Implementation: "high"},
			models:     config.ModelConfig{Implementation: "any-model"},
			reg:        nil,
			wantOK:     true,
			wantStatus: http.StatusOK,
		},
		{
			name:       "empty model accepts explicit effort",
			effort:     config.EffortConfig{Implementation: "high"},
			models:     config.ModelConfig{Implementation: ""},
			reg:        testRegistryWithCaps("sonnet", []llm.EffortLevel{llm.EffortLow, llm.EffortMedium, llm.EffortHigh}),
			wantOK:     true,
			wantStatus: http.StatusOK,
		},
		{
			name:       "empty effort config accepted",
			effort:     config.EffortConfig{},
			models:     config.ModelConfig{Implementation: "sonnet"},
			reg:        testRegistryWithCaps("sonnet", []llm.EffortLevel{llm.EffortLow, llm.EffortMedium, llm.EffortHigh}),
			wantOK:     true,
			wantStatus: http.StatusOK,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			ok := validateEffortConfig(rec, tc.effort, tc.models, tc.reg)
			if ok != tc.wantOK {
				t.Fatalf("validateEffortConfig = %v, want %v; status=%d body=%q", ok, tc.wantOK, rec.Code, rec.Body.String())
			}
			if !tc.wantOK && rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d, body=%q", rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}
}

func TestValidateAutomaticReviewMode(t *testing.T) {
	t.Parallel()

	valid := []string{"", "default", "enabled", "disabled"}
	for _, mode := range valid {
		mode := mode
		t.Run("accepts "+mode, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			if ok := validateAutomaticReviewMode(rec, &mode); !ok {
				t.Fatalf("validateAutomaticReviewMode(%q) = false; status=%d body=%q", mode, rec.Code, rec.Body.String())
			}
		})
	}

	t.Run("accepts omitted", func(t *testing.T) {
		t.Parallel()
		rec := httptest.NewRecorder()
		if ok := validateAutomaticReviewMode(rec, nil); !ok {
			t.Fatalf("validateAutomaticReviewMode(nil) = false; status=%d body=%q", rec.Code, rec.Body.String())
		}
	})

	t.Run("rejects invalid", func(t *testing.T) {
		t.Parallel()
		mode := "bogus"
		rec := httptest.NewRecorder()
		if ok := validateAutomaticReviewMode(rec, &mode); ok {
			t.Fatal("validateAutomaticReviewMode(bogus) = true, want false")
		}
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want %d, body=%q", rec.Code, http.StatusBadRequest, rec.Body.String())
		}
	})
}
