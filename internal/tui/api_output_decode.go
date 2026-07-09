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

package tui

import (
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/claude"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/codex"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/opencode"
)

// sessionOutputDecoder turns raw provider log lines (tailed from a remote
// session's /output/stream) into structured llm.SDKMessage values, using the
// same per-line parser the provider's own session uses server-side.
//
// The underlying Protocol is constructed fresh, never Handshake'd and never
// given a stdin writer, so it only ever decodes — it cannot send requests of
// its own. Codex and OpenCode correlate some response lines against
// request IDs they think they issued; since this instance issued none, those
// IDs are zero-valued and could spuriously match a real early response,
// which only affects this throwaway decoder's own bookkeeping (never a
// stdin write or an already-closed channel — verified against each
// provider's ParseLine/handleResponse). Worst case is an occasional
// misclassified line in the live preview, self-correcting on the next full
// transcript sync.
type sessionOutputDecoder struct {
	protocol llm.Protocol
}

func newSessionOutputDecoder(provider string) *sessionOutputDecoder {
	var p llm.Protocol
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "claude":
		p = claude.NewProtocol(llm.ProtocolOpts{})
	case "codex":
		p = codex.NewProtocol(llm.ProtocolOpts{})
	case "opencode":
		p = opencode.NewProtocol(llm.ProtocolOpts{})
	default:
		return &sessionOutputDecoder{}
	}
	return &sessionOutputDecoder{protocol: p}
}

// Decode parses one raw log line into zero or more SDK messages. Returns nil
// for blank lines, unrecognized providers, or lines the protocol treats as
// internal/filtered.
func (d *sessionOutputDecoder) Decode(line string) []llm.SDKMessage {
	if d == nil || d.protocol == nil || strings.TrimSpace(line) == "" {
		return nil
	}
	msgs, _ := d.protocol.ParseLine([]byte(line))
	return msgs
}
