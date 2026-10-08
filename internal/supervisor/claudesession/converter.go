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

// Package claudesession rebuilds a Claude CLI session JSONL file from the
// supervisor's durable transcript so a relaunched Claude process can
// `--resume` with the conversation's history. Claude's session format is
// undocumented; everything that depends on it lives in this package.
//
// The written file mirrors what the CLI itself writes, reduced to the
// fields resume needs: one line per content block, each carrying `type`,
// `message {role, content}`, `uuid`, `parentUuid` (chained in file order
// from a null root), `isSidechain: false`, `userType: "external"`, `cwd`,
// `sessionId` and `timestamp`. Assistant lines also carry `message.id` and
// `message.type: "message"`, the key the CLI uses to merge the per-block
// lines of one assistant turn back into a single API message; consecutive
// assistant lines share one id. `message.model` is never written.
package claudesession

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"  // register decoder for image validation
	_ "image/jpeg" // register decoder for image validation
	_ "image/png"  // register decoder for image validation
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/doordash-oss/agentic-orchestrator/internal/claudeconfig"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/supervisor"
)

// Harness is the harness name this converter is registered under.
const Harness = "claude"

// timestampLayout matches the CLI's own timestamps: UTC with milliseconds.
const timestampLayout = "2006-01-02T15:04:05.000Z"

// idNamespace seeds the deterministic line uuids and message ids so the same
// transcript always yields byte-identical files.
var idNamespace = uuid.NewSHA1(uuid.NameSpaceURL, []byte("https://github.com/doordash-oss/agentic-orchestrator/claudesession"))

// Options configures a Converter.
type Options struct {
	// ConfigDir resolves the Claude configuration directory. Nil means
	// claudeconfig.DefaultDir (CLAUDE_CONFIG_DIR, else $HOME/.claude).
	ConfigDir func() (string, error)
}

// Converter implements supervisor.Converter for the Claude CLI.
type Converter struct {
	configDir func() (string, error)
}

var _ supervisor.Converter = (*Converter)(nil)

// New returns a Claude session converter.
func New(opts Options) *Converter {
	dir := opts.ConfigDir
	if dir == nil {
		dir = claudeconfig.DefaultDir
	}
	return &Converter{configDir: dir}
}

// Harness reports "claude".
func (c *Converter) Harness() string { return Harness }

// SessionPath is the session file the CLI resumes for sessionID in workDir:
// <configDir>/projects/<encoded workDir>/<sessionID>.jsonl.
func SessionPath(configDir, workDir, sessionID string) string {
	return filepath.Join(claudeconfig.ProjectsDir(configDir, workDir), sessionID+".jsonl")
}

// Rebuild renders in.Records and atomically replaces the session file for
// in.NativeSessionID. An empty selection writes nothing and returns
// Resume=false. A record that cannot be represented returns a
// *supervisor.ConversionError and nothing is written. A missing
// NativeSessionID or WorkDir, an unsafe NativeSessionID, a cancelled
// context, an unresolvable configuration directory and any file-system
// failure return a plain error that is not a ConversionError: these are
// caller or environment faults, not properties of the history.
func (c *Converter) Rebuild(ctx context.Context, in supervisor.RebuildInput) (supervisor.RebuildResult, error) {
	if err := ctx.Err(); err != nil {
		return supervisor.RebuildResult{}, err
	}
	// The CLI records and looks up sessions under its physical cwd.
	if in.WorkDir != "" {
		in.WorkDir = claudeconfig.ResolveWorkDir(in.WorkDir)
	}
	data, err := Render(in)
	if err != nil {
		return supervisor.RebuildResult{}, err
	}
	if len(data) == 0 {
		return supervisor.RebuildResult{SessionID: in.NativeSessionID}, nil
	}
	configDir, err := c.configDir()
	if err != nil {
		return supervisor.RebuildResult{}, fmt.Errorf("resolve claude config dir: %w", err)
	}
	if configDir == "" {
		return supervisor.RebuildResult{}, errors.New("resolve claude config dir: empty path")
	}
	path := SessionPath(configDir, in.WorkDir, in.NativeSessionID)
	if err := ctx.Err(); err != nil {
		return supervisor.RebuildResult{}, err
	}
	if err := writeFileAtomic(path, data); err != nil {
		return supervisor.RebuildResult{}, fmt.Errorf("write claude session file %s: %w", path, err)
	}
	return supervisor.RebuildResult{Resume: true, SessionID: in.NativeSessionID, Path: path}, nil
}

// Render converts in.Records to the bytes of a Claude session JSONL file
// without touching the file system. It returns nil when the selection is
// empty. Argument errors are plain errors; unrepresentable records are
// *supervisor.ConversionError.
func Render(in supervisor.RebuildInput) ([]byte, error) {
	if err := validateInput(in); err != nil {
		return nil, err
	}
	selection, err := supervisor.SelectCheckpoint(in.Records, Harness)
	if err != nil {
		return nil, err
	}
	in.Records = selection.Records
	entries, err := selectEntries(in.Records)
	if err != nil {
		return nil, err
	}
	return encodeLines(in, entries, selection)
}

func validateInput(in supervisor.RebuildInput) error {
	if in.NativeSessionID == "" {
		return errors.New("claude session rebuild: empty native session id")
	}
	if strings.ContainsAny(in.NativeSessionID, `/\`) || in.NativeSessionID == "." || in.NativeSessionID == ".." {
		return fmt.Errorf("claude session rebuild: unsafe native session id %q", in.NativeSessionID)
	}
	if in.WorkDir == "" {
		return errors.New("claude session rebuild: empty working directory")
	}
	return nil
}

const (
	roleUser      = "user"
	roleAssistant = "assistant"

	blockText       = "text"
	blockToolUse    = "tool_use"
	blockToolResult = "tool_result"
)

// entry is one selected record reduced to the blocks it contributes.
type entry struct {
	rec    *supervisor.Record
	role   string
	prompt *string // user prompt text: rendered as a string content line
	blocks []block
}

// block is one rendered content block plus what pairing needs to know.
type block struct {
	index int // position in the record's content, for stable ids
	kind  string
	id    string // tool_use id, or the tool_use_id of a tool_result
	raw   json.RawMessage
}

func conversionError(rec *supervisor.Record, format string, args ...any) error {
	return &supervisor.ConversionError{Seq: rec.Seq, Reason: fmt.Sprintf(format, args...)}
}

// selectEntries applies selection and every exclusion: interrupted turns'
// tool records, unpaired tool blocks and adjacent duplicates.
func selectEntries(records []supervisor.Record) ([]entry, error) {
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
		e, err := decodeEntry(rec)
		if err != nil {
			return nil, err
		}
		if rec.TurnID != "" && interrupted[rec.TurnID] {
			e.blocks = dropKinds(e.blocks, blockToolUse, blockToolResult)
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

func decodeEntry(rec *supervisor.Record) (entry, error) {
	if rec.Kind == supervisor.KindUser || rec.Kind == supervisor.KindNote {
		var u supervisor.UserData
		if err := decodePayload(rec, &u); err != nil {
			return entry{}, err
		}
		e := entry{rec: rec, role: roleUser}
		if text := supervisor.RenderUserMessage(u); strings.TrimSpace(text) != "" {
			e.prompt = &text
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
		b, ok, err := renderBlock(rec, role, i, cb)
		if err != nil {
			return entry{}, err
		}
		if ok {
			e.blocks = append(e.blocks, b)
		}
	}
	return e, nil
}

// Output block shapes, field order as the CLI writes them.
type textBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolUseBlock struct {
	Type  string          `json:"type"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type toolResultBlock struct {
	ToolUseID string          `json:"tool_use_id"`
	Type      string          `json:"type"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error"`
}

// renderBlock maps one durable content block. ok=false skips the block
// (thinking, empty text); an error means the block cannot be represented.
func renderBlock(rec *supervisor.Record, role string, index int, cb llm.ContentBlock) (block, bool, error) {
	switch cb.Type {
	case "thinking", "redacted_thinking":
		return block{}, false, nil
	case blockText:
		if role != roleAssistant {
			break
		}
		if strings.TrimSpace(cb.Text) == "" {
			return block{}, false, nil
		}
		raw, err := marshal(textBlock{Type: blockText, Text: cb.Text})
		if err != nil {
			return block{}, false, conversionError(rec, "encode text block %d: %v", index, err)
		}
		return block{index: index, kind: blockText, raw: raw}, true, nil
	case blockToolUse:
		if role != roleAssistant {
			break
		}
		if cb.ID == "" || cb.Name == "" {
			return block{}, false, conversionError(rec, "tool_use block %d lacks id or name", index)
		}
		input := bytes.TrimSpace(cb.Input)
		if len(input) == 0 || bytes.Equal(input, []byte("null")) {
			input = json.RawMessage(`{}`)
		} else if input[0] != '{' {
			return block{}, false, conversionError(rec, "tool_use block %d input is not an object", index)
		}
		raw, err := marshal(toolUseBlock{Type: blockToolUse, ID: cb.ID, Name: cb.Name, Input: input})
		if err != nil {
			return block{}, false, conversionError(rec, "encode tool_use block %d: %v", index, err)
		}
		return block{index: index, kind: blockToolUse, id: cb.ID, raw: raw}, true, nil
	case blockToolResult:
		if role != roleUser {
			break
		}
		if cb.ToolUseID == "" {
			return block{}, false, conversionError(rec, "tool_result block %d lacks tool_use_id", index)
		}
		content, err := sanitizeToolResultContent(cb.Content)
		if err != nil {
			return block{}, false, conversionError(rec, "tool_result block %d: %v", index, err)
		}
		raw, err := marshal(toolResultBlock{ToolUseID: cb.ToolUseID, Type: blockToolResult, Content: content, IsError: cb.IsError})
		if err != nil {
			return block{}, false, conversionError(rec, "encode tool_result block %d: %v", index, err)
		}
		return block{index: index, kind: blockToolResult, id: cb.ToolUseID, raw: raw}, true, nil
	}
	return block{}, false, conversionError(rec, "unsupported %q block %d in %s record", cb.Type, index, rec.Kind)
}

// sanitizeToolResultContent keeps string content as is and replaces image
// blocks whose data cannot be restored with a text placeholder.
func sanitizeToolResultContent(content json.RawMessage) (json.RawMessage, error) {
	content = bytes.TrimSpace(content)
	if len(content) == 0 || bytes.Equal(content, []byte("null")) {
		return nil, nil
	}
	switch content[0] {
	case '"':
		return content, nil
	case '[':
	default:
		return nil, errors.New("content is neither a string nor a block array")
	}
	var items []json.RawMessage
	if err := json.Unmarshal(content, &items); err != nil {
		return nil, fmt.Errorf("decode content: %w", err)
	}
	changed := false
	for i, item := range items {
		var head struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(item, &head); err != nil {
			return nil, fmt.Errorf("decode content item %d: %w", i, err)
		}
		if head.Type != "image" {
			continue
		}
		if placeholder, ok := imagePlaceholder(item); ok {
			raw, err := marshal(textBlock{Type: blockText, Text: placeholder})
			if err != nil {
				return nil, err
			}
			items[i] = raw
			changed = true
		}
	}
	if !changed {
		return content, nil
	}
	return marshal(items)
}

// imagePlaceholder reports whether an image block must be replaced and the
// placeholder text naming it. Base64 images are kept only when the data
// decodes and, for formats the standard library knows, the image decodes in
// full (a clipped payload fails here even when its base64 is well formed).
func imagePlaceholder(item json.RawMessage) (string, bool) {
	var img struct {
		Source *struct {
			Type      string `json:"type"`
			MediaType string `json:"media_type"`
			Data      string `json:"data"`
			URL       string `json:"url"`
		} `json:"source"`
	}
	if err := json.Unmarshal(item, &img); err != nil || img.Source == nil {
		return omittedImage(""), true
	}
	src := img.Source
	switch {
	case src.Type == "url" && src.URL != "":
		return "", false
	case src.Type == "base64" && restorableBase64(src.MediaType, src.Data):
		return "", false
	}
	return omittedImage(src.MediaType), true
}

func restorableBase64(mediaType, data string) bool {
	if data == "" {
		return false
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(data)
	if err != nil || len(decoded) == 0 {
		return false
	}
	switch mediaType {
	case "image/png", "image/jpeg", "image/gif":
		_, _, err := image.Decode(bytes.NewReader(decoded))
		return err == nil
	}
	return true
}

func omittedImage(mediaType string) string {
	if mediaType == "" {
		mediaType = "image"
	}
	return "[image omitted: " + mediaType + " could not be restored]"
}

func dropKinds(blocks []block, kinds ...string) []block {
	out := blocks[:0:0]
	for _, b := range blocks {
		drop := false
		for _, k := range kinds {
			if b.kind == k {
				drop = true
			}
		}
		if !drop {
			out = append(out, b)
		}
	}
	return out
}

// pairTools keeps a tool_use only when a later tool_result answers it and a
// tool_result only when an earlier tool_use asked for it; the first of each
// per id wins so a resumed request never repeats a tool id.
func pairTools(entries []entry) []entry {
	usesSeen := map[string]bool{}
	answered := map[string]bool{}
	for _, e := range entries {
		for _, b := range e.blocks {
			switch b.kind {
			case blockToolUse:
				usesSeen[b.id] = true
			case blockToolResult:
				if usesSeen[b.id] {
					answered[b.id] = true
				}
			}
		}
	}
	keptUse := map[string]bool{}
	keptResult := map[string]bool{}
	for i := range entries {
		var kept []block
		for _, b := range entries[i].blocks {
			switch b.kind {
			case blockToolUse:
				if !answered[b.id] || keptUse[b.id] {
					continue
				}
				keptUse[b.id] = true
			case blockToolResult:
				if !answered[b.id] || !keptUse[b.id] || keptResult[b.id] {
					continue
				}
				keptResult[b.id] = true
			}
			kept = append(kept, b)
		}
		entries[i].blocks = kept
	}
	return entries
}

// collapseDuplicates drops entries that yield nothing and any entry whose
// role and content equal the previous kept entry's.
func collapseDuplicates(entries []entry) []entry {
	var out []entry
	prev := ""
	for _, e := range entries {
		if e.prompt == nil && len(e.blocks) == 0 {
			continue
		}
		key := envelopeKey(e)
		if key == prev {
			continue
		}
		prev = key
		out = append(out, e)
	}
	return out
}

func envelopeKey(e entry) string {
	var sb strings.Builder
	sb.WriteString(e.role)
	if e.prompt != nil {
		sb.WriteString("\x00prompt\x00")
		sb.WriteString(*e.prompt)
	}
	for _, b := range e.blocks {
		sb.WriteByte(0)
		sb.Write(b.raw)
	}
	return sb.String()
}

type sessionLine struct {
	ParentUUID       *string          `json:"parentUuid"`
	IsSidechain      bool             `json:"isSidechain"`
	Type             string           `json:"type"`
	Message          lineMessage      `json:"message"`
	UUID             string           `json:"uuid"`
	Timestamp        string           `json:"timestamp"`
	UserType         string           `json:"userType"`
	CWD              string           `json:"cwd"`
	SessionID        string           `json:"sessionId"`
	Subtype          string           `json:"subtype,omitempty"`
	CompactMetadata  *compactMetadata `json:"compactMetadata,omitempty"`
	IsCompactSummary bool             `json:"isCompactSummary,omitempty"`
}

type compactMetadata struct {
	Trigger   string `json:"trigger,omitempty"`
	PreTokens int    `json:"preTokens,omitempty"`
}

type lineMessage struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type,omitempty"`
	Role    string `json:"role"`
	Content any    `json:"content"`
}

func encodeLines(in supervisor.RebuildInput, entries []entry, selection supervisor.CheckpointSelection) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	var parent *string
	if rec := selection.Checkpoint; rec != nil {
		data := selection.Data
		emitCheckpointLine := func(index int, role string, content any, subtype string, metadata *compactMetadata, summary bool) error {
			id := lineID(in.ConversationID, rec.Seq, index, "checkpoint")
			line := sessionLine{ParentUUID: parent, Type: role, Message: lineMessage{Role: role, Content: content}, UUID: id,
				Timestamp: rec.CreatedAt.UTC().Format(timestampLayout), UserType: "external", CWD: in.WorkDir,
				SessionID: in.NativeSessionID, Subtype: subtype, CompactMetadata: metadata, IsCompactSummary: summary}
			if err := enc.Encode(line); err != nil {
				return conversionError(rec, "encode checkpoint line: %v", err)
			}
			parent = &id
			return nil
		}
		if data.NativeBaseline != nil && data.NativeBaseline.Harness == Harness {
			if err := emitCheckpointLine(0, "system", "", "compact_boundary", &compactMetadata{Trigger: data.Trigger, PreTokens: data.PreTokens}, false); err != nil {
				return nil, err
			}
			var baseline struct {
				Content json.RawMessage `json:"content"`
			}
			if err := json.Unmarshal(data.NativeBaseline.Payload, &baseline); err != nil {
				return nil, conversionError(rec, "decode Claude baseline: %v", err)
			}
			var content any = data.Summary
			if len(baseline.Content) > 0 && json.Valid(baseline.Content) {
				content = baseline.Content
			}
			if err := emitCheckpointLine(1, roleUser, content, "", nil, true); err != nil {
				return nil, err
			}
		} else {
			if err := emitCheckpointLine(0, roleUser, supervisor.CheckpointNote(data), "", nil, false); err != nil {
				return nil, err
			}
		}
	}
	prevKey := ""
	messageID := ""
	for _, e := range entries {
		emit := func(index int, content any, key string) error {
			if key == prevKey {
				return nil
			}
			prevKey = key
			id := lineID(in.ConversationID, e.rec.Seq, index, "line")
			msg := lineMessage{Role: e.role, Content: content}
			if e.role == roleAssistant {
				if messageID == "" {
					messageID = "msg_" + strings.ReplaceAll(lineID(in.ConversationID, e.rec.Seq, index, "message"), "-", "")
				}
				msg.ID, msg.Type = messageID, "message"
			} else {
				messageID = ""
			}
			line := sessionLine{
				ParentUUID:  parent,
				IsSidechain: false,
				Type:        e.role,
				Message:     msg,
				UUID:        id,
				Timestamp:   e.rec.CreatedAt.UTC().Format(timestampLayout),
				UserType:    "external",
				CWD:         in.WorkDir,
				SessionID:   in.NativeSessionID,
			}
			if err := enc.Encode(line); err != nil {
				return conversionError(e.rec, "encode session line: %v", err)
			}
			parent = &id
			return nil
		}
		if e.prompt != nil {
			if err := emit(0, *e.prompt, e.role+"\x00prompt\x00"+*e.prompt); err != nil {
				return nil, err
			}
		}
		for _, b := range e.blocks {
			if err := emit(b.index, []json.RawMessage{b.raw}, e.role+"\x00"+string(b.raw)); err != nil {
				return nil, err
			}
		}
	}
	if buf.Len() == 0 {
		return nil, nil
	}
	return buf.Bytes(), nil
}

func lineID(conversationID string, seq int64, index int, purpose string) string {
	name := conversationID + "/" + strconv.FormatInt(seq, 10) + "/" + strconv.Itoa(index) + "/" + purpose
	return uuid.NewSHA1(idNamespace, []byte(name)).String()
}

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
