package tui

import "testing"

func TestSessionOutputDecoderClaudeParsesAssistantLine(t *testing.T) {
	d := newSessionOutputDecoder("claude")
	line := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}`
	msgs := d.Decode(line)
	if len(msgs) != 1 || msgs[0].Assistant == nil || msgs[0].Assistant.Message.Content[0].Text != "hi" {
		t.Fatalf("Decode = %+v, want one assistant text message", msgs)
	}
}

func TestSessionOutputDecoderUnknownProviderReturnsNil(t *testing.T) {
	d := newSessionOutputDecoder("some-future-provider")
	if msgs := d.Decode(`{"type":"assistant"}`); msgs != nil {
		t.Fatalf("Decode = %+v, want nil for unknown provider", msgs)
	}
}
