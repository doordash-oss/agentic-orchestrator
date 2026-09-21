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
	"strconv"
	"strings"
)

// Block is one Block Kit value. Concrete shapes live below.
type Block interface {
	blockType() string
}

const (
	blockTypeHeader  = "header"
	blockTypeSection = "section"
	blockTypeContext = "context"

	textTypePlain  = "plain_text"
	textTypeMrkdwn = "mrkdwn"
)

// Slack's documented block limits. Truncation is the last line of defence
// after bounding and escaping, so a rendered block can never exceed them.
const (
	headerTextLimit   = 150
	fieldTextLimit    = 2000
	contextTextLimit  = 2000
	fallbackTextLimit = 300
	repoFieldLimit    = 400
)

type textObject struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type headerBlock struct {
	Type string     `json:"type"`
	Text textObject `json:"text"`
}

func (headerBlock) blockType() string { return blockTypeHeader }

type sectionBlock struct {
	Type   string       `json:"type"`
	Fields []textObject `json:"fields,omitempty"`
}

func (sectionBlock) blockType() string { return blockTypeSection }

type contextBlock struct {
	Type     string       `json:"type"`
	Elements []textObject `json:"elements"`
}

func (contextBlock) blockType() string { return blockTypeContext }

func headerBlockFor(name string) headerBlock {
	return headerBlock{
		Type: blockTypeHeader,
		Text: textObject{Type: textTypePlain, Text: name},
	}
}

func sectionBlockFor(fields []textObject) sectionBlock {
	return sectionBlock{Type: blockTypeSection, Fields: fields}
}

func contextBlockFor(elements []textObject) contextBlock {
	return contextBlock{Type: blockTypeContext, Elements: elements}
}

// safeText renders record-derived text for a mrkdwn surface: the safe
// display bounding first, then mrkdwn escaping so a feature named like a
// channel mention can never ping anyone, then a hard truncation at the
// block limit.
func safeText(text string, limit int) string {
	text = boundDisplayText(text, limit)
	text = escapeMrkdwn(text)
	if len(text) > limit {
		text = text[:limit]
	}
	return text
}

// safePlain renders record-derived text for a plain_text surface, bounding
// only: plain text carries no markup to escape.
func safePlain(text string, limit int) string {
	return boundDisplayText(text, limit)
}

func boundDisplayText(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit > 0 && len(text) > limit {
		return text[:limit] + "..."
	}
	return text
}

// escapeMrkdwn neutralizes the characters Slack interprets as entity or
// link markup, so user-controlled strings can never ping anyone.
func escapeMrkdwn(text string) string {
	text = strings.ReplaceAll(text, "&", "&amp;")
	text = strings.ReplaceAll(text, "<", "&lt;")
	text = strings.ReplaceAll(text, ">", "&gt;")
	return text
}

// slackDateToken renders a timestamp as a Slack date token so every reader
// sees it in their own time zone, with a bounded plain fallback for clients
// that cannot render the token.
func slackDateToken(unixSeconds int64, fallback string) string {
	token := "<!date^" + strconv.FormatInt(unixSeconds, 10) +
		"^{date_short_pretty} at {time}|Updated " + escapeMrkdwn(safePlain(fallback, 100)) + ">"
	if len(token) > contextTextLimit {
		token = token[:contextTextLimit] + ">"
	}
	return token
}
