// Copyright 2026 DoorDash, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package opencode

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestConfiguredEndpointsReadsCustomProviderBaseURLs(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	extra := filepath.Join(t.TempDir(), "extra.json")
	t.Setenv("OPENCODE_CONFIG", extra)
	dir := filepath.Join(xdg, "opencode")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(dir, "opencode.jsonc"), `{
  // gateway behind the VPN
  "model": "portkey/kimi",
  "provider": {
    "portkey": {"options": {"baseURL": "https://gateway.example.team/v1", "headers": {"x-key": "secret"}}},
    /* built-in provider: no endpoint */
    "anthropic": {"options": {}},
    "templated": {"options": {"baseURL": "{env:GATEWAY}"}}
  }
}`)
	write(extra, `{"provider": {"local": {"options": {"baseURL": "http://127.0.0.1:4000"}}, "dup": {"options": {"baseURL": "https://gateway.example.team/v1"}}}}`)

	got := (&Provider{}).ConfiguredEndpoints()
	want := []string{"http://127.0.0.1:4000", "https://gateway.example.team/v1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ConfiguredEndpoints() = %v; want %v", got, want)
	}
}

func TestConfiguredEndpointsWithoutConfigIsEmpty(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("OPENCODE_CONFIG", "")
	if got := (&Provider{}).ConfiguredEndpoints(); len(got) != 0 {
		t.Fatalf("ConfiguredEndpoints() = %v; want none", got)
	}
}
