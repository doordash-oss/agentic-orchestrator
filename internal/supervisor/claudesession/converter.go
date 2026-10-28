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

var _ supervisor.RebuiltLineLocator = (*Converter)(nil)

// Harness reports "claude".
func (c *Converter) Harness() string { return Harness }

// sessionPath is the session file the CLI resumes for sessionID in workDir:
// <configDir>/projects/<encoded workDir>/<sessionID>.jsonl.
func sessionPath(configDir, workDir, sessionID string) string {
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
	data, trim, err := render(in)
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
	path := sessionPath(configDir, in.WorkDir, in.NativeSessionID)
	if err := ctx.Err(); err != nil {
		return supervisor.RebuildResult{}, err
	}
	if err := supervisor.WriteFileAtomic(path, data, 0o700, 0o600); err != nil {
		return supervisor.RebuildResult{}, fmt.Errorf("write claude session file %s: %w", path, err)
	}
	return supervisor.RebuildResult{Resume: true, SessionID: in.NativeSessionID, Path: path, Trim: trim}, nil
}

// Render converts in.Records to the bytes of a Claude session JSONL file
// without touching the file system. It returns nil when the selection is
// empty. Argument errors are plain errors; unrepresentable records are
// *supervisor.ConversionError.
func Render(in supervisor.RebuildInput) ([]byte, error) {
	data, _, err := render(in)
	return data, err
}

func render(in supervisor.RebuildInput) ([]byte, supervisor.TrimStats, error) {
	if err := supervisor.ValidateNativeRebuildInput(in, "claude session rebuild"); err != nil {
		return nil, supervisor.TrimStats{}, err
	}
	selection, err := supervisor.SelectCheckpoint(in.Records, Harness)
	if err != nil {
		return nil, supervisor.TrimStats{}, err
	}
	entries, records, trim, err := supervisor.SelectTrimmedEntries(selection.Records, selectionRules())
	if err != nil {
		return nil, supervisor.TrimStats{}, err
	}
	in.Records = records
	data, err := encodeLines(in, entries, selection)
	return data, trim, err
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

// selectionRules wires the Claude entry and block types into the shared
// selection algorithm. The duplicate key covers role, prompt and raw blocks,
// and a prompt-only entry is non-empty: a lone user prompt is still history.
func selectionRules() supervisor.SelectionRules[entry, block] {
	return supervisor.SelectionRules[entry, block]{
		Decode:         decodeEntry,
		Parts:          func(e *entry) []block { return e.blocks },
		SetParts:       func(e *entry, parts []block) { e.blocks = parts },
		PartKind:       func(b block) string { return b.kind },
		PartID:         func(b block) string { return b.id },
		ToolUseKind:    blockToolUse,
		ToolResultKind: blockToolResult,
		EntryKey:       envelopeKey,
		NonEmpty:       func(e entry) bool { return e.prompt != nil || len(e.blocks) > 0 },
	}
}

func decodeEntry(rec *supervisor.Record) (entry, error) {
	if rec.Kind == supervisor.KindUser || rec.Kind == supervisor.KindNote {
		var u supervisor.UserData
		if err := supervisor.DecodeRecordPayload(rec, &u); err != nil {
			return entry{}, err
		}
		e := entry{rec: rec, role: roleUser}
		if text := supervisor.RenderUserMessage(u); strings.TrimSpace(text) != "" {
			e.prompt = &text
		}
		return e, nil
	}

	var cd supervisor.ContentData
	if err := supervisor.DecodeRecordPayload(rec, &cd); err != nil {
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
		return supervisor.OmittedImagePlaceholder(""), true
	}
	src := img.Source
	switch {
	case src.Type == "url" && src.URL != "":
		return "", false
	case src.Type == "base64" && restorableBase64(src.MediaType, src.Data):
		return "", false
	}
	return supervisor.OmittedImagePlaceholder(src.MediaType), true
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

// LocateRebuiltLine returns the seq of the earliest record rendered as the
// session line id.
func (c *Converter) LocateRebuiltLine(conversationID string, records []supervisor.Record, id string) (int64, bool) {
	for i := range records {
		e, err := decodeEntry(&records[i])
		if err != nil {
			continue
		}
		seq := records[i].Seq
		if e.prompt != nil && lineID(conversationID, seq, 0, "line") == id {
			return seq, true
		}
		for _, b := range e.blocks {
			if lineID(conversationID, seq, b.index, "line") == id {
				return seq, true
			}
		}
	}
	return 0, false
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
