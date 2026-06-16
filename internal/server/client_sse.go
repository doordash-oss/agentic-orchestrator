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
	"time"
)

type EventSubscriptionOptions struct {
	HeartbeatInterval time.Duration
	ReconnectDelay    time.Duration
	MaxReconnects     int
}

type RefreshSignal struct {
	Event            SSEEventDTO
	Resource         ResourceDTO
	SnapshotRequired bool
}

type RefreshSnapshot struct {
	Health        *HealthResponse
	Features      *FeatureListResponse
	Feature       *FeatureDetailResponse
	RuntimeConfig *RuntimeConfigResponse
	FeatureConfig *FeatureConfigResponse
	Prompts       *PromptSnapshotResponse
	Permissions   *PermissionSnapshotResponse
	Recovery      *RecoverySnapshotResponse
	Sessions      *SessionListResponse
	Session       *SessionDetailResponse
	Transcript    *TranscriptResponse
	LivePreview   *LivePreviewResponse
}

const refreshTranscriptLimit = 50

func (c *Client) SubscribeEvents(ctx context.Context, opts EventSubscriptionOptions) (<-chan RefreshSignal, <-chan error) {
	signals := make(chan RefreshSignal, 16)
	errs := make(chan error, 1)
	go func() {
		defer close(signals)
		defer close(errs)
		if err := c.eventLoop(ctx, opts, signals); err != nil {
			errs <- err
		}
	}()
	return signals, errs
}

func (c *Client) eventLoop(ctx context.Context, opts EventSubscriptionOptions, signals chan<- RefreshSignal) error {
	reconnectDelay := opts.ReconnectDelay
	if reconnectDelay <= 0 {
		reconnectDelay = 250 * time.Millisecond
	}
	reconnects := 0
	for {
		if ctx.Err() != nil {
			return nil
		}
		err := c.consumeEvents(ctx, opts, signals)
		if ctx.Err() != nil {
			return nil
		}
		reconnects++
		if opts.MaxReconnects > 0 && reconnects > opts.MaxReconnects {
			if err != nil {
				return fmt.Errorf("event stream reconnect limit reached: %w", err)
			}
			return fmt.Errorf("event stream reconnect limit reached")
		}
		timer := time.NewTimer(reconnectDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func (c *Client) consumeEvents(ctx context.Context, opts EventSubscriptionOptions, signals chan<- RefreshSignal) error {
	query := url.Values{}
	if opts.HeartbeatInterval > 0 {
		query.Set("heartbeat_ms", strconv.FormatInt(opts.HeartbeatInterval.Milliseconds(), 10))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint("/api/v1/events", query), nil)
	if err != nil {
		return fmt.Errorf("build event request: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("connect event stream: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return decodeAPIError(resp, http.MethodGet, "/api/v1/events")
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var block sseBlock
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := c.dispatchSSEBlock(ctx, block, signals); err != nil {
				return err
			}
			block = sseBlock{}
			continue
		}
		block.addLine(line)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read event stream: %w", err)
	}
	return nil
}

func (c *Client) dispatchSSEBlock(ctx context.Context, block sseBlock, signals chan<- RefreshSignal) error {
	if strings.TrimSpace(block.data.String()) == "" {
		return nil
	}
	var evt SSEEventDTO
	if err := json.Unmarshal([]byte(block.data.String()), &evt); err != nil {
		return fmt.Errorf("decode event stream payload: %w", err)
	}
	if evt.Kind == "" {
		evt.Kind = block.event
	}
	if evt.ID == "" {
		evt.ID = block.id
	}
	if evt.Kind == "heartbeat" && !evt.SnapshotRequired {
		return nil
	}
	signal := RefreshSignal{
		Event:            evt,
		Resource:         evt.Resource,
		SnapshotRequired: evt.SnapshotRequired,
	}
	select {
	case signals <- signal:
		return nil
	case <-ctx.Done():
		return nil
	}
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

func (c *Client) FetchRefreshSnapshot(ctx context.Context, signal RefreshSignal) (RefreshSnapshot, error) {
	evt := signal.Event
	resource := signal.Resource
	if resource.Type == "" {
		resource = evt.Resource
	}
	switch {
	case evt.Kind == "config.updated":
		if resource.FeatureID != "" {
			cfg, err := c.FeatureConfig(ctx, resource.FeatureID)
			return RefreshSnapshot{FeatureConfig: &cfg}, err
		}
		cfg, err := c.RuntimeConfig(ctx)
		return RefreshSnapshot{RuntimeConfig: &cfg}, err
	case evt.Kind == "prompt.updated":
		prompts, err := c.Prompts(ctx)
		return RefreshSnapshot{Prompts: &prompts}, err
	case evt.Kind == "permission.updated":
		permissions, err := c.Permissions(ctx)
		return RefreshSnapshot{Permissions: &permissions}, err
	case resource.Type == "live_preview" || evt.Kind == "live_preview.updated":
		preview, err := c.LivePreview(ctx, resource.FeatureID)
		return RefreshSnapshot{LivePreview: &preview}, err
	case resource.Type == "session" || evt.Kind == "session.updated" || evt.Kind == "log.updated":
		snapshot := RefreshSnapshot{}
		if resource.ID != "" {
			session, err := c.SessionDetail(ctx, resource.ID)
			if err != nil {
				return snapshot, err
			}
			snapshot.Session = &session
			end := session.Session.TranscriptCursor.End
			if end == 0 {
				end = session.Session.TranscriptCursor.Total
			}
			if end > 0 {
				start := max(0, end-refreshTranscriptLimit)
				transcript, err := c.Transcript(ctx, resource.ID, CursorQuery{Cursor: start, Limit: refreshTranscriptLimit})
				if err != nil {
					return snapshot, err
				}
				snapshot.Transcript = &transcript
			}
		} else {
			sessions, err := c.Sessions(ctx)
			if err != nil {
				return snapshot, err
			}
			snapshot.Sessions = &sessions
		}
		if resource.FeatureID != "" {
			preview, err := c.LivePreview(ctx, resource.FeatureID)
			if err != nil {
				return snapshot, err
			}
			snapshot.LivePreview = &preview
		}
		return snapshot, nil
	case resource.FeatureID != "":
		feature, err := c.FeatureDetail(ctx, resource.FeatureID)
		if err != nil {
			return RefreshSnapshot{}, err
		}
		preview, err := c.LivePreview(ctx, resource.FeatureID)
		if err != nil {
			return RefreshSnapshot{Feature: &feature}, err
		}
		return RefreshSnapshot{Feature: &feature, LivePreview: &preview}, nil
	case evt.Kind == "recovery.updated":
		health, err := c.Health(ctx)
		if err != nil {
			return RefreshSnapshot{Health: &health}, err
		}
		recovery, err := c.Recovery(ctx)
		return RefreshSnapshot{Health: &health, Recovery: &recovery}, err
	case resource.Type == "runtime" || evt.Kind == "connected" || evt.Kind == "shutdown.updated":
		health, err := c.Health(ctx)
		return RefreshSnapshot{Health: &health}, err
	default:
		features, err := c.Features(ctx)
		return RefreshSnapshot{Features: &features}, err
	}
}
