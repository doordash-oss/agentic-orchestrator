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

// Package testsupport provides a reusable scripted Slack Web API server.
package testsupport

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

const rawUploadMethod = "upload"

// Response is one queued Slack method response.
type Response struct {
	Status  int
	Body    any
	Headers http.Header
	Delay   time.Duration
	Started chan<- struct{}
	Release <-chan struct{}
}

// Request is a credential-scrubbed record of one received request.
type Request struct {
	Method         string
	Path           string
	BearerPresent  bool
	ContentType    string
	ContentLength  int64
	Digest         string
	Fields         map[string]any
	ReturnedTS     string
	ReturnedFileID string
}

// Server serves per-method scripted responses and records requests.
type Server struct {
	server *httptest.Server

	mu       sync.Mutex
	scripts  map[string][]Response
	requests []Request
	nextFile int64
	// defaultResponder answers unscripted calls (empty or missing method
	// queue) so lifecycle tests need not pre-count posts. Nil keeps the
	// unknown_method fallback.
	defaultResponder func(method string, request Request) Response
}

// New starts a server and registers cleanup with t.
func New(t testing.TB) *Server {
	t.Helper()
	server := NewServer()
	t.Cleanup(server.Close)
	return server
}

// NewServer starts a server whose lifecycle is controlled by the caller.
func NewServer() *Server {
	server := &Server{scripts: make(map[string][]Response)}
	server.server = httptest.NewServer(http.HandlerFunc(server.serveHTTP))
	return server
}

// SetDefault installs the per-method responder used when a method's script
// queue is empty, so lifecycle tests need not pre-count posts.
func (s *Server) SetDefault(responder func(method string, request Request) Response) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.defaultResponder = responder
}

// URL returns a Slack-compatible API base URL.
func (s *Server) URL() string {
	return s.server.URL + "/api/"
}

// Close stops the server.
func (s *Server) Close() {
	s.server.Close()
}

// Script appends responses to a Slack method queue.
func (s *Server) Script(method string, responses ...Response) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scripts[method] = append(s.scripts[method], responses...)
}

// CallCount returns the number of recorded calls for method.
func (s *Server) CallCount(method string) int {
	return len(s.Requests(method))
}

// Requests returns copies of recorded calls for method.
func (s *Server) Requests(method string) []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]Request, 0)
	for _, request := range s.requests {
		if slackMethod(request.Path) == method {
			result = append(result, cloneRequest(request))
		}
	}
	return result
}

// AllRequests returns copies of all recorded calls.
func (s *Server) AllRequests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]Request, len(s.requests))
	for i, request := range s.requests {
		result[i] = cloneRequest(request)
	}
	return result
}

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	method := slackMethod(r.URL.Path)
	contentType := r.Header.Get("Content-Type")
	if isRawUpload(method, contentType) {
		method = rawUploadMethod
	}
	request := Request{
		Method:        r.Method,
		Path:          r.URL.Path,
		BearerPresent: strings.HasPrefix(r.Header.Get("Authorization"), "Bearer "),
		ContentType:   contentType,
	}
	if method == rawUploadMethod {
		request.ContentLength, request.Digest = rawMetadata(r.Body)
		request.Fields = map[string]any{}
	} else {
		request.Fields = decodeFields(r)
	}

	s.mu.Lock()
	s.requests = append(s.requests, request)
	queue := s.scripts[method]
	var response Response
	if len(queue) > 0 {
		response = queue[0]
		s.scripts[method] = queue[1:]
	} else if uploadResponse, ok := s.defaultUploadResponse(method, request); ok {
		response = uploadResponse
	} else if s.defaultResponder != nil {
		response = s.defaultResponder(method, cloneRequest(request))
	} else {
		response = Response{
			Status: http.StatusOK,
			Body:   map[string]any{"ok": false, "error": "unknown_method"},
		}
	}
	request.ReturnedTS, request.ReturnedFileID = returnedMetadata(response.Body)
	s.requests[len(s.requests)-1].ReturnedTS = request.ReturnedTS
	s.requests[len(s.requests)-1].ReturnedFileID = request.ReturnedFileID
	s.mu.Unlock()

	if response.Started != nil {
		select {
		case response.Started <- struct{}{}:
		case <-r.Context().Done():
			return
		}
	}
	if response.Release != nil {
		select {
		case <-response.Release:
		case <-r.Context().Done():
			return
		}
	}
	if response.Delay > 0 {
		timer := time.NewTimer(response.Delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-r.Context().Done():
			return
		}
	}
	for key, values := range response.Headers {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	status := response.Status
	if status == 0 {
		status = http.StatusOK
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	switch body := response.Body.(type) {
	case nil:
		_, _ = io.WriteString(w, "{}")
	case string:
		_, _ = io.WriteString(w, body)
	case []byte:
		_, _ = w.Write(body)
	default:
		_ = json.NewEncoder(w).Encode(body)
	}
}

func isRawUpload(method, contentType string) bool {
	if method == rawUploadMethod {
		return true
	}
	return contentType != "" &&
		!strings.HasPrefix(contentType, "application/json") &&
		!strings.HasPrefix(contentType, "application/x-www-form-urlencoded")
}

func (s *Server) defaultUploadResponse(method string, request Request) (Response, bool) {
	switch method {
	case "files.getUploadURLExternal":
		s.nextFile++
		fileID := "F" + fmt.Sprintf("%08d", s.nextFile)
		return Response{Body: map[string]any{
			"ok":         true,
			"upload_url": s.server.URL + "/upload/" + fileID,
			"file_id":    fileID,
		}}, true
	case rawUploadMethod:
		return Response{Body: "OK"}, true
	case "files.completeUploadExternal":
		fileID := completionFileID(request.Fields["files"])
		return Response{Body: map[string]any{
			"ok": true,
			"files": []any{map[string]any{
				"id": fileID,
			}},
		}}, true
	default:
		return Response{}, false
	}
}

func rawMetadata(body io.Reader) (int64, string) {
	digest := sha256.New()
	length, _ := io.Copy(digest, body)
	return length, fmt.Sprintf("%x", digest.Sum(nil))
}

func completionFileID(value any) string {
	raw, _ := value.(string)
	var files []struct {
		ID string `json:"id"`
	}
	if json.Unmarshal([]byte(raw), &files) == nil && len(files) > 0 {
		return files[0].ID
	}
	return ""
}

func returnedMetadata(body any) (string, string) {
	fields, ok := body.(map[string]any)
	if !ok {
		return "", ""
	}
	timestamp, _ := fields["ts"].(string)
	if fileID, ok := fields["file_id"].(string); ok {
		return timestamp, fileID
	}
	if file, ok := fields["file"].(map[string]any); ok {
		fileID, _ := file["id"].(string)
		return timestamp, fileID
	}
	files, _ := fields["files"].([]any)
	if len(files) > 0 {
		if file, ok := files[0].(map[string]any); ok {
			fileID, _ := file["id"].(string)
			return timestamp, fileID
		}
	}
	return timestamp, ""
}

func decodeFields(r *http.Request) map[string]any {
	contentType := r.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "application/json") {
		var fields map[string]any
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&fields); err == nil {
			return fields
		}
		return map[string]any{}
	}
	_ = r.ParseForm()
	return formFields(r.Form)
}

func formFields(values url.Values) map[string]any {
	fields := make(map[string]any, len(values))
	for key, value := range values {
		if len(value) == 1 {
			fields[key] = value[0]
		} else {
			fields[key] = append([]string(nil), value...)
		}
	}
	return fields
}

func slackMethod(path string) string {
	if strings.HasPrefix(path, "/upload/") {
		return rawUploadMethod
	}
	return strings.TrimPrefix(path, "/api/")
}

func cloneRequest(request Request) Request {
	cloned := request
	cloned.Fields = make(map[string]any, len(request.Fields))
	for key, value := range request.Fields {
		cloned.Fields[key] = value
	}
	return cloned
}
