package app

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"fontvault/internal/fonts"
	"fontvault/internal/fsutil"
	"fontvault/internal/gitops"
	"fontvault/internal/report"
	"fontvault/internal/workdir"
)

const maxPushAttempts = 6 // initial push + up to 5 retries

type pushChoice int

const (
	pushChoiceReplace pushChoice = iota
	pushChoiceSkip
	pushChoiceUseRepo // sync only: the repo copy replaces the local copy in pull
)

// pushResult carries the outcome of the push phase. added/replaced/skipped
// are cumulative across rebase retries, keyed so a pass re-run cannot
// duplicate entries.
type pushResult struct {
	added        []*fonts.Font
	replaced     []*fonts.Font
	skipped      []report.Entry
	addedKeys    map[string]bool
	replacedKeys map[string]bool
	skippedKeys  map[string]bool
	errors_      []report.Entry
	newSHA       string
	useRepo      map[string]*workdir.Manifest
	answers      map[string]pushChoice
	reasons      map[string]string
}

// runPush archives local fonts into the repository.
func (a *App) runPush(ctx context.Context, st *runState) (int, *report.Report) {
	code, rep, _ := a.runPushPhase(ctx, st, false)
	return code, rep
}

// runPushPhase implements push (or the push phase of sync). In sync mode
// the conflict options include "use repo".
func (a *App) runPushPhase(ctx context.Context, st *runState, sync bool) (int, *report.Report, *pushResult) {
	rep := a.baseReport("push")
	if sync {
		rep.Header.Mode = "sync"
	}
	rep.Header.BaseSHA = st.workdir.BaseSHA

	res := &pushResult{
		useRepo:      map[string]*workdir.Manifest{},
		answers:      map[string]pushChoice{},
		reasons:      map[string]string{},
		addedKeys:    map[string]bool{},
		replacedKeys: map[string]bool{},
		skippedKeys:  map[string]bool{},
	}

	// .gitattributes first: fix it in its own commit before any font commit.
	if err := a.ensureGitAttributes(ctx, st); err != nil {
		a.cleanupAbortedPush(ctx, st)
		return a.fail("%v", err), nil, nil
	}

	errs := append([]report.Entry{}, st.manifestErrs...)
	errs = append(errs, scanErrorEntries(st.scanRes)...)
	errs = append(errs, st.sysScanErrs...)
	var excluded []report.Entry
	for _, d := range st.decisions {
		if d.Excluded {
			excluded = append(excluded, fontEntry(d.Font, d.Reason))
		}
	}
	excluded = append(excluded, st.notChosen...)

	apply := func() error { return a.applyPushFonts(ctx, st, sync, res) }
	if err := apply(); err != nil {
		a.cleanupAbortedPush(ctx, st)
		return a.fail("%v", err), nil, nil
	}
	if err := a.verifyStagedFonts(ctx, st, res); err != nil {
		a.cleanupAbortedPush(ctx, st)
		return a.fail("%v", err), nil, nil
	}
	if err := a.commitIfStaged(ctx, st, res); err != nil {
		a.cleanupAbortedPush(ctx, st)
		return a.fail("%v", err), nil, nil
	}
	pushed, err := a.pushWithRetries(ctx, st, res, apply)
	if err != nil {
		a.cleanupAbortedPush(ctx, st)
		return a.fail("%v", err), nil, nil
	}
	if pushed {
		sha, err := a.git.RevParse(ctx, st.workdir.Dir)
		if err != nil {
			a.cleanupAbortedPush(ctx, st)
			return a.fail("resolve new commit: %v", err), nil, nil
		}
		if sha != st.workdir.BaseSHA {
			res.newSHA = sha
		}
	}
	rep.Header.NewSHA = res.newSHA

	// Fonts leave the working tree.
	if _, err := a.git.SparseReapply(ctx, st.workdir.Dir); err != nil {
		a.Log.Warn("git sparse-checkout reapply", "error", err)
	}
	a.pushPhaseOK = true

	errs = append(errs, res.errors_...)
	skippedAuto := 0
	for _, e := range res.skipped {
		if isAutoSkipReason(e.Reason) {
			skippedAuto++
		}
	}
	rep.AddSection(report.SectionAdded, fontEntries(res.added))
	rep.AddSection(report.SectionReplacedRepo, fontEntries(res.replaced))
	rep.AddSection(report.SectionConflictsSkipped, res.skipped)
	rep.AddSection(report.SectionExcluded, excluded)
	rep.AddSection(report.SectionErrors, errs)
	rep.AddCountSection(report.SectionSkippedFormats, st.scanRes.Skipped)

	return finalCode(len(errs), skippedAuto), rep, res
}

func fontEntries(list []*fonts.Font) []report.Entry {
	out := make([]report.Entry, 0, len(list))
	for _, f := range list {
		out = append(out, fontEntry(f, ""))
	}
	return out
}

// cleanupAbortedPush leaves no staged font changes behind after a fatal
// abort and keeps the sparse worktree clean.
func (a *App) cleanupAbortedPush(ctx context.Context, st *runState) {
	if a.git == nil || st == nil || st.workdir == nil {
		return
	}
	if _, err := a.git.Reset(ctx, st.workdir.Dir); err != nil {
		a.Log.Warn("reset after abort", "error", err)
	}
	if _, err := a.git.SparseReapply(ctx, st.workdir.Dir); err != nil {
		a.Log.Warn("sparse reapply after abort", "error", err)
	}
}

// ensureGitAttributes writes the canonical .gitattributes (push only) and
// commits it alone before any font commit.
func (a *App) ensureGitAttributes(ctx context.Context, st *runState) error {
	path := filepath.Join(st.workdir.Dir, ".gitattributes")
	data, err := os.ReadFile(path)
	complete := err == nil && workdir.GitAttributesComplete(string(data))
	if complete {
		return nil
	}
	msg := "Configure Git LFS for font files"
	if st.workdir.BaseSHA == "" {
		msg = "Initialize fontvault repository with LFS configuration"
	}
	if a.Opts.DryRun {
		fmt.Fprintf(a.Stdout, "dry-run: would write .gitattributes and commit it (%s)\n", msg)
		return nil
	}
	if err := fsutil.AtomicWrite(path, []byte(workdir.GitAttributesContent), 0o644); err != nil {
		return fmt.Errorf("write .gitattributes in %s: %w", st.workdir.Dir, err)
	}
	if _, err := a.git.AddSparse(ctx, st.workdir.Dir, ".gitattributes"); err != nil {
		return fmt.Errorf("stage .gitattributes: %w", err)
	}
	if err := a.checkGitIdentity(ctx, st.workdir.Dir); err != nil {
		return err
	}
	if _, err := a.git.Commit(ctx, st.workdir.Dir, []byte(msg+"\n")); err != nil {
		return fmt.Errorf("commit .gitattributes: %w", err)
	}
	a.Log.Info("committed .gitattributes", "message", msg)
	return nil
}

// applyPushFonts runs one pass over the local fonts, writing new and
// replaced fonts and their manifests. Answered conflicts keep their answer
// across rebase retries; outcome entries are cumulative across passes.
func (a *App) applyPushFonts(ctx context.Context, st *runState, sync bool, res *pushResult) error {
	for _, f := range sortedReps(st.reps) {
		if ctx.Err() != nil {
			return fmt.Errorf("interrupted: %w", ctx.Err())
		}
		repo, exists := st.idx.ByKey[f.Key]
		switch {
		case !exists:
			if err := a.pushAdd(ctx, st, f, res); err != nil {
				return err
			}
		case repo.SHA256 == f.SHA256:
			// Identical: no-op.
		default:
			if err := a.pushConflict(ctx, st, sync, f, repo, res); err != nil {
				return err
			}
		}
	}
	return nil
}

// pushAdd writes a new font and its manifest.
func (a *App) pushAdd(ctx context.Context, st *runState, f *fonts.Font, res *pushResult) error {
	repoPath := f.RepoPath()
	manifestPath := f.ManifestPath()

	// Invariant: the repo path must not exist for a key that is not in the
	// index.
	exists, err := a.pathExists(ctx, st, repoPath)
	if err != nil {
		return fmt.Errorf("check repo path %s: %w", repoPath, err)
	}
	if exists {
		return fmt.Errorf("invariant violation: repo path %s exists but key %q has no manifest; refusing to overwrite", repoPath, f.Key)
	}

	if a.Opts.DryRun {
		fmt.Fprintf(a.Stdout, "dry-run: would add %s -> %s\n", f.Path, repoPath)
		if !res.addedKeys[f.Key] {
			res.addedKeys[f.Key] = true
			res.added = append(res.added, f)
		}
		return nil
	}
	content, err := os.ReadFile(f.Path)
	if err != nil {
		return fmt.Errorf("read %s: %w", f.Path, err)
	}
	full := filepath.Join(st.workdir.Dir, filepath.FromSlash(repoPath))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return fmt.Errorf("create repo dir for %s: %w", repoPath, err)
	}
	if err := fsutil.AtomicWrite(full, content, 0o644); err != nil {
		return fmt.Errorf("write repo file %s: %w", repoPath, err)
	}
	manifest := manifestForFont(f, repoPath)
	workdir.NewFromFont(manifest, a.Clock())
	if err := a.writeManifest(st, manifestPath, manifest); err != nil {
		return err
	}
	if _, err := a.git.AddSparse(ctx, st.workdir.Dir, filepath.FromSlash(repoPath), filepath.FromSlash(manifestPath)); err != nil {
		return fmt.Errorf("stage %s and %s: %w", repoPath, manifestPath, err)
	}
	if !res.addedKeys[f.Key] {
		res.addedKeys[f.Key] = true
		res.added = append(res.added, f)
	}
	a.Log.Info("staged new font", "path", repoPath)
	return nil
}

// pushConflict resolves a same-key/different-hash conflict.
func (a *App) pushConflict(ctx context.Context, st *runState, sync bool, f *fonts.Font, repo *workdir.Manifest, res *pushResult) error {
	choice, answered := res.answers[f.Key]
	if !answered {
		switch {
		case a.Opts.Yes:
			choice = pushChoiceSkip
			res.reasons[f.Key] = "-y"
			appendSkipped(res, f.Key, conflictEntry(f, repo, "-y"))
		default:
			fmt.Fprintf(a.Stdout,
				"Conflict: %s %s (key %s)\n  local: version %s | revision %s | %d bytes | sha %s | %s\n  repo:  version %s | revision %s | %d bytes | sha %s | %s\n",
				f.Family, f.Subfamily, f.Key,
				orDash(f.VersionString), orDash(f.FontRevision), f.Size, fonts.ShortHash(f.SHA256), f.Path,
				orDash(repo.VersionString), orDash(repo.FontRevision), repo.Size, fonts.ShortHash(repo.SHA256), repo.RepoPath)
			valid := map[string]string{"r": "r", "s": "s"}
			if sync {
				valid = map[string]string{"l": "l", "r": "r", "s": "s"}
			}
			answer, err := a.prompter.choice(a.conflictPrompt(sync), valid)
			if err != nil {
				a.Log.Warn("read conflict answer", "error", err)
				answer = "s"
			}
			switch answer {
			case "s":
				choice = pushChoiceSkip
				res.reasons[f.Key] = "user skip"
				appendSkipped(res, f.Key, conflictEntry(f, repo, "user skip"))
			case "r":
				if sync {
					// "use [r]epo": the repo copy replaces the local copy
					// during the pull phase.
					choice = pushChoiceUseRepo
				} else {
					choice = pushChoiceReplace
				}
			case "l":
				// "use [l]ocal": replace the repo copy.
				choice = pushChoiceReplace
			}
		}
		res.answers[f.Key] = choice
		if choice == pushChoiceSkip {
			return nil
		}
	}

	switch choice {
	case pushChoiceSkip:
		reason := res.reasons[f.Key]
		if reason == "" {
			reason = "user skip"
		}
		appendSkipped(res, f.Key, conflictEntry(f, repo, reason))
		return nil
	case pushChoiceUseRepo:
		// Sync only: the repo copy replaces the local copy in the pull
		// phase. The conflict is resolved, not skipped.
		res.useRepo[f.Key] = repo
		return nil
	case pushChoiceReplace:
		// Continue below.
	}

	if a.Opts.DryRun {
		fmt.Fprintf(a.Stdout, "dry-run: would replace %s -> %s\n", f.Path, repo.RepoPath)
		if !res.replacedKeys[f.Key] {
			res.replacedKeys[f.Key] = true
			res.replaced = append(res.replaced, f)
		}
		return nil
	}
	content, err := os.ReadFile(f.Path)
	if err != nil {
		return fmt.Errorf("read %s: %w", f.Path, err)
	}
	full := filepath.Join(st.workdir.Dir, filepath.FromSlash(repo.RepoPath))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return fmt.Errorf("create repo dir for %s: %w", repo.RepoPath, err)
	}
	if err := fsutil.AtomicWrite(full, content, 0o644); err != nil {
		return fmt.Errorf("write repo file %s: %w", repo.RepoPath, err)
	}
	manifest := manifestForFont(f, repo.RepoPath)
	manifest.FirstArchived = repo.FirstArchived
	workdir.UpdateContent(manifest, a.Clock())
	if err := a.writeManifest(st, f.ManifestPath(), manifest); err != nil {
		return err
	}
	if _, err := a.git.AddSparse(ctx, st.workdir.Dir, filepath.FromSlash(repo.RepoPath), filepath.FromSlash(f.ManifestPath())); err != nil {
		return fmt.Errorf("stage %s and %s: %w", repo.RepoPath, f.ManifestPath(), err)
	}
	st.idx.ByKey[f.Key] = manifest
	st.idx.ByPath[manifest.RepoPath] = manifest
	if !res.replacedKeys[f.Key] {
		res.replacedKeys[f.Key] = true
		res.replaced = append(res.replaced, f)
	}
	a.Log.Info("staged replacement", "path", repo.RepoPath)
	return nil
}

// appendSkipped records a skipped conflict exactly once per key.
func appendSkipped(res *pushResult, key string, entry report.Entry) {
	if res.skippedKeys[key] {
		return
	}
	res.skippedKeys[key] = true
	res.skipped = append(res.skipped, entry)
}

func (a *App) conflictPrompt(sync bool) string {
	if sync {
		return "Use [l]ocal (replace the repo copy) / use [r]epo (replace the local copy) / [s]kip: "
	}
	return "[r]eplace repo copy with local / [s]kip: "
}

// manifestForFont builds a manifest for a local font going to repoPath.
func manifestForFont(f *fonts.Font, repoPath string) *workdir.Manifest {
	return &workdir.Manifest{
		Key:            f.Key,
		PostScriptName: f.PostScriptName,
		VendorID:       f.VendorID,
		FullName:       f.FullName,
		Family:         f.Family,
		Subfamily:      f.Subfamily,
		VersionString:  f.VersionString,
		FontRevision:   f.FontRevision,
		Copyright:      f.Copyright,
		LicenseDesc:    f.LicenseDesc,
		LicenseURL:     f.LicenseURL,
		SHA256:         f.SHA256,
		Size:           f.Size,
		Ext:            f.Ext,
		RepoPath:       repoPath,
	}
}

// writeManifest writes manifest JSON (mkdir as needed).
func (a *App) writeManifest(st *runState, manifestPath string, m *workdir.Manifest) error {
	data, err := workdir.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal manifest for %s: %w", m.Key, err)
	}
	full := filepath.Join(st.workdir.Dir, filepath.FromSlash(manifestPath))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return fmt.Errorf("create manifest dir for %s: %w", manifestPath, err)
	}
	if err := fsutil.AtomicWrite(full, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write manifest %s: %w", manifestPath, err)
	}
	return nil
}

// pathExists checks the filesystem and the branch tree for a repo path.
func (a *App) pathExists(ctx context.Context, st *runState, repoPath string) (bool, error) {
	full := filepath.Join(st.workdir.Dir, filepath.FromSlash(repoPath))
	if _, err := os.Lstat(full); err == nil {
		return true, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if st.workdir.BaseSHA == "" {
		return false, nil
	}
	return a.git.PathInBranch(ctx, st.workdir.Dir, st.workdir.BaseSHA, repoPath)
}

// verifyStagedFonts confirms every staged font is an LFS pointer with the
// expected oid. Any raw blob aborts the run before committing.
func (a *App) verifyStagedFonts(ctx context.Context, st *runState, res *pushResult) error {
	if a.Opts.DryRun {
		return nil
	}
	staged, err := a.git.StagedFontPaths(ctx, st.workdir.Dir)
	if err != nil {
		return fmt.Errorf("list staged fonts: %w", err)
	}
	if len(staged) == 0 {
		return nil
	}
	expected := map[string]string{}
	for _, f := range res.added {
		expected[filepath.ToSlash(f.RepoPath())] = f.SHA256
	}
	for _, f := range res.replaced {
		expected[filepath.ToSlash(f.RepoPath())] = f.SHA256
	}
	if _, err := a.git.LFSStatus(ctx, st.workdir.Dir); err != nil {
		a.Log.Warn("git lfs status --porcelain", "error", err)
	}
	for _, p := range staged {
		blob, err := a.git.CatFileIndexBlob(ctx, st.workdir.Dir, p)
		if err != nil {
			return fmt.Errorf("read staged blob %s: %w", p, err)
		}
		want := expected[filepath.ToSlash(p)]
		if want == "" || !gitops.IsLFSPointer(blob, want) {
			return fmt.Errorf("pre-commit verification failed: staged font %s is not an LFS pointer for its content; aborting without committing", p)
		}
	}
	return nil
}

// commitIfStaged commits the index when anything is staged.
func (a *App) commitIfStaged(ctx context.Context, st *runState, res *pushResult) error {
	if a.Opts.DryRun {
		return nil
	}
	staged, err := a.git.StagedFontPaths(ctx, st.workdir.Dir)
	if err != nil {
		return fmt.Errorf("list staged files: %w", err)
	}
	attrs, err := a.git.Run(ctx, st.workdir.Dir, "diff", "--cached", "--name-only", "--", ".gitattributes")
	if err != nil {
		return fmt.Errorf("list staged .gitattributes: %w", err)
	}
	if len(staged) == 0 && strings.TrimSpace(attrs) == "" {
		return nil
	}
	if err := a.checkGitIdentity(ctx, st.workdir.Dir); err != nil {
		return err
	}
	msg := commitMessage(res.added, res.replaced)
	if _, err := a.git.Commit(ctx, st.workdir.Dir, msg); err != nil {
		return fmt.Errorf("commit fonts: %w", err)
	}
	a.Log.Info("committed fonts", "added", len(res.added), "replaced", len(res.replaced))
	return nil
}

// checkGitIdentity verifies that author/committer can come from git config.
func (a *App) checkGitIdentity(ctx context.Context, dir string) error {
	name, err := a.git.ConfigGet(ctx, dir, "user.name")
	if err != nil {
		return fmt.Errorf("read git config user.name: %w", err)
	}
	email, err := a.git.ConfigGet(ctx, dir, "user.email")
	if err != nil {
		return fmt.Errorf("read git config user.email: %w", err)
	}
	if name == "" || email == "" {
		return errors.New("git user.name and user.email are not configured; set them with: git config --global user.name <name> && git config --global user.email <email>")
	}
	return nil
}

// commitMessage builds the commit message: summary line with counts, body
// with the affected families.
func commitMessage(added, replaced []*fonts.Font) []byte {
	var parts []string
	if n := len(added); n > 0 {
		noun := "font"
		if n != 1 {
			noun = "fonts"
		}
		parts = append(parts, fmt.Sprintf("Add %d %s", n, noun))
	}
	if n := len(replaced); n > 0 {
		noun := "font"
		if n != 1 {
			noun = "fonts"
		}
		parts = append(parts, fmt.Sprintf("replace %d %s", n, noun))
	}
	families := map[string]bool{}
	for _, f := range append(append([]*fonts.Font{}, added...), replaced...) {
		families[f.Family] = true
	}
	sorted := make([]string, 0, len(families))
	for fam := range families {
		sorted = append(sorted, fam)
	}
	sort.Strings(sorted)
	var b strings.Builder
	b.WriteString(strings.Join(parts, ", "))
	b.WriteString("\n")
	if len(sorted) > 0 {
		b.WriteString("\nFamilies:\n")
		for _, fam := range sorted {
			fmt.Fprintf(&b, "  %s\n", fam)
		}
	}
	return []byte(b.String())
}

// pushWithRetries pushes LFS objects then the ref, rebasing and re-running
// the apply loop after non-fast-forward rejections.
func (a *App) pushWithRetries(ctx context.Context, st *runState, res *pushResult, apply func() error) (bool, error) {
	if a.Opts.DryRun {
		fmt.Fprintf(a.Stdout, "dry-run: would push LFS objects and branch %s to origin\n", st.workdir.Branch)
		return false, nil
	}
	backoff := time.Second
	for attempt := 1; attempt <= maxPushAttempts; attempt++ {
		if _, err := a.git.LFSPush(ctx, st.workdir.Dir, st.workdir.Branch); err != nil {
			return false, fmt.Errorf("push LFS objects for %s: %w", st.workdir.Branch, err)
		}
		if _, err := a.git.Push(ctx, st.workdir.Dir, st.workdir.Branch); err != nil {
			if !isNonFastForward(err) {
				return false, fmt.Errorf("push %s to origin: %w", st.workdir.Branch, err)
			}
			if attempt == maxPushAttempts {
				return false, fmt.Errorf("push %s rejected %d times (non-fast-forward); local commits are retained: %w", st.workdir.Branch, maxPushAttempts, err)
			}
			a.Log.Warn("push rejected (non-fast-forward); fetching and rebasing", "attempt", attempt)
			select {
			case <-ctx.Done():
				return false, fmt.Errorf("interrupted: %w", ctx.Err())
			case <-time.After(backoff + time.Duration(rand.Int64N(int64(backoff/2+1)))):
			}
			backoff *= 2
			if _, err := a.git.Fetch(ctx, st.workdir.Dir, st.workdir.Branch); err != nil {
				return false, fmt.Errorf("fetch %s for rebase: %w", st.workdir.Branch, err)
			}
			if _, err := a.git.Rebase(ctx, st.workdir.Dir, st.workdir.Branch); err != nil {
				return false, fmt.Errorf("rebase onto origin/%s failed; local commits are retained: %w", st.workdir.Branch, err)
			}
			// Reload manifests and redo the apply loop against the new
			// state; previously answered conflicts keep their answers.
			idx, errs := workdir.LoadIndex(a.workdir)
			for _, e := range errs {
				res.errors_ = append(res.errors_, report.Entry{Path: "manifest", Reason: e.Error()})
			}
			st.idx = idx
			if err := apply(); err != nil {
				return false, err
			}
			if err := a.verifyStagedFonts(ctx, st, res); err != nil {
				return false, err
			}
			if err := a.commitIfStaged(ctx, st, res); err != nil {
				return false, err
			}
			continue
		}
		// Verify the ref now points at our commit.
		local, err := a.git.RevParse(ctx, st.workdir.Dir)
		if err != nil {
			return false, fmt.Errorf("resolve HEAD after push: %w", err)
		}
		remote, err := a.git.LsRemote(ctx, st.workdir.Dir, st.workdir.Branch)
		if err != nil {
			return false, fmt.Errorf("verify pushed ref: %w", err)
		}
		if remote != local {
			return false, fmt.Errorf("push verification failed: origin/%s is %s, expected %s", st.workdir.Branch, remote, local)
		}
		return true, nil
	}
	return false, errors.New("push exhausted retries")
}

func isNonFastForward(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "non-fast-forward") ||
		strings.Contains(msg, "fetch first") ||
		strings.Contains(msg, "rejected")
}
