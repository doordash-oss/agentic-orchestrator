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
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestRenderUserMessage(t *testing.T) {
	t.Parallel()
	img := Attachment{Path: "/s/conversations/c/attachments/a1.png", Kind: AttachmentKindImage, Name: "login.png", Size: 3}
	img2 := Attachment{Path: "/s/conversations/c/attachments/a2.jpg", Kind: AttachmentKindImage, Name: "mock.jpg", Size: 4}
	file := Attachment{Path: "/s/conversations/c/attachments/a3.pdf", Kind: AttachmentKindFile, Name: "spec.pdf", Size: 5}
	for _, tc := range []struct {
		name string
		data UserData
		want string
	}{
		{"text only", UserData{Text: "hello"}, "hello"},
		{"text and both kinds", UserData{Text: "look", Attachments: []Attachment{img, img2, file}},
			"look\n\nAttached Images:\n- [Image #1]: /s/conversations/c/attachments/a1.png\n- [Image #2]: /s/conversations/c/attachments/a2.jpg\n\nAttached Files:\n- [spec.pdf]: /s/conversations/c/attachments/a3.pdf"},
		{"blank text, file only", UserData{Text: "  ", Attachments: []Attachment{file}},
			"Attached Files:\n- [spec.pdf]: /s/conversations/c/attachments/a3.pdf"},
		{"images only", UserData{Text: "x", Attachments: []Attachment{img}},
			"x\n\nAttached Images:\n- [Image #1]: /s/conversations/c/attachments/a1.png"},
	} {
		if got := RenderUserMessage(tc.data); got != tc.want {
			t.Errorf("%s: RenderUserMessage =\n%q\nwant\n%q", tc.name, got, tc.want)
		}
	}
}

// stagingProbe is a Message.Stage double: it writes one file per
// descriptor into the conversation's attachments directory and records
// whether the coordinator committed or rolled the staging back.
type stagingProbe struct {
	mu        sync.Mutex
	dirs      []string
	paths     []string
	commits   int
	rollbacks int
}

func (p *stagingProbe) stage(descriptors []Attachment) func(string) ([]Attachment, func(bool), error) {
	return func(dir string) ([]Attachment, func(bool), error) {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.dirs = append(p.dirs, dir)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, nil, err
		}
		out := make([]Attachment, 0, len(descriptors))
		var written []string
		for i, d := range descriptors {
			path := filepath.Join(dir, "copy-"+string(rune('a'+i))+filepath.Ext(d.Name))
			if err := os.WriteFile(path, make([]byte, d.Size), 0o600); err != nil {
				return nil, nil, err
			}
			written = append(written, path)
			d.Path = path
			out = append(out, d)
		}
		p.paths = append(p.paths, written...)
		return out, func(commit bool) {
			p.mu.Lock()
			defer p.mu.Unlock()
			if commit {
				p.commits++
				return
			}
			p.rollbacks++
			for _, path := range written {
				_ = os.Remove(path)
			}
		}, nil
	}
}

func (p *stagingProbe) counts() (staged, commits, rollbacks int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.dirs), p.commits, p.rollbacks
}

func testDescriptors() []Attachment {
	return []Attachment{
		{Kind: AttachmentKindImage, Name: "login.png", Size: 3},
		{Kind: AttachmentKindFile, Name: "spec.pdf", Size: 5},
	}
}

func attachMessage(text, cmid string, probe *stagingProbe) Message {
	return Message{Text: text, ClientMessageID: cmid, Attachments: testDescriptors(), Stage: probe.stage(testDescriptors())}
}

func TestCoordinator_AttachmentsAreCopiedCommittedAndRendered(t *testing.T) {
	launcher := &fakeLauncher{}
	stateDir := t.TempDir()
	c := newTestCoordinator(t, stateDir, launcher)
	chooseSettings(t, c)
	probe := &stagingProbe{}
	res, err := c.SendMessage(context.Background(), attachMessage("look at these", "cm-1", probe))
	if err != nil {
		t.Fatal(err)
	}
	st := c.State()
	wantDir := filepath.Join(c.dir, conversationsDir, st.ConversationID, attachmentsDirName)
	if staged, commits, rollbacks := probe.counts(); staged != 1 || commits != 1 || rollbacks != 0 || probe.dirs[0] != wantDir {
		t.Fatalf("staging = %d/%d/%d into %v, want one commit into %s", staged, commits, rollbacks, probe.dirs, wantDir)
	}
	var data UserData
	if err := json.Unmarshal(res.Record.Data, &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Attachments) != 2 || data.Attachments[0].Path != probe.paths[0] || data.Attachments[1].Kind != AttachmentKindFile || data.Attachments[1].Name != "spec.pdf" || data.Attachments[1].Size != 5 {
		t.Fatalf("record attachments = %+v", data.Attachments)
	}
	sent := launcher.session(0).Sent()
	want := "look at these\n\nAttached Images:\n- [Image #1]: " + probe.paths[0] + "\n\nAttached Files:\n- [spec.pdf]: " + probe.paths[1]
	if len(sent) != 1 || sent[0] != want {
		t.Fatalf("delivered %q, want %q", sent, want)
	}

	// The idle path renders the same block, and hidden context still
	// travels separately ahead of the rendered text.
	launcher.session(0).emit(successResult())
	waitLifecycle(t, c, LifecycleIdle)
	probe2 := &stagingProbe{}
	msg := attachMessage("", "cm-2", probe2)
	msg.HiddenContext = "BUNDLE"
	if _, err := c.SendMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	sess := launcher.session(0)
	if got := sess.Sent()[1]; got != "Attached Images:\n- [Image #1]: "+probe2.paths[0]+"\n\nAttached Files:\n- [spec.pdf]: "+probe2.paths[1] {
		t.Fatalf("idle delivery = %q", got)
	}
	if got := sess.Hidden()[1]; got != "BUNDLE" {
		t.Fatalf("hidden = %q", got)
	}
	if _, commits, _ := probe2.counts(); commits != 1 {
		t.Fatalf("idle send commits = %d", commits)
	}
}

func TestCoordinator_AttachmentDedupIncludesTheAttachmentList(t *testing.T) {
	launcher := &fakeLauncher{}
	c := newTestCoordinator(t, t.TempDir(), launcher)
	chooseSettings(t, c)
	if _, err := c.SendMessage(context.Background(), attachMessage("hi", "cm-1", &stagingProbe{})); err != nil {
		t.Fatal(err)
	}
	retry := &stagingProbe{}
	res, err := c.SendMessage(context.Background(), attachMessage("hi", "cm-1", retry))
	if err != nil || !res.Deduplicated {
		t.Fatalf("identical resend = %+v, %v; want deduplicated", res, err)
	}
	if staged, commits, rollbacks := retry.counts(); commits != 0 || staged != rollbacks {
		t.Fatalf("dedup staging = %d/%d/%d, want nothing kept", staged, commits, rollbacks)
	}
	changed := Message{Text: "hi", ClientMessageID: "cm-1", Attachments: testDescriptors()[:1]}
	var conflict *ClientMessageConflictError
	if _, err := c.SendMessage(context.Background(), changed); !errors.As(err, &conflict) {
		t.Fatalf("resend with other attachments = %v, want conflict", err)
	}
	if _, err := c.Send(context.Background(), "hi", "", "cm-1"); !errors.As(err, &conflict) {
		t.Fatalf("resend without attachments = %v, want conflict", err)
	}
	if got := len(launcher.session(0).Sent()); got != 1 {
		t.Fatalf("deliveries = %d, want 1", got)
	}
}

func TestCoordinator_RefusedAndFailedSendsRollAttachmentsBack(t *testing.T) {
	assertRolledBack := func(t *testing.T, probe *stagingProbe) {
		t.Helper()
		staged, commits, rollbacks := probe.counts()
		if commits != 0 || staged != rollbacks {
			t.Fatalf("staging = %d/%d/%d, want every staging rolled back", staged, commits, rollbacks)
		}
		for _, path := range probe.paths {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("copy %s survived: %v", path, err)
			}
		}
	}
	t.Run("settings_required", func(t *testing.T) {
		c := newTestCoordinator(t, t.TempDir(), &fakeLauncher{})
		probe := &stagingProbe{}
		if _, err := c.SendMessage(context.Background(), attachMessage("hi", "cm-1", probe)); !errors.Is(err, ErrSettingsRequired) {
			t.Fatalf("err = %v", err)
		}
		assertRolledBack(t, probe)
	})
	t.Run("turn_active", func(t *testing.T) {
		launcher := &fakeLauncher{}
		c := newTestCoordinator(t, t.TempDir(), launcher)
		chooseSettings(t, c)
		if _, err := c.Send(context.Background(), "first", "", "cm-1"); err != nil {
			t.Fatal(err)
		}
		probe := &stagingProbe{}
		if _, err := c.SendMessage(context.Background(), attachMessage("hi", "cm-2", probe)); !errors.Is(err, ErrTurnActive) {
			t.Fatalf("err = %v", err)
		}
		assertRolledBack(t, probe)
	})
	t.Run("launch failure", func(t *testing.T) {
		c := newTestCoordinator(t, t.TempDir(), &fakeLauncher{failNext: 1})
		chooseSettings(t, c)
		probe := &stagingProbe{}
		var launch *LaunchFailedError
		if _, err := c.SendMessage(context.Background(), attachMessage("hi", "cm-1", probe)); !errors.As(err, &launch) {
			t.Fatalf("err = %v", err)
		}
		assertRolledBack(t, probe)
		if staged, _, _ := probe.counts(); staged != 1 {
			t.Fatalf("staged = %d, want the launch path to stage once", staged)
		}
	})
	t.Run("handshake timeout", func(t *testing.T) {
		c := newTestCoordinator(t, t.TempDir(), &fakeLauncher{silent: true}, func(o *Options) { o.HandshakeTimeout = 50 * time.Millisecond })
		chooseSettings(t, c)
		probe := &stagingProbe{}
		var launch *LaunchFailedError
		if _, err := c.SendMessage(context.Background(), attachMessage("hi", "cm-1", probe)); !errors.As(err, &launch) {
			t.Fatalf("err = %v", err)
		}
		assertRolledBack(t, probe)
	})
	t.Run("staging error", func(t *testing.T) {
		launcher := &fakeLauncher{}
		c := newTestCoordinator(t, t.TempDir(), launcher)
		chooseSettings(t, c)
		boom := errors.New("copy failed")
		msg := Message{Text: "hi", ClientMessageID: "cm-1", Attachments: testDescriptors(), Stage: func(string) ([]Attachment, func(bool), error) { return nil, nil, boom }}
		if _, err := c.SendMessage(context.Background(), msg); !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
		if launcher.launchCount() != 0 || c.State().HeadSeq != 0 {
			t.Fatal("a failed staging launched or committed")
		}
	})
}

func TestCoordinator_BlankTextWithoutAttachmentsIsRefused(t *testing.T) {
	c := newTestCoordinator(t, t.TempDir(), &fakeLauncher{})
	chooseSettings(t, c)
	if _, err := c.SendMessage(context.Background(), Message{Text: "  ", ClientMessageID: "cm-1"}); !errors.Is(err, ErrEmptyMessage) {
		t.Fatalf("err = %v, want ErrEmptyMessage", err)
	}
}
