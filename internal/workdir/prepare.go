package workdir

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"fontvault/internal/gitops"
	"fontvault/internal/lockfile"
)

// GitAttributesContent is the canonical .gitattributes routing .ttf and
// .otf files through Git LFS.
const GitAttributesContent = "*.ttf filter=lfs diff=lfs merge=lfs -text\n" +
	"*.otf filter=lfs diff=lfs merge=lfs -text\n"

// GitAttributesComplete reports whether content routes both extensions
// through LFS.
func GitAttributesComplete(content string) bool {
	has := func(ext string) bool {
		for _, line := range strings.Split(content, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "#") || line == "" {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			if strings.EqualFold(fields[0], ext) {
				joined := strings.Join(fields[1:], " ")
				if strings.Contains(joined, "filter=lfs") && strings.Contains(joined, "diff=lfs") &&
					strings.Contains(joined, "merge=lfs") && strings.Contains(joined, "-text") {
					return true
				}
			}
		}
		return false
	}
	return has("*.ttf") && has("*.otf")
}

// Manager prepares and inspects the workdir.
type Manager struct {
	Git *gitops.Runner
	Log *slog.Logger
}

func NewManager(g *gitops.Runner, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Manager{Git: g, Log: log}
}

// State describes a prepared workdir.
type State struct {
	Dir       string
	Branch    string
	RemoteURL string
	// BaseSHA is the commit the run compares against ("" when the branch
	// is unborn or the repository is empty).
	BaseSHA string
}

// EnsureReady makes the workdir exist and be on the requested branch,
// fetched and fast-forwarded, with the sparse cone limited to manifest/.
// The lock is acquired inside: cloning cannot tolerate an existing lock
// file in the destination, so a fresh clone happens first and is then
// locked; for an existing workdir the lock is taken first.
func (m *Manager) EnsureReady(ctx context.Context, dir, remoteURL, branch string) (*State, *lockfile.Lock, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, nil, fmt.Errorf("create workdir %s: %w", dir, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		lock, err := lockfile.Acquire(dir)
		if err != nil {
			return nil, nil, err
		}
		state, err := m.prepareExisting(ctx, dir, remoteURL, branch)
		if err != nil {
			lock.Release()
			return nil, nil, err
		}
		return state, lock, nil
	}

	// No repository yet. A fresh workdir must be empty for git clone.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("read workdir %s: %w", dir, err)
	}
	empty := true
	for _, e := range entries {
		// A leftover lock file from a crashed run is cleaned up rather than
		// blocking the clone.
		if e.Name() == ".fontvault.lock" {
			if pid, perr := lockfile.PID(filepath.Join(dir, e.Name())); perr == nil && pid > 0 && lockfile.ProcessAlive(pid) {
				lock, lerr := lockfile.Acquire(dir)
				if lerr != nil {
					return nil, nil, lerr
				}
				state, serr := m.prepareExisting(ctx, dir, remoteURL, branch)
				if serr != nil {
					lock.Release()
					return nil, nil, serr
				}
				return state, lock, nil
			}
			os.Remove(filepath.Join(dir, e.Name()))
			continue
		}
		empty = false
	}
	if !empty {
		return nil, nil, fmt.Errorf("workdir %s exists but is not a git repository and is not empty", dir)
	}

	if _, err := m.Git.CloneSparse(ctx, remoteURL, dir); err != nil {
		if gitops.CloneSparseEmptyError(err) {
			// The remote repository has no commits: initialize it locally.
			lock, lerr := lockfile.Acquire(dir)
			if lerr != nil {
				return nil, nil, lerr
			}
			state, serr := m.initEmpty(ctx, dir, remoteURL, branch)
			if serr != nil {
				lock.Release()
				return nil, nil, serr
			}
			return state, lock, nil
		}
		if strings.Contains(err.Error(), "not an empty directory") {
			// Another instance cloned concurrently; retry the existing
			// workdir path.
			lock, lerr := lockfile.Acquire(dir)
			if lerr != nil {
				return nil, nil, lerr
			}
			state, serr := m.prepareExisting(ctx, dir, remoteURL, branch)
			if serr != nil {
				lock.Release()
				return nil, nil, serr
			}
			return state, lock, nil
		}
		return nil, nil, fmt.Errorf("clone %s into %s: %w", remoteURL, dir, err)
	}

	lock, err := lockfile.Acquire(dir)
	if err != nil {
		return nil, nil, err
	}
	state, err := m.prepareExisting(ctx, dir, remoteURL, branch)
	if err != nil {
		lock.Release()
		return nil, nil, err
	}
	return state, lock, nil
}

// InitEmptyRepo initializes an unborn branch for a push into an empty
// repository. It is idempotent: a clone of an empty repository already has
// .git and an origin remote.
func (m *Manager) InitEmptyRepo(ctx context.Context, dir, remoteURL, branch string) error {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		if _, err := m.Git.Init(ctx, dir, branch); err != nil {
			return fmt.Errorf("init repository in %s: %w", dir, err)
		}
	}
	if got, err := m.Git.RemoteGetURL(ctx, dir); err == nil {
		if got != remoteURL {
			if _, err := m.Git.Run(ctx, dir, "remote", "set-url", "origin", remoteURL); err != nil {
				return fmt.Errorf("set origin remote to %s: %w", remoteURL, err)
			}
		}
	} else {
		if _, err := m.Git.RemoteAdd(ctx, dir, remoteURL); err != nil {
			return fmt.Errorf("add remote %s: %w", remoteURL, err)
		}
	}
	// An unborn repository (fresh init or a clone of an empty remote) may
	// have HEAD pointing at a different default branch name; point it at
	// the requested branch before the first commit.
	if _, err := m.Git.Run(ctx, dir, "symbolic-ref", "HEAD", "refs/heads/"+branch); err != nil {
		return fmt.Errorf("set HEAD to refs/heads/%s: %w", branch, err)
	}
	return m.applySparse(ctx, dir, true)
}

func (m *Manager) initEmpty(ctx context.Context, dir, remoteURL, branch string) (*State, error) {
	if err := m.InitEmptyRepo(ctx, dir, remoteURL, branch); err != nil {
		return nil, err
	}
	return &State{Dir: dir, Branch: branch, RemoteURL: remoteURL, BaseSHA: ""}, nil
}

func (m *Manager) applySparse(ctx context.Context, dir string, initCone bool) error {
	if initCone {
		if _, err := m.Git.SparseInitCone(ctx, dir); err != nil {
			return fmt.Errorf("init sparse checkout: %w", err)
		}
	}
	if _, err := m.Git.SparseSet(ctx, dir); err != nil {
		return fmt.Errorf("set sparse checkout to manifest: %w", err)
	}
	return nil
}

// prepareExisting verifies and fast-forwards an existing repository. The
// caller must hold the lock.
func (m *Manager) prepareExisting(ctx context.Context, dir, remoteURL, branch string) (*State, error) {
	got, err := m.Git.RemoteGetURL(ctx, dir)
	if err != nil {
		return nil, fmt.Errorf("read origin remote of workdir %s: %w", dir, err)
	}
	if got != remoteURL {
		return nil, fmt.Errorf("workdir %s has remote %q, expected %q", dir, got, remoteURL)
	}

	// The lock file is untracked by design; ignore it in the dirty check.
	status, err := m.Git.StatusPorcelain(ctx, dir)
	if err != nil {
		return nil, fmt.Errorf("check workdir status: %w", err)
	}
	for _, line := range status {
		if line == "" {
			continue
		}
		if strings.HasSuffix(strings.TrimSpace(line), ".fontvault.lock") {
			continue
		}
		return nil, fmt.Errorf("workdir %s is dirty (%s); commit or revert local changes and retry", dir, strings.TrimSpace(line))
	}

	// Fetch all refs: works for empty repositories (nothing to fetch) and
	// updates origin/<branch> for the fast-forward below.
	if _, err := m.Git.FetchAll(ctx, dir); err != nil {
		return nil, fmt.Errorf("fetch origin in workdir %s: %w", dir, err)
	}

	head, headErr := m.Git.RevParse(ctx, dir)
	if headErr != nil {
		// Unborn HEAD: either an empty repository or an initialized-but-
		// empty branch.
		has, herr := m.Git.RemoteHasBranch(ctx, dir, branch)
		if herr != nil {
			return nil, fmt.Errorf("check remote branch %s: %w", branch, herr)
		}
		if !has {
			// Stay unborn: empty repository. Point HEAD at the requested
			// branch (a clone of an empty remote may default elsewhere)
			// and ensure the sparse cone is configured.
			if _, err := m.Git.Run(ctx, dir, "symbolic-ref", "HEAD", "refs/heads/"+branch); err != nil {
				return nil, fmt.Errorf("set HEAD to refs/heads/%s: %w", branch, err)
			}
			if err := m.applySparse(ctx, dir, false); err != nil {
				return nil, err
			}
			return &State{Dir: dir, Branch: branch, RemoteURL: remoteURL, BaseSHA: ""}, nil
		}
		if _, err := m.Git.CheckoutBranch(ctx, dir, branch); err != nil {
			return nil, fmt.Errorf("checkout %s: %w", branch, err)
		}
		if err := m.applySparse(ctx, dir, false); err != nil {
			return nil, err
		}
		head, err = m.Git.RevParse(ctx, dir)
		if err != nil {
			return nil, fmt.Errorf("resolve HEAD after checkout: %w", err)
		}
	} else {
		current, err := m.Git.CurrentBranch(ctx, dir)
		if err != nil {
			return nil, fmt.Errorf("resolve current branch: %w", err)
		}
		if current != branch {
			has, herr := m.Git.RemoteHasBranch(ctx, dir, branch)
			if herr != nil {
				return nil, fmt.Errorf("check remote branch %s: %w", branch, herr)
			}
			if !has {
				return nil, fmt.Errorf("branch %q does not exist on the remote repository", branch)
			}
			if _, err := m.Git.CheckoutBranch(ctx, dir, branch); err != nil {
				return nil, fmt.Errorf("switch to branch %s: %w", branch, err)
			}
		}
		if _, err := m.Git.MergeFF(ctx, dir, branch); err != nil {
			return nil, fmt.Errorf("fast-forward %s: workdir has diverged from the remote: %w", branch, err)
		}
		if err := m.applySparse(ctx, dir, false); err != nil {
			return nil, err
		}
		head, err = m.Git.RevParse(ctx, dir)
		if err != nil {
			return nil, fmt.Errorf("resolve HEAD: %w", err)
		}
	}

	return &State{Dir: dir, Branch: branch, RemoteURL: remoteURL, BaseSHA: head}, nil
}
