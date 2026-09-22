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

package slack

import (
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

const (
	responderPollInterval = 10 * time.Second
	responderPageSize     = 100
	responderPageBudget   = 3
)

type responderThread struct {
	channelID string
	rootTS    string
	oldest    string
}

type responderPollState struct {
	oldest string
	cursor string
}

func parsePermissionReply(text string) (ports.SlackPermissionDecision, bool) {
	switch normalizeResponderReply(text) {
	case "allow", "approve", "yes":
		return ports.SlackPermissionAllowOnce, true
	case "deny", "no":
		return ports.SlackPermissionDeny, true
	default:
		return "", false
	}
}

func parseReviewReply(text string) bool {
	normalized := normalizeResponderReply(text)
	return normalized == "approve" || normalized == "✅"
}

func normalizeResponderReply(text string) string {
	text = strings.TrimSpace(strings.ToLower(text))
	return strings.TrimRightFunc(text, unicode.IsPunct)
}

func (n *Notifier) runResponder() {
	defer n.responderWG.Done()
	for n.responderClock.Sleep(n.responderBase, responderPollInterval) {
		n.responderTick()
	}
}

func (n *Notifier) responderTick() {
	settings := n.settings.SlackSettings()
	if !settings.Enabled || settings.Token == "" || !settings.Categories.NeedsInput {
		return
	}

	threads := n.responderThreads()
	if len(threads) == 0 {
		return
	}
	client, err := n.newClient(settings.Token)
	if err != nil {
		return
	}
	active := make(map[string]bool, len(threads))
	for _, thread := range threads {
		key := thread.channelID + "\x00" + thread.rootTS
		active[key] = true
		state := n.responderPolls[key]
		if state.oldest != thread.oldest {
			state = responderPollState{oldest: thread.oldest}
		}
		for pageNumber := 0; pageNumber < responderPageBudget; pageNumber++ {
			page, err := client.ThreadReplies(
				n.responderBase,
				thread.channelID,
				thread.rootTS,
				thread.oldest,
				responderPageSize,
				state.cursor,
			)
			if err != nil {
				break
			}
			state.cursor = page.NextCursor
			if state.cursor == "" {
				break
			}
		}
		n.responderPolls[key] = state
	}
	for key := range n.responderPolls {
		if !active[key] {
			delete(n.responderPolls, key)
		}
	}
}

func (n *Notifier) responderThreads() []responderThread {
	n.recordMu.Lock()
	defer n.recordMu.Unlock()

	var threads []responderThread
	for _, record := range n.records {
		for key, destination := range record.Destinations {
			if destination.ChannelID == "" || destination.RootTS == "" {
				continue
			}
			oldest := ""
			for _, pending := range record.Pending {
				messageTS := pending.MessageTS[key]
				if messageTS != "" &&
					(oldest == "" || compareSlackTimestamps(messageTS, oldest) < 0) {
					oldest = messageTS
				}
			}
			if oldest != "" {
				threads = append(threads, responderThread{
					channelID: destination.ChannelID,
					rootTS:    destination.RootTS,
					oldest:    oldest,
				})
			}
		}
	}
	sort.Slice(threads, func(i, j int) bool {
		if threads[i].channelID != threads[j].channelID {
			return threads[i].channelID < threads[j].channelID
		}
		return threads[i].rootTS < threads[j].rootTS
	})
	return threads
}

func compareSlackTimestamps(left, right string) int {
	leftWhole, leftFraction, leftOK := splitSlackTimestamp(left)
	rightWhole, rightFraction, rightOK := splitSlackTimestamp(right)
	if !leftOK || !rightOK {
		return strings.Compare(left, right)
	}
	if len(leftWhole) != len(rightWhole) {
		if len(leftWhole) < len(rightWhole) {
			return -1
		}
		return 1
	}
	if compared := strings.Compare(leftWhole, rightWhole); compared != 0 {
		return compared
	}
	width := max(len(leftFraction), len(rightFraction))
	leftFraction += strings.Repeat("0", width-len(leftFraction))
	rightFraction += strings.Repeat("0", width-len(rightFraction))
	return strings.Compare(leftFraction, rightFraction)
}

func splitSlackTimestamp(value string) (string, string, bool) {
	whole, fraction, found := strings.Cut(value, ".")
	if !found || whole == "" || fraction == "" {
		return "", "", false
	}
	for _, part := range []string{whole, fraction} {
		if strings.IndexFunc(part, func(r rune) bool {
			return r < '0' || r > '9'
		}) >= 0 {
			return "", "", false
		}
	}
	whole = strings.TrimLeft(whole, "0")
	if whole == "" {
		whole = "0"
	}
	return whole, fraction, true
}
