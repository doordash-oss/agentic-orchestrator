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

import "testing"

func TestSlackAnswerGrammarAuthorFilter(t *testing.T) {
	const (
		featureID = "feature-1"
		key       = "channel:C-ENG"
		rootTS    = "100.000001"
		postedTS  = "100.000002"
	)
	thread := responderThread{
		featureID: featureID, destinationKey: key,
		channelID: "C-ENG", rootTS: rootTS, oldest: postedTS,
	}
	tests := []struct {
		name    string
		message Message
		want    bool
	}{
		{name: "bot identity", message: Message{User: "U-BOT", BotID: "B-DEPLOY", Text: "answer"}},
		{name: "app identity", message: Message{User: "U-APP", AppID: "A-DEPLOY", Text: "answer"}},
		{name: "no person identity", message: Message{Text: "answer"}},
		{name: "channel join", message: Message{User: "U-ADA", Subtype: "channel_join", Text: "answer"}},
		{name: "channel leave", message: Message{User: "U-ADA", Subtype: "channel_leave", Text: "answer"}},
		{name: "edited event", message: Message{User: "U-ADA", Subtype: "message_changed", Text: "answer"}},
		{name: "deleted tombstone", message: Message{User: "U-ADA", Subtype: "message_deleted", Text: "answer"}},
		{name: "person broadcast", message: Message{User: "U-ADA", Subtype: "thread_broadcast", Text: "answer"}, want: true},
		{name: "person attachment without fallback text", message: Message{User: "U-ADA"}, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			destination := destinationRecord{
				RootTS: rootTS,
				Ledger: []string{rootTS, postedTS},
				PostingIndex: []postingIndexEntry{{
					Identity: "question:1", MessageTS: postedTS, Tag: "#1",
				}},
			}
			notifier := &Notifier{records: map[string]*featureRecord{
				featureID: {Destinations: map[string]destinationRecord{key: destination}},
			}}
			message := tc.message
			message.TS = "100.000003"
			message.ThreadTS = rootTS
			for poll := 0; poll < 2; poll++ {
				replies, reactions := notifier.extractResponderCandidates(thread, []Message{message})
				wantReplies := 0
				if tc.want {
					wantReplies = 1
				}
				if len(replies) != wantReplies || len(reactions) != 0 {
					t.Errorf("extractResponderCandidates(%s, poll %d) = replies %#v, reactions %#v; want reply=%t and no reactions",
						tc.name, poll, replies, reactions, tc.want)
				}
				if tc.want && len(replies) == 1 &&
					(replies[0].Target.Identity != "question:1" || replies[0].Message.User != "U-ADA") {
					t.Errorf("extractResponderCandidates(%s) = %#v; want question target and person author", tc.name, replies)
				}
			}
		})
	}
}

func TestSlackAnswerGrammarAuthorFilterDoesNotMatchOutputText(t *testing.T) {
	const (
		featureID = "feature-1"
		key       = "user:U-OWNER"
	)
	for _, text := range []string{
		"Answer in Agentico.",
		"#1 was answered by <@U-OWNER> via Slack.",
		"#1 was resolved in Agentico.",
	} {
		t.Run(text, func(t *testing.T) {
			notifier := &Notifier{records: map[string]*featureRecord{
				featureID: {Destinations: map[string]destinationRecord{
					key: {
						Ledger: []string{"100.000001", "100.000002"},
						PostingIndex: []postingIndexEntry{{
							Identity: "question:1", MessageTS: "100.000002", Tag: "#1",
						}},
					},
				}},
			}}
			replies, _ := notifier.extractResponderCandidates(
				responderThread{featureID: featureID, destinationKey: key},
				[]Message{{TS: "100.000003", User: "U-OWNER", Text: text}},
			)
			if len(replies) != 1 || replies[0].Message.Text != decodeSlackText(text) {
				t.Errorf("extractResponderCandidates(%q) = %#v; want authored text eligible", text, replies)
			}
		})
	}
}

func TestSlackAnswerGrammarAuthorFilterRetainsReplyMarks(t *testing.T) {
	const (
		featureID = "feature-1"
		key       = "user:U-OWNER"
	)
	for _, tc := range []struct {
		name        string
		destination destinationRecord
	}{
		{name: "reloaded last-seen", destination: destinationRecord{LastSeenReplyTS: "100.000003"}},
		{name: "submitted reply", destination: destinationRecord{SubmittedReplies: []string{"100.000003"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			notifier := &Notifier{records: map[string]*featureRecord{
				featureID: {Destinations: map[string]destinationRecord{key: tc.destination}},
			}}
			replies, reactions := notifier.extractResponderCandidates(
				responderThread{featureID: featureID, destinationKey: key},
				[]Message{{TS: "100.000003", User: "U-OWNER", Text: "answer"}},
			)
			if len(replies) != 0 || len(reactions) != 0 {
				t.Errorf("extractResponderCandidates(%s) = replies %#v, reactions %#v; want marked reply skipped", tc.name, replies, reactions)
			}
		})
	}
}

func TestSlackAnswerGrammarAuthorFilterUsesLedgerNotOwnerIdentity(t *testing.T) {
	const (
		featureID = "feature-1"
		key       = "user:U-OWNER"
		rootTS    = "100.000001"
		postedTS  = "100.000003"
	)
	thread := responderThread{featureID: featureID, destinationKey: key, rootTS: rootTS}
	integrationOutput := []struct {
		ts, text string
	}{
		{"100.000001", "root card"},
		{"100.000002", "Progress"},
		{"100.000003", "question"},
		{"100.000004", "1 of 2 answered"},
		{"100.000005", "#1 was answered 'Broad scope' by <@U-OWNER> via Slack."},
		{"100.000006", "hint"},
		{"100.000007", "Resolved in Agentico"},
	}
	ledger := make([]string, 0, len(integrationOutput))
	messages := make([]Message, 0, len(integrationOutput)+1)
	for _, output := range integrationOutput {
		ledger = append(ledger, output.ts)
		messages = append(messages, Message{
			TS: output.ts, ThreadTS: rootTS, User: "U-OWNER", Text: output.text,
		})
	}
	messages = append(messages, Message{
		TS: "100.000008", ThreadTS: rootTS, User: "U-OWNER", Text: "the answer",
	})
	notifier := &Notifier{records: map[string]*featureRecord{
		featureID: {Destinations: map[string]destinationRecord{
			key: {
				RootTS: rootTS, Ledger: ledger,
				PostingIndex: []postingIndexEntry{{
					Identity: "question:1", MessageTS: postedTS, Tag: "#1",
				}},
			},
		}},
	}}
	for _, botIdentity := range []string{"", "B-AGENTICO"} {
		t.Run("bot_identity_"+botIdentity, func(t *testing.T) {
			threadMessages := append([]Message(nil), messages...)
			if botIdentity != "" {
				for index := range integrationOutput {
					threadMessages[index].BotID = botIdentity
				}
			}
			replies, reactions := notifier.extractResponderCandidates(thread, threadMessages)
			if len(replies) != 1 || replies[0].Message.TS != "100.000008" || len(reactions) != 0 {
				t.Fatalf("extractResponderCandidates() = replies %#v, reactions %#v; want only owner's typed reply", replies, reactions)
			}
		})
	}
}
