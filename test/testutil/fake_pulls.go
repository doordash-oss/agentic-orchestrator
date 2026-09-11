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

package testutil

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// FakePullRequest is one pull-request record served by a FakePullStore.
type FakePullRequest struct {
	Repo   string
	Number int
	Title  string
	Head   string
	Base   string
	Body   string
	Draft  bool
	State  string
	Merged bool
}

// FakePullStore serves the pull-request REST surface for one GitHub owner on
// a FakeGitHubAPI: POST creates a numbered pull request, GET returns its
// state and body, and PATCH updates its body and state. Records are kept in
// creation order so tests can assert the exact PR creation sequence.
type FakePullStore struct {
	owner   string
	mu      sync.Mutex
	next    map[string]int
	pulls   map[string]*FakePullRequest
	created []*FakePullRequest
	patched int
}

// NewFakePullStore creates a store serving /repos/{owner}/{repo}/pulls for
// the repositories it is later installed for.
func NewFakePullStore(owner string) *FakePullStore {
	return &FakePullStore{
		owner: owner,
		next:  map[string]int{},
		pulls: map[string]*FakePullRequest{},
	}
}

// URL returns the canonical pull-request URL the fake answers at.
func (s *FakePullStore) URL(repo string, number int) string {
	return fmt.Sprintf("https://github.com/%s/%s/pull/%d", s.owner, repo, number)
}

// Install registers the create/get/patch handlers for each repository.
func (s *FakePullStore) Install(t *testing.T, fake *FakeGitHubAPI, repos ...string) {
	t.Helper()
	for _, repo := range repos {
		s.mu.Lock()
		if _, ok := s.next[repo]; !ok {
			s.next[repo] = 1
		}
		s.mu.Unlock()
		s.installRepo(t, fake, repo)
	}
}

func (s *FakePullStore) installRepo(t *testing.T, fake *FakeGitHubAPI, repo string) {
	t.Helper()
	writeJSON := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	handle := func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/v3")
		rest := strings.TrimPrefix(path, "/repos/"+s.owner+"/"+repo+"/pulls")
		switch {
		case r.Method == http.MethodPost && rest == "":
			var payload struct {
				Title string `json:"title"`
				Head  string `json:"head"`
				Base  string `json:"base"`
				Body  string `json:"body"`
				Draft bool   `json:"draft"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("decode create payload for %s: %v", repo, err)
				writeJSON(w, http.StatusBadRequest, map[string]string{"message": err.Error()})
				return
			}
			s.mu.Lock()
			number := s.next[repo]
			s.next[repo]++
			pr := &FakePullRequest{
				Repo: repo, Number: number,
				Title: payload.Title, Head: payload.Head, Base: payload.Base,
				Body: payload.Body, Draft: payload.Draft, State: "open",
			}
			s.pulls[pullKey(repo, number)] = pr
			s.created = append(s.created, pr)
			s.mu.Unlock()
			writeJSON(w, http.StatusCreated, map[string]any{
				"html_url": s.URL(repo, number),
				"number":   number,
			})
		case r.Method == http.MethodGet && strings.HasPrefix(rest, "/"):
			number, ok := pullNumber(rest)
			if !ok {
				writeJSON(w, http.StatusNotFound, map[string]string{"message": "not found"})
				return
			}
			s.mu.Lock()
			pr, found := s.pulls[pullKey(repo, number)]
			s.mu.Unlock()
			if !found {
				writeJSON(w, http.StatusNotFound, map[string]string{"message": "not found"})
				return
			}
			mergedAt := any(nil)
			if pr.Merged {
				mergedAt = "2026-01-01T00:00:00Z"
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"html_url":  s.URL(repo, number),
				"title":     pr.Title,
				"body":      pr.Body,
				"state":     pr.State,
				"merged_at": mergedAt,
				"draft":     pr.Draft,
				"base":      map[string]string{"ref": pr.Base},
				"head":      map[string]string{"ref": pr.Head},
			})
		case r.Method == http.MethodPatch && strings.HasPrefix(rest, "/"):
			number, ok := pullNumber(rest)
			if !ok {
				writeJSON(w, http.StatusNotFound, map[string]string{"message": "not found"})
				return
			}
			var payload struct {
				Body  *string `json:"body"`
				State *string `json:"state"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("decode patch payload for %s#%d: %v", repo, number, err)
				writeJSON(w, http.StatusBadRequest, map[string]string{"message": err.Error()})
				return
			}
			s.mu.Lock()
			pr, found := s.pulls[pullKey(repo, number)]
			if found {
				if payload.Body != nil {
					pr.Body = *payload.Body
				}
				if payload.State != nil {
					pr.State = *payload.State
				}
				s.patched++
			}
			s.mu.Unlock()
			if !found {
				writeJSON(w, http.StatusNotFound, map[string]string{"message": "not found"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"html_url": s.URL(repo, number)})
		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"message": "unsupported"})
		}
	}
	// go-gh routes github.com hosts at the api.github.com root but any
	// other host (a GitHub Enterprise style host) at an /api/v3 prefix; the
	// same records must answer at both path shapes.
	for _, prefix := range []string{"", "/api/v3"} {
		fake.Mux.HandleFunc(prefix+"/repos/"+s.owner+"/"+repo+"/pulls", handle)
		fake.Mux.HandleFunc(prefix+"/repos/"+s.owner+"/"+repo+"/pulls/", handle)
	}
}

// Created returns the pull requests in creation order.
func (s *FakePullStore) Created() []FakePullRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]FakePullRequest, 0, len(s.created))
	for _, pr := range s.created {
		out = append(out, *pr)
	}
	return out
}

// CreatedCount counts pull requests created for one repository.
func (s *FakePullStore) CreatedCount(repo string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, pr := range s.created {
		if pr.Repo == repo {
			count++
		}
	}
	return count
}

// PatchedCount counts accepted body/state updates.
func (s *FakePullStore) PatchedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.patched
}

// Pull returns one stored pull request record.
func (s *FakePullStore) Pull(repo string, number int) (FakePullRequest, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pr, ok := s.pulls[pullKey(repo, number)]
	if !ok {
		return FakePullRequest{}, false
	}
	return *pr, true
}

func pullKey(repo string, number int) string {
	return repo + "#" + strconv.Itoa(number)
}

func pullNumber(rest string) (int, bool) {
	number, err := strconv.Atoi(strings.TrimPrefix(rest, "/"))
	return number, err == nil
}
