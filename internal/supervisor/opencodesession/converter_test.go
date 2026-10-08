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

package opencodesession

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/supervisor"
)

var updateGolden = flag.Bool("update", false, "rewrite the expected OpenCode history seed goldens under testdata/")

const (
	testConvID  = "conv-golden"
	testWorkDir = "/work/repo.app"
	testModel   = "anthropic/claude-sonnet-4"
	// budgetWindow is the context window, in tokens, at which the fixture
	// no longer fits whole and its oldest turn (three messages) is dropped.
	budgetWindow = 780
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

func input(dir string, recs []supervisor.Record) supervisor.RebuildInput {
	return supervisor.RebuildInput{
		ConversationID:  testConvID,
		ConversationDir: dir,
		WorkDir:         testWorkDir,
		Model:           testModel,
		Effort:          "high",
		Records:         recs,
	}
}

func TestReadableCheckpointSeedsSummaryAndCutsHistory(t *testing.T) {
	recs := loadRecords(t, "transcript.jsonl")
	checkpoint := supervisor.Record{Seq: 100, Kind: supervisor.KindCheckpoint, Visibility: supervisor.VisibilityModelOnly, CreatedAt: recs[1].CreatedAt,
		Data: json.RawMessage(`{"covers_through_seq":2,"summary":"Earlier summary","native_baseline":{"harness":"claude","payload":{}},"reason":"native_auto","model":"claude"}`)}
	recs = append(recs, checkpoint)
	got, err := Render(input(t.TempDir(), recs), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got, []byte("Agentico note: Summary of the earlier conversation, compacted on claude: Earlier summary")) || bytes.Contains(got, []byte("Hello, what is in this repo?")) || !bytes.Contains(got, []byte("Run the tests")) {
		t.Fatalf("seed checkpoint wrong: %s", got)
	}
	opaque := checkpoint
	opaque.Data = json.RawMessage(`{"covers_through_seq":2,"native_baseline":{"harness":"codex","payload":{}},"reason":"native_auto","model":"gpt"}`)
	recs[len(recs)-1] = opaque
	got, err = Render(input(t.TempDir(), recs), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got, []byte("Hello, what is in this repo?")) {
		t.Fatalf("opaque checkpoint cut history: %s", got)
	}
}

func TestLatestAndLastOpenCodeCheckpoint(t *testing.T) {
	recs := loadRecords(t, "transcript.jsonl")
	first := supervisor.Record{Seq: 100, Kind: supervisor.KindCheckpoint, Visibility: supervisor.VisibilityModelOnly, CreatedAt: recs[0].CreatedAt,
		Data: json.RawMessage(`{"covers_through_seq":1,"summary":"first summary","native_baseline":{"harness":"claude","payload":{}},"reason":"native_auto"}`)}
	last := supervisor.Record{Seq: 101, Kind: supervisor.KindCheckpoint, Visibility: supervisor.VisibilityModelOnly, CreatedAt: recs[2].CreatedAt,
		Data: json.RawMessage(`{"covers_through_seq":100,"summary":"latest summary","native_baseline":{"harness":"claude","payload":{}},"reason":"native_auto"}`)}
	got, err := Render(input(t.TempDir(), []supervisor.Record{recs[0], first, recs[2], last}), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(got, []byte("first summary")) || !bytes.Contains(got, []byte("latest summary")) || bytes.Contains(got, []byte("Run the tests")) {
		t.Fatalf("latest checkpoint: %s", got)
	}
	result, err := New(Options{}).Rebuild(context.Background(), input(t.TempDir(), []supervisor.Record{last}))
	if err != nil || !result.Resume {
		t.Fatalf("checkpoint only seed = %#v, %v", result, err)
	}
}

func TestCheckpointDropsStraddlingOpenCodeToolResult(t *testing.T) {
	recs := loadRecords(t, "transcript.jsonl")
	cut := supervisor.Record{Seq: 6, Kind: supervisor.KindCheckpoint, Visibility: supervisor.VisibilityModelOnly, CreatedAt: recs[5].CreatedAt,
		Data: json.RawMessage(`{"covers_through_seq":5,"summary":"tool condensed","native_baseline":{"harness":"claude","payload":{}},"reason":"native_auto"}`)}
	got, err := Render(input(t.TempDir(), []supervisor.Record{recs[4], cut, recs[7]}), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(got, []byte("Tool (")) || bytes.Contains(got, []byte("result:")) {
		t.Fatalf("straddling pair leaked: %s", got)
	}
}

// windowFor reports window tokens for testModel and 0 (unknown) otherwise.
func windowFor(window int) func(string) int {
	return func(model string) int {
		if model == testModel {
			return window
		}
		return 0
	}
}

// snapshot lists every path beneath root with its content.
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

func compareGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	goldenPath := filepath.Join("testdata", name)
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
		t.Fatalf("seed differs from %s (run with -update after an intentional change)\n--- got ---\n%s\n--- want ---\n%s", goldenPath, got, want)
	}
}

func TestHarness(t *testing.T) {
	t.Parallel()
	c := New(Options{})
	if got := c.Harness(); got != "opencode" {
		t.Fatalf("Harness() = %q, want opencode", got)
	}
	var conv supervisor.Converter = c
	seeder, ok := conv.(supervisor.HistorySeeder)
	if !ok || !seeder.SeedsHistory() {
		t.Fatal("the OpenCode converter must declare that it seeds history")
	}
	if _, ok := conv.(supervisor.HarnessAssignedIDs); ok {
		t.Fatal("a seeding converter must not ask the coordinator to adopt a native id")
	}
}

func TestRebuildGolden(t *testing.T) {
	dir := t.TempDir()
	res, err := New(Options{ContextWindow: windowFor(1_000_000)}).Rebuild(context.Background(), input(dir, loadRecords(t, "transcript.jsonl")))
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if !res.Resume || res.SessionID != "" || res.Path != filepath.Join(dir, SeedFileName) {
		t.Fatalf("Rebuild result = %+v, want Resume with the seed under %s and no session id", res, dir)
	}
	got, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatalf("read seed: %v", err)
	}
	compareGolden(t, "transcript.golden.txt", got)
	assertShape(t, got)

	text := string(got)
	if !strings.Contains(text, "Earlier messages omitted: 0.") {
		t.Error("full seed does not state that nothing was omitted")
	}
	for _, keep := range []string{
		"User: Hello, what is in this repo?",                                         // plain exchange
		"Assistant: It is a Go service.",                                             //
		`Assistant: called Bash with {"command":"go test ./... && echo \"<done>\""}`, // tool call with input, no HTML escaping
		"Tool (Bash): result: ok  \texample.com/repo\t0.1s\n  <done>",                // result, continuation indented
		"User: Deploy to staging",                                                    // interrupted turn prompt kept
		"Assistant: Deploying now; first I will build.",                              // and its partial text
		"User: Refactor the handler",                                                 // stopped turn prompt
		"Assistant: Starting the refactor.",                                          //
		"Tool (Bash): result: 001_init.sql",                                          // late result still paired
		"Tool (AskUserQuestion): result: User answered: Postgres",                    // model_only result
		"Tool (Read): result (error): permission denied",                             // error result
		"[image omitted: image/png could not be restored]",                           // clipped png
		"[image omitted: image/jpeg could not be restored]",                          //
		"[image omitted: image could not be restored]",                               // missing source
		"User: Add a NOTES file & summarize <README>",                                //
	} {
		if !strings.Contains(text, keep) {
			t.Errorf("seed lacks %q", keep)
		}
	}
	for _, drop := range []string{
		"handler.go",      // unmatched call of a stopped turn
		"make build",      // interrupted turn's completed pair
		"build ok",        //
		"make deploy",     // interrupted turn's dangling call
		"orphan result",   // orphan result
		"display-only",    // display_only assistant record
		"weighing",        // thinking block
		"Interrupted",     // markers
		"req-",            // request records
		"Permission mode", // marker text
		"failed to start", //
	} {
		if strings.Contains(text, drop) {
			t.Errorf("seed unexpectedly contains %q", drop)
		}
	}
	if n := strings.Count(text, "Assistant: The README describes setup & usage."); n != 1 {
		t.Errorf("duplicate assistant text appears %d times, want 1", n)
	}
}

func TestModelOnlyNoteBecomesUserHistory(t *testing.T) {
	recs := loadRecords(t, "transcript.jsonl")
	recs = append(recs, supervisor.Record{Seq: 40, ID: "note-40", Generation: 2, CreatedAt: recs[len(recs)-1].CreatedAt, Kind: supervisor.KindNote, Visibility: supervisor.VisibilityModelOnly, Data: json.RawMessage(`{"text":"Agentico note: The model changed to sonnet."}`)})
	data, err := Render(input(t.TempDir(), recs), Options{ContextWindow: windowFor(1_000_000)})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("User: Agentico note: The model changed to sonnet.")) {
		t.Fatalf("note absent from user history: %s", data)
	}
	compareGolden(t, "note.golden.txt", data)
}

func TestCrossHarnessGolden(t *testing.T) {
	data, err := Render(input(t.TempDir(), loadRecords(t, "cross_harness.jsonl")), Options{ContextWindow: windowFor(1_000_000)})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, data)
	for _, want := range []string{
		`Assistant: called Bash with {"command":"go test ./..."}`,
		`Tool (Bash): result: tests passed`,
		`Assistant: called Write with {"file_path":"status.txt","content":"checked"}`,
		`Tool (Write): result: wrote status.txt`,
		`User: Agentico note: The user switched this conversation from Claude to OpenCode`,
	} {
		if !bytes.Contains(data, []byte(want)) {
			t.Errorf("cross-harness seed lacks %q", want)
		}
	}
	if bytes.Contains(data, []byte("Switched to OpenCode ·")) {
		t.Error("display-only harness marker entered rebuilt history")
	}
	compareGolden(t, "cross_harness.golden.txt", data)
}

func TestRebuildBudgetDropsOldestTurn(t *testing.T) {
	dir := t.TempDir()
	res, err := New(Options{ContextWindow: windowFor(budgetWindow)}).Rebuild(context.Background(), input(dir, loadRecords(t, "transcript.jsonl")))
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	got, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	compareGolden(t, "budget.golden.txt", got)
	assertShape(t, got)
	text := string(got)
	if !strings.Contains(text, "Earlier messages omitted: 3.") {
		t.Errorf("budget seed does not count the oldest turn's three messages:\n%s", text)
	}
	for _, drop := range []string{"Hello, what is in this repo?", "It is a Go service.", "It has two packages."} {
		if strings.Contains(text, drop) {
			t.Errorf("budget seed kept %q from the oldest turn", drop)
		}
	}
	if !strings.Contains(text, "User: Run the tests") || !strings.Contains(text, "User: Continue") {
		t.Error("budget seed dropped a newer turn")
	}

	// A window large enough for the whole fixture omits nothing.
	full, err := Render(input(dir, loadRecords(t, "transcript.jsonl")), Options{ContextWindow: windowFor(budgetWindow + 200)})
	if err != nil || !strings.Contains(string(full), "Earlier messages omitted: 0.") {
		t.Fatalf("a larger window still omitted messages (err %v)", err)
	}
}

func TestRenderBudgetKeepsWholeTurnsNewestFirst(t *testing.T) {
	t.Parallel()
	recs := []supervisor.Record{
		user(1, "t1", strings.Repeat("a", 30)),
		assistant(2, "t1", "first reply"),
		user(3, "t2", strings.Repeat("b", 30)),
		assistant(4, "t2", "second reply"),
		user(5, "t3", "third"),
		assistant(6, "t3", "third reply"),
	}
	// Turn 3 costs 12+23=35 characters, turn 2 37+24=61 and turn 1 37+23=60
	// (each line plus its newline): a 100-character budget keeps turns 3 and
	// 2 and omits both messages of turn 1.
	data, err := Render(input("", recs), Options{ContextWindow: func(string) int { return 50 }})
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "Earlier messages omitted: 2.") || strings.Contains(text, "aaa") || strings.Contains(text, "first reply") {
		t.Fatalf("turn 1 was not dropped whole:\n%s", text)
	}
	if !strings.Contains(text, "bbb") || !strings.Contains(text, "second reply") || !strings.Contains(text, "third reply") {
		t.Fatalf("newer turns were dropped:\n%s", text)
	}

	// A 40-character budget fits only the newest turn.
	data, err = Render(input("", recs), Options{ContextWindow: func(string) int { return 20 }})
	if err != nil {
		t.Fatal(err)
	}
	text = string(data)
	if !strings.Contains(text, "Earlier messages omitted: 4.") || strings.Contains(text, "aaa") || !strings.Contains(text, "User: third") {
		t.Fatalf("budget did not keep only the newest turn:\n%s", text)
	}
}

func TestRenderDefaultWindowWhenUnknown(t *testing.T) {
	t.Parallel()
	var recs []supervisor.Record
	// 200 turns of ~3K characters each is about 600K characters, more than
	// the 400K-character default budget (200K tokens / 2 * 4).
	for i := 0; i < 200; i++ {
		turn := fmt.Sprintf("t%03d", i)
		// Distinct texts, so duplicate collapse keeps every turn.
		recs = append(recs, user(int64(i+1), turn, fmt.Sprintf("%03d", i)+strings.Repeat("x", 2997)))
	}
	for name, opts := range map[string]Options{
		"nil lookup":     {},
		"unknown model":  {ContextWindow: func(string) int { return 0 }},
		"negative value": {ContextWindow: func(string) int { return -1 }},
	} {
		data, err := Render(input("", recs), opts)
		if err != nil {
			t.Fatal(err)
		}
		// Every message is "User: " plus 3000 characters and a newline,
		// 3007 characters: 133 fit in 400,000.
		if !strings.Contains(string(data), "Earlier messages omitted: 67.") {
			t.Errorf("%s: default window not applied: %s", name, strings.SplitN(string(data), "\n", 3)[1])
		}
	}
}

func TestRenderClipsEachItem(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("é", maxTextChars+5)
	input0 := `{"command":"` + strings.Repeat("c", maxInputChars) + `"}`
	result := strings.Repeat("r", maxResultChars+10)
	recs := []supervisor.Record{
		user(1, "t1", long),
		{Seq: 2, TurnID: "t1", Kind: supervisor.KindToolUse, Visibility: supervisor.VisibilityContent,
			Data: json.RawMessage(`{"content":[{"type":"tool_use","id":"c1","name":"Bash","input":` + input0 + `}]}`)},
		{Seq: 3, TurnID: "t1", Kind: supervisor.KindToolResult, Visibility: supervisor.VisibilityContent,
			Data: json.RawMessage(`{"content":[{"type":"tool_result","tool_use_id":"c1","content":"` + result + `"}]}`)},
	}
	data, err := Render(input("", recs), Options{})
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		"User: " + strings.Repeat("é", maxTextChars) + " [clipped 5 characters]\n",
		"Assistant: called Bash with " + input0[:maxInputChars] + " [clipped 14 characters]\n",
		"Tool (Bash): result: " + result[:maxResultChars] + " [clipped 10 characters]\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("seed lacks clipped item %.60q...", want)
		}
	}
}

func TestRebuildEmptyHistoryWritesNothing(t *testing.T) {
	t.Parallel()
	for name, recs := range map[string][]supervisor.Record{
		"non-content records": loadRecords(t, "empty.jsonl"),
		"no records":          nil,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			res, err := New(Options{}).Rebuild(context.Background(), input(dir, recs))
			if err != nil {
				t.Fatalf("Rebuild: %v", err)
			}
			if res != (supervisor.RebuildResult{}) {
				t.Fatalf("Rebuild result = %+v, want Resume=false with no path", res)
			}
			if got := keys(snapshot(t, dir)); len(got) != 1 {
				t.Fatalf("empty history wrote files: %v", got)
			}
			data, err := Render(input(dir, recs), Options{})
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
				dir := t.TempDir()
				const previous = "previous seed\n"
				if existing {
					if err := os.WriteFile(SeedPath(dir), []byte(previous), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				before := snapshot(t, dir)
				_, err := New(Options{}).Rebuild(context.Background(), input(dir, recs))
				var convErr *supervisor.ConversionError
				if !errors.As(err, &convErr) || convErr.Seq != 42 {
					t.Fatalf("Rebuild error = %v, want *supervisor.ConversionError for seq 42", err)
				}
				after := snapshot(t, dir)
				if strings.Join(keys(before), ",") != strings.Join(keys(after), ",") || before[SeedFileName] != after[SeedFileName] {
					t.Fatalf("conversion error changed the conversation dir: %v -> %v", before, after)
				}
			}
		})
	}
}

func TestRebuildInvalidInputIsNotConversionError(t *testing.T) {
	t.Parallel()
	in := input("", loadRecords(t, "transcript.jsonl"))
	_, err := New(Options{}).Rebuild(context.Background(), in)
	var convErr *supervisor.ConversionError
	if err == nil || errors.As(err, &convErr) {
		t.Fatalf("Rebuild error = %v, want a non-conversion argument error", err)
	}
}

func TestRebuildUnwritableDirIsNotConversionError(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dir := filepath.Join(root, "conv")
	if err := os.WriteFile(dir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := New(Options{}).Rebuild(context.Background(), input(dir, loadRecords(t, "transcript.jsonl")))
	var convErr *supervisor.ConversionError
	if err == nil || errors.As(err, &convErr) {
		t.Fatalf("Rebuild error = %v, want a plain I/O error", err)
	}
}

func TestRebuildCancelledContext(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := New(Options{}).Rebuild(ctx, input(dir, loadRecords(t, "transcript.jsonl")))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Rebuild error = %v, want context.Canceled", err)
	}
	if got := keys(snapshot(t, dir)); len(got) != 1 {
		t.Fatalf("cancelled rebuild wrote files: %v", got)
	}
}

func TestRebuildReplacesSeedAtomically(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	stale := strings.Repeat("stale history line\n", 5000)
	if err := os.WriteFile(SeedPath(dir), []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	want, err := Render(input(dir, loadRecords(t, "transcript.jsonl")), Options{})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		res, err := New(Options{}).Rebuild(context.Background(), input(dir, loadRecords(t, "transcript.jsonl")))
		if err != nil {
			t.Fatalf("Rebuild %d: %v", i, err)
		}
		got, _ := os.ReadFile(res.Path)
		if !bytes.Equal(got, want) {
			t.Fatalf("rebuild %d did not fully replace the previous seed", i)
		}
	}
	info, err := os.Stat(SeedPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("seed mode = %v, want 0600", perm)
	}
	if got := keys(snapshot(t, dir)); strings.Join(got, ",") != "./,"+SeedFileName {
		t.Fatalf("conversation dir = %v, want only the seed (no temp files)", got)
	}
}

func TestRegistryContextWindow(t *testing.T) {
	t.Parallel()
	if got := RegistryContextWindow(nil)("m"); got != 0 {
		t.Fatalf("nil registry window = %d", got)
	}
	if got := RegistryContextWindow(llm.NewRegistry())("m"); got != 0 {
		t.Fatalf("registry without OpenCode window = %d", got)
	}
}

// assertShape checks the invariants every seed must satisfy: the fixed
// preamble and closing, and that every message entry begins with a role
// label.
func assertShape(t *testing.T, data []byte) {
	t.Helper()
	text := string(data)
	if !strings.HasPrefix(text, Preamble+"\nEarlier messages omitted: ") {
		t.Fatalf("seed does not open with the preamble:\n%.300s", text)
	}
	if !strings.HasSuffix(text, "\n\n"+Closing+"\n") {
		t.Fatalf("seed does not end with the closing line")
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	body := lines[3 : len(lines)-2]
	for i, l := range body {
		if strings.HasPrefix(l, "  ") {
			continue
		}
		if !strings.HasPrefix(l, "User: ") && !strings.HasPrefix(l, "Assistant: ") && !strings.HasPrefix(l, "Tool") {
			t.Errorf("message line %d lacks a role label: %q", i, l)
		}
	}
}

func user(seq int64, turn, text string) supervisor.Record {
	data, _ := json.Marshal(supervisor.UserData{Text: text})
	return supervisor.Record{Seq: seq, TurnID: turn, Kind: supervisor.KindUser, Visibility: supervisor.VisibilityContent, Data: data}
}

func assistant(seq int64, turn, text string) supervisor.Record {
	data, _ := json.Marshal(supervisor.ContentData{Content: []llm.ContentBlock{{Type: "text", Text: text}}})
	return supervisor.Record{Seq: seq, TurnID: turn, Kind: supervisor.KindAssistant, Visibility: supervisor.VisibilityContent, Data: data}
}

func TestAttachmentsGolden(t *testing.T) {
	data, err := Render(input(t.TempDir(), loadRecords(t, "attachments.jsonl")), Options{ContextWindow: windowFor(1_000_000)})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, data)
	for _, want := range []string{
		// Continuation lines of one seed item are indented two spaces.
		"User: Review the login mock and the spec\n  \n  Attached Images:\n  - [Image #1]: /state/supervisor/conversations/conv-golden/attachments/0123456789abcdef0123456789abcdef.png\n  \n  Attached Files:\n  - [spec.pdf]: /state/supervisor/conversations/conv-golden/attachments/fedcba9876543210fedcba9876543210.pdf",
		"User: Attached Files:\n  - [notes.txt]: /state/supervisor/conversations/conv-golden/attachments/00112233445566778899aabbccddeeff.txt",
	} {
		if !bytes.Contains(data, []byte(want)) {
			t.Errorf("seed lacks %q", want)
		}
	}
	compareGolden(t, "attachments.golden.txt", data)
}

func TestAttachmentBlockIsClippedWithTheText(t *testing.T) {
	recs := loadRecords(t, "attachments.jsonl")
	long := strings.Repeat("x", maxTextChars)
	recs[0].Data = json.RawMessage(`{"text":"` + long + `","attachments":[{"path":"/a/b.png","kind":"image","name":"b.png","size":1}]}`)
	data, err := Render(input(t.TempDir(), recs), Options{ContextWindow: windowFor(1_000_000)})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("/a/b.png")) || !bytes.Contains(data, []byte("[clipped ")) {
		t.Fatalf("the clip did not apply to the rendered text:\n%s", data)
	}
}
