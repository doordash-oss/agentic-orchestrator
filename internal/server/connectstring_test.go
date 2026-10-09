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
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// parsedConnectionString is the round-trip view of a generated connection
// string. Clients own the parser; these tests only confirm what generation
// encodes.
type parsedConnectionString struct {
	Token string
	Host  string
	Port  int
	Name  string
}

func parseConnectionString(raw string) (parsedConnectionString, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return parsedConnectionString{}, err
	}
	if u.Scheme != connectionStringScheme || u.User == nil {
		return parsedConnectionString{}, fmt.Errorf("connection string %q lacks the scheme or token", raw)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		return parsedConnectionString{}, err
	}
	return parsedConnectionString{Token: u.User.Username(), Host: u.Hostname(), Port: port, Name: u.Query().Get("name")}, nil
}

func TestConnectionStringGenerateParseRoundTrip(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		token string
		host  string
		port  int
		sname string
	}{
		{"ipv4", "dG9rZW4td2l0aF9iNjQtdXJs", "10.1.2.3", 8080, ""},
		{"ipv4 named", "abc_DEF-1234", "192.168.1.10", 9090, "frothy-macchiato"},
		{"hostname", "tok", "nas.local", 443, "home server"},
		{"special name", "tok", "10.0.0.5", 7000, "rig & gamma: 100%"},
		{"ipv6", "tok", "fe80::1", 8080, ""},
	}
	for _, tc := range cases {
		raw, err := generateConnectionString(tc.token, tc.host, tc.port, tc.sname)
		if err != nil {
			t.Errorf("%s: generateConnectionString() error = %v", tc.name, err)
			continue
		}
		if !strings.HasPrefix(raw, "agentico://") {
			t.Errorf("%s: %q lacks the agentico:// scheme", tc.name, raw)
		}
		parsed, err := parseConnectionString(raw)
		if err != nil {
			t.Errorf("%s: parseConnectionString(%q) error = %v", tc.name, raw, err)
			continue
		}
		if parsed.Token != tc.token || parsed.Host != tc.host || parsed.Port != tc.port || parsed.Name != tc.sname {
			t.Errorf("%s: round-trip = %+v; want token=%q host=%q port=%d name=%q",
				tc.name, parsed, tc.token, tc.host, tc.port, tc.sname)
		}
	}
}

func TestGenerateConnectionStringStrict(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		token   string
		host    string
		port    int
		wantErr string
	}{
		{"empty token", "", "10.1.2.3", 8080, "token is required"},
		{"empty host", "tok", "", 8080, "host is required"},
		{"wildcard v4", "tok", "0.0.0.0", 8080, "wildcard"},
		{"wildcard v6", "tok", "::", 8080, "wildcard"},
		{"bad port", "tok", "10.1.2.3", 0, "out of range"},
	} {
		if _, err := generateConnectionString(tc.token, tc.host, tc.port, ""); err == nil ||
			!strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("%s: generateConnectionString() error = %v; want %q", tc.name, err, tc.wantErr)
		}
	}
}

func TestConnectionStringFromBaseURL(t *testing.T) {
	t.Parallel()
	raw, err := ConnectionStringFromBaseURL("http://10.9.8.7:8080", "tok", "my server")
	if err != nil {
		t.Fatalf("ConnectionStringFromBaseURL() error = %v", err)
	}
	parsed, err := parseConnectionString(raw)
	if err != nil {
		t.Fatalf("parseConnectionString(%q) error = %v", raw, err)
	}
	if parsed.Token != "tok" || parsed.Host != "10.9.8.7" || parsed.Port != 8080 || parsed.Name != "my server" {
		t.Fatalf("round-trip = %+v; want tok@10.9.8.7:8080 name=my server", parsed)
	}
	for _, bad := range []string{"https://10.0.0.5:8080", "not a url", "http://10.0.0.5", ""} {
		if _, err := ConnectionStringFromBaseURL(bad, "tok", ""); err == nil {
			t.Errorf("ConnectionStringFromBaseURL(%q) succeeded; want rejection", bad)
		}
	}
}
