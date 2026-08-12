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
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// DefaultListenAddr is the bind address used when no explicit loopback
// address is requested: an ephemeral port on 127.0.0.1.
const DefaultListenAddr = "127.0.0.1:0"

// ListenResolution is the full result of normalizing a --listen value: the
// TCP bind address, the host advertised to clients (which differs from the
// bind address for wildcard binds, where the primary interface address is
// advertised instead), and the runtime policy the bind selects.
type ListenResolution struct {
	// BindAddr is the TCP address passed to net.Listen.
	BindAddr string
	// AdvertiseHost is the host part of the base URL published to clients
	// (discovery, registry, startup output). For wildcard binds it is the
	// resolved primary non-loopback IPv4, never 0.0.0.0/::.
	AdvertiseHost string
	// Policy is CompatibilityRuntimePolicy for loopback binds or
	// CompatibilityNetworkRuntimePolicy for network binds.
	Policy string
	// Wildcard reports whether BindAddr binds all local interfaces.
	Wildcard bool
}

// lookupListenHostIPs and probePrimaryIPv4 are seams for tests: hostname
// classification and primary-interface probing touch host networking.
var (
	lookupListenHostIPs = func(host string) ([]net.IP, error) {
		return net.LookupIP(host)
	}
	probePrimaryIPv4 = primaryNonLoopbackIPv4
)

// ResolveListenAddr normalizes a --listen value into a TCP bind address, the
// same way it did when only loopback binds were supported: it performs no
// DNS lookups or interface probing beyond hostname classification. Use
// ResolveListen when the runtime policy and advertised host are also needed.
func ResolveListenAddr(value string) (string, error) {
	res, err := resolveListen(value, nil)
	if err != nil {
		return "", err
	}
	return res.BindAddr, nil
}

// ResolveListen normalizes a --listen value into its full resolution.
// Accepted forms:
//
//   - bare port ("8080") and loopback host:port ("127.0.0.1:8080",
//     "localhost:8080", "[::1]:8080") — loopback policy, unchanged from the
//     loopback-only releases;
//   - concrete non-loopback IPs and hostnames ("10.0.0.5:8080",
//     "myhost:8080") — network policy; a hostname is classified loopback
//     only when it resolves exclusively to loopback addresses;
//   - wildcards ("0.0.0.0:8080", "[::]:8080") — network policy with
//     primary-interface resolution: the advertised host becomes the primary
//     non-loopback IPv4 an outbound dial would route from, and resolution
//     fails fast when no such interface exists.
func ResolveListen(value string) (ListenResolution, error) {
	return resolveListen(value, probePrimaryIPv4)
}

func resolveListen(value string, probe func() (string, error)) (ListenResolution, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ListenResolution{
			BindAddr:      DefaultListenAddr,
			AdvertiseHost: "127.0.0.1",
			Policy:        CompatibilityRuntimePolicy,
		}, nil
	}
	if !strings.Contains(trimmed, ":") {
		port, err := parseListenPort(trimmed)
		if err != nil {
			return ListenResolution{}, fmt.Errorf("invalid --listen value %q: %v", value, err)
		}
		return ListenResolution{
			BindAddr:      net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
			AdvertiseHost: "127.0.0.1",
			Policy:        CompatibilityRuntimePolicy,
		}, nil
	}
	host, portText, err := net.SplitHostPort(trimmed)
	if err != nil {
		return ListenResolution{}, fmt.Errorf("invalid --listen value %q: use <port> or [host:]port, e.g. 8080 or 127.0.0.1:8080", value)
	}
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if host == "" {
		// An explicit empty host (":8080") keeps the loopback default from
		// the loopback-only releases; it is NOT a wildcard.
		host = "127.0.0.1"
	}
	port, err := parseListenPort(portText)
	if err != nil {
		return ListenResolution{}, fmt.Errorf("invalid --listen value %q: %v", value, err)
	}

	loopback, err := classifyListenHost(host, lookupListenHostIPs)
	if err != nil {
		return ListenResolution{}, fmt.Errorf("invalid --listen host %q: %v", host, err)
	}
	if loopback {
		return ListenResolution{
			BindAddr:      net.JoinHostPort(host, strconv.Itoa(port)),
			AdvertiseHost: host,
			Policy:        CompatibilityRuntimePolicy,
		}, nil
	}
	if host == "0.0.0.0" || host == "::" {
		if probe == nil {
			// Parse-time normalization only produces the bind address; the
			// primary-interface probe runs at Start via ResolveListen.
			return ListenResolution{
				BindAddr:      net.JoinHostPort(host, strconv.Itoa(port)),
				AdvertiseHost: "",
				Policy:        CompatibilityNetworkRuntimePolicy,
				Wildcard:      true,
			}, nil
		}
		primary, err := probe()
		if err != nil {
			return ListenResolution{}, fmt.Errorf("wildcard --listen bind %q needs a non-loopback network interface to advertise: %w (use a concrete address or a loopback bind instead)", trimmed, err)
		}
		return ListenResolution{
			BindAddr:      net.JoinHostPort(host, strconv.Itoa(port)),
			AdvertiseHost: primary,
			Policy:        CompatibilityNetworkRuntimePolicy,
			Wildcard:      true,
		}, nil
	}
	return ListenResolution{
		BindAddr:      net.JoinHostPort(host, strconv.Itoa(port)),
		AdvertiseHost: host,
		Policy:        CompatibilityNetworkRuntimePolicy,
	}, nil
}

// classifyListenHost reports whether host binds the loopback interface only.
// "localhost" is loopback by construction; any other hostname is loopback
// only when it resolves exclusively to loopback addresses, per the locked
// classification rule.
func classifyListenHost(host string, lookup func(string) ([]net.IP, error)) (bool, error) {
	ip := net.ParseIP(host)
	if ip != nil {
		return ip.IsLoopback(), nil
	}
	if strings.EqualFold(host, hostLocalhost) {
		return true, nil
	}
	ips, err := lookup(host)
	if err != nil {
		return false, fmt.Errorf("resolve hostname: %w", err)
	}
	if len(ips) == 0 {
		return false, fmt.Errorf("resolve hostname: no addresses")
	}
	for _, resolved := range ips {
		if !resolved.IsLoopback() {
			return false, nil
		}
	}
	return true, nil
}

// primaryNonLoopbackIPv4 finds the primary non-loopback IPv4 by probing which
// local address an outbound UDP dial would route from. UDP dial sends no
// packets, so the arbitrary documentation address is never contacted.
func primaryNonLoopbackIPv4() (string, error) {
	conn, err := net.Dial("udp4", "192.0.2.1:80")
	if err != nil {
		return "", err
	}
	defer conn.Close()
	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || addr.IP == nil || addr.IP.IsLoopback() {
		return "", errors.New("no non-loopback interface found")
	}
	return addr.IP.String(), nil
}

func parseListenPort(text string) (int, error) {
	port, err := strconv.Atoi(text)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("port %q must be a number between 1 and 65535", text)
	}
	return port, nil
}
