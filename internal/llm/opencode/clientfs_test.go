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
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// fsProtocol builds a minimal protocol (no handshake needed for the hosted fs
// handlers) whose only writable root is dir.
func fsProtocol(t *testing.T, dir string) (*Protocol, *syncBuffer) {
	t.Helper()
	buf := &syncBuffer{}
	p := NewProtocol(llm.ProtocolOpts{
		Model:         "opencode:anthropic/claude-sonnet-4-5",
		WritableRoots: []string{dir},
	})
	p.SetStdin(buf)
	return p, buf
}

// fsResponse parses the last written JSON-RPC line as an fs/* response.
func fsResponse(t *testing.T, buf *syncBuffer) (result json.RawMessage, errObj *RPCError) {
	t.Helper()
	var resp struct {
		Result json.RawMessage `json:"result"`
		Error  *RPCError       `json:"error"`
	}
	if err := json.Unmarshal(buf.lastLine(t), &resp); err != nil {
		t.Fatalf("fs response not JSON-RPC: %v (raw %q)", err, buf.String())
	}
	return resp.Result, resp.Error
}

// TestWriteTextFile_WithinWritableRootWritesAndReplies proves a hosted write to a
// path inside the writable roots creates the file (including missing parents) and
// replies with a JSON-RPC result, producing no SDK message that could seal the turn.
func TestWriteTextFile_WithinWritableRootWritesAndReplies(t *testing.T) {
	dir := t.TempDir()
	p, buf := fsProtocol(t, dir)

	target := filepath.Join(dir, "knowledge-base", "index.md")
	msgs := mustParse(t, p, serverRequestLine(t, 1, writeTextFileMethod, map[string]any{
		"sessionId": "ses_x",
		"path":      target,
		"content":   "# KB\n",
	}))
	if len(msgs) != 0 {
		t.Fatalf("fs/write_text_file produced %+v, want no SDK messages", msgs)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("file not written: %v", err)
	}
	if string(got) != "# KB\n" {
		t.Fatalf("file content = %q, want %q", got, "# KB\n")
	}

	result, errObj := fsResponse(t, buf)
	if errObj != nil {
		t.Fatalf("write replied with error %+v, want success", errObj)
	}
	if string(result) != "null" {
		t.Fatalf("write result = %s, want null", result)
	}
}

// TestWriteTextFile_OutsideWritableRootsIsRejected proves a write outside the
// granted writable roots is refused with a JSON-RPC error and never touches disk —
// defense-in-depth over OpenCode's own permission gate — without crashing the turn.
func TestWriteTextFile_OutsideWritableRootsIsRejected(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "escape.md")
	p, buf := fsProtocol(t, dir)

	msgs := mustParse(t, p, serverRequestLine(t, 2, writeTextFileMethod, map[string]any{
		"sessionId": "ses_x",
		"path":      outside,
		"content":   "nope",
	}))
	if len(msgs) != 0 {
		t.Fatalf("rejected write produced %+v, want no SDK messages (no terminal)", msgs)
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("out-of-bounds path exists (stat err=%v), want it never written", err)
	}
	if _, errObj := fsResponse(t, buf); errObj == nil {
		t.Fatal("out-of-bounds write replied without an error object")
	}
}

func TestWriteTextFile_ExplicitWritableRootsDoNotImplicitlyAllowWorkDirOrStateDir(t *testing.T) {
	scratch := t.TempDir()
	workDir := t.TempDir()
	stateDir := t.TempDir()
	buf := &syncBuffer{}
	p := NewProtocol(llm.ProtocolOpts{
		Model:         "opencode:anthropic/claude-sonnet-4-5",
		WorkDir:       workDir,
		StateDir:      stateDir,
		WritableRoots: []string{scratch},
	})
	p.SetStdin(buf)

	for _, target := range []string{
		filepath.Join(workDir, "source.go"),
		filepath.Join(stateDir, "feature.yaml"),
	} {
		msgs := mustParse(t, p, serverRequestLine(t, 3, writeTextFileMethod, map[string]any{
			"sessionId": "ses_x",
			"path":      target,
			"content":   "nope",
		}))
		if len(msgs) != 0 {
			t.Fatalf("rejected write to %s produced %+v, want no SDK messages", target, msgs)
		}
		if _, err := os.Stat(target); !os.IsNotExist(err) {
			t.Fatalf("out-of-bounds path %s exists (stat err=%v), want it never written", target, err)
		}
		if _, errObj := fsResponse(t, buf); errObj == nil {
			t.Fatalf("write to %s replied without an error object", target)
		}
	}

	allowed := filepath.Join(scratch, "evidence.txt")
	mustParse(t, p, serverRequestLine(t, 4, writeTextFileMethod, map[string]any{
		"sessionId": "ses_x",
		"path":      allowed,
		"content":   "ok",
	}))
	if _, errObj := fsResponse(t, buf); errObj != nil {
		t.Fatalf("write to scratch replied with error %+v, want success", errObj)
	}
}

// TestReadTextFile_ReturnsContentWithLineAndLimit proves a hosted read returns the
// file content and honors the optional 1-based line offset and line limit.
func TestReadTextFile_ReturnsContentWithLineAndLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("a\nb\nc\nd\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, buf := fsProtocol(t, dir)

	line, limit := 2, 2
	mustParse(t, p, serverRequestLine(t, 3, readTextFileMethod, map[string]any{
		"sessionId": "ses_x",
		"path":      path,
		"line":      line,
		"limit":     limit,
	}))

	result, errObj := fsResponse(t, buf)
	if errObj != nil {
		t.Fatalf("read replied with error %+v, want success", errObj)
	}
	var rr ReadTextFileResult
	if err := json.Unmarshal(result, &rr); err != nil {
		t.Fatalf("read result not a ReadTextFileResult: %v (raw %s)", err, result)
	}
	if rr.Content != "b\nc" {
		t.Fatalf("read content = %q, want %q (lines 2-3)", rr.Content, "b\nc")
	}
}

// TestReadTextFile_MissingFileRepliesError proves a read of a nonexistent file is
// reported as a per-request JSON-RPC error rather than crashing the session.
func TestReadTextFile_MissingFileRepliesError(t *testing.T) {
	dir := t.TempDir()
	p, buf := fsProtocol(t, dir)

	msgs := mustParse(t, p, serverRequestLine(t, 4, readTextFileMethod, map[string]any{
		"sessionId": "ses_x",
		"path":      filepath.Join(dir, "missing.txt"),
	}))
	if len(msgs) != 0 {
		t.Fatalf("read of missing file produced %+v, want no SDK messages", msgs)
	}
	if _, errObj := fsResponse(t, buf); errObj == nil {
		t.Fatal("read of missing file replied without an error object")
	}
}
