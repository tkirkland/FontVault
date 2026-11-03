package app

// Package repo content reads the repository's own files without a checkout.
//
// The workdir is sparse: only manifest/ is in the working tree, so a family
// archive under fonts/ has to be fetched (git cat-file piped through git-lfs
// smudge) before it can be read or edited.

import (
	"context"
	"fmt"

	"fontvault/internal/fonts"
	"fontvault/internal/workdir"
)

// repoFetch returns the content of a repository path without checking it
// out: cat-file the blob and let LFS smudge it.
func (a *App) repoFetch(ctx context.Context, st *runState, repoPath string) ([]byte, error) {
	blob, err := a.git.Pipe(ctx, st.workdir.Dir, nil,
		[]string{"cat-file", "blob", "HEAD:" + repoPath},
		[]string{"lfs", "smudge", repoPath})
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", repoPath, err)
	}
	return blob, nil
}

// displayFont names where a local font would sit in the repository: its
// family archive, then the member name it would be stored under. Dry-run
// output shows both, because the archive path alone does not identify one
// font.
func displayFont(f *fonts.Font) string {
	return f.RepoPath() + "#" + f.MemberName()
}

// displayEntry names where a font sits in the repository: its family archive,
// then the member stored inside it. Reports, prompts and dry-run output show
// both, because the archive path alone does not identify one font.
func displayEntry(m *workdir.Manifest) string {
	if m.Member == "" {
		return m.RepoPath
	}
	return m.RepoPath + "#" + m.Member
}
