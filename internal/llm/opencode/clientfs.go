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

package opencode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Client filesystem (fs/*) surface. OpenCode performs in-workspace file I/O with
// its own tools but delegates I/O for paths outside its session workspace to the
// ACP client; Agentico advertises fs.read/writeTextFile (see ClientCapabilities)
// and hosts those requests here so out-of-workspace artifacts — knowledge-base
// graphs, phase artifacts under the feature state dir — can be written. Each
// handler replies with a JSON-RPC result or a per-request error and never seals
// the turn: an out-of-bounds write or an I/O failure is reported to OpenCode so
// it unblocks gracefully rather than crashing the session.
const (
	writeTextFileMethod = "fs/write_text_file"
	readTextFileMethod  = "fs/read_text_file"

	jsonRPCInvalidParams = -32602
	jsonRPCInternalError = -32603
)

// handleWriteTextFile hosts an fs/write_text_file request. The target is bounded
// to the session's writable roots as defense-in-depth over OpenCode's own edit
// permission gate; a write outside them, or an I/O failure, is answered with a
// JSON-RPC error rather than performed or crashed.
func (p *Protocol) handleWriteTextFile(rawID json.RawMessage, params json.RawMessage) {
	var wp WriteTextFileParams
	if err := json.Unmarshal(params, &wp); err != nil {
		p.writeFSError(rawID, jsonRPCInvalidParams, "invalid fs/write_text_file params")
		return
	}
	path := p.resolveFSPath(wp.Path)
	if path == "" {
		p.writeFSError(rawID, jsonRPCInvalidParams, "fs/write_text_file requires a path")
		return
	}
	if !p.fsWriteAllowed(path) {
		p.logDebug("[opencode] denying out-of-bounds fs/write_text_file: %s", path)
		p.writeFSError(rawID, jsonRPCInternalError, "write path is outside the session's writable roots")
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		p.logDebug("[opencode] fs/write_text_file mkdir failed for %s: %v", path, err)
		p.writeFSError(rawID, jsonRPCInternalError, "creating parent directory failed")
		return
	}
	if err := os.WriteFile(path, []byte(wp.Content), 0o644); err != nil {
		p.logDebug("[opencode] fs/write_text_file write failed for %s: %v", path, err)
		p.writeFSError(rawID, jsonRPCInternalError, "writing file failed")
		return
	}
	// ACP write result is null.
	_ = p.writeJSON(FSResultResponse{JSONRPC: "2.0", ID: rawID, Result: nil})
}

// handleReadTextFile hosts an fs/read_text_file request, honoring the optional
// 1-based line offset and line limit. Reads are not path-bounded: they are
// non-destructive and OpenCode has already cleared the read against its own
// permission map before delegating, so the handler only reports I/O failures.
func (p *Protocol) handleReadTextFile(rawID json.RawMessage, params json.RawMessage) {
	var rp ReadTextFileParams
	if err := json.Unmarshal(params, &rp); err != nil {
		p.writeFSError(rawID, jsonRPCInvalidParams, "invalid fs/read_text_file params")
		return
	}
	path := p.resolveFSPath(rp.Path)
	if path == "" {
		p.writeFSError(rawID, jsonRPCInvalidParams, "fs/read_text_file requires a path")
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		p.logDebug("[opencode] fs/read_text_file failed for %s: %v", path, err)
		p.writeFSError(rawID, jsonRPCInternalError, "reading file failed")
		return
	}
	content := sliceTextLines(string(data), rp.Line, rp.Limit)
	_ = p.writeJSON(FSResultResponse{JSONRPC: "2.0", ID: rawID, Result: ReadTextFileResult{Content: content}})
}

// resolveFSPath trims and cleans an ACP fs path, resolving a relative path
// against the session working directory. Empty stays empty so the caller rejects
// it.
func (p *Protocol) resolveFSPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if !filepath.IsAbs(path) && p.opts.WorkDir != "" {
		path = filepath.Join(p.opts.WorkDir, path)
	}
	return filepath.Clean(path)
}

// fsWriteAllowed reports whether a resolved path lies within the explicit
// writable roots. WorkDir and StateDir are fallback writable surfaces only when
// the session did not provide explicit roots.
func (p *Protocol) fsWriteAllowed(path string) bool {
	roots := append([]string(nil), p.opts.WritableRoots...)
	if len(roots) == 0 {
		if p.opts.WorkDir != "" {
			roots = append(roots, p.opts.WorkDir)
		}
		if p.opts.StateDir != "" {
			roots = append(roots, p.opts.StateDir)
		}
	}
	return pathWithinAny(path, roots)
}

// pathWithinAny reports whether target (cleaned, absolute) is one of, or nested
// under, any root.
func pathWithinAny(target string, roots []string) bool {
	target = filepath.Clean(target)
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		root = filepath.Clean(root)
		if target == root {
			return true
		}
		rel, err := filepath.Rel(root, target)
		if err != nil {
			continue
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			continue
		}
		return true
	}
	return false
}

// sliceTextLines applies ACP read_text_file's optional 1-based line offset and
// line limit. Nil bounds return the whole file unchanged.
func sliceTextLines(content string, line, limit *int) string {
	if line == nil && limit == nil {
		return content
	}
	lines := strings.Split(content, "\n")
	start := 0
	if line != nil && *line > 1 {
		start = *line - 1
	}
	if start > len(lines) {
		start = len(lines)
	}
	lines = lines[start:]
	if limit != nil && *limit >= 0 && *limit < len(lines) {
		lines = lines[:*limit]
	}
	return strings.Join(lines, "\n")
}

// writeFSError replies to a hosted fs/* request with a JSON-RPC error, echoing
// the raw id so OpenCode unblocks the delegated call without the session failing.
func (p *Protocol) writeFSError(rawID json.RawMessage, code int, msg string) {
	_ = p.writeJSON(ErrorResponse{
		JSONRPC: "2.0",
		ID:      rawID,
		Error:   RPCError{Code: code, Message: msg},
	})
}
