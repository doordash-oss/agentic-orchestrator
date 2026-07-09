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
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// SessionOutputStreamOptions configures a SubscribeSessionOutput call.
type SessionOutputStreamOptions struct {
	// AfterOffset resumes the tail from this byte offset. Zero starts from
	// the beginning of the session's log.
	AfterOffset int64
}

// SessionOutputLine is one decoded raw provider line from a session's log,
// recovered from the byte-window chunks the server streams.
type SessionOutputLine struct {
	SessionID string
	Text      string
}

// SubscribeSessionOutput tails a single session's raw output over
// /api/v1/sessions/{id}/output/stream, recombining the server's byte-window
// chunks into whole lines. The channel closes when the session finishes
// (session.output.done) or the context is cancelled; callers wanting
// reconnect-on-error semantics should watch the error channel and re-call.
func (c *Client) SubscribeSessionOutput(ctx context.Context, sessionID string, opts SessionOutputStreamOptions) (<-chan SessionOutputLine, <-chan error) {
	lines := make(chan SessionOutputLine, 64)
	errs := make(chan error, 1)
	go func() {
		defer close(lines)
		defer close(errs)
		if err := c.consumeSessionOutput(ctx, sessionID, opts, lines); err != nil {
			select {
			case errs <- err:
			case <-ctx.Done():
			}
		}
	}()
	return lines, errs
}

func (c *Client) consumeSessionOutput(ctx context.Context, sessionID string, opts SessionOutputStreamOptions, lines chan<- SessionOutputLine) error {
	query := url.Values{}
	if opts.AfterOffset > 0 {
		query.Set("from", strconv.FormatInt(opts.AfterOffset, 10))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint("/api/v1/sessions/"+pathSegment(sessionID)+"/output/stream", query), nil)
	if err != nil {
		return fmt.Errorf("build session output request: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("connect session output stream: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return decodeAPIError(resp, http.MethodGet, "/api/v1/sessions/"+sessionID+"/output/stream")
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var block sseBlock
	var pending strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			done, err := dispatchSessionOutputBlock(sessionID, block, &pending, lines, ctx)
			if err != nil {
				return err
			}
			block = sseBlock{}
			if done {
				return nil
			}
			continue
		}
		block.addLine(line)
	}
	return scanner.Err()
}

func dispatchSessionOutputBlock(sessionID string, block sseBlock, pending *strings.Builder, lines chan<- SessionOutputLine, ctx context.Context) (bool, error) {
	raw := strings.TrimSpace(block.data.String())
	if raw == "" {
		return false, nil
	}
	var resp SessionOutputResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		return false, fmt.Errorf("decode session output payload: %w", err)
	}
	event := block.event
	if event == "session.output.error" {
		return false, nil
	}
	pending.WriteString(resp.Data)
	buffered := pending.String()
	segments := strings.Split(buffered, "\n")
	pending.Reset()
	if len(segments) > 0 {
		if strings.HasSuffix(buffered, "\n") {
			segments = segments[:len(segments)-1]
		} else {
			pending.WriteString(segments[len(segments)-1])
			segments = segments[:len(segments)-1]
		}
	}
	for _, seg := range segments {
		select {
		case lines <- SessionOutputLine{SessionID: sessionID, Text: seg}:
		case <-ctx.Done():
			return true, nil
		}
	}
	return event == "session.output.done" || resp.Done, nil
}
