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

package claudesession

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/claudeconfig"
	"github.com/doordash-oss/agentic-orchestrator/internal/supervisor"
)

var updateGolden = flag.Bool("update", false, "rewrite the expected Claude session goldens under testdata/")

const (
	testSessionID = "11111111-2222-4333-8444-555555555555"
	testWorkDir   = "/work/repo.app"
	testConvID    = "conv-golden"
)

func loadRecords(t *testing.T, name string) []supervisor.Record {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()
	var recs []supervisor.Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		var r supervisor.Record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("decode fixture line: %v", err)
		}
		recs = append(recs, r)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan fixture: %v", err)
	}
	return recs
}

func input(recs []supervisor.Record) supervisor.RebuildInput {
	return supervisor.RebuildInput{
		ConversationID:  testConvID,
		NativeSessionID: testSessionID,
		WorkDir:         testWorkDir,
		Records:         recs,
	}
}

func converterAt(dir string) *Converter {
	return New(Options{ConfigDir: func() (string, error) { return dir, nil }})
}

func TestCheckpointRendersNativePrefixAndCutsCoveredHistory(t *testing.T) {
	recs := loadRecords(t, "transcript.jsonl")
	checkpoint := supervisor.Record{Seq: 100, Kind: supervisor.KindCheckpoint, Visibility: supervisor.VisibilityModelOnly, CreatedAt: recs[1].CreatedAt,
		Data: json.RawMessage(`{"covers_through_seq":2,"summary":"Earlier summary","native_baseline":{"harness":"claude","payload":{"content":[{"type":"text","text":"Earlier summary"}]}},"reason":"native_auto","model":"claude","trigger":"auto","pre_tokens":123}`)}
	recs = append(recs, checkpoint)
	got, err := Render(input(recs))
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(got), []byte{'\n'})
	if len(lines) < 3 {
		t.Fatalf("got %d lines", len(lines))
	}
	var boundary, summary map[string]any
	if err := json.Unmarshal(lines[0], &boundary); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(lines[1], &summary); err != nil {
		t.Fatal(err)
	}
	if boundary["type"] != "system" || boundary["subtype"] != "compact_boundary" || summary["isCompactSummary"] != true {
		t.Fatalf("checkpoint prefix: %s\n%s", lines[0], lines[1])
	}
	if strings.Contains(string(got), "Hello, what is in this repo?") || !strings.Contains(string(got), "Run the tests") {
		t.Fatalf("cut is wrong: %s", got)
	}
	if summary["parentUuid"] != boundary["uuid"] {
		t.Fatal("checkpoint lines are not chained")
	}
}

func TestOpaqueForeignCheckpointKeepsFullHistory(t *testing.T) {
	recs := loadRecords(t, "transcript.jsonl")
	full, err := Render(input(recs))
	if err != nil {
		t.Fatal(err)
	}
	recs = append(recs, supervisor.Record{Seq: 100, Kind: supervisor.KindCheckpoint, Visibility: supervisor.VisibilityModelOnly, CreatedAt: recs[1].CreatedAt,
		Data: json.RawMessage(`{"covers_through_seq":2,"native_baseline":{"harness":"codex","payload":{}},"reason":"native_auto","model":"gpt"}`)})
	got, err := Render(input(recs))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, full) {
		t.Fatal("opaque foreign checkpoint changed Claude history")
	}
}

func TestLatestAndLastClaudeCheckpoint(t *testing.T) {
	recs := loadRecords(t, "transcript.jsonl")
	first := supervisor.Record{Seq: 100, Kind: supervisor.KindCheckpoint, Visibility: supervisor.VisibilityModelOnly, CreatedAt: recs[0].CreatedAt,
		Data: json.RawMessage(`{"covers_through_seq":1,"summary":"first summary","native_baseline":{"harness":"claude","payload":{}},"reason":"native_auto"}`)}
	last := supervisor.Record{Seq: 101, Kind: supervisor.KindCheckpoint, Visibility: supervisor.VisibilityModelOnly, CreatedAt: recs[2].CreatedAt,
		Data: json.RawMessage(`{"covers_through_seq":100,"summary":"latest summary","native_baseline":{"harness":"claude","payload":{}},"reason":"native_auto"}`)}
	got, err := Render(input([]supervisor.Record{recs[0], first, recs[2], last}))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(got, []byte("first summary")) || !bytes.Contains(got, []byte("latest summary")) || len(bytes.Split(bytes.TrimSpace(got), []byte{'\n'})) != 2 {
		t.Fatalf("latest checkpoint: %s", got)
	}
	result, err := converterAt(t.TempDir()).Rebuild(context.Background(), input([]supervisor.Record{last}))
	if err != nil || !result.Resume {
		t.Fatalf("checkpoint only resume = %#v, %v", result, err)
	}
}

func TestCheckpointDropsStraddlingClaudeToolResult(t *testing.T) {
	recs := loadRecords(t, "transcript.jsonl")
	cut := supervisor.Record{Seq: 6, Kind: supervisor.KindCheckpoint, Visibility: supervisor.VisibilityModelOnly, CreatedAt: recs[5].CreatedAt,
		Data: json.RawMessage(`{"covers_through_seq":5,"summary":"tool condensed","native_baseline":{"harness":"claude","payload":{}},"reason":"native_auto"}`)}
	got, err := Render(input([]supervisor.Record{recs[4], cut, recs[7]}))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(got, []byte("tool_result")) || bytes.Contains(got, []byte("tool_use")) {
		t.Fatalf("straddling pair leaked: %s", got)
	}
}

// snapshot lists every path beneath root with its content, so a test can
// assert exactly what a rebuild created.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() {
			out[rel+"/"] = ""
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[rel] = string(data)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}

func keys(m map[string]string) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func TestHarness(t *testing.T) {
	t.Parallel()
	if got := New(Options{}).Harness(); got != "claude" {
		t.Fatalf("Harness() = %q, want claude", got)
	}
}

func TestRebuildGolden(t *testing.T) {
	cfg := t.TempDir()
	res, err := converterAt(cfg).Rebuild(context.Background(), input(loadRecords(t, "transcript.jsonl")))
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	wantPath := filepath.Join(cfg, "projects", "-work-repo-app", testSessionID+".jsonl")
	if !res.Resume || res.SessionID != testSessionID || res.Path != wantPath {
		t.Fatalf("Rebuild result = %+v, want Resume with %s at %s", res, testSessionID, wantPath)
	}
	got, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatalf("read rebuilt file: %v", err)
	}

	goldenPath := filepath.Join("testdata", "transcript.golden.jsonl")
	if *updateGolden {
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatalf("update golden: %v", err)
		}
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden (run with -update to create): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("rebuilt session differs from %s (run with -update after an intentional change)\n--- got ---\n%s\n--- want ---\n%s", goldenPath, got, want)
	}

	assertLineShape(t, got)

	text := string(got)
	for _, keep := range []string{
		`"content":"Deploy to staging"`,                     // interrupted turn prompt kept
		"Deploying now; first I will build.",                // and its partial text
		`"content":"Refactor the handler"`,                  // user-stopped turn prompt
		"Starting the refactor.",                            // and its text
		`"tool_use_id":"toolu_01"`,                          // completed tool pair
		`"id":"toolu_01"`,                                   //
		`"tool_use_id":"toolu_03"`,                          // model_only result rebuilt
		`"tool_use_id":"toolu_07"`,                          // error result rebuilt
		`"is_error":true`,                                   //
		"[image omitted: image/png could not be restored]",  // clipped png
		"[image omitted: image/jpeg could not be restored]", // undecodable base64
		"[image omitted: image could not be restored]",      // missing source
		`"media_type":"image/png","data":"iVBORw0KGgo`,      // intact image kept
		`"content":"Summarize <README> & docs"`,             // no HTML escaping
	} {
		if !strings.Contains(text, keep) {
			t.Errorf("rebuilt session lacks %q", keep)
		}
	}
	for _, drop := range []string{
		"toolu_04",        // unmatched tool call of a stopped turn
		"toolu_05",        // interrupted turn's completed tool pair
		"toolu_06",        // interrupted turn's dangling call
		"toolu_99",        // orphan result
		"display-only",    // display_only assistant record
		"weighing",        // thinking block
		"Interrupted",     // markers
		"req-",            // request records
		"Permission mode", // marker text
		`"model"`,         // never a model key
	} {
		if strings.Contains(text, drop) {
			t.Errorf("rebuilt session unexpectedly contains %q", drop)
		}
	}
	if n := strings.Count(text, "The README describes setup & usage."); n != 1 {
		t.Errorf("duplicate assistant text appears %d times, want 1", n)
	}
}

func TestModelOnlyNoteBecomesUserHistory(t *testing.T) {
	recs := loadRecords(t, "transcript.jsonl")
	recs = append(recs, supervisor.Record{Seq: 40, ID: "note-40", Generation: 2, CreatedAt: recs[len(recs)-1].CreatedAt, Kind: supervisor.KindNote, Visibility: supervisor.VisibilityModelOnly, Data: json.RawMessage(`{"text":"Agentico note: The model changed to sonnet."}`)})
	data, err := Render(input(recs))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"role":"user","content":"Agentico note: The model changed to sonnet."`)) {
		t.Fatalf("note absent from user history: %s", data)
	}
	path := filepath.Join("testdata", "note.golden.jsonl")
	if *updateGolden {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, want) {
		t.Fatalf("note golden mismatch: %v", err)
	}
}

func TestCrossHarnessGolden(t *testing.T) {
	data, err := Render(input(loadRecords(t, "cross_harness.jsonl")))
	if err != nil {
		t.Fatal(err)
	}
	assertLineShape(t, data)
	for _, want := range []string{
		`"id":"call_shell_01","name":"shell_command"`,
		`"tool_use_id":"call_shell_01"`,
		`"id":"call_patch_02","name":"apply_patch"`,
		`"tool_use_id":"call_patch_02"`,
		`"role":"user","content":"Agentico note: The user switched this conversation from Codex to Claude`,
	} {
		if !bytes.Contains(data, []byte(want)) {
			t.Errorf("cross-harness session lacks %q", want)
		}
	}
	if bytes.Contains(data, []byte("Switched to Claude ·")) {
		t.Error("display-only harness marker entered rebuilt history")
	}
	path := filepath.Join("testdata", "cross_harness.golden.jsonl")
	if *updateGolden {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, want) {
		t.Fatalf("cross-harness golden mismatch: %v", err)
	}
}

// assertLineShape checks the invariants every rebuilt file must satisfy.
func assertLineShape(t *testing.T, data []byte) {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) == 0 {
		t.Fatal("no lines")
	}
	var prev string
	seen := map[string]bool{}
	for i, raw := range lines {
		var line map[string]any
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			t.Fatalf("line %d is not JSON: %v\n%s", i, err, raw)
		}
		if line["sessionId"] != testSessionID {
			t.Errorf("line %d sessionId = %v", i, line["sessionId"])
		}
		if line["cwd"] != testWorkDir {
			t.Errorf("line %d cwd = %v", i, line["cwd"])
		}
		if line["isSidechain"] != false || line["userType"] != "external" {
			t.Errorf("line %d isSidechain/userType = %v/%v", i, line["isSidechain"], line["userType"])
		}
		if i == 0 {
			if v, ok := line["parentUuid"]; !ok || v != nil {
				t.Errorf("line 0 parentUuid = %v (present %v), want null", v, ok)
			}
		} else if line["parentUuid"] != prev {
			t.Errorf("line %d parentUuid = %v, want %s", i, line["parentUuid"], prev)
		}
		id, _ := line["uuid"].(string)
		if id == "" || seen[id] {
			t.Errorf("line %d uuid %q empty or repeated", i, id)
		}
		seen[id] = true
		prev = id
		ts, _ := line["timestamp"].(string)
		if len(ts) != len("2006-01-02T15:04:05.000Z") || !strings.HasSuffix(ts, "Z") {
			t.Errorf("line %d timestamp %q not in Claude's millisecond UTC form", i, ts)
		}
		msg, ok := line["message"].(map[string]any)
		if !ok {
			t.Fatalf("line %d has no message object", i)
		}
		if _, has := msg["model"]; has {
			t.Errorf("line %d message carries a model key", i)
		}
		if msg["role"] != line["type"] {
			t.Errorf("line %d role %v != type %v", i, msg["role"], line["type"])
		}
		switch c := msg["content"].(type) {
		case string:
			if line["type"] != "user" {
				t.Errorf("line %d: string content on %v line", i, line["type"])
			}
		case []any:
			if len(c) != 1 {
				t.Errorf("line %d: %d content blocks, want one per line", i, len(c))
			}
		default:
			t.Errorf("line %d: content is %T", i, c)
		}
	}
}

func TestRebuildAssistantLinesShareMessageID(t *testing.T) {
	t.Parallel()
	data, err := Render(input(loadRecords(t, "transcript.jsonl")))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	type msg struct {
		Type    string `json:"type"`
		Message struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"message"`
	}
	var lines []msg
	for _, raw := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		var m msg
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, m)
	}
	ids := map[string]bool{}
	for i, l := range lines {
		if l.Type != "assistant" {
			if l.Message.ID != "" || l.Message.Type != "" {
				t.Errorf("user line %d carries message id/type", i)
			}
			continue
		}
		if !strings.HasPrefix(l.Message.ID, "msg_") || l.Message.Type != "message" {
			t.Errorf("assistant line %d message id/type = %q/%q", i, l.Message.ID, l.Message.Type)
		}
		if i > 0 && lines[i-1].Type == "assistant" {
			if l.Message.ID != lines[i-1].Message.ID {
				t.Errorf("assistant line %d does not share the run's message id", i)
			}
		} else if ids[l.Message.ID] {
			t.Errorf("assistant run at line %d reuses message id %s", i, l.Message.ID)
		}
		ids[l.Message.ID] = true
	}
	again, err := Render(input(loadRecords(t, "transcript.jsonl")))
	if err != nil || !bytes.Equal(data, again) {
		t.Fatalf("Render is not deterministic (err %v)", err)
	}
}

func TestRebuildEmptyHistoryWritesNothing(t *testing.T) {
	t.Parallel()
	for name, recs := range map[string][]supervisor.Record{
		"non-content records": loadRecords(t, "empty.jsonl"),
		"no records":          nil,
	} {
		t.Run(name, func(t *testing.T) {
			cfg := t.TempDir()
			res, err := converterAt(cfg).Rebuild(context.Background(), input(recs))
			if err != nil {
				t.Fatalf("Rebuild: %v", err)
			}
			if res.Resume || res.Path != "" || res.SessionID != testSessionID {
				t.Fatalf("Rebuild result = %+v, want Resume=false with no path", res)
			}
			if got := keys(snapshot(t, cfg)); len(got) != 1 {
				t.Fatalf("empty history touched the config dir: %v", got)
			}
		})
	}
}

func TestRebuildConversionErrorWritesNothing(t *testing.T) {
	t.Parallel()
	base := loadRecords(t, "transcript.jsonl")[:2]
	bad := func(kind supervisor.RecordKind, vis supervisor.Visibility, data string) supervisor.Record {
		return supervisor.Record{Seq: 42, ConversationID: testConvID, TurnID: "turn-x", Kind: kind, Visibility: vis, Data: json.RawMessage(data)}
	}
	cases := map[string]supervisor.Record{
		"undecodable assistant data": bad(supervisor.KindAssistant, supervisor.VisibilityContent, `{"content":"not-an-array"}`),
		"missing payload":            bad(supervisor.KindUser, supervisor.VisibilityContent, ``),
		"null payload":               bad(supervisor.KindToolUse, supervisor.VisibilityContent, `null`),
		"user payload not an object": bad(supervisor.KindUser, supervisor.VisibilityContent, `"hello"`),
		"unknown block type":         bad(supervisor.KindAssistant, supervisor.VisibilityContent, `{"content":[{"type":"server_tool_use","id":"x"}]}`),
		"tool_result in assistant":   bad(supervisor.KindAssistant, supervisor.VisibilityContent, `{"content":[{"type":"tool_result","tool_use_id":"x"}]}`),
		"tool_use without id":        bad(supervisor.KindToolUse, supervisor.VisibilityContent, `{"content":[{"type":"tool_use","name":"Bash"}]}`),
		"tool_use input not object":  bad(supervisor.KindToolUse, supervisor.VisibilityContent, `{"content":[{"type":"tool_use","id":"t","name":"Bash","input":[1]}]}`),
		"tool_result without id":     bad(supervisor.KindToolResult, supervisor.VisibilityModelOnly, `{"content":[{"type":"tool_result","content":"x"}]}`),
		"tool_result object content": bad(supervisor.KindToolResult, supervisor.VisibilityContent, `{"content":[{"type":"tool_result","tool_use_id":"t","content":{"a":1}}]}`),
		"undecodable marker":         bad(supervisor.KindMarker, supervisor.VisibilityDisplayOnly, `{"marker":`),
	}
	for name, rec := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			recs := append(append([]supervisor.Record(nil), base...), rec)
			for _, existing := range []bool{false, true} {
				cfg := t.TempDir()
				path := SessionPath(cfg, testWorkDir, testSessionID)
				const previous = "previous content\n"
				if existing {
					if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte(previous), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				before := snapshot(t, cfg)
				_, err := converterAt(cfg).Rebuild(context.Background(), input(recs))
				var convErr *supervisor.ConversionError
				if !errors.As(err, &convErr) {
					t.Fatalf("Rebuild error = %v, want *supervisor.ConversionError", err)
				}
				if convErr.Seq != 42 {
					t.Errorf("ConversionError.Seq = %d, want 42", convErr.Seq)
				}
				after := snapshot(t, cfg)
				if strings.Join(keys(before), ",") != strings.Join(keys(after), ",") {
					t.Fatalf("conversion error changed the config dir: %v -> %v", keys(before), keys(after))
				}
				if existing {
					if got, _ := os.ReadFile(path); string(got) != previous {
						t.Fatalf("existing session file changed to %q", got)
					}
				} else if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("session file exists after conversion error: %v", err)
				}
			}
		})
	}
}

func TestRebuildUnwritableProjectsDirIsNotConversionError(t *testing.T) {
	t.Parallel()
	recs := loadRecords(t, "transcript.jsonl")

	t.Run("file in place of projects dir", func(t *testing.T) {
		t.Parallel()
		cfg := t.TempDir()
		if err := os.WriteFile(filepath.Join(cfg, "projects"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		assertIOError(t, cfg, recs)
	})

	t.Run("read-only projects dir", func(t *testing.T) {
		t.Parallel()
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("permission bits are not enforced for this user")
		}
		cfg := t.TempDir()
		dir := claudeconfig.ProjectsDir(cfg, testWorkDir)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		assertIOError(t, cfg, recs)
		entries, _ := os.ReadDir(dir)
		if len(entries) != 0 {
			t.Fatalf("read-only projects dir gained entries: %v", entries)
		}
	})

	t.Run("config dir resolution failure", func(t *testing.T) {
		t.Parallel()
		boom := errors.New("no home")
		c := New(Options{ConfigDir: func() (string, error) { return "", boom }})
		_, err := c.Rebuild(context.Background(), input(recs))
		var convErr *supervisor.ConversionError
		if !errors.Is(err, boom) || errors.As(err, &convErr) {
			t.Fatalf("Rebuild error = %v, want wrapped %v and not a ConversionError", err, boom)
		}
	})
}

func assertIOError(t *testing.T, cfg string, recs []supervisor.Record) {
	t.Helper()
	res, err := converterAt(cfg).Rebuild(context.Background(), input(recs))
	if err == nil {
		t.Fatalf("Rebuild succeeded (%+v), want I/O error", res)
	}
	var convErr *supervisor.ConversionError
	if errors.As(err, &convErr) {
		t.Fatalf("Rebuild error %v is a ConversionError, want plain I/O error", err)
	}
}

func TestRebuildInvalidInputIsNotConversionError(t *testing.T) {
	t.Parallel()
	recs := loadRecords(t, "transcript.jsonl")
	for name, mut := range map[string]func(*supervisor.RebuildInput){
		"empty session id":   func(in *supervisor.RebuildInput) { in.NativeSessionID = "" },
		"path in session id": func(in *supervisor.RebuildInput) { in.NativeSessionID = "../escape" },
		"dot-dot session id": func(in *supervisor.RebuildInput) { in.NativeSessionID = ".." },
		"empty work dir":     func(in *supervisor.RebuildInput) { in.WorkDir = "" },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cfg := t.TempDir()
			in := input(recs)
			mut(&in)
			_, err := converterAt(cfg).Rebuild(context.Background(), in)
			var convErr *supervisor.ConversionError
			if err == nil || errors.As(err, &convErr) {
				t.Fatalf("Rebuild error = %v, want a non-conversion argument error", err)
			}
			if got := keys(snapshot(t, cfg)); len(got) != 1 {
				t.Fatalf("invalid input touched the config dir: %v", got)
			}
		})
	}
}

func TestRebuildCancelledContext(t *testing.T) {
	t.Parallel()
	cfg := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := converterAt(cfg).Rebuild(ctx, input(loadRecords(t, "transcript.jsonl")))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Rebuild error = %v, want context.Canceled", err)
	}
	if got := keys(snapshot(t, cfg)); len(got) != 1 {
		t.Fatalf("cancelled rebuild touched the config dir: %v", got)
	}
}

func TestRebuildDefaultConfigDirHonoursClaudeConfigDir(t *testing.T) {
	recs := loadRecords(t, "transcript.jsonl")
	root := t.TempDir()
	home := filepath.Join(root, "home")
	custom := filepath.Join(root, "custom-claude")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv(claudeconfig.EnvConfigDir, custom)

	res, err := New(Options{}).Rebuild(context.Background(), input(recs))
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if want := SessionPath(custom, testWorkDir, testSessionID); res.Path != want {
		t.Fatalf("Rebuild path = %q, want %q", res.Path, want)
	}
	if got := keys(snapshot(t, home)); len(got) != 1 {
		t.Fatalf("HOME was touched while CLAUDE_CONFIG_DIR was set: %v", got)
	}
}

func TestRebuildDefaultConfigDirFallsBackToHome(t *testing.T) {
	recs := loadRecords(t, "transcript.jsonl")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(claudeconfig.EnvConfigDir, "")

	res, err := New(Options{}).Rebuild(context.Background(), input(recs))
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if want := SessionPath(filepath.Join(home, ".claude"), testWorkDir, testSessionID); res.Path != want {
		t.Fatalf("Rebuild path = %q, want %q", res.Path, want)
	}
}

func TestRebuildReplacesExistingFileAtomically(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cfg := filepath.Join(root, "claude")
	path := SessionPath(cfg, testWorkDir, testSessionID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	longer := strings.Repeat(`{"type":"user","message":{"role":"user","content":"stale"}}`+"\n", 500)
	if err := os.WriteFile(path, []byte(longer), 0o600); err != nil {
		t.Fatal(err)
	}
	want, err := Render(input(loadRecords(t, "transcript.jsonl")))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(want) >= len(longer) {
		t.Fatalf("fixture must be shorter than the stale file to prove replacement")
	}

	for i := 0; i < 2; i++ {
		res, err := converterAt(cfg).Rebuild(context.Background(), input(loadRecords(t, "transcript.jsonl")))
		if err != nil {
			t.Fatalf("Rebuild %d: %v", i, err)
		}
		if res.Path != path {
			t.Fatalf("Rebuild path = %q, want %q", res.Path, path)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("rebuild %d did not fully replace the previous content", i)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("session file mode = %v, want 0600", perm)
	}

	// Only the session file exists under the temp root: no temp files remain
	// and nothing else was created.
	wantTree := []string{
		"./",
		"claude/",
		"claude/projects/",
		"claude/projects/-work-repo-app/",
		"claude/projects/-work-repo-app/" + testSessionID + ".jsonl",
	}
	if got := keys(snapshot(t, root)); strings.Join(got, ",") != strings.Join(wantTree, ",") {
		t.Fatalf("temp tree = %v, want %v", got, wantTree)
	}
}

func TestRebuildTouchesOnlyInjectedConfigDir(t *testing.T) {
	// Point the process-level Claude locations at sentinels so a write that
	// bypassed the injected resolver would land somewhere observable.
	root := t.TempDir()
	home := filepath.Join(root, "home")
	envCfg := filepath.Join(root, "env-claude")
	injected := filepath.Join(root, "injected")
	for _, d := range []string{home, envCfg} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv(claudeconfig.EnvConfigDir, envCfg)

	if _, err := converterAt(injected).Rebuild(context.Background(), input(loadRecords(t, "transcript.jsonl"))); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	want := []string{
		"./",
		"env-claude/",
		"home/",
		"injected/",
		"injected/projects/",
		"injected/projects/-work-repo-app/",
		"injected/projects/-work-repo-app/" + testSessionID + ".jsonl",
	}
	if got := keys(snapshot(t, root)); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("temp tree = %v, want %v", got, want)
	}
}

func TestImagePlaceholder(t *testing.T) {
	t.Parallel()
	const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="
	cases := []struct {
		name string
		item string
		keep bool
	}{
		{"intact png", `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + png + `"}}`, true},
		{"clipped png", `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + png[:40] + `"}}`, false},
		{"bad padding", `{"type":"image","source":{"type":"base64","media_type":"image/webp","data":"` + png[:41] + `"}}`, false},
		{"empty data", `{"type":"image","source":{"type":"base64","media_type":"image/png","data":""}}`, false},
		{"no source", `{"type":"image"}`, false},
		{"url source", `{"type":"image","source":{"type":"url","url":"https://example.com/a.png"}}`, true},
		{"unknown source", `{"type":"image","source":{"type":"file","file_id":"f"}}`, false},
	}
	for _, tc := range cases {
		text, replace := imagePlaceholder(json.RawMessage(tc.item))
		if replace == tc.keep {
			t.Errorf("%s: replace = %v, want %v", tc.name, replace, !tc.keep)
		}
		if replace && !strings.HasPrefix(text, "[image omitted: ") {
			t.Errorf("%s: placeholder %q", tc.name, text)
		}
	}
}
