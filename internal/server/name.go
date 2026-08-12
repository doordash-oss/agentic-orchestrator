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
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// serverNameFilename is the owner-only identity file holding the generated
// server name for one runtime dir. It mirrors the auth-token pattern:
// generated once on absence, 0600, read thereafter.
const serverNameFilename = ".agentico-server-name"

// MaxServerNameLength is the maximum rune count accepted for a server name
// from any source: --name flag, server.name config, or the persisted
// identity file.
const MaxServerNameLength = 64

var serverNameAdjectives = []string{
	"amber", "bold", "brisk", "breezy", "calm", "clever", "cozy", "crisp",
	"dapper", "eager", "frisky", "frosty", "frothy", "gentle", "golden",
	"happy", "hearty", "jolly", "keen", "lively", "mellow", "merry",
	"nimble", "peppery", "plucky", "quiet", "savory", "snappy", "sprightly",
	"sunny", "swift", "toasty", "velvet", "witty", "zesty",
}

var serverNameCoffeeTypes = []string{
	"americano", "breve", "cappuccino", "cortado", "espresso", "flat-white",
	"frappe", "latte", "lungo", "macchiato", "mocha", "nitro", "ristretto",
	"affogato", "cafe-au-lait", "cold-brew",
}

func ServerNamePath(runtimeDir string) string {
	return filepath.Join(runtimeDir, serverNameFilename)
}

// ValidateServerName enforces the shared name contract used everywhere a
// name enters: trimmed, non-empty, no control characters, at most
// MaxServerNameLength runes.
func ValidateServerName(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return errors.New("name is empty")
	}
	if utf8Len(trimmed) > MaxServerNameLength {
		return fmt.Errorf("name is %d characters; maximum is %d", utf8Len(trimmed), MaxServerNameLength)
	}
	for _, r := range trimmed {
		if unicode.IsControl(r) {
			return fmt.Errorf("name contains a control character (U+%04X)", r)
		}
	}
	return nil
}

func utf8Len(s string) int {
	return len([]rune(s))
}

// GenerateServerName returns a random "adjective-coffee" name, e.g.
// "frothy-macchiato".
func GenerateServerName() (string, error) {
	adjective, err := pickServerNameWord(serverNameAdjectives)
	if err != nil {
		return "", err
	}
	coffee, err := pickServerNameWord(serverNameCoffeeTypes)
	if err != nil {
		return "", err
	}
	return adjective + "-" + coffee, nil
}

func pickServerNameWord(words []string) (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(len(words))))
	if err != nil {
		return "", fmt.Errorf("generate server name: %w", err)
	}
	return words[n.Int64()], nil
}

// EnsureServerName loads the persisted generated name for runtimeDir,
// generating and persisting one on absence. A corrupt or invalid persisted
// file is regenerated rather than crashing.
func EnsureServerName(runtimeDir string) (string, error) {
	if runtimeDir == "" {
		return "", errors.New("runtime dir is empty")
	}
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return "", fmt.Errorf("create runtime dir: %w", err)
	}
	path := ServerNamePath(runtimeDir)
	data, err := os.ReadFile(path)
	if err == nil {
		if err := os.Chmod(path, 0o600); err != nil {
			return "", fmt.Errorf("repair server name permissions: %w", err)
		}
		name := strings.TrimSpace(string(data))
		if ValidateServerName(name) == nil {
			return name, nil
		}
		// Corrupt/invalid persisted value: fall through and regenerate.
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read server name: %w", err)
	}
	name, err := GenerateServerName()
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(name+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write server name: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", fmt.Errorf("chmod server name: %w", err)
	}
	return name, nil
}

// ResolveServerName applies the name precedence chain: an explicit flag
// override wins over server.name config, which wins over the persisted
// generated name. Explicit overrides are trimmed and validated — an invalid
// override is a hard startup error — but they never rewrite the persisted
// identity file.
func ResolveServerName(flagValue, configValue, runtimeDir string) (string, error) {
	if name := strings.TrimSpace(flagValue); name != "" {
		if err := ValidateServerName(name); err != nil {
			return "", fmt.Errorf("invalid --name value: %w", err)
		}
		return name, nil
	}
	if name := strings.TrimSpace(configValue); name != "" {
		if err := ValidateServerName(name); err != nil {
			return "", fmt.Errorf("invalid server.name config value: %w", err)
		}
		return name, nil
	}
	return EnsureServerName(runtimeDir)
}
