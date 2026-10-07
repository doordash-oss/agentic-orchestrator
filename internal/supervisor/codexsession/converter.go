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

// Package codexsession rebuilds a Codex CLI rollout JSONL file from the
// supervisor's durable transcript so a relaunched Codex process can resume
// the thread with the conversation's history. The rollout format is
// undocumented; everything that depends on it lives in this package.
//
// Every line is `{"timestamp", "type", "payload"}`. The file opens with one
// `session_meta` line naming the thread id and cwd. Each durable turn starts
// with a `turn_context` line carrying the cwd, policies and model, followed by
// `response_item` lines (messages, function calls and their outputs) as the
// model saw them. User and assistant messages are mirrored by `event_msg`
// lines, which is what the CLI replays into its own history view.
//
// Codex mints its own thread ids, so a rollout is rebuilt under the id the
// harness reported and replaces that thread's existing rollout file in place
// wherever its date partition is.
package codexsession

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/codex"
	"github.com/doordash-oss/agentic-orchestrator/internal/supervisor"
)

// Harness is the harness name this converter is registered under.
const Harness = "codex"

// timestampLayout matches the CLI's own timestamps: UTC with milliseconds.
const timestampLayout = "2006-01-02T15:04:05.000Z"

// fileTimeLayout is the timestamp embedded in rollout file names.
const fileTimeLayout = "2006-01-02T15-04-05"

const (
	defaultCLIVersion    = "agentico"
	defaultModelProvider = "openai"
	originator           = "agentico"
)

// Options configures a Converter.
type Options struct {
	// Home resolves the Codex home directory. Nil means codex.ResolveHome
	// (CODEX_HOME, else $HOME/.codex).
	Home func() (string, error)
	// CLIVersion is recorded in session_meta. Empty means "agentico".
	CLIVersion string
	// ModelProvider is recorded in session_meta. Empty means "openai".
	ModelProvider string
}

func (o Options) withDefaults() Options {
	if o.Home == nil {
		o.Home = codex.ResolveHome
	}
	if o.CLIVersion == "" {
		o.CLIVersion = defaultCLIVersion
	}
	if o.ModelProvider == "" {
		o.ModelProvider = defaultModelProvider
	}
	return o
}

// Converter implements supervisor.Converter for the Codex CLI.
type Converter struct {
	opts Options
}

var (
	_ supervisor.Converter          = (*Converter)(nil)
	_ supervisor.HarnessAssignedIDs = (*Converter)(nil)
)

// New returns a Codex rollout converter.
func New(opts Options) *Converter {
	return &Converter{opts: opts.withDefaults()}
}

// Harness reports "codex".
func (c *Converter) Harness() string { return Harness }

// HarnessAssignsSessionID reports true: Codex mints thread ids itself.
func (c *Converter) HarnessAssignsSessionID() bool { return true }

// RolloutPath is the conventional rollout file for a thread started at t:
// <home>/sessions/YYYY/MM/DD/rollout-YYYY-MM-DDThh-mm-ss-<id>.jsonl, in local
// time as the CLI names them.
func RolloutPath(home, threadID string, t time.Time) string {
	t = t.Local()
	return filepath.Join(home, "sessions",
		t.Format("2006"), t.Format("01"), t.Format("02"),
		"rollout-"+t.Format(fileTimeLayout)+"-"+threadID+".jsonl")
}

// FindRollout returns an existing rollout file for threadID anywhere beneath
// <home>/sessions. A missing sessions directory is not an error.
func FindRollout(home, threadID string) (string, bool, error) {
	root := filepath.Join(home, "sessions")
	info, err := os.Stat(root)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if !info.IsDir() {
		return "", false, nil
	}
	suffix := "-" + threadID + ".jsonl"
	found := ""
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.Type().IsRegular() && strings.HasPrefix(name, "rollout-") && strings.HasSuffix(name, suffix) {
			found = path
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", false, err
	}
	return found, found != "", nil
}

// Rebuild renders in.Records and atomically replaces the rollout file for
// in.NativeSessionID: the existing one wherever it lives under the sessions
// directory, else a new one under the date of the first rendered record. An
// empty selection writes nothing and returns Resume=false. A record that
// cannot be represented returns a *supervisor.ConversionError and nothing is
// written. Invalid input, a cancelled context, an unresolvable home and any
// file-system failure return a plain error that is not a ConversionError.
func (c *Converter) Rebuild(ctx context.Context, in supervisor.RebuildInput) (supervisor.RebuildResult, error) {
	if err := ctx.Err(); err != nil {
		return supervisor.RebuildResult{}, err
	}
	data, started, err := render(in, c.opts)
	if err != nil {
		return supervisor.RebuildResult{}, err
	}
	if len(data) == 0 {
		return supervisor.RebuildResult{SessionID: in.NativeSessionID}, nil
	}
	home, err := c.opts.Home()
	if err != nil {
		return supervisor.RebuildResult{}, fmt.Errorf("resolve codex home: %w", err)
	}
	if home == "" {
		return supervisor.RebuildResult{}, errors.New("resolve codex home: empty path")
	}
	path, ok, err := FindRollout(home, in.NativeSessionID)
	if err != nil {
		return supervisor.RebuildResult{}, fmt.Errorf("find codex rollout for %s: %w", in.NativeSessionID, err)
	}
	if !ok {
		path = RolloutPath(home, in.NativeSessionID, started)
	}
	if err := ctx.Err(); err != nil {
		return supervisor.RebuildResult{}, err
	}
	if err := writeFileAtomic(path, data); err != nil {
		return supervisor.RebuildResult{}, fmt.Errorf("write codex rollout %s: %w", path, err)
	}
	return supervisor.RebuildResult{Resume: true, SessionID: in.NativeSessionID, Path: path}, nil
}

// Render converts in.Records to the bytes of a Codex rollout file without
// touching the file system. It returns nil when the selection is empty.
// Argument errors are plain errors; unrepresentable records are
// *supervisor.ConversionError.
func Render(in supervisor.RebuildInput, opts Options) ([]byte, error) {
	data, _, err := render(in, opts.withDefaults())
	return data, err
}

func render(in supervisor.RebuildInput, opts Options) ([]byte, time.Time, error) {
	if err := validateInput(in); err != nil {
		return nil, time.Time{}, err
	}
	entries, err := selectEntries(in.Records, in.WorkDir)
	if err != nil {
		return nil, time.Time{}, err
	}
	return encodeLines(in, opts, entries)
}

func validateInput(in supervisor.RebuildInput) error {
	if in.NativeSessionID == "" {
		return errors.New("codex rollout rebuild: empty native session id")
	}
	if strings.ContainsAny(in.NativeSessionID, `/\`) || in.NativeSessionID == "." || in.NativeSessionID == ".." {
		return fmt.Errorf("codex rollout rebuild: unsafe native session id %q", in.NativeSessionID)
	}
	if in.WorkDir == "" {
		return errors.New("codex rollout rebuild: empty working directory")
	}
	return nil
}

const (
	roleUser      = "user"
	roleAssistant = "assistant"

	blockText       = "text"
	blockToolUse    = "tool_use"
	blockToolResult = "tool_result"

	itemMessage            = "message"
	itemFunctionCall       = "function_call"
	itemFunctionCallOutput = "function_call_output"
)

// entry is one selected record reduced to the response items it contributes.
type entry struct {
	rec   *supervisor.Record
	role  string
	items []item
}

// item is one rendered response_item, its optional event_msg mirror, and
// what pairing needs to know.
type item struct {
	kind  string
	id    string // call id of a function call or its output
	raw   json.RawMessage
	event json.RawMessage
}

func conversionError(rec *supervisor.Record, format string, args ...any) error {
	return &supervisor.ConversionError{Seq: rec.Seq, Reason: fmt.Sprintf(format, args...)}
}

// selectEntries applies selection and every exclusion: interrupted turns'
// tool records, unpaired calls and outputs, and adjacent duplicates.
func selectEntries(records []supervisor.Record, workDir string) ([]entry, error) {
	interrupted, err := interruptedTurns(records)
	if err != nil {
		return nil, err
	}
	var entries []entry
	for i := range records {
		rec := &records[i]
		if !selected(rec) {
			continue
		}
		e, err := decodeEntry(rec, workDir)
		if err != nil {
			return nil, err
		}
		if rec.TurnID != "" && interrupted[rec.TurnID] {
			e.items = dropKinds(e.items, itemFunctionCall, itemFunctionCallOutput)
		}
		entries = append(entries, e)
	}
	entries = pairTools(entries)
	return collapseDuplicates(entries), nil
}

func selected(rec *supervisor.Record) bool {
	switch rec.Visibility {
	case supervisor.VisibilityContent, supervisor.VisibilityModelOnly:
	default:
		return false
	}
	switch rec.Kind {
	case supervisor.KindUser, supervisor.KindAssistant, supervisor.KindToolUse, supervisor.KindToolResult, supervisor.KindNote:
		return true
	}
	return false
}

// interruptedTurns collects the turns named by interrupted markers.
func interruptedTurns(records []supervisor.Record) (map[string]bool, error) {
	turns := map[string]bool{}
	for i := range records {
		rec := &records[i]
		if rec.Kind != supervisor.KindMarker {
			continue
		}
		var m supervisor.MarkerData
		if err := decodePayload(rec, &m); err != nil {
			return nil, err
		}
		if m.Marker == supervisor.MarkerInterrupted && rec.TurnID != "" {
			turns[rec.TurnID] = true
		}
	}
	return turns, nil
}

func decodePayload(rec *supervisor.Record, v any) error {
	data := bytes.TrimSpace(rec.Data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		return conversionError(rec, "%s record has no payload", rec.Kind)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return conversionError(rec, "decode %s payload: %v", rec.Kind, err)
	}
	return nil
}

// Response item and event payload shapes, field order as the CLI writes them.
type contentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type messageItem struct {
	Type    string        `json:"type"`
	Role    string        `json:"role"`
	Content []contentPart `json:"content"`
}

type functionCallItem struct {
	Type      string `json:"type"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	CallID    string `json:"call_id"`
}

type functionCallOutputItem struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

type userMessageEvent struct {
	Type    string   `json:"type"`
	Message string   `json:"message"`
	Images  []string `json:"images"`
}

type agentMessageEvent struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func decodeEntry(rec *supervisor.Record, workDir string) (entry, error) {
	if rec.Kind == supervisor.KindUser || rec.Kind == supervisor.KindNote {
		var u supervisor.UserData
		if err := decodePayload(rec, &u); err != nil {
			return entry{}, err
		}
		e := entry{rec: rec, role: roleUser}
		if strings.TrimSpace(u.Text) != "" {
			it, err := messageWithEvent(roleUser, "input_text", u.Text,
				userMessageEvent{Type: "user_message", Message: u.Text, Images: []string{}})
			if err != nil {
				return entry{}, conversionError(rec, "encode user message: %v", err)
			}
			e.items = append(e.items, it)
		}
		return e, nil
	}

	var cd supervisor.ContentData
	if err := decodePayload(rec, &cd); err != nil {
		return entry{}, err
	}
	role := roleAssistant
	if rec.Kind == supervisor.KindToolResult {
		role = roleUser
	}
	e := entry{rec: rec, role: role}
	for i, cb := range cd.Content {
		it, ok, err := renderBlock(rec, role, i, cb, workDir)
		if err != nil {
			return entry{}, err
		}
		if ok {
			e.items = append(e.items, it)
		}
	}
	return e, nil
}

func messageWithEvent(role, partType, text string, event any) (item, error) {
	raw, err := marshal(messageItem{Type: itemMessage, Role: role, Content: []contentPart{{Type: partType, Text: text}}})
	if err != nil {
		return item{}, err
	}
	ev, err := marshal(event)
	if err != nil {
		return item{}, err
	}
	return item{kind: itemMessage, raw: raw, event: ev}, nil
}

// renderBlock maps one durable content block. ok=false skips the block
// (thinking, empty text); an error means the block cannot be represented.
func renderBlock(rec *supervisor.Record, role string, index int, cb llm.ContentBlock, workDir string) (item, bool, error) {
	switch cb.Type {
	case "thinking", "redacted_thinking":
		return item{}, false, nil
	case blockText:
		if role != roleAssistant {
			break
		}
		if strings.TrimSpace(cb.Text) == "" {
			return item{}, false, nil
		}
		it, err := messageWithEvent(roleAssistant, "output_text", cb.Text,
			agentMessageEvent{Type: "agent_message", Message: cb.Text})
		if err != nil {
			return item{}, false, conversionError(rec, "encode text block %d: %v", index, err)
		}
		return it, true, nil
	case blockToolUse:
		if role != roleAssistant {
			break
		}
		if cb.ID == "" || cb.Name == "" {
			return item{}, false, conversionError(rec, "tool_use block %d lacks id or name", index)
		}
		input := bytes.TrimSpace(cb.Input)
		if len(input) == 0 || bytes.Equal(input, []byte("null")) {
			input = json.RawMessage(`{}`)
		} else if input[0] != '{' {
			return item{}, false, conversionError(rec, "tool_use block %d input is not an object", index)
		}
		name, args, err := functionCall(cb.Name, input, workDir)
		if err != nil {
			return item{}, false, conversionError(rec, "tool_use block %d: %v", index, err)
		}
		raw, err := marshal(functionCallItem{Type: itemFunctionCall, Name: name, Arguments: args, CallID: cb.ID})
		if err != nil {
			return item{}, false, conversionError(rec, "encode tool_use block %d: %v", index, err)
		}
		return item{kind: itemFunctionCall, id: cb.ID, raw: raw}, true, nil
	case blockToolResult:
		if role != roleUser {
			break
		}
		if cb.ToolUseID == "" {
			return item{}, false, conversionError(rec, "tool_result block %d lacks tool_use_id", index)
		}
		output, err := toolResultOutput(cb.Content)
		if err != nil {
			return item{}, false, conversionError(rec, "tool_result block %d: %v", index, err)
		}
		raw, err := marshal(functionCallOutputItem{Type: itemFunctionCallOutput, CallID: cb.ToolUseID, Output: output})
		if err != nil {
			return item{}, false, conversionError(rec, "encode tool_result block %d: %v", index, err)
		}
		return item{kind: itemFunctionCallOutput, id: cb.ToolUseID, raw: raw}, true, nil
	}
	return item{}, false, conversionError(rec, "unsupported %q block %d in %s record", cb.Type, index, rec.Kind)
}

// functionCall maps a recorded tool call to Codex's tool name and its
// JSON-string arguments: shell commands become shell_command, file writes
// apply_patch, anything else keeps its name and input.
func functionCall(name string, input json.RawMessage, workDir string) (string, string, error) {
	switch name {
	case "Bash":
		var in struct {
			Command *string `json:"command"`
		}
		if err := json.Unmarshal(input, &in); err != nil {
			return "", "", fmt.Errorf("decode Bash input: %w", err)
		}
		if in.Command == nil {
			return "", "", errors.New("Bash input lacks a command string")
		}
		args, err := marshal(struct {
			Command string `json:"command"`
			Workdir string `json:"workdir"`
		}{*in.Command, workDir})
		if err != nil {
			return "", "", err
		}
		return "shell_command", string(args), nil
	case "Write":
		args, err := compact(input)
		return "apply_patch", args, err
	}
	args, err := compact(input)
	return name, args, err
}

func compact(input json.RawMessage) (string, error) {
	var buf bytes.Buffer
	if err := json.Compact(&buf, input); err != nil {
		return "", fmt.Errorf("compact input: %w", err)
	}
	return buf.String(), nil
}

// toolResultOutput flattens tool_result content to the string Codex
// records: string content as is, text blocks joined by newlines, and every
// image replaced by a placeholder since a string output cannot carry one.
func toolResultOutput(content json.RawMessage) (string, error) {
	content = bytes.TrimSpace(content)
	if len(content) == 0 || bytes.Equal(content, []byte("null")) {
		return "", nil
	}
	switch content[0] {
	case '"':
		var s string
		if err := json.Unmarshal(content, &s); err != nil {
			return "", fmt.Errorf("decode content: %w", err)
		}
		return s, nil
	case '[':
	default:
		return "", errors.New("content is neither a string nor a block array")
	}
	var items []json.RawMessage
	if err := json.Unmarshal(content, &items); err != nil {
		return "", fmt.Errorf("decode content: %w", err)
	}
	parts := make([]string, 0, len(items))
	for i, it := range items {
		var head struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(it, &head); err != nil {
			return "", fmt.Errorf("decode content item %d: %w", i, err)
		}
		switch head.Type {
		case blockText:
			parts = append(parts, head.Text)
		case "image":
			parts = append(parts, omittedImage(imageMediaType(it)))
		default:
			return "", fmt.Errorf("unsupported %q content item %d", head.Type, i)
		}
	}
	return strings.Join(parts, "\n"), nil
}

// imageMediaType names an image block for its placeholder. The placeholder
// wording is the Claude converter's; unlike Claude, no image is kept, since
// a string output cannot carry one even when its data is restorable.
func imageMediaType(it json.RawMessage) string {
	var img struct {
		Source *struct {
			Type      string `json:"type"`
			MediaType string `json:"media_type"`
			Data      string `json:"data"`
		} `json:"source"`
	}
	if err := json.Unmarshal(it, &img); err != nil || img.Source == nil {
		return ""
	}
	return img.Source.MediaType
}

func omittedImage(mediaType string) string {
	if mediaType == "" {
		mediaType = "image"
	}
	return "[image omitted: " + mediaType + " could not be restored]"
}

func dropKinds(items []item, kinds ...string) []item {
	out := items[:0:0]
	for _, it := range items {
		drop := false
		for _, k := range kinds {
			if it.kind == k {
				drop = true
			}
		}
		if !drop {
			out = append(out, it)
		}
	}
	return out
}

// pairTools keeps a call only when a later output answers it and an output
// only when an earlier call asked for it; the first of each per id wins so a
// resumed thread never repeats a call id.
func pairTools(entries []entry) []entry {
	callsSeen := map[string]bool{}
	answered := map[string]bool{}
	for _, e := range entries {
		for _, it := range e.items {
			switch it.kind {
			case itemFunctionCall:
				callsSeen[it.id] = true
			case itemFunctionCallOutput:
				if callsSeen[it.id] {
					answered[it.id] = true
				}
			}
		}
	}
	keptCall := map[string]bool{}
	keptOutput := map[string]bool{}
	for i := range entries {
		var kept []item
		for _, it := range entries[i].items {
			switch it.kind {
			case itemFunctionCall:
				if !answered[it.id] || keptCall[it.id] {
					continue
				}
				keptCall[it.id] = true
			case itemFunctionCallOutput:
				if !answered[it.id] || !keptCall[it.id] || keptOutput[it.id] {
					continue
				}
				keptOutput[it.id] = true
			}
			kept = append(kept, it)
		}
		entries[i].items = kept
	}
	return entries
}

// collapseDuplicates drops entries that yield nothing and any entry whose
// role and items equal the previous kept entry's.
func collapseDuplicates(entries []entry) []entry {
	var out []entry
	prev := ""
	for _, e := range entries {
		if len(e.items) == 0 {
			continue
		}
		key := entryKey(e)
		if key == prev {
			continue
		}
		prev = key
		out = append(out, e)
	}
	return out
}

func entryKey(e entry) string {
	var sb strings.Builder
	sb.WriteString(e.role)
	for _, it := range e.items {
		sb.WriteByte(0)
		sb.Write(it.raw)
	}
	return sb.String()
}

type rolloutLine struct {
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Payload   any    `json:"payload"`
}

type sessionMeta struct {
	ID            string `json:"id"`
	Timestamp     string `json:"timestamp"`
	CWD           string `json:"cwd"`
	Originator    string `json:"originator"`
	CLIVersion    string `json:"cli_version"`
	Source        string `json:"source"`
	ModelProvider string `json:"model_provider"`
}

type turnContext struct {
	CWD            string        `json:"cwd"`
	ApprovalPolicy string        `json:"approval_policy"`
	SandboxPolicy  sandboxPolicy `json:"sandbox_policy"`
	Model          string        `json:"model"`
	Effort         string        `json:"effort,omitempty"`
	Summary        string        `json:"summary"`
}

type sandboxPolicy struct {
	Type                string   `json:"type"`
	WritableRoots       []string `json:"writable_roots"`
	NetworkAccess       bool     `json:"network_access"`
	ExcludeTmpdirEnvVar bool     `json:"exclude_tmpdir_env_var"`
	ExcludeSlashTmp     bool     `json:"exclude_slash_tmp"`
}

// encodeLines writes the rollout and reports the time of its first record,
// which names a new file's date partition.
func encodeLines(in supervisor.RebuildInput, opts Options, entries []entry) ([]byte, time.Time, error) {
	if len(entries) == 0 {
		return nil, time.Time{}, nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	started := entries[0].rec.CreatedAt
	write := func(rec *supervisor.Record, ts, typ string, payload any) error {
		if err := enc.Encode(rolloutLine{Timestamp: ts, Type: typ, Payload: payload}); err != nil {
			return conversionError(rec, "encode %s line: %v", typ, err)
		}
		return nil
	}

	startTS := stamp(started)
	meta := sessionMeta{
		ID:            in.NativeSessionID,
		Timestamp:     startTS,
		CWD:           in.WorkDir,
		Originator:    originator,
		CLIVersion:    opts.CLIVersion,
		Source:        "vscode",
		ModelProvider: opts.ModelProvider,
	}
	if err := write(entries[0].rec, startTS, "session_meta", meta); err != nil {
		return nil, time.Time{}, err
	}

	ctxLine := turnContext{
		CWD:            in.WorkDir,
		ApprovalPolicy: "on-request",
		SandboxPolicy: sandboxPolicy{
			Type:          "workspace-write",
			WritableRoots: []string{in.WorkDir},
			NetworkAccess: true,
		},
		Model:   in.Model,
		Effort:  in.Effort,
		Summary: "auto",
	}
	prevItem := ""
	prevTurn := ""
	emitted := false
	for _, e := range entries {
		ts := stamp(e.rec.CreatedAt)
		for _, it := range e.items {
			if string(it.raw) == prevItem {
				continue
			}
			prevItem = string(it.raw)
			if !emitted || e.rec.TurnID != prevTurn {
				emitted = true
				prevTurn = e.rec.TurnID
				if err := write(e.rec, ts, "turn_context", ctxLine); err != nil {
					return nil, time.Time{}, err
				}
			}
			if err := write(e.rec, ts, "response_item", it.raw); err != nil {
				return nil, time.Time{}, err
			}
			if it.event != nil {
				if err := write(e.rec, ts, "event_msg", it.event); err != nil {
					return nil, time.Time{}, err
				}
			}
		}
	}
	return buf.Bytes(), started, nil
}

func stamp(t time.Time) string { return t.UTC().Format(timestampLayout) }

func marshal(v any) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// writeFileAtomic replaces path through a same-directory temp file that is
// fsynced before the rename; the directory is fsynced after so the rename
// survives a crash. Readers never observe a partial file.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	fail := func(err error) error {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		return fail(err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	// Directory fsync is best effort: some platforms reject it on a
	// directory handle, and the rename has already replaced the file.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
