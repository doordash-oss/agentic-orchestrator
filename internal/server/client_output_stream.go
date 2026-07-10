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
	// AfterIndex resumes from this transcript row index. Zero starts from
	// the beginning of the session's transcript.
	AfterIndex int
}

// SessionOutputRecord is one structured transcript row delivered live over
// /output/stream — the same TranscriptMessageDTO shape the /transcript REST
// endpoint returns, addressed by the same row index.
type SessionOutputRecord struct {
	SessionID string
	Index     int
	Message   TranscriptMessageDTO
}

// SubscribeSessionOutput tails a single session's transcript over
// /api/v1/sessions/{id}/output/stream. The channel closes when the session
// finishes (session.output.done) or the context is cancelled.
func (c *Client) SubscribeSessionOutput(ctx context.Context, sessionID string, opts SessionOutputStreamOptions) (<-chan SessionOutputRecord, <-chan error) {
	records := make(chan SessionOutputRecord, 64)
	errs := make(chan error, 1)
	go func() {
		defer close(records)
		defer close(errs)
		if err := c.consumeSessionOutput(ctx, sessionID, opts, records); err != nil {
			errs <- err
		}
	}()
	return records, errs
}

func (c *Client) consumeSessionOutput(ctx context.Context, sessionID string, opts SessionOutputStreamOptions, records chan<- SessionOutputRecord) error {
	query := url.Values{}
	if opts.AfterIndex > 0 {
		query.Set("from", strconv.Itoa(opts.AfterIndex))
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
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			done, err := dispatchSessionOutputBlock(block, records, ctx)
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

func dispatchSessionOutputBlock(block sseBlock, records chan<- SessionOutputRecord, ctx context.Context) (bool, error) {
	raw := strings.TrimSpace(block.data.String())
	if raw == "" {
		return false, nil
	}
	var chunk SessionOutputChunk
	if err := json.Unmarshal([]byte(raw), &chunk); err != nil {
		return false, fmt.Errorf("decode session output payload: %w", err)
	}
	if block.event == "session.output.error" {
		return false, fmt.Errorf("session output stream error at index %d", chunk.Index)
	}
	if !chunk.Done {
		select {
		case records <- SessionOutputRecord{SessionID: chunk.SessionID, Index: chunk.Index, Message: chunk.Message}:
		case <-ctx.Done():
			return true, nil
		}
	}
	return block.event == "session.output.done" || chunk.Done, nil
}
