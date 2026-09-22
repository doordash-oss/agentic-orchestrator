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

package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	defaultAPIBase       = "https://slack.com/api/"
	defaultTimeout       = 10 * time.Second
	defaultUploadTimeout = 60 * time.Second
	userAgent            = "Agentico/1 Slack"

	// EnvSlackAPIBase overrides the Slack Web API base URL.
	EnvSlackAPIBase = "AGENTICO_SLACK_API_BASE"
)

// APIError is a successful HTTP response whose Slack envelope was not ok.
type APIError struct {
	SlackError string
	Needed     string
}

func (e *APIError) Error() string {
	return "Slack API error: " + e.SlackError
}

// TransportError reports an HTTP, decoding, connection, or timeout failure.
type TransportError struct {
	StatusCode int
	RetryAfter time.Duration
	Detail     string
}

func (e *TransportError) Error() string {
	switch {
	case e.StatusCode != 0 && e.RetryAfter > 0:
		return fmt.Sprintf("Slack transport error: HTTP %d, retry after %s", e.StatusCode, e.RetryAfter)
	case e.StatusCode != 0 && e.Detail != "":
		return fmt.Sprintf("Slack transport error: HTTP %d: %s", e.StatusCode, e.Detail)
	case e.StatusCode != 0:
		return fmt.Sprintf("Slack transport error: HTTP %d", e.StatusCode)
	case e.Detail != "":
		return "Slack transport error: " + e.Detail
	default:
		return "Slack transport error"
	}
}

// AuthTestResponse is the typed result of Slack's auth.test method.
type AuthTestResponse struct {
	TeamID        string
	TeamName      string
	UserName      string
	UserID        string
	WorkspaceURL  string
	BotID         string
	GrantedScopes []string
}

// User is the Slack member data needed for recipient resolution.
type User struct {
	ID          string
	Name        string
	RealName    string
	DisplayName string
	Deleted     bool
	IsBot       bool
}

// UsersPage is one cursor page from users.list.
type UsersPage struct {
	Users      []User
	NextCursor string
}

// Conversation is the Slack channel data needed for recipient resolution.
type Conversation struct {
	ID         string
	Name       string
	IsPrivate  bool
	IsMember   bool
	IsArchived bool
}

// ConversationsPage is one cursor page from conversations.list.
type ConversationsPage struct {
	Conversations []Conversation
	NextCursor    string
}

// ClientOption customizes a Slack client.
type ClientOption func(*clientConfig)

type clientConfig struct {
	baseURL          string
	timeout          time.Duration
	uploadTimeout    time.Duration
	uploadTimeoutSet bool
	httpClient       *http.Client
}

// WithBaseURL overrides the Slack Web API base URL.
func WithBaseURL(baseURL string) ClientOption {
	return func(cfg *clientConfig) {
		cfg.baseURL = baseURL
	}
}

// WithTimeout overrides the per-request context timeout.
func WithTimeout(timeout time.Duration) ClientOption {
	return func(cfg *clientConfig) {
		cfg.timeout = timeout
	}
}

// WithUploadTimeout overrides the raw byte upload timeout.
func WithUploadTimeout(timeout time.Duration) ClientOption {
	return func(cfg *clientConfig) {
		cfg.uploadTimeout = timeout
		cfg.uploadTimeoutSet = true
	}
}

// WithHTTPClient overrides the HTTP client while retaining request timeouts.
func WithHTTPClient(client *http.Client) ClientOption {
	return func(cfg *clientConfig) {
		cfg.httpClient = client
	}
}

// Client is a token-bound Slack Web API client.
type Client struct {
	token         string
	baseURL       *url.URL
	timeout       time.Duration
	uploadTimeout time.Duration
	httpClient    *http.Client
}

// NewClient constructs a client without making a network request.
func NewClient(token string, opts ...ClientOption) (*Client, error) {
	baseURL := os.Getenv(EnvSlackAPIBase)
	if baseURL == "" {
		baseURL = defaultAPIBase
	}
	cfg := clientConfig{
		baseURL:       baseURL,
		timeout:       defaultTimeout,
		uploadTimeout: defaultUploadTimeout,
		httpClient:    &http.Client{},
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.timeout <= 0 {
		return nil, errors.New("Slack request timeout must be positive")
	}
	if cfg.uploadTimeout <= 0 {
		return nil, errors.New("Slack upload timeout must be positive")
	}
	if !cfg.uploadTimeoutSet && cfg.uploadTimeout <= cfg.timeout {
		cfg.uploadTimeout = 2 * cfg.timeout
	}
	if cfg.httpClient == nil {
		return nil, errors.New("Slack HTTP client must not be nil")
	}
	base, err := url.Parse(cfg.baseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("invalid Slack API base URL %q", scrub(token, cfg.baseURL))
	}
	if !strings.HasSuffix(base.Path, "/") {
		base.Path += "/"
	}
	return &Client{
		token:         token,
		baseURL:       base,
		timeout:       cfg.timeout,
		uploadTimeout: cfg.uploadTimeout,
		httpClient:    cfg.httpClient,
	}, nil
}

// AuthTest validates the client's token and returns its workspace identity.
func (c *Client) AuthTest(ctx context.Context) (AuthTestResponse, error) {
	var envelope struct {
		OK     bool   `json:"ok"`
		Error  string `json:"error"`
		Needed string `json:"needed"`
		Team   string `json:"team"`
		TeamID string `json:"team_id"`
		User   string `json:"user"`
		UserID string `json:"user_id"`
		URL    string `json:"url"`
		BotID  string `json:"bot_id"`
	}
	scopes, err := c.call(ctx, "auth.test", nil, &envelope)
	if err != nil {
		return AuthTestResponse{}, err
	}
	if !envelope.OK {
		return AuthTestResponse{}, c.apiError(envelope.Error, envelope.Needed)
	}
	return AuthTestResponse{
		TeamID:        envelope.TeamID,
		TeamName:      envelope.Team,
		UserName:      envelope.User,
		UserID:        envelope.UserID,
		WorkspaceURL:  envelope.URL,
		BotID:         envelope.BotID,
		GrantedScopes: scopes,
	}, nil
}

// LookupUserByEmail calls users.lookupByEmail.
func (c *Client) LookupUserByEmail(ctx context.Context, email string) (User, error) {
	var envelope struct {
		OK     bool    `json:"ok"`
		Error  string  `json:"error"`
		Needed string  `json:"needed"`
		User   apiUser `json:"user"`
	}
	fields := url.Values{"email": {email}}
	if _, err := c.call(ctx, "users.lookupByEmail", fields, &envelope); err != nil {
		return User{}, err
	}
	if !envelope.OK {
		return User{}, c.apiError(envelope.Error, envelope.Needed)
	}
	return envelope.User.user(), nil
}

// UserInfo calls users.info.
func (c *Client) UserInfo(ctx context.Context, userID string) (User, error) {
	var envelope struct {
		OK     bool    `json:"ok"`
		Error  string  `json:"error"`
		Needed string  `json:"needed"`
		User   apiUser `json:"user"`
	}
	fields := url.Values{"user": {userID}}
	if _, err := c.call(ctx, "users.info", fields, &envelope); err != nil {
		return User{}, err
	}
	if !envelope.OK {
		return User{}, c.apiError(envelope.Error, envelope.Needed)
	}
	return envelope.User.user(), nil
}

// UsersList calls one cursor page of users.list.
func (c *Client) UsersList(ctx context.Context, cursor string, limit int) (UsersPage, error) {
	var envelope struct {
		OK       bool      `json:"ok"`
		Error    string    `json:"error"`
		Needed   string    `json:"needed"`
		Members  []apiUser `json:"members"`
		Metadata struct {
			NextCursor string `json:"next_cursor"`
		} `json:"response_metadata"`
	}
	fields := url.Values{"limit": {strconv.Itoa(limit)}}
	if cursor != "" {
		fields.Set("cursor", cursor)
	}
	if _, err := c.call(ctx, "users.list", fields, &envelope); err != nil {
		return UsersPage{}, err
	}
	if !envelope.OK {
		return UsersPage{}, c.apiError(envelope.Error, envelope.Needed)
	}
	page := UsersPage{
		Users:      make([]User, len(envelope.Members)),
		NextCursor: strings.TrimSpace(envelope.Metadata.NextCursor),
	}
	for index, member := range envelope.Members {
		page.Users[index] = member.user()
	}
	return page, nil
}

// ConversationsList calls one cursor page of conversations.list.
func (c *Client) ConversationsList(
	ctx context.Context,
	cursor string,
	limit int,
) (ConversationsPage, error) {
	var envelope struct {
		OK       bool              `json:"ok"`
		Error    string            `json:"error"`
		Needed   string            `json:"needed"`
		Channels []apiConversation `json:"channels"`
		Metadata struct {
			NextCursor string `json:"next_cursor"`
		} `json:"response_metadata"`
	}
	fields := url.Values{
		"types":            {"public_channel,private_channel"},
		"exclude_archived": {"false"},
		"limit":            {strconv.Itoa(limit)},
	}
	if cursor != "" {
		fields.Set("cursor", cursor)
	}
	if _, err := c.call(ctx, "conversations.list", fields, &envelope); err != nil {
		return ConversationsPage{}, err
	}
	if !envelope.OK {
		return ConversationsPage{}, c.apiError(envelope.Error, envelope.Needed)
	}
	page := ConversationsPage{
		Conversations: make([]Conversation, len(envelope.Channels)),
		NextCursor:    strings.TrimSpace(envelope.Metadata.NextCursor),
	}
	for index, channel := range envelope.Channels {
		page.Conversations[index] = channel.conversation()
	}
	return page, nil
}

// ConversationInfo calls conversations.info.
func (c *Client) ConversationInfo(ctx context.Context, channelID string) (Conversation, error) {
	var envelope struct {
		OK      bool            `json:"ok"`
		Error   string          `json:"error"`
		Needed  string          `json:"needed"`
		Channel apiConversation `json:"channel"`
	}
	fields := url.Values{"channel": {channelID}}
	if _, err := c.call(ctx, "conversations.info", fields, &envelope); err != nil {
		return Conversation{}, err
	}
	if !envelope.OK {
		return Conversation{}, c.apiError(envelope.Error, envelope.Needed)
	}
	return envelope.Channel.conversation(), nil
}

// OpenConversation opens a direct message and returns its channel ID.
func (c *Client) OpenConversation(ctx context.Context, userID string) (string, error) {
	var envelope struct {
		OK      bool   `json:"ok"`
		Error   string `json:"error"`
		Needed  string `json:"needed"`
		Channel struct {
			ID string `json:"id"`
		} `json:"channel"`
	}
	fields := url.Values{"users": {userID}}
	if _, err := c.call(ctx, "conversations.open", fields, &envelope); err != nil {
		return "", err
	}
	if !envelope.OK {
		return "", c.apiError(envelope.Error, envelope.Needed)
	}
	return envelope.Channel.ID, nil
}

// PostMessage sends a plain-text chat.postMessage request.
func (c *Client) PostMessage(ctx context.Context, channelID, text string) error {
	_, err := c.PostMessageRich(ctx, PostMessageInput{
		Channel:      channelID,
		FallbackText: text,
	})
	return err
}

// PostMessageInput describes one rich chat.postMessage request. Blocks are
// Block Kit values (see blocks.go); the fallback text is what notifications
// and thread previews show.
type PostMessageInput struct {
	Channel        string
	FallbackText   string
	Blocks         []Block
	ThreadTS       string
	ReplyBroadcast bool
}

// PostMessageResult echoes back the timestamps Slack assigns.
type PostMessageResult struct {
	TS      string
	Channel string
}

// UploadFileInput describes a file uploaded and shared into a thread.
type UploadFileInput struct {
	Filename  string
	Title     string
	Data      []byte
	ChannelID string
	ThreadTS  string
}

// UploadFileResult identifies the uploaded file and its share message.
type UploadFileResult struct {
	FileID  string
	ShareTS string
}

// UploadURLResult identifies the raw upload target allocated by Slack.
type UploadURLResult struct {
	UploadURL string
	FileID    string
}

// CompleteUploadInput binds one uploaded file to a channel thread.
type CompleteUploadInput struct {
	FileID    string
	Title     string
	ChannelID string
	ThreadTS  string
}

// UploadFileToThread uploads bytes through Slack's external upload pair and
// shares the completed file into one channel thread.
func (c *Client) UploadFileToThread(
	ctx context.Context,
	input UploadFileInput,
) (UploadFileResult, error) {
	target, err := c.RequestUploadURL(ctx, input.Filename, len(input.Data))
	if err != nil {
		return UploadFileResult{}, err
	}
	if err := c.UploadBytes(ctx, target.UploadURL, input.Data); err != nil {
		return UploadFileResult{}, err
	}
	return c.CompleteUploadToThread(ctx, CompleteUploadInput{
		FileID:    target.FileID,
		Title:     input.Title,
		ChannelID: input.ChannelID,
		ThreadTS:  input.ThreadTS,
	})
}

// RequestUploadURL allocates one external upload target.
func (c *Client) RequestUploadURL(
	ctx context.Context,
	filename string,
	length int,
) (UploadURLResult, error) {
	var uploadURLResponse struct {
		OK        bool   `json:"ok"`
		Error     string `json:"error"`
		Needed    string `json:"needed"`
		UploadURL string `json:"upload_url"`
		FileID    string `json:"file_id"`
	}
	if _, err := c.call(ctx, "files.getUploadURLExternal", url.Values{
		"filename": {scrub(c.token, filename)},
		"length":   {strconv.Itoa(length)},
	}, &uploadURLResponse); err != nil {
		return UploadURLResult{}, err
	}
	if !uploadURLResponse.OK {
		return UploadURLResult{}, c.apiError(uploadURLResponse.Error, uploadURLResponse.Needed)
	}
	return UploadURLResult{
		UploadURL: uploadURLResponse.UploadURL,
		FileID:    uploadURLResponse.FileID,
	}, nil
}

// UploadBytes posts the raw file body to Slack's allocated upload URL.
func (c *Client) UploadBytes(ctx context.Context, uploadURL string, data []byte) error {
	return c.uploadBytes(ctx, uploadURL, data)
}

// CompleteUploadToThread shares an uploaded file into one channel thread.
func (c *Client) CompleteUploadToThread(
	ctx context.Context,
	input CompleteUploadInput,
) (UploadFileResult, error) {
	files, err := json.Marshal([]map[string]string{{
		"id":    input.FileID,
		"title": scrub(c.token, input.Title),
	}})
	if err != nil {
		return UploadFileResult{}, c.transportError(0, 0, err)
	}
	var completionResponse struct {
		OK     bool      `json:"ok"`
		Error  string    `json:"error"`
		Needed string    `json:"needed"`
		File   apiFile   `json:"file"`
		Files  []apiFile `json:"files"`
	}
	if _, err := c.call(ctx, "files.completeUploadExternal", url.Values{
		"files":      {string(files)},
		"channel_id": {input.ChannelID},
		"thread_ts":  {input.ThreadTS},
	}, &completionResponse); err != nil {
		return UploadFileResult{}, err
	}
	if !completionResponse.OK {
		return UploadFileResult{}, c.apiError(completionResponse.Error, completionResponse.Needed)
	}
	completedFiles := completionResponse.Files
	if completionResponse.File.ID != "" {
		completedFiles = append(completedFiles, completionResponse.File)
	}
	return UploadFileResult{
		FileID: input.FileID,
		ShareTS: fileShareTimestamp(
			completedFiles,
			input.FileID,
			input.ChannelID,
		),
	}, nil
}

// PostMessageRich posts a Block Kit message, optionally as a thread reply,
// and returns the message timestamp Slack echoes back.
func (c *Client) PostMessageRich(ctx context.Context, input PostMessageInput) (PostMessageResult, error) {
	var envelope struct {
		OK      bool   `json:"ok"`
		Error   string `json:"error"`
		Needed  string `json:"needed"`
		TS      string `json:"ts"`
		Channel string `json:"channel"`
	}
	fields := url.Values{
		"channel": {input.Channel},
		"text":    {input.FallbackText},
	}
	if len(input.Blocks) > 0 {
		encoded, err := json.Marshal(input.Blocks)
		if err != nil {
			return PostMessageResult{}, c.transportError(0, 0, err)
		}
		fields.Set("blocks", string(encoded))
	}
	if input.ThreadTS != "" {
		fields.Set("thread_ts", input.ThreadTS)
	}
	if input.ReplyBroadcast {
		fields.Set("reply_broadcast", "true")
	}
	if _, err := c.call(ctx, "chat.postMessage", fields, &envelope); err != nil {
		return PostMessageResult{}, err
	}
	if !envelope.OK {
		return PostMessageResult{}, c.apiError(envelope.Error, envelope.Needed)
	}
	return PostMessageResult{TS: envelope.TS, Channel: envelope.Channel}, nil
}

// UpdateMessage edits an existing message in place through chat.update.
func (c *Client) UpdateMessage(
	ctx context.Context,
	channelID, ts, fallbackText string,
	blocks []Block,
) error {
	var envelope struct {
		OK     bool   `json:"ok"`
		Error  string `json:"error"`
		Needed string `json:"needed"`
	}
	fields := url.Values{
		"channel": {channelID},
		"ts":      {ts},
		"text":    {fallbackText},
	}
	if len(blocks) > 0 {
		encoded, err := json.Marshal(blocks)
		if err != nil {
			return c.transportError(0, 0, err)
		}
		fields.Set("blocks", string(encoded))
	}
	if _, err := c.call(ctx, "chat.update", fields, &envelope); err != nil {
		return err
	}
	if !envelope.OK {
		return c.apiError(envelope.Error, envelope.Needed)
	}
	return nil
}

type apiUser struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	RealName string `json:"real_name"`
	Profile  struct {
		DisplayName string `json:"display_name"`
	} `json:"profile"`
	Deleted bool `json:"deleted"`
	IsBot   bool `json:"is_bot"`
}

func (u apiUser) user() User {
	return User{
		ID:          u.ID,
		Name:        u.Name,
		RealName:    u.RealName,
		DisplayName: u.Profile.DisplayName,
		Deleted:     u.Deleted,
		IsBot:       u.IsBot,
	}
}

type apiConversation struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	IsPrivate  bool   `json:"is_private"`
	IsMember   bool   `json:"is_member"`
	IsArchived bool   `json:"is_archived"`
}

type apiFile struct {
	ID     string `json:"id"`
	Shares struct {
		Public  map[string][]apiFileShare `json:"public"`
		Private map[string][]apiFileShare `json:"private"`
	} `json:"shares"`
}

type apiFileShare struct {
	TS string `json:"ts"`
}

func fileShareTimestamp(files []apiFile, fileID, channelID string) string {
	for _, file := range files {
		if file.ID != "" && file.ID != fileID {
			continue
		}
		for _, shares := range []map[string][]apiFileShare{file.Shares.Public, file.Shares.Private} {
			for _, share := range shares[channelID] {
				if timestamp := strings.TrimSpace(share.TS); timestamp != "" {
					return timestamp
				}
			}
		}
	}
	return ""
}

func (c apiConversation) conversation() Conversation {
	return Conversation{
		ID:         c.ID,
		Name:       c.Name,
		IsPrivate:  c.IsPrivate,
		IsMember:   c.IsMember,
		IsArchived: c.IsArchived,
	}
}

func (c *Client) apiError(slackError, needed string) *APIError {
	return &APIError{
		SlackError: scrub(c.token, slackError),
		Needed:     scrub(c.token, needed),
	}
}

func (c *Client) call(
	ctx context.Context,
	method string,
	fields url.Values,
	target any,
) ([]string, error) {
	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	endpoint := c.baseURL.ResolveReference(&url.URL{Path: method})
	encodedFields := ""
	if fields != nil {
		encodedFields = fields.Encode()
	}
	req, err := http.NewRequestWithContext(
		requestCtx,
		http.MethodPost,
		endpoint.String(),
		strings.NewReader(encodedFields),
	)
	if err != nil {
		return nil, c.transportError(0, 0, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, c.transportError(0, 0, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 32<<10))
		return nil, c.transportError(resp.StatusCode, retryAfter(resp.Header), nil)
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 1<<20))
	if err := decoder.Decode(target); err != nil {
		return nil, c.transportError(resp.StatusCode, 0, fmt.Errorf("decoding JSON response: %v", err))
	}
	return parseScopes(resp.Header.Get("X-OAuth-Scopes")), nil
}

func (c *Client) uploadBytes(ctx context.Context, uploadURL string, data []byte) error {
	requestCtx, cancel := context.WithTimeout(ctx, c.uploadTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(
		requestCtx,
		http.MethodPost,
		uploadURL,
		bytes.NewReader(data),
	)
	if err != nil {
		return c.transportError(0, 0, err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return c.transportError(0, 0, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 32<<10))
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return c.transportError(resp.StatusCode, retryAfter(resp.Header), nil)
	}
	return nil
}

func (c *Client) transportError(status int, retry time.Duration, err error) *TransportError {
	detail := ""
	if err != nil {
		detail = scrub(c.token, err.Error())
	}
	return &TransportError{StatusCode: status, RetryAfter: retry, Detail: detail}
}

func retryAfter(header http.Header) time.Duration {
	raw := strings.TrimSpace(header.Get("Retry-After"))
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds < 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func parseScopes(header string) []string {
	set := make(map[string]struct{})
	for _, raw := range strings.Split(header, ",") {
		if scope := strings.TrimSpace(raw); scope != "" {
			set[scope] = struct{}{}
		}
	}
	scopes := make([]string, 0, len(set))
	for scope := range set {
		scopes = append(scopes, scope)
	}
	sort.Strings(scopes)
	return scopes
}

var (
	slackTokenPattern    = regexp.MustCompile(`(?i)\bxox[a-z]-[A-Za-z0-9-]{8,}\b`)
	authHeaderKeyPattern = regexp.MustCompile(`(?i)(?:["']authorization["']|\bauthorization)\s*[:=]\s*`)
	urlCredentialPattern = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^/@\s]+@`)
)

func scrub(token, text string) string {
	text = scrubAuthorizationHeaders(text)
	if token != "" {
		text = strings.ReplaceAll(text, token, "[REDACTED]")
	}
	text = slackTokenPattern.ReplaceAllString(text, "[REDACTED]")
	return urlCredentialPattern.ReplaceAllString(text, `${1}[REDACTED]@`)
}

func scrubAuthorizationHeaders(text string) string {
	var result strings.Builder
	for {
		match := authHeaderKeyPattern.FindStringIndex(text)
		if match == nil {
			result.WriteString(text)
			return result.String()
		}

		result.WriteString(text[:match[1]])
		text = text[match[1]:]
		end, replacement := authorizationValueBoundary(text)
		result.WriteString(replacement)
		text = text[end:]
	}
}

func authorizationValueBoundary(text string) (int, string) {
	if text == "" {
		return 0, "[REDACTED]"
	}
	switch text[0] {
	case '"', '\'':
		if end := quotedValueEnd(text, text[0]); end > 0 {
			return end, string(text[0]) + "[REDACTED]" + string(text[0])
		}
	case '[':
		if end := delimitedValueEnd(text, '[', ']'); end > 0 {
			return end, `["[REDACTED]"]`
		}
	}
	return rawAuthorizationValueEnd(text), "[REDACTED]"
}

func quotedValueEnd(text string, quote byte) int {
	escaped := false
	for i := 1; i < len(text); i++ {
		switch {
		case escaped:
			escaped = false
		case text[i] == '\\':
			escaped = true
		case text[i] == quote:
			return i + 1
		}
	}
	return 0
}

func delimitedValueEnd(text string, open, close byte) int {
	depth := 0
	var quote byte
	escaped := false
	for i := 0; i < len(text); i++ {
		switch {
		case escaped:
			escaped = false
		case quote != 0 && text[i] == '\\':
			escaped = true
		case quote != 0 && text[i] == quote:
			quote = 0
		case quote != 0:
		case text[i] == '"' || text[i] == '\'':
			quote = text[i]
		case text[i] == open:
			depth++
		case text[i] == close:
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return 0
}

func rawAuthorizationValueEnd(text string) int {
	var quote byte
	escaped := false
	for i := 0; i < len(text); i++ {
		switch {
		case escaped:
			escaped = false
		case quote != 0 && text[i] == '\\':
			escaped = true
		case quote != 0 && text[i] == quote:
			quote = 0
		case quote != 0:
		case text[i] == '"' || text[i] == '\'':
			quote = text[i]
		case text[i] == '\r' || text[i] == '\n' || text[i] == ';' ||
			text[i] == '}' || text[i] == ']':
			return i
		}
	}
	return len(text)
}
