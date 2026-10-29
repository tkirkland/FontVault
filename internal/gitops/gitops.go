// Package gitops runs git and git-lfs commands on behalf of fontvault.
//
// The GitHub token is supplied to every invocation via
// `-c http.extraHeader=AUTHORIZATION: basic <base64>`. It is never placed in
// a URL, in git config, or on disk, and it is redacted from logs and error
// text.
package gitops

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
)

// Runner executes git/git-lfs commands in a workdir.
type Runner struct {
	Token string
	Log   *slog.Logger
}

// New returns a Runner. A nil logger discards debug output.
func New(token string, log *slog.Logger) *Runner {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Runner{Token: token, Log: log}
}

// LookPath verifies that a tool is on PATH.
func LookPath(name string) error {
	_, err := exec.LookPath(name)
	if err != nil {
		return fmt.Errorf("%s is not on PATH: %w", name, err)
	}
	return nil
}

// authArgs returns the per-invocation auth config. With no token the args
// are empty (used by tests against local remotes).
func (r *Runner) authArgs() []string {
	if r.Token == "" {
		return nil
	}
	creds := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + r.Token))
	return []string{"-c", "http.extraHeader=AUTHORIZATION: basic " + creds}
}

// baseEnv returns the environment for git subprocesses: non-interactive,
// C-locale output for stable parsing.
func baseEnv() []string {
	return append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"LC_ALL=C",
		"LANG=C",
	)
}

func withEnv(extra ...string) []string {
	env := baseEnv()
	return append(env, extra...)
}

// redact removes the token and its base64 form from text.
func (r *Runner) redact(s string) string {
	if r.Token == "" {
		return s
	}
	s = strings.ReplaceAll(s, r.Token, "<redacted>")
	creds := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + r.Token))
	s = strings.ReplaceAll(s, creds, "<redacted>")
	return s
}

func (r *Runner) logArgs(args []string) string {
	redacted := make([]string, len(args))
	for i, a := range args {
		if strings.HasPrefix(a, "http.extraHeader=") || strings.HasPrefix(a, "AUTHORIZATION:") {
			redacted[i] = "http.extraHeader=<redacted>"
		} else {
			redacted[i] = r.redact(a)
		}
	}
	return strings.Join(redacted, " ")
}

// Run executes git with args in dir and returns trimmed stdout.
func (r *Runner) Run(ctx context.Context, dir string, args ...string) (string, error) {
	return r.run(ctx, dir, nil, nil, args...)
}

// RunEnv executes git with extra environment variables (e.g.
// GIT_LFS_SKIP_SMUDGE=1) in dir.
func (r *Runner) RunEnv(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	return r.run(ctx, dir, env, nil, args...)
}

func (r *Runner) run(ctx context.Context, dir string, env []string, stdin []byte, args ...string) (string, error) {
	full := append(r.authArgs(), args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = dir
	cmd.Env = withEnv(env...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	r.Log.Debug("git", "args", r.logArgs(full), "dir", dir)
	err := cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(r.redact(stderr.String()))
		if ctx.Err() != nil {
			return "", fmt.Errorf("git %s: %w", r.logArgs(args), ctx.Err())
		}
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %w: %s", r.logArgs(args), err, msg)
	}
	return strings.TrimRight(stdout.String(), "\r\n"), nil
}

// Pipe streams stdout of git cmd1 into git cmd2 and returns cmd2's stdout.
// It is used for `git cat-file blob <ref>:<path> | git lfs smudge <path>`.
func (r *Runner) Pipe(ctx context.Context, dir string, env []string, args1, args2 []string) ([]byte, error) {
	cmd1 := exec.CommandContext(ctx, "git", append(r.authArgs(), args1...)...)
	cmd1.Dir = dir
	cmd1.Env = withEnv(env...)
	var stderr1 bytes.Buffer
	cmd1.Stderr = &stderr1

	cmd2 := exec.CommandContext(ctx, "git", append(r.authArgs(), args2...)...)
	cmd2.Dir = dir
	cmd2.Env = withEnv(env...)
	var stderr2 bytes.Buffer
	cmd2.Stderr = &stderr2

	pipe, err := cmd1.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("pipe git %s: %w", r.logArgs(args1), err)
	}
	cmd2.Stdin = pipe
	var stdout2 bytes.Buffer
	cmd2.Stdout = &stdout2

	r.Log.Debug("git", "args", r.logArgs(args1), "dir", dir)
	r.Log.Debug("git", "args", r.logArgs(args2), "dir", dir)
	if err := cmd1.Start(); err != nil {
		return nil, fmt.Errorf("start git %s: %w", r.logArgs(args1), err)
	}
	if err := cmd2.Start(); err != nil {
		cmd1.Wait()
		return nil, fmt.Errorf("start git %s: %w", r.logArgs(args2), err)
	}
	err1 := cmd1.Wait()
	err2 := cmd2.Wait()
	if err1 != nil {
		return nil, fmt.Errorf("git %s: %w: %s", r.logArgs(args1), err1,
			strings.TrimSpace(r.redact(stderr1.String())))
	}
	if err2 != nil {
		return nil, fmt.Errorf("git %s: %w: %s", r.logArgs(args2), err2,
			strings.TrimSpace(r.redact(stderr2.String())))
	}
	return stdout2.Bytes(), nil
}

// RunStdin runs git with the given stdin (used for `git commit -F -`).
func (r *Runner) RunStdin(ctx context.Context, dir string, stdin []byte, args ...string) (string, error) {
	return r.run(ctx, dir, nil, stdin, args...)
}

// ---- higher level helpers -------------------------------------------------

// LFSInstall runs `git lfs install --skip-repo` (preflight).
func (r *Runner) LFSInstall(ctx context.Context) (string, error) {
	return r.Run(ctx, "", "lfs", "install", "--skip-repo")
}

// CloneSparse clones a repo without smudging LFS files.
func (r *Runner) CloneSparse(ctx context.Context, url, dir string) (string, error) {
	return r.RunEnv(ctx, "", []string{"GIT_LFS_SKIP_SMUDGE=1"},
		"clone", "--sparse", "--", url, dir)
}

// CloneSparseEmptyError reports whether err indicates the remote repository
// has no commits (an empty repository cannot be cloned).
func CloneSparseEmptyError(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "empty")
}

// SparseSet limits the checkout to the manifest directory (cone mode).
func (r *Runner) SparseSet(ctx context.Context, dir string) (string, error) {
	return r.Run(ctx, dir, "sparse-checkout", "set", "manifest")
}

// SparseInitCone initializes sparse checkout in cone mode (fresh init).
func (r *Runner) SparseInitCone(ctx context.Context, dir string) (string, error) {
	return r.Run(ctx, dir, "sparse-checkout", "init", "--cone")
}

// SparseReapply reapplies the sparse patterns, removing out-of-cone files
// from the working tree.
func (r *Runner) SparseReapply(ctx context.Context, dir string) (string, error) {
	return r.Run(ctx, dir, "sparse-checkout", "reapply")
}

// Init creates a new repository.
func (r *Runner) Init(ctx context.Context, dir, defaultBranch string) (string, error) {
	return r.Run(ctx, dir, "init", "-b", defaultBranch)
}

// RemoteAdd adds the origin remote.
func (r *Runner) RemoteAdd(ctx context.Context, dir, url string) (string, error) {
	return r.Run(ctx, dir, "remote", "add", "origin", url)
}

// RemoteGetURL returns the origin remote URL.
func (r *Runner) RemoteGetURL(ctx context.Context, dir string) (string, error) {
	return r.Run(ctx, dir, "remote", "get-url", "origin")
}

// Fetch fetches the named branch from origin.
func (r *Runner) Fetch(ctx context.Context, dir, branch string) (string, error) {
	return r.Run(ctx, dir, "fetch", "origin", branch)
}

// FetchAll fetches all refs from origin.
func (r *Runner) FetchAll(ctx context.Context, dir string) (string, error) {
	return r.Run(ctx, dir, "fetch", "origin")
}

// CheckoutBranch creates (or resets) the local branch at origin/<branch>.
func (r *Runner) CheckoutBranch(ctx context.Context, dir, branch string) (string, error) {
	return r.Run(ctx, dir, "checkout", "-B", branch, "origin/"+branch)
}

// MergeFF fast-forwards the current branch to origin/<branch>.
func (r *Runner) MergeFF(ctx context.Context, dir, branch string) (string, error) {
	return r.Run(ctx, dir, "merge", "--ff-only", "origin/"+branch)
}

// RevParse returns the HEAD commit, or an error if HEAD is unborn.
func (r *Runner) RevParse(ctx context.Context, dir string) (string, error) {
	out, err := r.Run(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// CurrentBranch returns the current branch name, or an error on a detached
// or unborn HEAD.
func (r *Runner) CurrentBranch(ctx context.Context, dir string) (string, error) {
	out, err := r.Run(ctx, dir, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// LsRemote returns the remote ref value for refs/heads/<branch> ("" when the
// remote has no such branch).
func (r *Runner) LsRemote(ctx context.Context, dir, branch string) (string, error) {
	out, err := r.Run(ctx, dir, "ls-remote", "origin", "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(out)
	if len(fields) >= 1 {
		return fields[0], nil
	}
	return "", nil
}

// RemoteHasBranch reports whether origin has the branch.
func (r *Runner) RemoteHasBranch(ctx context.Context, dir, branch string) (bool, error) {
	sha, err := r.LsRemote(ctx, dir, branch)
	if err != nil {
		return false, err
	}
	return sha != "", nil
}

// StatusPorcelain returns `git status --porcelain` output lines.
func (r *Runner) StatusPorcelain(ctx context.Context, dir string) ([]string, error) {
	out, err := r.Run(ctx, dir, "status", "--porcelain", "--untracked-files=normal")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// AddSparse stages paths that may be outside the sparse cone.
func (r *Runner) AddSparse(ctx context.Context, dir string, paths ...string) (string, error) {
	args := append([]string{"add", "--sparse", "--"}, paths...)
	return r.Run(ctx, dir, args...)
}

// Commit commits the index with the message on stdin.
func (r *Runner) Commit(ctx context.Context, dir string, message []byte) (string, error) {
	return r.RunStdin(ctx, dir, message, "commit", "-F", "-")
}

// ConfigGet reads a git config value ("" when unset).
func (r *Runner) ConfigGet(ctx context.Context, dir, key string) (string, error) {
	out, err := r.Run(ctx, dir, "config", "--get", key)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// Push pushes the branch to origin.
func (r *Runner) Push(ctx context.Context, dir, branch string) (string, error) {
	return r.Run(ctx, dir, "push", "origin", branch)
}

// LFSPrune removes local LFS objects no longer needed after a successful
// push/pull.
func (r *Runner) LFSPrune(ctx context.Context, dir string) (string, error) {
	return r.Run(ctx, dir, "lfs", "prune", "--verify-remote")
}

// GCAuto runs git maintenance opportunistically.
func (r *Runner) GCAuto(ctx context.Context, dir string) (string, error) {
	return r.Run(ctx, dir, "gc", "--auto")
}

// LFSPush uploads LFS objects for the branch before the ref is pushed.
func (r *Runner) LFSPush(ctx context.Context, dir, branch string) (string, error) {
	return r.Run(ctx, dir, "lfs", "push", "origin", branch)
}

// Rebase replays local commits onto origin/<branch> after a non-fast-forward
// push rejection.
func (r *Runner) Rebase(ctx context.Context, dir, branch string) (string, error) {
	return r.Run(ctx, dir, "rebase", "origin/"+branch)
}

// CatFileBlob prints the byte-exact content of <ref>:<path>.
func (r *Runner) CatFileBlob(ctx context.Context, dir, ref, path string) ([]byte, error) {
	return r.rawOutput(ctx, dir, "cat-file", "blob", ref+":"+path)
}

// CatFileIndexBlob prints the byte-exact staged (index) content of path.
func (r *Runner) CatFileIndexBlob(ctx context.Context, dir, path string) ([]byte, error) {
	return r.rawOutput(ctx, dir, "cat-file", "blob", ":"+path)
}

// rawOutput runs git and returns stdout bytes untouched (no trailing
// newline trimming), which matters for blob content.
func (r *Runner) rawOutput(ctx context.Context, dir string, args ...string) ([]byte, error) {
	full := append(r.authArgs(), args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = dir
	cmd.Env = withEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	r.Log.Debug("git", "args", r.logArgs(full), "dir", dir)
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", r.logArgs(args), err,
			strings.TrimSpace(r.redact(stderr.String())))
	}
	return stdout.Bytes(), nil
}

// LsTree lists files under a path in the branch.
func (r *Runner) LsTree(ctx context.Context, dir, branch, path string) ([]string, error) {
	out, err := r.Run(ctx, dir, "ls-tree", "-r", "--name-only", branch, "--", path)
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// PathInBranch reports whether a path exists in the branch tree.
func (r *Runner) PathInBranch(ctx context.Context, dir, branch, path string) (bool, error) {
	_, err := r.Run(ctx, dir, "cat-file", "-e", branch+":"+path)
	if err == nil {
		return true, nil
	}
	if strings.Contains(err.Error(), "Not a valid object name") ||
		strings.Contains(err.Error(), "does not exist") ||
		strings.Contains(err.Error(), "pathspec") {
		return false, nil
	}
	return false, err
}

// StagedFontPaths lists staged files under fonts/ with a .ttf or .otf
// extension.
func (r *Runner) StagedFontPaths(ctx context.Context, dir string) ([]string, error) {
	out, err := r.Run(ctx, dir, "diff", "--cached", "--name-only", "--", "fonts")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	var fonts []string
	for _, p := range strings.Split(out, "\n") {
		lp := strings.ToLower(p)
		if strings.HasSuffix(lp, ".ttf") || strings.HasSuffix(lp, ".otf") {
			fonts = append(fonts, p)
		}
	}
	return fonts, nil
}

// LFSStatus returns `git lfs status --porcelain` output lines.
func (r *Runner) LFSStatus(ctx context.Context, dir string) ([]string, error) {
	out, err := r.Run(ctx, dir, "lfs", "status", "--porcelain")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// Reset unstages everything without touching working tree files.
func (r *Runner) Reset(ctx context.Context, dir string) (string, error) {
	return r.Run(ctx, dir, "reset", "-q")
}

// IsLFSPointer reports whether blob bytes are a Git LFS pointer whose
// sha256 oid matches expectedSHA (a 64-hex string).
func IsLFSPointer(blob []byte, expectedSHA string) bool {
	text := string(blob)
	lines := strings.SplitN(strings.TrimSuffix(text, "\n"), "\n", 3)
	if len(lines) < 2 || lines[0] != "version https://git-lfs.github.com/spec/v1" {
		return false
	}
	oidLine := ""
	for _, l := range lines[1:] {
		if strings.HasPrefix(l, "oid ") {
			oidLine = l
			break
		}
	}
	if !strings.HasPrefix(oidLine, "oid sha256:") {
		return false
	}
	oid := strings.TrimPrefix(oidLine, "oid sha256:")
	if len(oid) != 64 {
		return false
	}
	for _, c := range oid {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return strings.EqualFold(oid, expectedSHA)
}
