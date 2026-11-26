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

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

type serializedStartupProtocol struct {
	*blockingHandshakeProtocol
	mu   *sync.Mutex
	fail bool
}

func (p serializedStartupProtocol) SerializeStartup() func() {
	p.mu.Lock()
	return sync.OnceFunc(p.mu.Unlock)
}

func (p serializedStartupProtocol) Handshake(ctx context.Context) error {
	if err := p.blockingHandshakeProtocol.Handshake(ctx); err != nil {
		return err
	}
	if p.fail {
		return errors.New("handshake failed")
	}
	return nil
}

func TestStartSessionSerializesProviderStartupUntilHandshakeEnds(t *testing.T) {
	eventCh := make(chan interface{}, 64)
	sm := NewManager(eventCh)
	defer sm.Shutdown()
	go func() {
		for range eventCh {
		}
	}()

	var mu sync.Mutex
	first := serializedStartupProtocol{
		blockingHandshakeProtocol: &blockingHandshakeProtocol{started: make(chan struct{}), release: make(chan struct{})},
		mu:                        &mu,
		fail:                      true,
	}
	second := serializedStartupProtocol{
		blockingHandshakeProtocol: &blockingHandshakeProtocol{started: make(chan struct{}), release: make(chan struct{})},
		mu:                        &mu,
	}
	close(second.release)

	firstErr := make(chan error, 1)
	go func() {
		_, err := sm.StartSession("first", "feat-1", feature.PhaseDesign, []string{"sleep", "30"}, t.TempDir(), nil,
			&SessionOpts{Protocol: first, CodexHandshakeTimeout: time.Minute})
		firstErr <- err
	}()
	<-first.started

	marker := filepath.Join(t.TempDir(), "spawned")
	secondErr := make(chan error, 1)
	go func() {
		_, err := sm.StartSession("second", "feat-1", feature.PhaseDesign, []string{"sh", "-c", "touch \"$0\"; exec sleep 30", marker}, t.TempDir(), nil,
			&SessionOpts{Protocol: second, CodexHandshakeTimeout: time.Minute})
		secondErr <- err
	}()

	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("second provider spawned while the first was still in its handshake")
	}

	close(first.release)
	if err := <-firstErr; err == nil {
		t.Fatal("first StartSession() succeeded, want its handshake error")
	}
	select {
	case err := <-secondErr:
		if err != nil {
			t.Fatalf("second StartSession() error = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("second StartSession() did not proceed after the first handshake failed")
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(marker); err == nil {
			return
		} else if time.Now().After(deadline) {
			t.Fatalf("second provider was not spawned: %v", err)
		}
	}
}
