package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// DefaultBranch is used when neither the CLI nor the cfg names one.
	DefaultBranch = "main"
	// DefaultTokenEnv is the environment variable consulted for the token.
	DefaultTokenEnv = "GITHUB_TOKEN"
)

// ResolveBranch: CLI switch, then cfg, then default.
func ResolveBranch(cliBranch, cfgBranch string) string {
	if cliBranch != "" {
		return cliBranch
	}
	if cfgBranch != "" {
		return cfgBranch
	}
	return DefaultBranch
}

// ResolveTokenEnv: CLI switch, then cfg, then default.
func ResolveTokenEnv(cliTokenEnv, cfgTokenEnv string) string {
	if cliTokenEnv != "" {
		return cliTokenEnv
	}
	if cfgTokenEnv != "" {
		return cfgTokenEnv
	}
	return DefaultTokenEnv
}

// ResolveExcludeFile: CLI switch, then cfg, then none.
func ResolveExcludeFile(cliExclude, cfgExclude string) string {
	if cliExclude != "" {
		return cliExclude
	}
	return cfgExclude
}

// ResolveWorkdir: CLI switch, then cfg, then the login-based default under
// the user cache directory.
func ResolveWorkdir(cliWorkdir, cfgWorkdir, login string) (string, error) {
	if cliWorkdir != "" {
		return cliWorkdir, nil
	}
	if cfgWorkdir != "" {
		return cfgWorkdir, nil
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("resolve user cache dir for workdir: %w", err)
	}
	return filepath.Join(cache, "fontvault", login+"_fontvault"), nil
}

// TokenSourceEnv returns the label used for an env-var token source.
func TokenSourceEnv(name string) string { return "env " + name }

// ResolveToken applies the documented order:
// 1. -t/--token switch, 2. env var named by token_env, 3. cfg, 4. gh.
// env is a lookup function; ghToken runs `gh auth token`. If every source
// is empty or unavailable, an error listing all four sources is returned.
func ResolveToken(switchToken, tokenEnvName string, env func(string) (string, bool), cfgToken string, ghToken func() (string, error)) (token, source string, err error) {
	if switchToken != "" {
		return switchToken, "switch", nil
	}
	if tokenEnvName != "" {
		if v, ok := env(tokenEnvName); ok && strings.TrimSpace(v) != "" {
			return v, TokenSourceEnv(tokenEnvName), nil
		}
	}
	if cfgToken != "" {
		return cfgToken, "cfg", nil
	}
	if ghToken != nil {
		if v, err := ghToken(); err == nil && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v), "gh", nil
		}
	}
	return "", "", fmt.Errorf("no GitHub token found: set -t/--token, the %s environment variable (configurable with -e/--token-env), the token in %s, or authenticate gh (gh auth login)", tokenEnvName, "fontvault.cfg")
}

// PersistedFlags lists the switches that are written to the cfg when
// supplied on the command line.
var PersistedFlags = []string{"branch", "workdir", "token", "token-env", "exclude-file"}

// MergeForSave builds the cfg content to persist: existing cfg values are
// kept, and any persisted switch supplied on the command line overrides its
// field.
func MergeForSave(existing *File, supplied map[string]bool, cli BranchWorkdirToken, tokenEnvName string) *File {
	var out File
	if existing != nil {
		out = *existing
	}
	if supplied["branch"] {
		out.Branch = cli.Branch
	}
	if supplied["workdir"] {
		out.Workdir = cli.Workdir
	}
	if supplied["token"] {
		out.Token = cli.Token
	}
	if supplied["token-env"] {
		out.TokenEnv = tokenEnvName
	}
	if supplied["exclude-file"] {
		out.ExcludeFile = cli.ExcludeFile
	}
	out.FormatVersion = 1
	return &out
}

// BranchWorkdirToken carries CLI switch values used when persisting the cfg.
type BranchWorkdirToken struct {
	Branch      string
	Workdir     string
	Token       string
	ExcludeFile string
}

// AnySupplied reports whether any persisted switch was supplied on the
// command line.
func AnySupplied(supplied map[string]bool) bool {
	for _, f := range PersistedFlags {
		if supplied[f] {
			return true
		}
	}
	return false
}
