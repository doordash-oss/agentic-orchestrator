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

package session

import "testing"

func TestStripANSI(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"plain text", "hello world", "hello world"},
		{"empty", "", ""},
		{"color codes", "\x1b[32mgreen\x1b[0m", "green"},
		{"bold", "\x1b[1mbold text\x1b[22m", "bold text"},
		{"cursor movement", "\x1b[2A\x1b[3Btext", "text"},
		{"erase line", "\x1b[2Ksome text", "some text"},
		{"carriage return", "old text\rnew text", "old textnew text"},
		{"OSC with BEL", "\x1b]0;title\x07content", "content"},
		{"OSC with ST", "\x1b]0;title\x1b\\content", "content"},
		{"mixed", "\x1b[1;32m> Thinking...\x1b[0m\r\x1b[2K\x1b[1;32m> Done!\x1b[0m", "> Thinking...> Done!"},
		{"Ink spinner", "\x1b[?25l\x1b[1G\x1b[2K⠋ Processing...\x1b[?25h", "⠋ Processing..."},
		{"nested escapes", "\x1b[38;5;208mwarning\x1b[0m", "warning"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripANSI(tt.input)
			if got != tt.want {
				t.Errorf("stripANSI(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
