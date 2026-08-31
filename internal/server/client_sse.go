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
	"io"
	"net/http"
	"net/url"
	"strings"
)

// newSSERequest builds an authenticated SSE GET request. The session output
// stream uses the same bearer-token plumbing as the retained REST methods.
func (c *Client) newSSERequest(ctx context.Context, path string, query url.Values) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint(path, query), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	return req, nil
}

// scanSSEBlocks reads Server-Sent Events from body, accumulating lines into
// blocks separated by blank lines, and invokes dispatch on each complete
// block. It stops early if dispatch reports done or returns an error.
func scanSSEBlocks(body io.Reader, dispatch func(sseBlock) (done bool, err error)) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var block sseBlock
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			done, err := dispatch(block)
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

type sseBlock struct {
	id    string
	event string
	data  strings.Builder
}

func (b *sseBlock) addLine(line string) {
	name, value, ok := strings.Cut(line, ":")
	if !ok {
		return
	}
	value = strings.TrimPrefix(value, " ")
	switch name {
	case "id":
		b.id = value
	case "event":
		b.event = value
	case "data":
		if b.data.Len() > 0 {
			b.data.WriteByte('\n')
		}
		b.data.WriteString(value)
	}
}
