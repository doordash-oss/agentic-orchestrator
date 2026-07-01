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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const MCPEndpointPath = "/mcp"

const (
	mcpAccessControlAllowHeaders  = "Content-Type, Mcp-Protocol-Version, Mcp-Session-Id, Mcp-Method, Mcp-Name, Last-Event-ID"
	mcpAccessControlExposeHeaders = "Mcp-Session-Id"
)

func (h *apiHandler) mcpHTTPHandler() http.Handler {
	server := h.newMCPServer()
	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{SessionTimeout: 30 * time.Minute},
	)
	return h.mcpBrowserOriginHandler(handler)
}

func (h *apiHandler) mcpBrowserOriginHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rejectMCPLocalhostProtection(w, r) {
			return
		}
		if h.handleMCPPreflight(w, r) {
			return
		}
		applyMCPCORS(w, r)
		next.ServeHTTP(w, r)
	})
}

func (h *apiHandler) handleMCPPreflight(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodOptions {
		return false
	}
	if rejectMCPLocalhostProtection(w, r) {
		return true
	}
	origin := r.Header.Get("Origin")
	if origin == "" || !isLoopbackOrigin(origin) {
		writeAPIError(w, http.StatusForbidden, "forbidden", "browser origin is not trusted", nil)
		return true
	}
	requestMethod := strings.ToUpper(strings.TrimSpace(r.Header.Get("Access-Control-Request-Method")))
	if !containsMethod([]string{http.MethodGet, http.MethodPost, http.MethodDelete}, requestMethod) {
		w.Header().Set("Allow", "GET, POST, DELETE")
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
		return true
	}
	if !isAllowedMCPPreflightHeaders(r.Header.Get("Access-Control-Request-Headers")) {
		writeAPIError(w, http.StatusForbidden, "forbidden", "MCP preflight headers are not trusted", nil)
		return true
	}
	w.Header().Set("Vary", "Origin, Access-Control-Request-Method, Access-Control-Request-Headers")
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Access-Control-Allow-Methods", requestMethod)
	w.Header().Set("Access-Control-Allow-Headers", mcpAccessControlAllowHeaders)
	w.WriteHeader(http.StatusNoContent)
	return true
}

func applyMCPCORS(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if origin != "" && isLoopbackOrigin(origin) {
		w.Header().Add("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Expose-Headers", mcpAccessControlExposeHeaders)
	}
}

func isAllowedMCPPreflightHeaders(raw string) bool {
	for _, part := range strings.Split(raw, ",") {
		header := strings.ToLower(strings.TrimSpace(part))
		if header == "" {
			continue
		}
		switch header {
		case "content-type", "mcp-protocol-version", "mcp-session-id", "mcp-method", "mcp-name", "last-event-id":
		default:
			return false
		}
	}
	return true
}

func rejectMCPLocalhostProtection(w http.ResponseWriter, r *http.Request) bool {
	localAddr, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok || localAddr == nil {
		return false
	}
	if isLoopbackHost(localAddr.String()) && !isLoopbackHost(r.Host) {
		http.Error(w, fmt.Sprintf("Forbidden: invalid Host header %q", r.Host), http.StatusForbidden)
		return true
	}
	return false
}

func isLoopbackHost(raw string) bool {
	host := raw
	if splitHost, _, err := net.SplitHostPort(raw); err == nil {
		host = splitHost
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (h *apiHandler) newMCPServer() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "agentico",
		Title:   "Agentico",
		Version: APIVersion,
	}, &mcp.ServerOptions{
		Instructions: "Agentico MCP tools adapt the local REST API version " + APIVersion + ". Mutating tools run synchronously and return endpoint-specific result bodies.",
	})
	h.registerMCPTools(server)
	return server
}

type restToolError struct {
	status int
	body   []byte
}

func (e restToolError) Error() string {
	body := strings.TrimSpace(string(e.body))
	if body != "" {
		return body
	}
	return fmt.Sprintf(`{"api_version":%q,"error":{"code":"http_error","message":%q,"status":%d}}`, APIVersion, http.StatusText(e.status), e.status)
}

func callRESTTool[Out any](ctx context.Context, h *apiHandler, mcpRequest *mcp.CallToolRequest, method, path string, query url.Values, body any, trusted bool) (*mcp.CallToolResult, Out, error) {
	var zero Out
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, zero, fmt.Errorf("encoding REST adapter request: %w", err)
		}
		reader = bytes.NewReader(data)
	}
	target := path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req := httptest.NewRequest(method, target, reader).WithContext(ctx)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if trusted {
		req.Header.Set("X-Agentico-Client", "local")
	}
	if origin := mcpRequestOrigin(mcpRequest); origin != "" {
		req.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	h.restRoutes().ServeHTTP(w, req)
	resp := w.Result()
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, zero, fmt.Errorf("reading REST adapter response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, zero, restToolError{status: resp.StatusCode, body: data}
	}
	var out Out
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, zero, fmt.Errorf("decoding REST adapter response: %w", err)
	}
	return nil, out, nil
}

func mcpRequestOrigin(req *mcp.CallToolRequest) string {
	if req == nil || req.Extra == nil || req.Extra.Header == nil {
		return ""
	}
	return req.Extra.Header.Get("Origin")
}

func noQuery() url.Values {
	return nil
}

func emptyBody() map[string]any {
	return map[string]any{}
}
