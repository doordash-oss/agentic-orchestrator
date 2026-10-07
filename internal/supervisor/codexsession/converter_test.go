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

package codexsession

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
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/supervisor"
)

var updateGolden = flag.Bool("update", false, "rewrite the expected Codex rollout goldens under testdata/")

const (
	testThreadID   = "019a0b1c-2d3e-7f40-8a51-6b7c8d9e0f12"
	testWorkDir    = "/work/repo.app"
	testConvID     = "conv-golden"
	testModel      = "gpt-5.2-codex"
	testEffort     = "high"
	testCLIVersion = "0.156.0"
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
		NativeSessionID: testThreadID,
		WorkDir:         testWorkDir,
		Model:           testModel,
		Effort:          testEffort,
		Records:         recs,
	}
}

func testOptions(home func() (string, error)) Options {
	return Options{Home: home, CLIVersion: testCLIVersion}
}

func converterAt(home string) *Converter {
	return New(testOptions(func() (string, error) { return home, nil }))
}

// firstRecordTime is the CreatedAt of the fixture's first selected record,
// which names a new rollout's date partition.
func firstRecordTime(t *testing.T) time.Time {
	t.Helper()
	return loadRecords(t, "transcript.jsonl")[0].CreatedAt
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
	c := New(Options{})
	if got := c.Harness(); got != "codex" {
		t.Fatalf("Harness() = %q, want codex", got)
	}
	var conv supervisor.Converter = c
	assigned, ok := conv.(supervisor.HarnessAssignedIDs)
	if !ok || !assigned.HarnessAssignsSessionID() {
		t.Fatal("codex converter must report harness-assigned session ids")
	}
}

func TestRolloutPath(t *testing.T) {
	t.Parallel()
	ts := time.Date(2026, 10, 7, 14, 37, 23, 61e6, time.Local)
	want := filepath.Join("/h", "sessions", "2026", "10", "07", "rollout-2026-10-07T14-37-23-abc.jsonl")
	if got := RolloutPath("/h", "abc", ts); got != want {
		t.Fatalf("RolloutPath = %q, want %q", got, want)
	}
}

func TestRebuildGolden(t *testing.T) {
	home := t.TempDir()
	res, err := converterAt(home).Rebuild(context.Background(), input(loadRecords(t, "transcript.jsonl")))
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	wantPath := RolloutPath(home, testThreadID, firstRecordTime(t))
	if !res.Resume || res.SessionID != testThreadID || res.Path != wantPath {
		t.Fatalf("Rebuild result = %+v, want Resume with %s at %s", res, testThreadID, wantPath)
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
		t.Fatalf("rebuilt rollout differs from %s (run with -update after an intentional change)\n--- got ---\n%s\n--- want ---\n%s", goldenPath, got, want)
	}

	assertLineShape(t, got)

	text := string(got)
	for _, keep := range []string{
		`"text":"Deploy to staging"`,                        // interrupted turn prompt kept
		"Deploying now; first I will build.",                // and its partial text
		`"text":"Refactor the handler"`,                     // user-stopped turn prompt
		"Starting the refactor.",                            // and its text
		`"name":"shell_command"`,                            // Bash mapped
		`go test ./... && echo \\\"<done>\\\"`,              // command carried, no HTML escaping
		`"name":"apply_patch"`,                              // Write mapped
		`"name":"Read"`,                                     // other tools keep their name
		`"call_id":"call_05"`,                               // late result still paired
		"001_init.sql",                                      //
		`"call_id":"call_04"`,                               // model_only result rebuilt
		"permission denied",                                 // error result rebuilt
		"[image omitted: image/png could not be restored]",  // clipped png
		"[image omitted: image/jpeg could not be restored]", // undecodable base64
		"[image omitted: image could not be restored]",      // missing source
		`"message":"Add a NOTES file & summarize <README>"`, // user event mirror
		`"effort":"high"`,                                   //
		`"model":"gpt-5.2-codex"`,                           //
	} {
		if !strings.Contains(text, keep) {
			t.Errorf("rebuilt rollout lacks %q", keep)
		}
	}
	for _, drop := range []string{
		"call_06",         // unmatched call of a stopped turn
		"call_07",         // interrupted turn's completed pair
		"call_08",         // interrupted turn's dangling call
		"call_99",         // orphan result
		"display-only",    // display_only assistant record
		"weighing",        // thinking block
		"Interrupted",     // markers
		"req-",            // request records
		"Permission mode", // marker text
	} {
		if strings.Contains(text, drop) {
			t.Errorf("rebuilt rollout unexpectedly contains %q", drop)
		}
	}
	if n := strings.Count(text, `"type":"output_text","text":"The README describes setup & usage."`); n != 1 {
		t.Errorf("duplicate assistant text appears %d times, want 1", n)
	}
}

func TestModelOnlyNoteBecomesUserHistory(t *testing.T) {
	recs := loadRecords(t, "transcript.jsonl")
	recs = append(recs, supervisor.Record{Seq: 40, ID: "note-40", Generation: 2, CreatedAt: recs[len(recs)-1].CreatedAt, Kind: supervisor.KindNote, Visibility: supervisor.VisibilityModelOnly, Data: json.RawMessage(`{"text":"Agentico note: The model changed to sonnet."}`)})
	data, err := Render(input(recs), testOptions(nil))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"role":"user","content":[{"type":"input_text","text":"Agentico note: The model changed to sonnet."}]`)) {
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

type line struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

func parseLines(t *testing.T, data []byte) []line {
	t.Helper()
	raws := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	out := make([]line, 0, len(raws))
	for i, raw := range raws {
		var l line
		if err := json.Unmarshal([]byte(raw), &l); err != nil {
			t.Fatalf("line %d is not JSON: %v\n%s", i, err, raw)
		}
		out = append(out, l)
	}
	return out
}

// assertLineShape checks the invariants every rebuilt rollout must satisfy.
func assertLineShape(t *testing.T, data []byte) {
	t.Helper()
	lines := parseLines(t, data)
	if len(lines) < 2 {
		t.Fatalf("only %d lines", len(lines))
	}
	if lines[0].Type != "session_meta" {
		t.Fatalf("first line type = %q, want session_meta", lines[0].Type)
	}
	var meta struct {
		ID  string `json:"id"`
		CWD string `json:"cwd"`
	}
	if err := json.Unmarshal(lines[0].Payload, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.ID != testThreadID || meta.CWD != testWorkDir {
		t.Errorf("session_meta id/cwd = %q/%q", meta.ID, meta.CWD)
	}
	if lines[1].Type != "turn_context" {
		t.Errorf("second line type = %q, want turn_context", lines[1].Type)
	}

	calls := map[string]bool{}
	outputs := map[string]bool{}
	for i, l := range lines {
		if len(l.Timestamp) != len("2006-01-02T15:04:05.000Z") || !strings.HasSuffix(l.Timestamp, "Z") {
			t.Errorf("line %d timestamp %q not in millisecond UTC form", i, l.Timestamp)
		}
		switch l.Type {
		case "session_meta":
			if i != 0 {
				t.Errorf("session_meta at line %d", i)
			}
		case "turn_context", "event_msg":
		case "response_item":
			var p struct {
				Type      string `json:"type"`
				CallID    string `json:"call_id"`
				Arguments string `json:"arguments"`
			}
			if err := json.Unmarshal(l.Payload, &p); err != nil {
				t.Fatal(err)
			}
			switch p.Type {
			case "function_call":
				if calls[p.CallID] || outputs[p.CallID] {
					t.Errorf("line %d: call %s repeated or after its output", i, p.CallID)
				}
				calls[p.CallID] = true
				if !json.Valid([]byte(p.Arguments)) {
					t.Errorf("line %d: arguments %q are not JSON", i, p.Arguments)
				}
			case "function_call_output":
				if !calls[p.CallID] || outputs[p.CallID] {
					t.Errorf("line %d: output %s without an earlier call or repeated", i, p.CallID)
				}
				outputs[p.CallID] = true
			case "message":
			default:
				t.Errorf("line %d: unexpected response_item type %q", i, p.Type)
			}
		default:
			t.Errorf("line %d: unexpected line type %q", i, l.Type)
		}
	}
	for id := range calls {
		if !outputs[id] {
			t.Errorf("function_call %s has no function_call_output", id)
		}
	}
}

func TestRenderDeterministicAndMatchesRebuild(t *testing.T) {
	t.Parallel()
	opts := testOptions(nil)
	a, err := Render(input(loadRecords(t, "transcript.jsonl")), opts)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	b, err := Render(input(loadRecords(t, "transcript.jsonl")), opts)
	if err != nil || !bytes.Equal(a, b) {
		t.Fatalf("Render is not deterministic (err %v)", err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "transcript.golden.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, want) {
		t.Fatal("Render output differs from the golden")
	}
}

func TestRenderDefaults(t *testing.T) {
	t.Parallel()
	in := input(loadRecords(t, "transcript.jsonl"))
	in.Effort = ""
	data, err := Render(in, Options{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, `"cli_version":"agentico","source":"vscode","model_provider":"openai"`) {
		t.Errorf("session_meta lacks defaults:\n%s", strings.SplitN(text, "\n", 2)[0])
	}
	if strings.Contains(text, `"effort"`) {
		t.Error("empty effort was written")
	}
}

func TestTurnContextPerTurn(t *testing.T) {
	t.Parallel()
	data, err := Render(input(loadRecords(t, "transcript.jsonl")), testOptions(nil))
	if err != nil {
		t.Fatal(err)
	}
	lines := parseLines(t, data)
	n := 0
	for i, l := range lines {
		if l.Type != "turn_context" {
			continue
		}
		n++
		if i+1 >= len(lines) || lines[i+1].Type != "response_item" {
			t.Errorf("turn_context at line %d is not followed by a response_item", i)
		}
	}
	// Turns 1..10, plus a return to turn 6 for the late result and back to
	// turn 7 for its reply.
	if n != 12 {
		t.Errorf("turn_context lines = %d, want 12", n)
	}
}

func TestRebuildEmptyHistoryWritesNothing(t *testing.T) {
	t.Parallel()
	for name, recs := range map[string][]supervisor.Record{
		"non-content records": loadRecords(t, "empty.jsonl"),
		"no records":          nil,
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			res, err := converterAt(home).Rebuild(context.Background(), input(recs))
			if err != nil {
				t.Fatalf("Rebuild: %v", err)
			}
			if res.Resume || res.Path != "" || res.SessionID != testThreadID {
				t.Fatalf("Rebuild result = %+v, want Resume=false with no path", res)
			}
			if got := keys(snapshot(t, home)); len(got) != 1 {
				t.Fatalf("empty history touched the codex home: %v", got)
			}
			data, err := Render(input(recs), Options{})
			if err != nil || data != nil {
				t.Fatalf("Render = %q, %v; want nil", data, err)
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
		"undecodable assistant data":  bad(supervisor.KindAssistant, supervisor.VisibilityContent, `{"content":"not-an-array"}`),
		"missing payload":             bad(supervisor.KindUser, supervisor.VisibilityContent, ``),
		"null payload":                bad(supervisor.KindToolUse, supervisor.VisibilityContent, `null`),
		"user payload not an object":  bad(supervisor.KindUser, supervisor.VisibilityContent, `"hello"`),
		"unknown block type":          bad(supervisor.KindAssistant, supervisor.VisibilityContent, `{"content":[{"type":"server_tool_use","id":"x"}]}`),
		"tool_result in assistant":    bad(supervisor.KindAssistant, supervisor.VisibilityContent, `{"content":[{"type":"tool_result","tool_use_id":"x"}]}`),
		"tool_use without id":         bad(supervisor.KindToolUse, supervisor.VisibilityContent, `{"content":[{"type":"tool_use","name":"Bash"}]}`),
		"tool_use without name":       bad(supervisor.KindToolUse, supervisor.VisibilityContent, `{"content":[{"type":"tool_use","id":"t"}]}`),
		"tool_use input not object":   bad(supervisor.KindToolUse, supervisor.VisibilityContent, `{"content":[{"type":"tool_use","id":"t","name":"Bash","input":[1]}]}`),
		"Bash without command":        bad(supervisor.KindToolUse, supervisor.VisibilityContent, `{"content":[{"type":"tool_use","id":"t","name":"Bash","input":{"cmd":"ls"}}]}`),
		"Bash command not a string":   bad(supervisor.KindToolUse, supervisor.VisibilityContent, `{"content":[{"type":"tool_use","id":"t","name":"Bash","input":{"command":["ls"]}}]}`),
		"tool_result without id":      bad(supervisor.KindToolResult, supervisor.VisibilityModelOnly, `{"content":[{"type":"tool_result","content":"x"}]}`),
		"tool_result object content":  bad(supervisor.KindToolResult, supervisor.VisibilityContent, `{"content":[{"type":"tool_result","tool_use_id":"t","content":{"a":1}}]}`),
		"tool_result unknown item":    bad(supervisor.KindToolResult, supervisor.VisibilityContent, `{"content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"document"}]}]}`),
		"undecodable marker":          bad(supervisor.KindMarker, supervisor.VisibilityDisplayOnly, `{"marker":`),
		"text block in a tool result": bad(supervisor.KindToolResult, supervisor.VisibilityContent, `{"content":[{"type":"text","text":"x"}]}`),
	}
	for name, rec := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			recs := append(append([]supervisor.Record(nil), base...), rec)
			for _, existing := range []bool{false, true} {
				home := t.TempDir()
				path := RolloutPath(home, testThreadID, firstRecordTime(t))
				const previous = "previous content\n"
				if existing {
					if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte(previous), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				before := snapshot(t, home)
				_, err := converterAt(home).Rebuild(context.Background(), input(recs))
				var convErr *supervisor.ConversionError
				if !errors.As(err, &convErr) {
					t.Fatalf("Rebuild error = %v, want *supervisor.ConversionError", err)
				}
				if convErr.Seq != 42 {
					t.Errorf("ConversionError.Seq = %d, want 42", convErr.Seq)
				}
				after := snapshot(t, home)
				if strings.Join(keys(before), ",") != strings.Join(keys(after), ",") {
					t.Fatalf("conversion error changed the codex home: %v -> %v", keys(before), keys(after))
				}
				if existing {
					if got, _ := os.ReadFile(path); string(got) != previous {
						t.Fatalf("existing rollout changed to %q", got)
					}
				} else if _, err := os.Stat(filepath.Join(home, "sessions")); !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("sessions dir exists after conversion error: %v", err)
				}
			}
		})
	}
}

func TestRebuildUnwritableSessionsDirIsNotConversionError(t *testing.T) {
	t.Parallel()
	recs := loadRecords(t, "transcript.jsonl")

	t.Run("file in place of sessions dir", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		if err := os.WriteFile(filepath.Join(home, "sessions"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		res, err := converterAt(home).Rebuild(context.Background(), input(recs))
		if err == nil {
			t.Fatalf("Rebuild succeeded (%+v), want I/O error", res)
		}
		var convErr *supervisor.ConversionError
		if errors.As(err, &convErr) {
			t.Fatalf("Rebuild error %v is a ConversionError, want plain I/O error", err)
		}
	})

	t.Run("home resolution failure", func(t *testing.T) {
		t.Parallel()
		boom := errors.New("no home")
		c := New(Options{Home: func() (string, error) { return "", boom }})
		_, err := c.Rebuild(context.Background(), input(recs))
		var convErr *supervisor.ConversionError
		if !errors.Is(err, boom) || errors.As(err, &convErr) {
			t.Fatalf("Rebuild error = %v, want wrapped %v and not a ConversionError", err, boom)
		}
	})
}

func TestRebuildInvalidInputIsNotConversionError(t *testing.T) {
	t.Parallel()
	recs := loadRecords(t, "transcript.jsonl")
	for name, mut := range map[string]func(*supervisor.RebuildInput){
		"empty session id":   func(in *supervisor.RebuildInput) { in.NativeSessionID = "" },
		"path in session id": func(in *supervisor.RebuildInput) { in.NativeSessionID = "../escape" },
		"backslash id":       func(in *supervisor.RebuildInput) { in.NativeSessionID = `a\b` },
		"dot session id":     func(in *supervisor.RebuildInput) { in.NativeSessionID = "." },
		"dot-dot session id": func(in *supervisor.RebuildInput) { in.NativeSessionID = ".." },
		"empty work dir":     func(in *supervisor.RebuildInput) { in.WorkDir = "" },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			in := input(recs)
			mut(&in)
			_, err := converterAt(home).Rebuild(context.Background(), in)
			var convErr *supervisor.ConversionError
			if err == nil || errors.As(err, &convErr) {
				t.Fatalf("Rebuild error = %v, want a non-conversion argument error", err)
			}
			if got := keys(snapshot(t, home)); len(got) != 1 {
				t.Fatalf("invalid input touched the codex home: %v", got)
			}
		})
	}
}

func TestRebuildCancelledContext(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := converterAt(home).Rebuild(ctx, input(loadRecords(t, "transcript.jsonl")))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Rebuild error = %v, want context.Canceled", err)
	}
	if got := keys(snapshot(t, home)); len(got) != 1 {
		t.Fatalf("cancelled rebuild touched the codex home: %v", got)
	}
}

func TestRebuildDefaultHomeHonoursCodexHome(t *testing.T) {
	recs := loadRecords(t, "transcript.jsonl")
	root := t.TempDir()
	home := filepath.Join(root, "home")
	custom := filepath.Join(root, "custom-codex")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", custom)

	res, err := New(Options{}).Rebuild(context.Background(), input(recs))
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if want := RolloutPath(custom, testThreadID, firstRecordTime(t)); res.Path != want {
		t.Fatalf("Rebuild path = %q, want %q", res.Path, want)
	}
	if got := keys(snapshot(t, home)); len(got) != 1 {
		t.Fatalf("HOME was touched while CODEX_HOME was set: %v", got)
	}
}

func TestRebuildDefaultHomeFallsBackToHome(t *testing.T) {
	recs := loadRecords(t, "transcript.jsonl")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")

	res, err := New(Options{}).Rebuild(context.Background(), input(recs))
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if want := RolloutPath(filepath.Join(home, ".codex"), testThreadID, firstRecordTime(t)); res.Path != want {
		t.Fatalf("Rebuild path = %q, want %q", res.Path, want)
	}
}

func TestRebuildReplacesExistingRolloutInPlace(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	home := filepath.Join(root, "codex")
	// An earlier generation's rollout lives under another date partition and
	// file timestamp; a rebuild must replace it rather than add a second.
	existing := RolloutPath(home, testThreadID, time.Date(2025, 1, 2, 3, 4, 5, 0, time.Local))
	other := RolloutPath(home, "other-thread", firstRecordTime(t))
	for _, p := range []string{existing, other} {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	stale := strings.Repeat(`{"timestamp":"x","type":"response_item","payload":{"type":"message"}}`+"\n", 500)
	if err := os.WriteFile(existing, []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte("other\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	want, err := Render(input(loadRecords(t, "transcript.jsonl")), testOptions(nil))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(want) >= len(stale) {
		t.Fatalf("fixture must be shorter than the stale file to prove replacement")
	}
	before := keys(snapshot(t, root))

	for i := 0; i < 2; i++ {
		res, err := converterAt(home).Rebuild(context.Background(), input(loadRecords(t, "transcript.jsonl")))
		if err != nil {
			t.Fatalf("Rebuild %d: %v", i, err)
		}
		if res.Path != existing {
			t.Fatalf("Rebuild path = %q, want existing %q", res.Path, existing)
		}
		got, err := os.ReadFile(existing)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("rebuild %d did not fully replace the previous content", i)
		}
	}
	info, err := os.Stat(existing)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("rollout mode = %v, want 0600", perm)
	}
	if got, _ := os.ReadFile(other); string(got) != "other\n" {
		t.Errorf("another thread's rollout changed to %q", got)
	}
	// The tree is unchanged: no duplicate rollout, no temp files left.
	if got := keys(snapshot(t, root)); strings.Join(got, ",") != strings.Join(before, ",") {
		t.Fatalf("temp tree = %v, want %v", got, before)
	}
}

func TestRebuildCreatesOnlyTheRollout(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	res, err := converterAt(home).Rebuild(context.Background(), input(loadRecords(t, "transcript.jsonl")))
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	rel, _ := filepath.Rel(home, res.Path)
	parts := strings.Split(rel, string(filepath.Separator))
	want := []string{"./", "sessions/"}
	for i := 1; i < len(parts); i++ {
		want = append(want, strings.Join(parts[:i+1], "/")+"/")
	}
	want[len(want)-1] = strings.TrimSuffix(want[len(want)-1], "/")
	sort.Strings(want)
	if got := keys(snapshot(t, home)); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("codex home tree = %v, want %v", got, want)
	}
	found, ok, err := FindRollout(home, testThreadID)
	if err != nil || !ok || found != res.Path {
		t.Fatalf("FindRollout = %q, %v, %v; want %q", found, ok, err, res.Path)
	}
}

func TestFindRollout(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	if p, ok, err := FindRollout(home, testThreadID); err != nil || ok || p != "" {
		t.Fatalf("FindRollout without sessions dir = %q, %v, %v", p, ok, err)
	}
	dir := filepath.Join(home, "sessions", "2026", "09", "30")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"rollout-2026-09-30T10-00-00-x" + testThreadID + ".jsonl.bak", // wrong suffix
		"notes-" + testThreadID + ".jsonl",                            // wrong prefix
	} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok, err := FindRollout(home, testThreadID); err != nil || ok {
		t.Fatalf("FindRollout matched a non-rollout file (ok %v, err %v)", ok, err)
	}
	want := filepath.Join(dir, "rollout-2026-09-30T10-00-00-"+testThreadID+".jsonl")
	if err := os.WriteFile(want, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if p, ok, err := FindRollout(home, testThreadID); err != nil || !ok || p != want {
		t.Fatalf("FindRollout = %q, %v, %v; want %q", p, ok, err, want)
	}
}

func TestToolResultOutput(t *testing.T) {
	t.Parallel()
	const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="
	cases := []struct {
		name, content, want string
	}{
		{"empty", ``, ""},
		{"string", `"a\nb"`, "a\nb"},
		{"text blocks", `[{"type":"text","text":"a"},{"type":"text","text":"b"}]`, "a\nb"},
		{"intact png", `[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + png + `"}}]`, "[image omitted: image/png could not be restored]"},
		{"url image", `[{"type":"image","source":{"type":"url","url":"https://example.com/a.png"}}]`, "[image omitted: image could not be restored]"},
		{"no source", `[{"type":"image"}]`, "[image omitted: image could not be restored]"},
	}
	for _, tc := range cases {
		got, err := toolResultOutput(json.RawMessage(tc.content))
		if err != nil || got != tc.want {
			t.Errorf("%s: toolResultOutput = %q, %v; want %q", tc.name, got, err, tc.want)
		}
	}
}
