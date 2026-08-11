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
	"net"
	"net/url"
	"strconv"
)

// Connection-string grammar (mirrored by the desktop TypeScript parser):
//
//	agentico://<token>@<host>:<port>[?name=<urlencoded-name>]
//
// The bearer token is standard base64url and sits in the URL userinfo. The
// port is always explicit (there is no default), and the host is always the
// server-advertised dialable address — a wildcard bind address like 0.0.0.0
// never appears. The name query parameter is optional and query-escaped
// (spaces as "+"); unknown query parameters are preserved-parsable but
// ignored by this package.
const connectionStringScheme = "agentico"

// ConnectionString is the parsed form of the one-line network attach string.
type ConnectionString struct {
	Token string
	Host  string
	Port  int
	Name  string
}

// BaseURL returns the dialable http base URL the connection string targets.
func (c ConnectionString) BaseURL() string {
	return "http://" + net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
}

// GenerateConnectionString builds the one-line connection string for a
// network-bound server. Generation is strict: the token must be non-empty
// and the host must be non-empty, non-wildcard, and dialable, with a port in
// range.
func GenerateConnectionString(token, host string, port int, name string) (string, error) {
	if token == "" {
		return "", fmt.Errorf("connection string: token is required")
	}
	if host == "" {
		return "", fmt.Errorf("connection string: host is required")
	}
	if isWildcardHost(host) {
		return "", fmt.Errorf("connection string: host %q is a wildcard bind address, not dialable; advertise the primary interface address instead", host)
	}
	if port < 1 || port > 65535 {
		return "", fmt.Errorf("connection string: port %d out of range 1-65535", port)
	}
	u := &url.URL{
		Scheme: connectionStringScheme,
		User:   url.User(token),
		Host:   net.JoinHostPort(host, strconv.Itoa(port)),
	}
	if name != "" {
		q := url.Values{}
		q.Set("name", name)
		u.RawQuery = q.Encode()
	}
	return u.String(), nil
}

// ConnectionStringFromBaseURL builds the connection string from an advertised
// http base URL, the bearer token, and the server name — the shape startup
// output uses after a network bind.
func ConnectionStringFromBaseURL(baseURL, token, name string) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme != "http" || u.Host == "" {
		return "", fmt.Errorf("connection string: unusable base URL %q", baseURL)
	}
	// Hostname strips IPv6 brackets; Port returns empty when absent.
	host, portText := u.Hostname(), u.Port()
	if host == "" || portText == "" {
		return "", fmt.Errorf("connection string: base URL %q lacks a dialable host or explicit port", baseURL)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return "", fmt.Errorf("connection string: base URL %q has an unparseable port: %v", baseURL, err)
	}
	return GenerateConnectionString(token, host, port, name)
}

// ParseConnectionString parses and validates the one-line attach string,
// rejecting malformed input with distinct, actionable errors.
func ParseConnectionString(raw string) (ConnectionString, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return ConnectionString{}, fmt.Errorf("connection string %q is not a valid URL: %v", raw, err)
	}
	if u.Scheme != connectionStringScheme {
		return ConnectionString{}, fmt.Errorf("connection string must use the %s:// scheme, got %q", connectionStringScheme, u.Scheme)
	}
	if u.User == nil || u.User.Username() == "" {
		return ConnectionString{}, fmt.Errorf("connection string is missing the bearer token in userinfo (agentico://<token>@<host>:<port>)")
	}
	host := u.Hostname()
	if host == "" {
		return ConnectionString{}, fmt.Errorf("connection string is missing a host")
	}
	if isWildcardHost(host) {
		return ConnectionString{}, fmt.Errorf("connection string host %q is a wildcard bind address, not dialable", host)
	}
	portText := u.Port()
	if portText == "" {
		return ConnectionString{}, fmt.Errorf("connection string is missing an explicit port")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return ConnectionString{}, fmt.Errorf("connection string port %q is unparseable or out of range 1-65535", portText)
	}
	return ConnectionString{
		Token: u.User.Username(),
		Host:  host,
		Port:  port,
		Name:  u.Query().Get("name"),
	}, nil
}

func isWildcardHost(host string) bool {
	return host == "0.0.0.0" || host == "::"
}
