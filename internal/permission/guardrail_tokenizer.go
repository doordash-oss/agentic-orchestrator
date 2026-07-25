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

package permission

import (
	"errors"
	"fmt"
	"strings"
)

// errGuardrailUnsupported is returned by the tokenizer when it encounters an
// unsupported shell construct. The classifier converts any parse error to a
// deterministic deferral.
var errGuardrailUnsupported = errors.New("guardrail: unsupported shell construct")

// errGuardrailUnclosedQuote is returned when a quoted string is not closed.
var errGuardrailUnclosedQuote = errors.New("guardrail: unclosed quote")

// errGuardrailMalformed is returned when the command structure is malformed
// (e.g., a connector without a preceding command, a redirect without a target).
var errGuardrailMalformed = errors.New("guardrail: malformed command")

// tokenKind identifies the kind of token produced by the tokenizer.
type tokenKind int

const (
	tokWord     tokenKind = iota // literal word (possibly from quotes)
	tokAnd                       // &&
	tokOr                        // ||
	tokSemi                      // ;
	tokNewline                   // \n
	tokPipe                      // |
	tokRedirect                  // >, >>, <, 2>, 2>&1, etc.
)

// token is a single token produced by the tokenizer.
type token struct {
	kind   tokenKind
	text   string // word text or operator text
	quoted bool   // true if the word came from single or double quotes
}

// tokIsConnector reports whether k is a chain connector or pipeline.
func tokIsConnector(k tokenKind) bool {
	return k == tokAnd || k == tokOr || k == tokSemi || k == tokNewline || k == tokPipe
}

// tokenize scans a command string into tokens. It recognizes literal words,
// single and double quoting, foreground chain operators (&&, ||, ;, \n),
// pipelines (|), and redirection operators. Every unsupported shell construct
// (command/process substitution, nested shells, background, heredocs,
// expansion, globbing, etc.) yields an error so the classifier can defer.
func tokenize(command string) ([]token, error) {
	var tokens []token
	i := 0
	n := len(command)

	for i < n {
		c := command[i]

		if c == ' ' || c == '\t' {
			i++
			continue
		}

		if c == '\n' {
			tokens = append(tokens, token{kind: tokNewline})
			i++
			continue
		}

		if c == '\r' {
			return nil, errGuardrailUnsupported
		}

		if c == '\'' {
			j := i + 1
			for j < n && command[j] != '\'' {
				j++
			}
			if j >= n {
				return nil, errGuardrailUnclosedQuote
			}
			if j+1 < n && !isWordBoundary(command[j+1]) {
				return nil, errGuardrailUnsupported
			}
			tokens = append(tokens, token{kind: tokWord, text: command[i+1 : j], quoted: true})
			i = j + 1
			continue
		}

		if c == '"' {
			j := i + 1
			for j < n && command[j] != '"' {
				if command[j] == '$' || command[j] == '`' || command[j] == '\\' {
					return nil, errGuardrailUnsupported
				}
				j++
			}
			if j >= n {
				return nil, errGuardrailUnclosedQuote
			}
			if j+1 < n && !isWordBoundary(command[j+1]) {
				return nil, errGuardrailUnsupported
			}
			tokens = append(tokens, token{kind: tokWord, text: command[i+1 : j], quoted: true})
			i = j + 1
			continue
		}

		switch c {
		case '&':
			if i+1 < n && command[i+1] == '&' {
				tokens = append(tokens, token{kind: tokAnd})
				i += 2
				continue
			}
			return nil, errGuardrailUnsupported
		case '|':
			if i+1 < n && command[i+1] == '|' {
				tokens = append(tokens, token{kind: tokOr})
				i += 2
				continue
			}
			tokens = append(tokens, token{kind: tokPipe})
			i++
			continue
		case ';':
			tokens = append(tokens, token{kind: tokSemi})
			i++
			continue
		}

		if c == '>' || c == '<' {
			op, consumed, err := readRedirectOp(command, i, n, 0)
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, token{kind: tokRedirect, text: op})
			i += consumed
			continue
		}

		if c >= '0' && c <= '9' {
			if i+1 < n && (command[i+1] == '>' || command[i+1] == '<') {
				desc := int(c - '0')
				op, consumed, err := readRedirectOp(command, i+1, n, desc)
				if err != nil {
					return nil, err
				}
				tokens = append(tokens, token{kind: tokRedirect, text: op})
				i += 1 + consumed
				continue
			}
		}

		if isUnsupportedUnquoted(c) {
			return nil, errGuardrailUnsupported
		}

		start := i
		for i < n {
			c = command[i]
			if isWordTerminator(c) {
				break
			}
			if isUnsupportedUnquoted(c) {
				return nil, errGuardrailUnsupported
			}
			i++
		}
		if i > start {
			if i < n && (command[i] == '\'' || command[i] == '"') {
				return nil, errGuardrailUnsupported
			}
			tokens = append(tokens, token{kind: tokWord, text: command[start:i]})
		}
	}

	return tokens, nil
}

// readRedirectOp reads a redirection operator starting at position i (pointing
// at > or <). descriptor is the optional leading fd number (0 for none).
// Returns the operator text, bytes consumed (starting from the > or <), and
// any error. Heredocs (<<, <<<) and here-strings are unsupported.
func readRedirectOp(command string, i, n, descriptor int) (string, int, error) {
	if i >= n {
		return "", 0, errGuardrailUnsupported
	}
	c := command[i]
	if c != '>' && c != '<' {
		return "", 0, errGuardrailUnsupported
	}

	consumed := 1
	var op strings.Builder
	if descriptor > 0 {
		fmt.Fprintf(&op, "%d", descriptor)
	}

	if c == '>' && i+1 < n && command[i+1] == '>' {
		op.WriteString(">>")
		consumed = 2
	} else if c == '<' && i+1 < n && command[i+1] == '<' {
		return "", 0, errGuardrailUnsupported
	} else {
		op.WriteByte(c)
	}

	if c == '>' && i+consumed < n && command[i+consumed] == '&' {
		j := i + consumed + 1
		if j < n && command[j] >= '0' && command[j] <= '9' {
			op.WriteString("&")
			op.WriteByte(command[j])
			consumed += 2
		} else {
			return "", 0, errGuardrailUnsupported
		}
	}

	return op.String(), consumed, nil
}

func isWordTerminator(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' ||
		c == '&' || c == '|' || c == ';' || c == '>' || c == '<' ||
		c == '\'' || c == '"'
}

// isWordBoundary reports whether c is a structural shell separator (space,
// tab, newline, connector, or redirect). Unlike isWordTerminator, quotes
// are NOT boundaries: an adjacent quote means Bash would concatenate the
// fragments into one argv element, which the tokenizer fails closed on
// rather than reconstructing.
func isWordBoundary(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' ||
		c == '&' || c == '|' || c == ';' || c == '>' || c == '<'
}

func isUnsupportedUnquoted(c byte) bool {
	if c < 0x20 {
		return true
	}
	return c == '$' || c == '`' || c == '(' || c == ')' ||
		c == '{' || c == '}' || c == '*' || c == '?' ||
		c == '[' || c == ']' || c == '~' || c == '\\'
}

// assignment is a prefix KEY=value before the command name.
type assignment struct {
	key   string
	value string
}

// redirectSpec is a parsed redirection.
type redirectSpec struct {
	op        string // ">", ">>", "<", "2>", "2>&1", etc.
	isDevNull bool   // true if target is /dev/null
	isFdRedir bool   // true if fd redirect (e.g., 2>&1, 1>&2)
}

// parsedSegment is one simple command in a compound command.
type parsedSegment struct {
	assignments []assignment
	name        string
	nameQuoted  bool
	args        []string
	redirects   []redirectSpec
}

// parsedCommand is the fully parsed compound command.
type parsedCommand struct {
	segments   []parsedSegment
	connectors []tokenKind
}

// parseCommand tokenizes and parses a command string into a structured form.
// Returns an error for any unsupported construct or malformed structure.
func parseCommand(command string) (*parsedCommand, error) {
	tokens, err := tokenize(command)
	if err != nil {
		return nil, err
	}
	return parseTokens(tokens)
}

// parseTokens interprets a token list into segments separated by connectors.
func parseTokens(tokens []token) (*parsedCommand, error) {
	var parsed parsedCommand
	var seg *parsedSegment
	expectTarget := false

	startSegment := func() {
		parsed.segments = append(parsed.segments, parsedSegment{})
		seg = &parsed.segments[len(parsed.segments)-1]
	}

	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]

		if expectTarget {
			if tok.kind != tokWord {
				return nil, errGuardrailMalformed
			}
			r := seg.redirects[len(seg.redirects)-1]
			r.isDevNull = tok.text == "/dev/null"
			seg.redirects[len(seg.redirects)-1] = r
			expectTarget = false
			continue
		}

		switch tok.kind {
		case tokWord:
			if seg == nil {
				startSegment()
			}
			if seg.name == "" && !tok.quoted && isAssignmentWord(tok.text) {
				key, value := splitAssignment(tok.text)
				seg.assignments = append(seg.assignments, assignment{key: key, value: value})
			} else if seg.name == "" {
				seg.name = tok.text
				seg.nameQuoted = tok.quoted
			} else {
				seg.args = append(seg.args, tok.text)
			}

		case tokRedirect:
			if seg == nil {
				return nil, errGuardrailMalformed
			}
			if strings.Contains(tok.text, "&") {
				seg.redirects = append(seg.redirects, redirectSpec{op: tok.text, isFdRedir: true})
			} else {
				seg.redirects = append(seg.redirects, redirectSpec{op: tok.text})
				expectTarget = true
			}

		case tokPipe:
			if seg == nil || seg.name == "" {
				return nil, errGuardrailMalformed
			}
			parsed.connectors = append(parsed.connectors, tok.kind)
			seg = nil

		case tokAnd, tokOr, tokSemi, tokNewline:
			if seg == nil || seg.name == "" {
				return nil, errGuardrailMalformed
			}
			parsed.connectors = append(parsed.connectors, tok.kind)
			seg = nil
		}
	}

	if expectTarget {
		return nil, errGuardrailMalformed
	}
	if seg != nil && seg.name == "" {
		return nil, errGuardrailMalformed
	}
	if len(tokens) > 0 && tokIsConnector(tokens[len(tokens)-1].kind) {
		return nil, errGuardrailMalformed
	}
	expectedConnectors := 0
	if len(parsed.segments) > 0 {
		expectedConnectors = len(parsed.segments) - 1
	}
	if len(parsed.connectors) != expectedConnectors {
		return nil, errGuardrailMalformed
	}

	return &parsed, nil
}

// isAssignmentWord reports whether s matches KEY=value where KEY is a valid
// environment variable name ([a-zA-Z_][a-zA-Z0-9_]*).
func isAssignmentWord(s string) bool {
	idx := strings.Index(s, "=")
	if idx <= 0 {
		return false
	}
	key := s[:idx]
	for i := 0; i < len(key); i++ {
		c := key[i]
		if i == 0 {
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_') {
				return false
			}
		} else {
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_') {
				return false
			}
		}
	}
	return true
}

// splitAssignment splits KEY=value into key and value.
func splitAssignment(s string) (key, value string) {
	idx := strings.Index(s, "=")
	return s[:idx], s[idx+1:]
}
