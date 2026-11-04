package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"fontvault/internal/archive"
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
	res := newPushResult()

	if err := a.commitAndPushFonts(ctx, st, sync, res); err != nil {
		a.cleanupAbortedPush(ctx, st)
		return a.fail("%v", err), nil, nil
	}
	rep.Header.NewSHA = res.newSHA

	// Fonts leave the working tree.
	if _, err := a.git.SparseReapply(ctx, st.workdir.Dir); err != nil {
		a.Log.Warn("git sparse-checkout reapply", "error", err)
	}
	a.pushPhaseOK = true

	errs := append(pushErrorEntries(st), res.errors_...)
	// Local-duplicate keys skipped while resolving duplicates belong in this
	// report exactly as compare lists them; their reasons also feed the
	// exit-2 auto-skip tally. The two lists are disjoint by construction:
	// skipped keys are absent from st.reps, so push never prompts them and
	// res.skipped can never hold them.
	skippedAll := append(append([]report.Entry{}, res.skipped...), st.localConflictSkips...)
	rep.AddSection(report.SectionAdded, fontEntries(res.added))
	rep.AddSection(report.SectionReplacedRepo, fontEntries(res.replaced))
	rep.AddSection(report.SectionConflictsSkipped, skippedAll)
	rep.AddSection(report.SectionExcluded, pushExcludedEntries(st))
	rep.AddSection(report.SectionErrors, errs)
	rep.AddCountSection(report.SectionSkippedFormats, st.scanRes.Skipped)

	return finalCode(len(errs), countAutoSkips(skippedAll)), rep, res
}

// newPushResult starts the cumulative per-key bookkeeping for one push
// phase; outcome entries survive rebase retries.
func newPushResult() *pushResult {
	return &pushResult{
		useRepo:      map[string]*workdir.Manifest{},
		answers:      map[string]pushChoice{},
		reasons:      map[string]string{},
		addedKeys:    map[string]bool{},
		replacedKeys: map[string]bool{},
		skippedKeys:  map[string]bool{},
	}
}

// pushErrorEntries collects the errors found before the push phase ran.
func pushErrorEntries(st *runState) []report.Entry {
	errs := append([]report.Entry{}, st.manifestErrs...)
	errs = append(errs, scanErrorEntries(st.scanRes)...)
	errs = append(errs, st.sysScanErrs...)
	return errs
}

// pushExcludedEntries lists the fonts kept out of the push: policy-excluded
// decisions plus duplicates that were not chosen.
func pushExcludedEntries(st *runState) []report.Entry {
	var excluded []report.Entry
	for _, d := range st.decisions {
		if d.Excluded {
			excluded = append(excluded, fontEntry(d.Font, d.Reason))
		}
	}
	return append(excluded, st.notChosen...)
}

// commitAndPushFonts runs the git half of the push phase: the attributes
// commit, one apply/verify/commit pass, the push (with rebase retries) and
// the new-SHA resolution. The wrapped "resolve new commit" wording is
// preserved from the original call site.
func (a *App) commitAndPushFonts(ctx context.Context, st *runState, sync bool, res *pushResult) error {
	// .gitattributes first: fix it in its own commit before any font commit.
	if err := a.ensureGitAttributes(ctx, st); err != nil {
		return err
	}
	apply := func() error { return a.applyPushFonts(ctx, st, sync, res) }
	if err := apply(); err != nil {
		return err
	}
	if err := a.verifyStagedFonts(ctx, st); err != nil {
		return err
	}
	if err := a.commitIfStaged(ctx, st, res); err != nil {
		return err
	}
	pushed, err := a.pushWithRetries(ctx, st, res, apply)
	if err != nil {
		return err
	}
	if !pushed {
		return nil
	}
	sha, err := a.git.RevParse(ctx, st.workdir.Dir)
	if err != nil {
		return fmt.Errorf("resolve new commit: %w", err)
	}
	if sha != st.workdir.BaseSHA {
		res.newSHA = sha
	}
	return nil
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
		_, _ = fmt.Fprintf(a.Stdout, "dry-run: would write .gitattributes and commit it (%s)\n", msg)
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

// applyPushFonts runs one pass over the local fonts: it decides every font
// first (prompting for conflicts), collecting the new and replaced fonts per
// family, then stores the families. Answered conflicts keep their answer
// across rebase retries; outcome entries are cumulative across passes.
func (a *App) applyPushFonts(ctx context.Context, st *runState, sync bool, res *pushResult) error {
	batches := pushBatches{}
	for _, f := range sortedReps(st.reps) {
		if ctx.Err() != nil {
			return fmt.Errorf("interrupted: %w", ctx.Err())
		}
		repo, exists := st.idx.ByKey[f.Key]
		switch {
		case !exists:
			a.pushAdd(f, batches)
		case repo.SHA256 == f.SHA256:
			// Identical: no-op.
		default:
			if err := a.pushConflict(sync, f, repo, res, batches); err != nil {
				return err
			}
		}
	}
	return a.storeBatches(ctx, st, res, batches)
}

// pushAdd queues a font the repository does not have yet.
func (a *App) pushAdd(f *fonts.Font, batches pushBatches) {
	// Invariant: the repo path must not exist for a key that is not in the
	// index. familyFor checks it while resolving the family manifest.
	if a.Opts.DryRun {
		_, _ = fmt.Fprintf(a.Stdout, "dry-run: would add %s -> %s\n", f.Path, displayFont(f))
	}
	batches.add(f, nil, false)
}

// recordAdded lists a font under "added to repo" exactly once per key.
func recordAdded(res *pushResult, f *fonts.Font) {
	if !res.addedKeys[f.Key] {
		res.addedKeys[f.Key] = true
		res.added = append(res.added, f)
	}
}

// recordReplaced lists a font under "replaced in repo" exactly once per key.
func recordReplaced(res *pushResult, f *fonts.Font) {
	if !res.replacedKeys[f.Key] {
		res.replacedKeys[f.Key] = true
		res.replaced = append(res.replaced, f)
	}
}

// familyFor returns the family manifest that owns the font's archive: the
// one already in the index, or a new one.
//
// note: a family whose archive exists on the branch without a manifest is
// refused, exactly as pushAdd refused a loose font whose key had no manifest
// (index.go's manifestErrs covers a manifest that failed to load).
func (a *App) familyFor(ctx context.Context, st *runState, f *fonts.Font) (*workdir.FamilyManifest, bool, error) {
	repoPath := f.RepoPath()
	fam := st.idx.FamilyFor(repoPath)
	if fam == nil {
		exists, err := a.pathExists(ctx, st, repoPath)
		if err != nil {
			return nil, false, fmt.Errorf("check repo path %s: %w", repoPath, err)
		}
		if exists {
			return nil, false, fmt.Errorf("invariant violation: repo path %s exists but key %q has no manifest; refusing to overwrite", repoPath, f.Key)
		}
		fam = workdir.NewFamilyManifest(f.Family, repoPath, a.Clock())
	}
	// Path is the repository path this family's manifest is written to. The
	// loader fills it with the file it read; deriving it here keeps the value
	// the push path needs (a workdir-relative slash path).
	fam.Path = f.ManifestPath()
	return fam, st.idx.FamilyFor(repoPath) == nil, nil
}

// stageArchive returns the path of the family archive inside the workdir,
// fetching it first when the branch already has one: the workdir is sparse,
// so only manifest/ is checked out. fresh says the branch has no such archive
// yet, in which case the encoder creates it.
//
// note: a stale on-disk copy is refetched when its hash is not the one the
// manifest records. A rebase retry re-runs the apply pass, and a family
// another run extended lands as a clean file whose content the workdir copy
// would otherwise reuse silently, dropping the other run's font from the
// family and writing a manifest that does not describe the staged archive.
func (a *App) stageArchive(ctx context.Context, st *runState, fam *workdir.FamilyManifest, fresh bool) (string, error) {
	full := filepath.Join(st.workdir.Dir, filepath.FromSlash(fam.RepoPath))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", fmt.Errorf("create repo dir for %s: %w", fam.RepoPath, err)
	}
	if fresh {
		// note: anything already sitting at that path belongs to an aborted
		// run, and adding to it would put fonts no manifest describes back
		// into the family, so it is removed instead of updated.
		if _, err := os.Lstat(full); err == nil {
			a.Log.Info("removing family archive left by an earlier run", "path", fam.RepoPath)
			if err := os.Remove(full); err != nil {
				return "", fmt.Errorf("remove stale family archive %s: %w", fam.RepoPath, err)
			}
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("check %s: %w", fam.RepoPath, err)
		}
		return full, nil
	}
	if _, err := os.Lstat(full); err == nil {
		if a.archiveMatches(fam, full) {
			return full, nil
		}
		a.Log.Info("refetch family archive", "path", fam.RepoPath)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("check %s: %w", fam.RepoPath, err)
	}
	blob, err := a.repoFetch(ctx, st, fam.RepoPath)
	if err != nil {
		return "", err
	}
	if err := fsutil.AtomicWrite(full, blob, 0o644); err != nil {
		return "", fmt.Errorf("stage family archive %s: %w", fam.RepoPath, err)
	}
	return full, nil
}

// archiveMatches reports whether the file on disk has the archive hash the
// manifest records. A manifest that records no hash (a family written by an
// older run before this field existed, or a fetch that has not been hashed
// yet) matches nothing, so the archive is refetched.
func (a *App) archiveMatches(fam *workdir.FamilyManifest, path string) bool {
	if fam.ArchiveSHA256 == "" {
		return false
	}
	got, _, err := fsutil.HashAndSize(path)
	return err == nil && got == fam.ArchiveSHA256
}

// pushBatch is the set of fonts one family receives in an apply pass. The
// family archive is encoded once with all of them, after every conflict has
// been answered, so the slow Brotli encode runs once per changed family and
// families encode in parallel.
type pushBatch struct {
	fam         *workdir.FamilyManifest
	fresh       bool   // the branch has no archive for this family yet
	archivePath string // workdir path of the family archive
	stores      []pushStore
	// filled by encodeFamily
	sha256 string
	size   int64
}

// pushStore is one font going into its family archive. repo (nil for a new
// font) is the manifest entry being replaced: its first_archived carries
// over to the new entry.
type pushStore struct {
	font    *fonts.Font
	repo    *workdir.Manifest
	replace bool
}

// pushBatches collects the pass's stores by family archive path.
type pushBatches map[string]*pushBatch

// add queues f for its family, in decision order.
func (b pushBatches) add(f *fonts.Font, repo *workdir.Manifest, replace bool) {
	path := f.RepoPath()
	batch := b[path]
	if batch == nil {
		batch = &pushBatch{}
		b[path] = batch
	}
	batch.stores = append(batch.stores, pushStore{font: f, repo: repo, replace: replace})
}

// storeBatches writes every queued family: the existing archives are
// fetched one at a time (git), the archives are encoded in parallel (CPU),
// and the manifests are written and staged one at a time (git), in repo
// path order so the pass is deterministic. A dry run walks the same
// families in the same order and prints what each would get, so its output
// mirrors the real pass step for step.
func (a *App) storeBatches(ctx context.Context, st *runState, res *pushResult, batches pushBatches) error {
	ordered := make([]*pushBatch, 0, len(batches))
	for _, path := range slices.Sorted(maps.Keys(batches)) {
		ordered = append(ordered, batches[path])
	}
	if a.Opts.DryRun {
		return a.printBatchPlan(ctx, st, res, ordered)
	}
	for _, b := range ordered {
		fam, fresh, err := a.familyFor(ctx, st, b.stores[0].font)
		if err != nil {
			return err
		}
		path, err := a.stageArchive(ctx, st, fam, fresh)
		if err != nil {
			return err
		}
		b.fam, b.fresh, b.archivePath = fam, fresh, path
	}
	if err := a.encodeBatches(ctx, ordered); err != nil {
		return err
	}
	for _, b := range ordered {
		if err := a.recordBatch(ctx, st, res, b); err != nil {
			return err
		}
	}
	return nil
}

// printBatchPlan is storeBatches under -n: it resolves each family exactly
// as the real pass does (so an invariant violation surfaces in a dry run
// too), prints the archive and manifest the family would get, and lists the
// fonts in the outcome, without fetching, encoding or staging anything.
func (a *App) printBatchPlan(ctx context.Context, st *runState, res *pushResult, ordered []*pushBatch) error {
	for _, b := range ordered {
		fam, _, err := a.familyFor(ctx, st, b.stores[0].font)
		if err != nil {
			return err
		}
		added, replaced := 0, 0
		for _, s := range b.stores {
			if s.replace {
				replaced++
				recordReplaced(res, s.font)
			} else {
				added++
				recordAdded(res, s.font)
			}
		}
		total := len(fam.Fonts) + added
		_, _ = fmt.Fprintf(a.Stdout, "dry-run: would encode family archive %s (%s: %d added, %d replaced) and write %s\n",
			fam.RepoPath, plural(total, "font"), added, replaced, fam.Path)
	}
	return nil
}

// plural formats a count with its noun ("1 font", "2 fonts").
func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// encodeBatches encodes the family archives concurrently, one worker per
// CPU core at most. Every batch writes its own file, so the workers share
// nothing; the first failure in repo path order is reported.
func (a *App) encodeBatches(ctx context.Context, batches []*pushBatch) error {
	if len(batches) == 0 {
		return nil
	}
	workers := min(runtime.NumCPU(), len(batches))
	a.Log.Info("encoding family archives", "families", len(batches), "workers", workers)
	errs := make([]error, len(batches))
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i, b := range batches {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			errs[i] = a.encodeFamily(ctx, b)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// encodeFamily rebuilds one family archive: the members of the fetched
// archive (none for a fresh family) plus the batch's fonts, new ones added
// and replaced ones overwritten under their member name, encoded and written
// atomically to the workdir path. The archive hash is taken from the bytes
// written, so the manifest describes exactly the staged file.
func (a *App) encodeFamily(ctx context.Context, b *pushBatch) error {
	members := map[string][]byte{}
	if !b.fresh {
		data, err := os.ReadFile(b.archivePath)
		if err != nil {
			return fmt.Errorf("read family archive %s: %w", b.fam.RepoPath, err)
		}
		if members, err = archive.Decode(data); err != nil {
			return fmt.Errorf("decode family archive %s: %w", b.fam.RepoPath, err)
		}
	}
	for _, s := range b.stores {
		content, err := os.ReadFile(s.font.Path)
		if err != nil {
			return fmt.Errorf("read %s: %w", s.font.Path, err)
		}
		members[s.font.MemberName()] = content
	}
	if ctx.Err() != nil {
		return fmt.Errorf("interrupted: %w", ctx.Err())
	}
	start := time.Now()
	out, err := archive.Encode(members)
	if err != nil {
		return fmt.Errorf("encode family archive %s: %w", b.fam.RepoPath, err)
	}
	if err := fsutil.AtomicWrite(b.archivePath, out, 0o644); err != nil {
		return fmt.Errorf("write family archive %s: %w", b.fam.RepoPath, err)
	}
	sum := sha256.Sum256(out)
	b.sha256 = hex.EncodeToString(sum[:])
	b.size = int64(len(out))
	a.Log.Info("encoded family archive", "path", b.fam.RepoPath, "members", len(members),
		"bytes", b.size, "took", time.Since(start).Round(time.Millisecond))
	return nil
}

// recordBatch updates the family manifest for an encoded batch, writes it,
// stages it with the archive, and lists the fonts in the push outcome.
func (a *App) recordBatch(ctx context.Context, st *runState, res *pushResult, b *pushBatch) error {
	b.fam.SetArchive(b.sha256, b.size)
	for _, s := range b.stores {
		manifest := manifestForFont(s.font, b.fam.RepoPath, s.font.MemberName())
		workdir.NewFromFont(manifest, a.Clock())
		if s.repo != nil {
			manifest.FirstArchived = s.repo.FirstArchived
		}
		// PutEntry returns the entry now in the manifest; the index must
		// point at that same record, not at the local copy.
		st.idx.ByKey[s.font.Key] = b.fam.PutEntry(manifest, a.Clock())
	}
	if err := a.writeManifest(st, b.fam); err != nil {
		return err
	}
	archiveSlash := filepath.ToSlash(b.archivePath)
	if _, err := a.git.AddSparse(ctx, st.workdir.Dir, archiveSlash, b.fam.Path); err != nil {
		return fmt.Errorf("stage %s and %s: %w", archiveSlash, b.fam.Path, err)
	}
	st.idx.ByPath[b.fam.RepoPath] = b.fam
	for _, s := range b.stores {
		if s.replace {
			recordReplaced(res, s.font)
		} else {
			recordAdded(res, s.font)
		}
		a.Log.Info("staged font in family archive", "path", b.fam.RepoPath, "member", s.font.MemberName())
	}
	return nil
}

// pushConflict resolves a same-key/different-hash conflict.
func (a *App) pushConflict(sync bool, f *fonts.Font, repo *workdir.Manifest, res *pushResult, batches pushBatches) error {
	choice, answered := res.answers[f.Key]
	if !answered {
		choice = a.resolvePushChoice(sync, f, repo, res)
		if choice == pushChoiceSkip {
			return nil
		}
	}

	switch choice {
	case pushChoiceSkip:
		appendSkipped(res, f.Key, conflictEntry(f, repo, pushSkipReason(res, f.Key)))
		return nil
	case pushChoiceUseRepo:
		// Sync only: the repo copy replaces the local copy in the pull
		// phase. The conflict is resolved, not skipped.
		res.useRepo[f.Key] = repo
		return nil
	case pushChoiceReplace:
		// Continue below.
	}

	a.replaceRepoFont(f, repo, batches)
	return nil
}

// resolvePushChoice asks (or auto-skips under -y) about an unanswered
// conflict and records the answer in res.answers, so rebase retries keep
// it. Skip answers record their skipped entry here; later passes take
// pushConflict's answered path. A failed prompt read is treated as "s"
// (skip), matching modes.go's note on the pull conflict code.
func (a *App) resolvePushChoice(sync bool, f *fonts.Font, repo *workdir.Manifest, res *pushResult) pushChoice {
	if a.Opts.Yes {
		res.reasons[f.Key] = "-y"
		appendSkipped(res, f.Key, conflictEntry(f, repo, "-y"))
		res.answers[f.Key] = pushChoiceSkip
		return pushChoiceSkip
	}
	_, _ = fmt.Fprintf(a.Stdout,
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
	choice := pushChoiceSkip
	switch answer {
	case "s":
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
	res.answers[f.Key] = choice
	return choice
}

// replaceRepoFont overwrites the repo copy with the local font: dry-run
// notice, then store the font in its family archive and update the
// manifest, stage and index the result.
func (a *App) replaceRepoFont(f *fonts.Font, repo *workdir.Manifest, batches pushBatches) {
	if a.Opts.DryRun {
		_, _ = fmt.Fprintf(a.Stdout, "dry-run: would replace %s -> %s\n", f.Path, displayEntry(repo))
	}
	batches.add(f, repo, true)
}

// appendSkipped records a skipped conflict exactly once per key.
func appendSkipped(res *pushResult, key string, entry report.Entry) {
	if res.skippedKeys[key] {
		return
	}
	res.skippedKeys[key] = true
	res.skipped = append(res.skipped, entry)
}

// pushSkipReason reports the recorded reason for a skipped conflict; an
// unset reason defaults to "user skip", as the answered path did.
func pushSkipReason(res *pushResult, key string) string {
	reason := res.reasons[key]
	if reason == "" {
		reason = "user skip"
	}
	return reason
}

func (a *App) conflictPrompt(sync bool) string {
	if sync {
		return "Use [l]ocal (replace the repo copy) / use [r]epo (replace the local copy) / [s]kip: "
	}
	return "[r]eplace repo copy with local / [s]kip: "
}

// stagedArchivesForIndex maps every staged family archive to the archive
// hash the (updated) index records for it.
//
// note: the expectation is read from st.idx, not collected from the run's
// added/replaced lists, so a rebase retry that staged an archive in an earlier
// pass still verifies. The hash recorded at write time is what the staged
// blob must be an LFS pointer for: the OID git computed for the pointer is
// the hash of the archive content, not of the manifest entry.
func (a *App) stagedArchivesForIndex(ctx context.Context, st *runState) map[string]string {
	paths, err := a.git.StagedFontPaths(ctx, st.workdir.Dir)
	expected := map[string]string{}
	if err != nil {
		a.Log.Warn("list staged fonts", "error", err)
		return expected
	}
	for _, p := range paths {
		fam := st.idx.FamilyFor(filepath.ToSlash(p))
		if fam != nil {
			expected[filepath.ToSlash(p)] = fam.ArchiveSHA256
		}
	}
	return expected
}

// manifestForFont builds the manifest entry for a local font going into the
// family archive at repoPath, stored there under member.
func manifestForFont(f *fonts.Font, repoPath string, member string) *workdir.Manifest {
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
		Member:         member,
	}
}

// writeManifest writes one family manifest to its own path (mkdir as needed).
func (a *App) writeManifest(st *runState, fam *workdir.FamilyManifest) error {
	data, err := workdir.Marshal(fam)
	if err != nil {
		return fmt.Errorf("marshal manifest for family %s: %w", fam.Family, err)
	}
	full := filepath.Join(st.workdir.Dir, filepath.FromSlash(fam.Path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return fmt.Errorf("create manifest dir for %s: %w", fam.Path, err)
	}
	if err := fsutil.AtomicWrite(full, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write manifest %s: %w", fam.Path, err)
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

// verifyStagedFonts confirms every staged family archive is an LFS pointer
// with the expected oid. Any raw blob aborts the run before committing.
func (a *App) verifyStagedFonts(ctx context.Context, st *runState) error {
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
	expected := a.stagedArchivesForIndex(ctx, st)
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
		if len(res.added)+len(res.replaced) > 0 {
			summary, _, _ := strings.Cut(string(commitMessage(res.added, res.replaced)), "\n")
			_, _ = fmt.Fprintf(a.Stdout, "dry-run: would commit %q\n", summary)
		}
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
			_, _ = fmt.Fprintf(&b, "  %s\n", fam)
		}
	}
	return []byte(b.String())
}

// pushWithRetries pushes LFS objects then the ref, rebasing and re-running
// the apply loop after non-fast-forward rejections.
func (a *App) pushWithRetries(ctx context.Context, st *runState, res *pushResult, apply func() error) (bool, error) {
	if a.Opts.DryRun {
		_, _ = fmt.Fprintf(a.Stdout, "dry-run: would push LFS objects and branch %s to origin\n", st.workdir.Branch)
		return false, nil
	}
	backoff := time.Second
	for attempt := 1; attempt <= maxPushAttempts; attempt++ {
		pushed, err := a.pushAttempt(ctx, st, res, apply, attempt, &backoff)
		if err != nil {
			return false, err
		}
		if pushed {
			return true, nil
		}
	}
	return false, errors.New("push exhausted retries")
}

// pushAttempt does one push attempt: LFS objects, then the ref. On success,
// it verifies the pushed head and returns true; on a recoverable non-fast-forward
// rejection it rebases and restages and returns false so the caller retries.
// Any other error aborts the run.
func (a *App) pushAttempt(ctx context.Context, st *runState, res *pushResult, apply func() error, attempt int, backoff *time.Duration) (bool, error) {
	if _, err := a.git.LFSPush(ctx, st.workdir.Dir, st.workdir.Branch); err != nil {
		return false, fmt.Errorf("push LFS objects for %s: %w", st.workdir.Branch, err)
	}
	_, pushErr := a.git.Push(ctx, st.workdir.Dir, st.workdir.Branch)
	if pushErr == nil {
		// Verify the ref now points at our commit.
		if err := a.verifyPushedRef(ctx, st); err != nil {
			return false, err
		}
		return true, nil
	}
	if !isNonFastForward(pushErr) {
		return false, fmt.Errorf("push %s to origin: %w", st.workdir.Branch, pushErr)
	}
	if attempt == maxPushAttempts {
		return false, fmt.Errorf("push %s rejected %d times (non-fast-forward); local commits are retained: %w", st.workdir.Branch, maxPushAttempts, pushErr)
	}
	a.Log.Warn("push rejected (non-fast-forward); fetching and rebasing", "attempt", attempt)
	if err := a.recoverPushRejection(ctx, st, backoff); err != nil {
		return false, err
	}
	if err := a.reapplyAfterRebase(ctx, st, res, apply); err != nil {
		return false, err
	}
	return false, nil
}

// recoverPushRejection backs off (deterministic exponential + jitter, doubling
// each attempt), then fetches and rebases onto origin. When the rebase
// conflicts (both sides rewrote the same family archive and manifest) it
// abandons the rebase and resets the branch to origin instead: the dropped
// commits hold only this run's apply output, which the retry re-derives from
// the local fonts against the fresh index. Leaving the rebase in place would
// strand the workdir mid-rebase, and every later run would refuse it as dirty.
func (a *App) recoverPushRejection(ctx context.Context, st *runState, backoff *time.Duration) error {
	select {
	case <-ctx.Done():
		return fmt.Errorf("interrupted: %w", ctx.Err())
	case <-time.After(*backoff + time.Duration(rand.Int64N(int64(*backoff/2+1)))):
	}
	*backoff *= 2
	if _, err := a.git.Fetch(ctx, st.workdir.Dir, st.workdir.Branch); err != nil {
		return fmt.Errorf("fetch %s for rebase: %w", st.workdir.Branch, err)
	}
	if _, err := a.git.Rebase(ctx, st.workdir.Dir, st.workdir.Branch); err != nil {
		a.Log.Warn("rebase conflicted; dropping this run's commits and re-applying against origin", "branch", st.workdir.Branch)
		return a.resetToOrigin(ctx, st, err)
	}
	return nil
}

// resetToOrigin abandons a conflicted rebase and moves the branch to
// origin/<branch>. rebaseErr is the rebase failure, reported if the reset
// itself fails so the workdir state can be explained.
func (a *App) resetToOrigin(ctx context.Context, st *runState, rebaseErr error) error {
	if _, err := a.git.RebaseAbort(ctx, st.workdir.Dir); err != nil {
		return fmt.Errorf("abort conflicted rebase onto origin/%s (%v): %w", st.workdir.Branch, rebaseErr, err)
	}
	if _, err := a.git.ResetHardToOrigin(ctx, st.workdir.Dir, st.workdir.Branch); err != nil {
		return fmt.Errorf("reset %s to origin after conflicted rebase (%v): %w", st.workdir.Branch, rebaseErr, err)
	}
	return nil
}

// reapplyAfterRebase reloads the manifest index and redoes apply/verify/commit
// against the rebased state; previously answered conflicts keep their answers.
func (a *App) reapplyAfterRebase(ctx context.Context, st *runState, res *pushResult, apply func() error) error {
	idx, errs := workdir.LoadIndex(a.workdir)
	for _, e := range errs {
		res.errors_ = append(res.errors_, report.Entry{Path: "manifest", Reason: e.Error()})
	}
	st.idx = idx
	if err := apply(); err != nil {
		return err
	}
	if err := a.verifyStagedFonts(ctx, st); err != nil {
		return err
	}
	if err := a.commitIfStaged(ctx, st, res); err != nil {
		return err
	}
	return nil
}

// verifyPushedRef confirms origin's branch head matches local HEAD after
// a successful push.
func (a *App) verifyPushedRef(ctx context.Context, st *runState) error {
	local, err := a.git.RevParse(ctx, st.workdir.Dir)
	if err != nil {
		return fmt.Errorf("resolve HEAD after push: %w", err)
	}
	remote, err := a.git.LsRemote(ctx, st.workdir.Dir, st.workdir.Branch)
	if err != nil {
		return fmt.Errorf("verify pushed ref: %w", err)
	}
	if remote != local {
		return fmt.Errorf("push verification failed: origin/%s is %s, expected %s", st.workdir.Branch, remote, local)
	}
	return nil
}

func isNonFastForward(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "non-fast-forward") ||
		strings.Contains(msg, "fetch first") ||
		strings.Contains(msg, "rejected")
}
