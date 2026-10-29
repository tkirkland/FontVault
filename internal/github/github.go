// Package github implements the minimal GitHub REST API client fontvault
// needs. The Client interface keeps the API behind a seam for tests.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client is the GitHub API surface used by fontvault.
type Client interface {
	// CurrentUser returns the login of the authenticated account.
	CurrentUser(ctx context.Context) (login string, err error)
	// RepoExists reports whether owner/name exists.
	RepoExists(ctx context.Context, owner, name string) (bool, error)
	// CreateRepo creates a public repository with the given name under the
	// authenticated account. A 403/404 response becomes *CreateError.
	CreateRepo(ctx context.Context, name string, private bool) error
}

// CreateError is a repository creation failure carrying the status code and
// the X-Accepted-GitHub-Permissions response header, if present.
type CreateError struct {
	StatusCode          int
	AcceptedPermissions string
	Body                string
	Err                 error
}

func (e *CreateError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("create repository: HTTP %d: %v", e.StatusCode, e.Err)
	}
	return fmt.Sprintf("create repository: HTTP %d", e.StatusCode)
}

func (e *CreateError) Unwrap() error { return e.Err }

// HTTP is the production Client.
type HTTP struct {
	Token   string
	BaseURL string // default https://api.github.com
	Version string // fontvault version for the User-Agent
	Client  *http.Client
}

// New returns a production client with sensible defaults.
func New(token, version string) *HTTP {
	return &HTTP{
		Token:   token,
		BaseURL: "https://api.github.com",
		Version: version,
		Client:  &http.Client{Timeout: 30 * time.Second},
	}
}

func (h *HTTP) do(ctx context.Context, method, path string, body any, out any) (*http.Response, []byte, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, nil, fmt.Errorf("marshal request: %w", err)
		}
		rdr = bytes.NewReader(b)
	}
	url := strings.TrimSuffix(h.BaseURL, "/") + path
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return nil, nil, fmt.Errorf("build request %s %s: %w", method, url, err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+h.Token)
	ua := "fontvault"
	if h.Version != "" {
		ua += "/" + h.Version
	}
	req.Header.Set("User-Agent", ua)
	resp, err := h.Client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("%s %s: %w", method, url, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, nil, fmt.Errorf("read response %s %s: %w", method, url, err)
	}
	if out != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if err := json.Unmarshal(data, out); err != nil {
			return resp, data, fmt.Errorf("decode response %s %s: %w", method, url, err)
		}
	}
	return resp, data, nil
}

// CurrentUser returns the login of the authenticated account.
func (h *HTTP) CurrentUser(ctx context.Context) (string, error) {
	var u struct {
		Login string `json:"login"`
	}
	resp, _, err := h.do(ctx, http.MethodGet, "/user", nil, &u)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET /user: HTTP %d", resp.StatusCode)
	}
	if u.Login == "" {
		return "", fmt.Errorf("GET /user: response has no login field")
	}
	return u.Login, nil
}

// RepoExists reports whether owner/name exists.
func (h *HTTP) RepoExists(ctx context.Context, owner, name string) (bool, error) {
	resp, _, err := h.do(ctx, http.MethodGet, "/repos/"+owner+"/"+name, nil, nil)
	if err != nil {
		return false, err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("GET /repos/%s/%s: HTTP %d", owner, name, resp.StatusCode)
	}
}

// CreateRepo creates a repository under the authenticated account.
func (h *HTTP) CreateRepo(ctx context.Context, name string, private bool) error {
	resp, data, err := h.do(ctx, http.MethodPost, "/user/repos",
		map[string]any{"name": name, "private": private}, nil)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound {
		return &CreateError{
			StatusCode:          resp.StatusCode,
			AcceptedPermissions: resp.Header.Get("X-Accepted-GitHub-Permissions"),
			Body:                string(data),
		}
	}
	return fmt.Errorf("POST /user/repos: HTTP %d", resp.StatusCode)
}
