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
	"strings"
	"time"
)

type HTTPMetrics interface {
	ObserveHTTPRequest(route, method string, status int, duration time.Duration, requestBytes, responseBytes int64)
}

type metricResponseWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *metricResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *metricResponseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytes += int64(n)
	return n, err
}
func (w *metricResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func withHTTPMetrics(next http.Handler, metrics HTTPMetrics) http.Handler {
	if metrics == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route, ok := telemetryRoute(r)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		started := time.Now()
		mw := &metricResponseWriter{ResponseWriter: w}
		next.ServeHTTP(mw, r)
		status := mw.status
		if status == 0 {
			status = http.StatusOK
		}
		metrics.ObserveHTTPRequest(route, r.Method, status, time.Since(started), r.ContentLength, mw.bytes)
	})
}

func telemetryRoute(r *http.Request) (string, bool) {
	if r.Method == http.MethodOptions {
		return "", false
	}
	path := r.URL.EscapedPath()
	if path == apiPathHealth || path == apiPathEvents || strings.HasSuffix(path, "/output/stream") {
		return "", false
	}
	parts := strings.Split(path, "/")
	if len(parts) < 4 || parts[1] != "api" || parts[2] != "v1" {
		return "/{other}", true
	}
	switch parts[3] {
	case "features":
		if len(parts) > 4 {
			parts[4] = "{feature_id}"
			if len(parts) > 6 {
				switch parts[5] {
				case "actions":
					parts[6] = "{action}"
					if len(parts) > 7 {
						parts[7] = "{subaction}"
						parts = parts[:8]
					}
				case "reviews":
					parts[6] = "{review_id}"
					if len(parts) > 7 {
						if parts[7] != "draft" && parts[7] != "validate" && parts[7] != "decision" {
							parts[7] = "{other}"
						}
						parts = parts[:8]
					}
				case "repositories":
					parts[6] = "{repo_name}"
					if len(parts) > 7 {
						if parts[7] != "diff" && parts[7] != "path" {
							parts[7] = "{other}"
						}
						parts = parts[:8]
					}
				case "runs":
					parts[6] = "{run_number}"
					if len(parts) > 8 && parts[7] == "artifacts" {
						parts[8] = "{artifact_id}"
						parts = parts[:9]
					} else if len(parts) > 8 && parts[7] == "logs" {
						parts[8] = "{log_id}"
						parts = parts[:9]
					} else if len(parts) > 7 && parts[7] != "artifacts" && parts[7] != "logs" && parts[7] != "sessions" {
						parts[7] = "{other}"
						parts = parts[:8]
					}
				}
			}
			if len(parts) > 5 && !knownFeatureRouteSegment(parts[5]) {
				parts[5] = "{other}"
				parts = parts[:6]
			}
			if len(parts) > 6 && parts[5] != "actions" && parts[5] != "reviews" && parts[5] != "repositories" && parts[5] != "runs" {
				valid := (parts[5] == "completion" && parts[6] == "preflight") || (parts[5] == "rewind" && parts[6] == "preview")
				if !valid {
					parts[6] = "{other}"
				}
				parts = parts[:7]
			}
		}
	case "sessions":
		if len(parts) > 4 {
			parts[4] = "{session_id}"
			if len(parts) > 5 && parts[5] != "transcript" && parts[5] != "output" {
				parts[5] = "{other}"
				parts = parts[:6]
			} else if len(parts) > 6 {
				parts[6] = "{other}"
				parts = parts[:7]
			}
		}
	case "prompts", "permissions":
		if len(parts) > 4 {
			parts[4] = "{action}"
			parts = parts[:5]
		}
	default:
		if !knownStaticTelemetryRoute(path) {
			return "/api/v1/{other}", true
		}
	}
	return strings.Join(parts, "/"), true
}

func knownFeatureRouteSegment(segment string) bool {
	switch segment {
	case "actions", "completion", "config", "live-preview", "repositories", "reviews", "rewind", "runs":
		return true
	default:
		return false
	}
}

func knownStaticTelemetryRoute(path string) bool {
	switch path {
	case apiPathConfigRuntime, apiPathCatalogModels, apiPathCatalogRefresh,
		apiPathReadiness, apiPathReadinessRefresh, apiPathWorkspaceRepositoriesInit,
		apiPathRecovery, apiPathRecoveryActions, apiPathRecoveryLogs, apiPathUploads:
		return true
	default:
		return false
	}
}
