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
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
)

func beginSSE(w http.ResponseWriter) (http.Flusher, bool) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, errcat.InternalError, errcat.WithDiagnostics("streaming unavailable"))
		return nil, false
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	setStreamWriteDeadline(w)
	return flusher, true
}

func streamCursor(r *http.Request, queryKey string) string {
	raw := r.URL.Query().Get(queryKey)
	if raw == "" {
		raw = r.Header.Get("Last-Event-ID")
	}
	return raw
}

func writeSSEFrame(w http.ResponseWriter, event, id string, includeID bool, data any) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	setStreamWriteDeadline(w)
	if includeID {
		_, err = fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", id, event, payload)
	} else {
		_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, payload)
	}
	return err
}
