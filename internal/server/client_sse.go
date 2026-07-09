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
	AfterSeq          uint64
	Epoch             string
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
const chatUtilityFeatureID = ChatSessionID

func isUtilityFeatureID(featureID string) bool {
	return featureID == chatUtilityFeatureID
}

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
	cursor := eventStreamCursor{seq: opts.AfterSeq, epoch: opts.Epoch}
	reconnects := 0
	for {
		if ctx.Err() != nil {
			return nil
		}
		err := c.consumeEvents(ctx, opts, signals, &cursor)
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

type eventStreamCursor struct {
	seq   uint64
	epoch string
}

func (c *Client) consumeEvents(ctx context.Context, opts EventSubscriptionOptions, signals chan<- RefreshSignal, cursor *eventStreamCursor) error {
	query := url.Values{}
	if opts.HeartbeatInterval > 0 {
		query.Set("heartbeat_ms", strconv.FormatInt(opts.HeartbeatInterval.Milliseconds(), 10))
	}
	if cursor != nil && cursor.seq > 0 {
		query.Set("after", strconv.FormatUint(cursor.seq, 10))
		if cursor.epoch != "" {
			query.Set("epoch", cursor.epoch)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint("/api/v1/events", query), nil)
	if err != nil {
		return fmt.Errorf("build event request: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
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
			if err := c.dispatchSSEBlock(ctx, block, signals, cursor); err != nil {
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

func (c *Client) dispatchSSEBlock(ctx context.Context, block sseBlock, signals chan<- RefreshSignal, cursor *eventStreamCursor) error {
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
	if evt.Seq == 0 && evt.ID != "" {
		evt.Seq, _ = strconv.ParseUint(evt.ID, 10, 64)
	}
	if cursor != nil && evt.Seq > 0 {
		cursor.seq = evt.Seq
		if evt.Epoch != "" {
			cursor.epoch = evt.Epoch
		}
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
	// `connected` (and any forced-resync heartbeat) is a pure resync signal: it
	// carries no resource of its own, so the client must re-pull read-model
	// state because it may have missed events while disconnected. (Re)connect
	// is the only refresh trigger for a session sitting idle in WaitingHelp —
	// it emits no further events — so without a full re-snapshot here the
	// pending question never reaches the prompt snapshot that drives the
	// dashboard help badge and the attach question panel. Note: ordinary events
	// also set snapshot_required, but they carry a specific kind/resource that
	// the switch below uses to fetch only what changed.
	if evt.Kind == "connected" || (evt.Kind == "heartbeat" && signal.SnapshotRequired) {
		return c.fetchFullSnapshot(ctx)
	}
	if evt.Kind == "session.output.activity" {
		return RefreshSnapshot{}, nil
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
	case resource.Type == "session" || evt.Kind == "session.updated":
		snapshot := RefreshSnapshot{}
		featureID := resource.FeatureID
		refreshFeatureDetail := false
		if resource.ID != "" {
			terminal, err := c.hydrateSessionRefreshSnapshot(ctx, &snapshot, resource.ID, &featureID)
			if err != nil {
				return snapshot, err
			}
			refreshFeatureDetail = terminal
		} else {
			sessions, err := c.Sessions(ctx)
			if err != nil {
				return snapshot, err
			}
			snapshot.Sessions = &sessions
		}
		if featureID != "" && !isUtilityFeatureID(featureID) {
			prompts, err := c.Prompts(ctx)
			if err != nil {
				return snapshot, err
			}
			snapshot.Prompts = &prompts
			preview, err := c.LivePreview(ctx, featureID)
			if err != nil {
				return snapshot, err
			}
			snapshot.LivePreview = &preview
			if resource.ID == "" && preview.Session != nil && preview.Session.ID != "" {
				terminal, err := c.hydrateSessionRefreshSnapshot(ctx, &snapshot, preview.Session.ID, &featureID)
				if err != nil {
					return snapshot, err
				}
				refreshFeatureDetail = terminal
			}
			if refreshFeatureDetail {
				feature, err := c.FeatureDetail(ctx, featureID)
				if err != nil {
					return snapshot, err
				}
				snapshot.Feature = &feature
			}
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
	case resource.Type == "runtime" || evt.Kind == "shutdown.updated":
		health, err := c.Health(ctx)
		return RefreshSnapshot{Health: &health}, err
	default:
		features, err := c.Features(ctx)
		return RefreshSnapshot{Features: &features}, err
	}
}

// fetchFullSnapshot re-pulls the read-model state the dashboard depends on
// for its feature list and attention badges. Used when snapshot_required is
// set, e.g. on (re)connect, so a TUI catches state that changed while it was
// not subscribed — most notably a session resumed into WaitingHelp whose
// pending question is otherwise never announced via an event.
func (c *Client) fetchFullSnapshot(ctx context.Context) (RefreshSnapshot, error) {
	health, err := c.Health(ctx)
	if err != nil {
		return RefreshSnapshot{}, err
	}
	features, err := c.Features(ctx)
	if err != nil {
		return RefreshSnapshot{Health: &health}, err
	}
	prompts, err := c.Prompts(ctx)
	if err != nil {
		return RefreshSnapshot{Health: &health, Features: &features}, err
	}
	permissions, err := c.Permissions(ctx)
	if err != nil {
		return RefreshSnapshot{Health: &health, Features: &features, Prompts: &prompts}, err
	}
	sessions, err := c.Sessions(ctx)
	if err != nil {
		return RefreshSnapshot{Health: &health, Features: &features, Prompts: &prompts, Permissions: &permissions}, err
	}
	return RefreshSnapshot{
		Health:      &health,
		Features:    &features,
		Prompts:     &prompts,
		Permissions: &permissions,
		Sessions:    &sessions,
	}, nil
}

func (c *Client) hydrateSessionRefreshSnapshot(ctx context.Context, snapshot *RefreshSnapshot, sessionID string, featureID *string) (bool, error) {
	session, err := c.SessionDetail(ctx, sessionID)
	if err != nil {
		return false, err
	}
	snapshot.Session = &session
	if featureID != nil && *featureID == "" {
		*featureID = session.Session.FeatureID
	}
	end := session.Session.TranscriptCursor.End
	if end == 0 {
		end = session.Session.TranscriptCursor.Total
	}
	if end > 0 {
		start := max(0, end-refreshTranscriptLimit)
		transcript, err := c.Transcript(ctx, sessionID, CursorQuery{Cursor: start, Limit: refreshTranscriptLimit})
		if err != nil {
			return false, err
		}
		snapshot.Transcript = &transcript
	}
	return isTerminalSessionStatus(session.Session.Status), nil
}

func isTerminalSessionStatus(status string) bool {
	switch strings.ToLower(strings.ReplaceAll(strings.TrimSpace(status), "-", "_")) {
	case "done", "completed", "success", "failed", "error":
		return true
	default:
		return false
	}
}
