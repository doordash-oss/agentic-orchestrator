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
	"strconv"
	"strings"
)

// DefaultListenAddr is the bind address used when no explicit loopback
// address is requested: an ephemeral port on 127.0.0.1.
const DefaultListenAddr = "127.0.0.1:0"

// ResolveListenAddr normalizes a --listen value into a TCP bind address.
// Accepted forms are a bare port ("8080", bound on 127.0.0.1) and
// host:port with an explicit loopback host ("127.0.0.1:8080",
// "localhost:8080", "[::1]:8080"). Anything else is rejected before any
// socket is opened: today only loopback binds are supported; network
// exposure arrives with a later release's opt-in network policy.
func ResolveListenAddr(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return DefaultListenAddr, nil
	}
	if !strings.Contains(trimmed, ":") {
		port, err := parseListenPort(trimmed)
		if err != nil {
			return "", fmt.Errorf("invalid --listen value %q: %v", value, err)
		}
		return net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), nil
	}
	host, portText, err := net.SplitHostPort(trimmed)
	if err != nil {
		return "", fmt.Errorf("invalid --listen value %q: use <port> or [host:]port, e.g. 8080 or 127.0.0.1:8080", value)
	}
	host = strings.Trim(strings.TrimSpace(host), "[]")
	switch strings.ToLower(host) {
	case "", "127.0.0.1", hostLocalhost, "::1":
	default:
		return "", fmt.Errorf("--listen host %q is not allowed: only loopback binds (127.0.0.1, localhost, [::1]) are supported today; network exposure arrives with a later release's opt-in network policy", host)
	}
	port, err := parseListenPort(portText)
	if err != nil {
		return "", fmt.Errorf("invalid --listen value %q: %v", value, err)
	}
	if host == "" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}

func parseListenPort(text string) (int, error) {
	port, err := strconv.Atoi(text)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("port %q must be a number between 1 and 65535", text)
	}
	return port, nil
}
