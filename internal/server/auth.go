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
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const authTokenFilename = ".agentico-server-token"

func AuthTokenPath(runtimeDir string) string {
	return filepath.Join(runtimeDir, authTokenFilename)
}

func EnsureAuthToken(runtimeDir string) (string, error) {
	if runtimeDir == "" {
		return "", errors.New("runtime dir is empty")
	}
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return "", fmt.Errorf("create runtime dir: %w", err)
	}
	path := AuthTokenPath(runtimeDir)
	data, err := os.ReadFile(path)
	if err == nil {
		if err := os.Chmod(path, 0o600); err != nil {
			return "", fmt.Errorf("repair token permissions: %w", err)
		}
		token := strings.TrimSpace(string(data))
		if token == "" {
			return "", errors.New("auth token file is empty")
		}
		return token, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read auth token: %w", err)
	}
	token, err := newAuthToken()
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write auth token: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", fmt.Errorf("chmod auth token: %w", err)
	}
	return token, nil
}

func newAuthToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate auth token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}
