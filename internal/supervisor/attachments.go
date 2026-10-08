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

package supervisor

import (
	"errors"
	"strconv"
	"strings"
)

// Attachment kinds carried by a user record.
const (
	AttachmentKindImage = "image"
	AttachmentKindFile  = "file"
)

// attachmentsDirName is the per-conversation directory holding the copies
// of every attached file; user records reference only these copies.
const attachmentsDirName = "attachments"

// ErrEmptyMessage refuses a message with neither text nor attachments.
var ErrEmptyMessage = errors.New("supervisor message has no text and no attachments")

// Attachment is one file attached to a user message. Path names the copy
// under the conversation's attachments directory; Name is the original
// file name, for display and for the harness-facing file label.
type Attachment struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// Message is one user message submitted to the coordinator.
//
// Attachments describes the attached files in harness order (images, then
// files) before they are copied; its kind, name and size sequence is part
// of the message identity for client-message dedup. Stage, when set, is
// called once the send has passed every cheap refusal: it copies the files
// into dir (the conversation's attachments directory) and returns the
// attachments with their copy paths plus a finish callback. The
// coordinator calls finish(true) exactly when the user record is committed
// and finish(false) on every refusal or failure before that, so the caller
// can keep staged sources valid for a retry.
type Message struct {
	Text            string
	HiddenContext   string
	ClientMessageID string
	Attachments     []Attachment
	Stage           func(dir string) ([]Attachment, func(commit bool), error)
}

// RenderUserMessage produces the harness-facing text of a user record: the
// visible text, then the attachment block in the shape the feature prompts
// use (`Attached Images:` with `- [Image #N]: <path>` lines, then
// `Attached Files:` with `- [<name>]: <path>` lines). Live delivery and
// every native-history converter render user records through it, so the
// harness sees the same text on delivery and after any rebuild.
func RenderUserMessage(data UserData) string {
	var images, files []Attachment
	for _, a := range data.Attachments {
		if a.Kind == AttachmentKindImage {
			images = append(images, a)
		} else {
			files = append(files, a)
		}
	}
	var sections []string
	if strings.TrimSpace(data.Text) != "" {
		sections = append(sections, data.Text)
	}
	if len(images) > 0 {
		var b strings.Builder
		b.WriteString("Attached Images:")
		for i, a := range images {
			b.WriteString("\n- [Image #" + strconv.Itoa(i+1) + "]: " + a.Path)
		}
		sections = append(sections, b.String())
	}
	if len(files) > 0 {
		var b strings.Builder
		b.WriteString("Attached Files:")
		for _, a := range files {
			b.WriteString("\n- [" + a.Name + "]: " + a.Path)
		}
		sections = append(sections, b.String())
	}
	return strings.Join(sections, "\n\n")
}

// sameAttachments compares two attachment lists by identity (kind, name
// and size, in order); copy paths differ between a send and its resend.
func sameAttachments(a, b []Attachment) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Kind != b[i].Kind || a[i].Name != b[i].Name || a[i].Size != b[i].Size {
			return false
		}
	}
	return true
}
