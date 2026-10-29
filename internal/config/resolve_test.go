package config

import (
	"errors"
	"strings"
	"testing"
)

func TestResolveBranch(t *testing.T) {
	if got := ResolveBranch("", ""); got != "main" {
		t.Errorf("default = %q", got)
	}
	if got := ResolveBranch("dev", "cfg"); got != "dev" {
		t.Errorf("cli wins: %q", got)
	}
	if got := ResolveBranch("", "cfg"); got != "cfg" {
		t.Errorf("cfg wins over default: %q", got)
	}
}

func TestResolveTokenEnv(t *testing.T) {
	if got := ResolveTokenEnv("", ""); got != "GITHUB_TOKEN" {
		t.Errorf("default = %q", got)
	}
	if got := ResolveTokenEnv("MY_TOKEN", "CFG_VAR"); got != "MY_TOKEN" {
		t.Errorf("cli wins: %q", got)
	}
	if got := ResolveTokenEnv("", "CFG_VAR"); got != "CFG_VAR" {
		t.Errorf("cfg wins over default: %q", got)
	}
}

func TestResolveExcludeFile(t *testing.T) {
	if got := ResolveExcludeFile("", ""); got != "" {
		t.Errorf("default = %q", got)
	}
	if got := ResolveExcludeFile("cli.txt", "cfg.txt"); got != "cli.txt" {
		t.Errorf("cli wins: %q", got)
	}
	if got := ResolveExcludeFile("", "cfg.txt"); got != "cfg.txt" {
		t.Errorf("cfg wins: %q", got)
	}
}

func envFrom(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func TestResolveTokenOrder(t *testing.T) {
	ghToken := func() (string, error) { return "gh-token", nil }
	ghFail := func() (string, error) { return "", errors.New("gh not authenticated") }

	tests := []struct {
		name       string
		switchTok  string
		envName    string
		env        map[string]string
		cfgToken   string
		gh         func() (string, error)
		want       string
		wantSource string
		wantErr    bool
	}{
		{"switch wins", "cli-token", "GITHUB_TOKEN", map[string]string{"GITHUB_TOKEN": "env-token"}, "cfg-token", ghToken, "cli-token", "switch", false},
		{"env second", "", "GITHUB_TOKEN", map[string]string{"GITHUB_TOKEN": "env-token"}, "cfg-token", ghToken, "env-token", "env GITHUB_TOKEN", false},
		{"cfg third", "", "GITHUB_TOKEN", nil, "cfg-token", ghToken, "cfg-token", "cfg", false},
		{"gh fourth", "", "GITHUB_TOKEN", nil, "", ghToken, "gh-token", "gh", false},
		{"gh failure skipped", "", "GITHUB_TOKEN", map[string]string{"GITHUB_TOKEN": "env-token"}, "", ghFail, "env-token", "env GITHUB_TOKEN", false},
		{"all sources empty fails", "", "GITHUB_TOKEN", nil, "", ghFail, "", "", true},
		{"custom env name", "", "MY_TOKEN", map[string]string{"MY_TOKEN": "mine"}, "", ghToken, "mine", "env MY_TOKEN", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, src, err := ResolveToken(tt.switchTok, tt.envName, envFrom(tt.env), tt.cfgToken, tt.gh)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				for _, src := range []string{"-t/--token", "environment variable", "fontvault.cfg", "gh auth login"} {
					if !strings.Contains(err.Error(), src) {
						t.Errorf("error does not mention %q: %v", src, err)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want || src != tt.wantSource {
				t.Errorf("got (%q, %q), want (%q, %q)", got, src, tt.want, tt.wantSource)
			}
		})
	}
}

func TestMergeForSave(t *testing.T) {
	existing := &File{Branch: "old", Workdir: "/old", Token: "old-token", TokenEnv: "OLD_ENV", ExcludeFile: "/old.txt"}
	cli := BranchWorkdirToken{Branch: "new", Workdir: "/new", Token: "new-token", ExcludeFile: "/new.txt"}
	supplied := map[string]bool{"branch": true, "token": true}

	out := MergeForSave(existing, supplied, cli, "NEW_ENV")
	if out.Branch != "new" || out.Token != "new-token" {
		t.Errorf("supplied values not applied: %+v", out)
	}
	// Unsupplied values keep their cfg value.
	if out.Workdir != "/old" || out.TokenEnv != "OLD_ENV" || out.ExcludeFile != "/old.txt" {
		t.Errorf("unsupplied values must keep cfg values: %+v", out)
	}
	if out.FormatVersion != 1 {
		t.Errorf("format version = %d", out.FormatVersion)
	}
	// Nil existing: only supplied values are set.
	out = MergeForSave(nil, map[string]bool{"branch": true}, cli, "")
	if out.Branch != "new" || out.Workdir != "" || out.Token != "" {
		t.Errorf("merge with nil existing: %+v", out)
	}
	if !AnySupplied(map[string]bool{"workdir": true}) || AnySupplied(nil) {
		t.Error("AnySupplied wrong")
	}
}
