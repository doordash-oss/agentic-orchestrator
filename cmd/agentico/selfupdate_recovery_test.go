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

package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTolerableShutdownDeadline(t *testing.T) {
	t.Parallel()
	deadline := context.DeadlineExceeded
	other := errors.New("connection refused")
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, true},
		{"deadline only", deadline, true},
		{"joined deadlines", errors.Join(deadline, deadline), true},
		{"joined deadline and other", errors.Join(deadline, other), false},
		{"wrapped join of deadlines", fmt.Errorf("close: %w", errors.Join(deadline, deadline)), true},
		{"wrapped join with other leaf", fmt.Errorf("close: %w", errors.Join(deadline, other)), false},
		{"nested join of joins", errors.Join(errors.Join(deadline, deadline), deadline), true},
		{"nested join with other leaf", errors.Join(errors.Join(deadline, other), deadline), false},
		{"plain error", other, false},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tolerableShutdownDeadline(tc.err); got != tc.want {
				t.Fatalf("tolerableShutdownDeadline(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestWaitSelfHealthy(t *testing.T) {
	healthHandler := func(status int) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/v1/health" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(status)
		}
	}
	t.Run("healthy server returns nil", func(t *testing.T) {
		srv := httptest.NewServer(healthHandler(http.StatusOK))
		defer srv.Close()
		if err := waitSelfHealthy([]string{srv.URL}, time.Second); err != nil {
			t.Fatalf("waitSelfHealthy(200) = %v, want nil", err)
		}
	})
	t.Run("unhealthy status eventually errors", func(t *testing.T) {
		srv := httptest.NewServer(healthHandler(http.StatusInternalServerError))
		defer srv.Close()
		if err := waitSelfHealthy([]string{srv.URL}, 300*time.Millisecond); err == nil {
			t.Fatal("waitSelfHealthy(500) = nil, want error after the deadline")
		}
	})
	t.Run("connection refused errors", func(t *testing.T) {
		srv := httptest.NewServer(healthHandler(http.StatusOK))
		url := srv.URL
		srv.Close()
		if err := waitSelfHealthy([]string{url}, 300*time.Millisecond); err == nil {
			t.Fatal("waitSelfHealthy(refused) = nil, want error")
		}
	})
}
