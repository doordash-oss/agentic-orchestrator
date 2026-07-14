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

package serverapi

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGeneratedPermissionAnswerRequestPreservesEmptyRememberScope(t *testing.T) {
	scope := ""
	body, err := json.Marshal(PermissionAnswerRequest{
		RequestID:       "perm-1",
		Decision:        AllowRemember,
		RememberPattern: "Bash(go test *)",
		RememberScope:   &scope,
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if !strings.Contains(string(body), `"remember_scope":""`) {
		t.Fatalf("body = %s, want explicit empty remember_scope", body)
	}
}
