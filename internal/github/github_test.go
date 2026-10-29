package github

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCurrentUser(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("auth = %q", got)
		}
		if !strings.Contains(r.Header.Get("User-Agent"), "fontvault") {
			t.Errorf("user-agent = %q", r.Header.Get("User-Agent"))
		}
		w.Write([]byte(`{"login":"octocat"}`))
	}))
	defer srv.Close()
	c := &HTTP{Token: "tok", BaseURL: srv.URL, Client: srv.Client()}
	login, err := c.CurrentUser(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if login != "octocat" {
		t.Errorf("login = %q", login)
	}
}

func TestRepoExists(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/alice/fontvault" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c := &HTTP{Token: "tok", BaseURL: srv.URL, Client: srv.Client()}
	ok, err := c.RepoExists(context.Background(), "alice", "fontvault")
	if err != nil || !ok {
		t.Fatalf("exists = %v, err = %v", ok, err)
	}
	ok, err = c.RepoExists(context.Background(), "alice", "missing")
	if err != nil || ok {
		t.Fatalf("missing: exists = %v, err = %v", ok, err)
	}
}

func TestCreateRepo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/user/repos" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"name":"fontvault"}`))
	}))
	defer srv.Close()
	c := &HTTP{Token: "tok", BaseURL: srv.URL, Client: srv.Client()}
	if err := c.CreateRepo(context.Background(), "fontvault", false); err != nil {
		t.Fatal(err)
	}
}

func TestCreateRepoPermissionError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Accepted-GitHub-Permissions", "contents=read")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"message":"Resource not accessible by integration"}`))
	}))
	defer srv.Close()
	c := &HTTP{Token: "tok", BaseURL: srv.URL, Client: srv.Client()}
	err := c.CreateRepo(context.Background(), "fontvault", false)
	var ce *CreateError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v, want CreateError", err)
	}
	if ce.AcceptedPermissions != "contents=read" {
		t.Errorf("permissions = %q", ce.AcceptedPermissions)
	}
	if ce.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d", ce.StatusCode)
	}
}
