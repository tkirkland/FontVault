package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"

	"fontvault/internal/cli"
	"fontvault/internal/exclude"
	"fontvault/internal/fonts"
	"fontvault/internal/install"
	"fontvault/internal/report"
	"fontvault/internal/workdir"
)

// pullTarget is one font to fetch and install.
type pullTarget struct {
	manifest *workdir.Manifest
	replace  bool // replace the local copy (user-chosen conflict)
	local    *fonts.Font
}

// runPull installs repo fonts that are missing locally and applies
// user-chosen "replace local" resolutions.
func (a *App) runPull(ctx context.Context, st *runState, useRepo map[string]*workdir.Manifest) (int, *report.Report) {
	rep := a.baseReport("pull")
	rep.Header.BaseSHA = st.workdir.BaseSHA

	errs := append([]report.Entry{}, st.manifestErrs...)
	errs = append(errs, scanErrorEntries(st.scanRes)...)

	// Local copy of each key: the same Font feeds the conflict prompt and the
	// replacement flow, so what the user saw and what gets deleted cannot drift
	// apart.
	localByKey := map[string]*fonts.Font{}
	maps.Copy(localByKey, st.reps)

	// What to fetch and install, plus the conflicts this run declines to act
	// on. Both dry-run and the real run read the same decision, so -n reports
	// exactly the work the run would do.
	targets, skipped := a.pullTargets(st, localByKey, useRepo)
	keys := sortedTargetKeys(targets)

	var installed, replaced []report.Entry
	if a.Opts.DryRun {
		a.printPullDryRun(keys, targets)
		return a.finishPull(rep, st, installed, replaced, skipped, errs), rep
	}

	// The installer needs the installation dir to exist.
	if err := a.ensureInstallDir(); err != nil {
		return a.fail("%v", err), nil
	}
	installer := a.Platform.NewInstaller(a.Opts.Source == cli.SourceSystem, a.installDir)

	a.applyPullTargets(ctx, st, installer, keys, targets, &installed, &replaced, &skipped, &errs)

	// fc-cache (Linux) runs once after all installs; missing fc-cache warns
	// without failing.
	if err := installer.Finish(ctx); err != nil {
		errs = append(errs, report.Entry{Path: a.installDir, Reason: err.Error()})
	}
	a.pullPhaseOK = true

	return a.finishPull(rep, st, installed, replaced, skipped, errs), rep
}

// pullTargets decides which repo fonts the run fetches, in three passes over
// the repo index and the sync choices: keys missing locally install as new
// fonts, keys present with different content go through conflict resolution,
// and sync's "use repo" answers replace the local copy. A conflict the run
// declines is returned as a skipped entry instead of a target.
func (a *App) pullTargets(st *runState, localByKey map[string]*fonts.Font,
	useRepo map[string]*workdir.Manifest) (map[string]*pullTarget, []report.Entry) {
	targets := map[string]*pullTarget{}
	var skipped []report.Entry

	// Repo keys missing locally.
	for _, m := range sortedManifests(st.idx) {
		if st.presentKeys[m.Key] {
			continue
		}
		targets[m.Key] = &pullTarget{manifest: m}
	}

	// Repo keys present locally with a different hash: conflict resolution
	// (never prompted under -y; OS-default locals are skipped).
	// note: presentKeys and localByKey agree about which keys exist locally,
	// so a key that is present has a local Font here unless duplicate resolution
	// skipped the key, in which case there is nothing to compare.
	classifier := a.Platform.Classifier()
	for _, m := range sortedManifests(st.idx) {
		local, ok := localByKey[m.Key]
		if !ok || local.SHA256 == m.SHA256 {
			continue
		}
		t, reason := a.pullConflict(local, m, classifier)
		if t == nil {
			// note: reason is why the key was skipped, and it is read by
			// isAutoSkipReason for the exit-2 tally, so the wording stays literal.
			skipped = append(skipped, conflictEntry(local, m, reason))
			continue
		}
		targets[m.Key] = t
	}

	// Sync "use repo" choices from the push phase. The manifest is
	// re-resolved against the post-push index so the installed content is
	// the final repo state.
	// (a key answered "use repo" is a conflict this phase already resolved,
	// so it never reaches the loop above twice)
	for key, m := range useRepo {
		if fresh, ok := st.idx.ByKey[key]; ok {
			m = fresh
		}
		targets[key] = &pullTarget{manifest: m, replace: true, local: localByKey[key]}
	}
	return targets, skipped
}

// pullConflict resolves one local-vs.-repo conflict: it returns the target
// to install, or nil with the reason the conflict was skipped.
func (a *App) pullConflict(local *fonts.Font, m *workdir.Manifest, classifier exclude.OSClassifier) (*pullTarget, string) {
	// OS-default local fonts are never overwritten. The protection reason
	// counts as an automatic skip, like -y.
	protected, protectReason := exclude.ProtectionStatus(local, a.Opts.Source == cli.SourceSystem, classifier)
	if protected {
		return nil, protectReason
	}
	if a.Opts.Yes {
		return nil, "-y"
	}
	// Ask the user. note: the two blocks describe the same local/repo pair the
	// caller turns into a skipped entry, so the report cannot disagree with
	// what the user was shown.
	_, _ = fmt.Fprintf(a.Stdout,
		"Conflict: %s %s (key %s)\n  local: version %s | revision %s | %d bytes | sha %s | %s\n  repo:  version %s | revision %s | %d bytes | sha %s | %s\n",
		local.Family, local.Subfamily, local.Key,
		orDash(local.VersionString), orDash(local.FontRevision), local.Size, fonts.ShortHash(local.SHA256), local.Path,
		orDash(m.VersionString), orDash(m.FontRevision), m.Size, fonts.ShortHash(m.SHA256), m.RepoPath)
	answer, err := a.prompter.choice("[l]ocal replaced by repo copy / [s]kip: ", map[string]string{"l": "l", "s": "s"})
	// note: an unreadable answer is a decline, never a guess: the repo copy is
	// only installed over the local file when the user said so.
	if err != nil || answer == "s" {
		return nil, "user skip"
	}
	// note: the empty reason means "resolved", and the caller reads reason
	// only when the target is nil.
	return &pullTarget{manifest: m, replace: true, local: local}, ""
}

// sortedTargetKeys lists the target keys in a fixed order, so installs are
// deterministic; map iteration would pick a different font on every run.
func sortedTargetKeys(targets map[string]*pullTarget) []string {
	keys := make([]string, 0, len(targets))
	for k := range targets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// printPullDryRun lists the installations this run would make. Nothing is written
// and no install dir is created, because the caller returns before the
// installer exists.
func (a *App) printPullDryRun(keys []string, targets map[string]*pullTarget) {
	for _, k := range keys {
		t := targets[k]
		if t.replace {
			_, _ = fmt.Fprintf(a.Stdout, "dry-run: would replace local font with %s\n", t.manifest.RepoPath)
			continue
		}
		_, _ = fmt.Fprintf(a.Stdout, "dry-run: would install %s\n", t.manifest.RepoPath)
	}
}

// applyPullTargets installs the targets in key order and records every
// outcome in the report lists. A per-font failure is reported and does not
// stop the run; an interrupted run stops after the current font, whose
// partial work pullOne rolls back.
func (a *App) applyPullTargets(ctx context.Context, st *runState, installer install.Installer,
	keys []string, targets map[string]*pullTarget, installed, replaced, skipped, errs *[]report.Entry) {
	for _, k := range keys {
		if ctx.Err() != nil {
			a.Log.Warn("interrupted; rolling back current font")
			return
		}
		t := targets[k]
		err := a.pullOne(ctx, st, installer, t, installed, replaced, skipped, errs)
		if err != nil {
			*errs = append(*errs, report.Entry{Path: t.manifest.RepoPath, Reason: err.Error()})
		}
	}
}

// finishPull fills in the pull report's sections and returns the exit code.
// AddSection sorts each section, so the order entries were collected in does
// not matter; sync appends these sections after the push phase's own.
func (a *App) finishPull(rep *report.Report, st *runState, installed, replaced, skipped, errs []report.Entry) int {
	rep.AddSection(report.SectionInstalled, installed)
	rep.AddSection(report.SectionReplacedLocal, replaced)
	rep.AddSection(report.SectionConflictsSkipped, skipped)
	rep.AddSection(report.SectionErrors, errs)
	rep.AddCountSection(report.SectionSkippedFormats, st.scanRes.Skipped)
	return finalCode(len(errs), countAutoSkips(skipped))
}

// pullOne fetches and installs one target, rolling back this run's partial
// work on failure.
func (a *App) pullOne(ctx context.Context, st *runState, installer install.Installer, t *pullTarget, installed, replaced, skipped *[]report.Entry, errs *[]report.Entry) error {
	job := a.newPullJob(t, installed, replaced, skipped, errs)

	// Fetch content without checkout: cat-file the blob and let LFS smudge
	// it, then verify hash and parseability.
	blob, err := a.pullBlob(ctx, st, job.t.manifest)
	if err != nil {
		return err
	}
	// Write the temp file inside the installation dir so the final rename is
	// atomic.
	tmpName, err := a.writePullTemp(blob)
	if err != nil {
		return err
	}
	defer removeTempQuietly(tmpName)

	if !job.t.replace {
		return a.installMissingFont(ctx, installer, job, tmpName)
	}
	return a.replaceLocalFont(ctx, installer, job, tmpName)
}

// pullJob bundles one target with the names the installer needs and the
// report lists its outcome goes into, so the install flows below take two
// parameters instead of nine.
// note: the lists are held by pointer, exactly like pullOne's parameters, so
// an append reaches runPull's slices even though the job is copied by value.
type pullJob struct {
	t                                  *pullTarget
	meta                               install.Meta
	metaName                           report.Entry
	target                             string // where the font lands in the install dir
	installed, replaced, skipped, errs *[]report.Entry
}

// newPullJob builds that bundle.
// note: meta.Filename and target come from one filepath.Base of the repo path,
// so the path a skipped entry reports is the path the installer writes.
func (a *App) newPullJob(t *pullTarget, installed, replaced, skipped, errs *[]report.Entry) pullJob {
	m := t.manifest
	filename := filepath.Base(m.RepoPath)
	return pullJob{
		t:        t,
		meta:     install.Meta{Filename: filename, FullName: m.FullName, Ext: m.Ext},
		metaName: repoEntry(m),
		target:   filepath.Join(a.installDir, filename),

		installed: installed, replaced: replaced, skipped: skipped, errs: errs,
	}
}

// pullBlob fetches one repo font's content without a checkout: cat-file the
// blob and let LFS smudge it. The bytes must match the manifest hash and
// parse as a font before anything is written.
func (a *App) pullBlob(ctx context.Context, st *runState, m *workdir.Manifest) ([]byte, error) {
	blob, err := a.git.Pipe(ctx, st.workdir.Dir, nil,
		[]string{"cat-file", "blob", "HEAD:" + m.RepoPath},
		[]string{"lfs", "smudge", m.RepoPath})
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", m.RepoPath, err)
	}
	sum := sha256.Sum256(blob)
	got := hex.EncodeToString(sum[:])
	if got != m.SHA256 {
		return nil, fmt.Errorf("verify %s: content hash mismatch (got %s, manifest has %s)", m.RepoPath, got, m.SHA256)
	}
	if _, err := fonts.Parse(blob); err != nil {
		return nil, fmt.Errorf("verify %s: downloaded font does not parse: %w", m.RepoPath, err)
	}
	return blob, nil
}

// writePullTemp writes that content into a temp file inside the install dir,
// so the installer's final rename stays atomic.
// note: the handle is closed exactly once on every path. On a failed write or
// sync the close error is logged instead of dropped, but it never replaces
// the write/sync error, which is the cause the run reports. The file itself
// stays on disk, registered with addTemp, and the run's cleanup removes it.
func (a *App) writePullTemp(blob []byte) (string, error) {
	tmp, err := os.CreateTemp(a.installDir, ".fontvault-pull-*")
	if err != nil {
		return "", fmt.Errorf("create temp file in %s: %w", a.installDir, err)
	}
	tmpName := tmp.Name()
	a.addTemp(tmpName)
	if _, err := tmp.Write(blob); err != nil {
		a.closeTempAfterFailure(tmp)
		return "", fmt.Errorf("write temp file %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		a.closeTempAfterFailure(tmp)
		return "", fmt.Errorf("sync temp file %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close temp file %s: %w", tmpName, err)
	}
	return tmpName, nil
}

// closeTempAfterFailure closes the temp handle on a path that is already
// returning a different error.
// note: dropping the close error hid a second failure (a flush that never
// reached the disk); it is logged here and keeps the caller's error as the
// reported cause, so the run fails exactly as it did before.
func (a *App) closeTempAfterFailure(tmp *os.File) {
	if err := tmp.Close(); err != nil {
		a.Log.Warn("close temp file after failed write or sync", "path", tmp.Name(), "error", err)
	}
}

// installMissingFont installs a repo font that has no local copy.
// note: the target may have appeared since the scan, so it is checked again:
// same content means there is nothing to do, different content is a conflict
// nobody prompted about, and the installer itself never overwrites a file.
func (a *App) installMissingFont(ctx context.Context, installer install.Installer,
	job pullJob, tmpName string) error {
	m := job.t.manifest
	if existing, err := os.ReadFile(job.target); err == nil {
		esum := sha256.Sum256(existing)
		if hex.EncodeToString(esum[:]) == m.SHA256 {
			a.Log.Info("target already exists with same hash; skipping", "path", job.target)
			return nil
		}
		recordSkipped(job, "target path exists with different content")
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("check target %s: %w", job.target, err)
	}
	path, err := installer.Install(ctx, tmpName, job.meta)
	if err != nil {
		if errors.Is(err, install.ErrRegistryNameCollision) {
			recordSkipped(job, "registry name collision")
			return nil
		}
		return fmt.Errorf("install %s: %w", job.target, err)
	}
	*job.installed = append(*job.installed, job.metaName)
	a.Log.Info("installed font", "path", path)
	return nil
}

// replaceLocalFont replaces the local copy with the repo copy: check the
// registration name first (so the old font is not removed when the new name
// collides), then unregister and delete the old font, then install the new
// one. On deletion failure, re-register the old font and leave both copies
// unchanged.
func (a *App) replaceLocalFont(ctx context.Context, installer install.Installer, job pullJob, tmpName string) error {
	m := job.t.manifest
	old := job.t.local
	if old == nil {
		return fmt.Errorf("replace %s: no local file recorded", m.Key)
	}
	if collision, err := installer.NameCollision(job.meta); err != nil {
		return fmt.Errorf("check registration name for %s: %w", m.RepoPath, err)
	} else if collision {
		recordSkipped(job, "registry name collision")
		return nil
	}
	oldMeta := install.Meta{Filename: filepath.Base(old.Path), FullName: old.FullName, Ext: old.Ext}
	if err := installer.RemoveFont(ctx, old.Path, oldMeta); err != nil {
		if rerr := installer.RegisterFont(ctx, old.Path, oldMeta); rerr != nil {
			a.Log.Warn("re-register old font after failed removal", "path", old.Path, "error", rerr)
		}
		*job.errs = append(*job.errs, report.Entry{Path: old.Path, Reason: fmt.Sprintf("replace failed; old font left in place: %v", err)})
		return nil
	}
	path, err := installer.Install(ctx, tmpName, job.meta)
	if err != nil {
		if errors.Is(err, install.ErrRegistryNameCollision) {
			recordSkipped(job, "registry name collision")
			return nil
		}
		// Roll back what this run did for the font: the new file (already
		// removed by the installer on failure) and the registry value.
		return fmt.Errorf("install replacement %s: %w", job.target, err)
	}
	*job.replaced = append(*job.replaced, job.metaName)
	a.Log.Info("replaced font", "path", path)
	return nil
}

// recordSkipped appends the skipped-conflict entry for one pull font: the
// repo manifest against the path the install would have used.
// note: "registry name collision" is an automatic skip, so it counts toward
// exit code 2; the other reasons name a file the user or the run declined.
func recordSkipped(job pullJob, reason string) {
	*job.skipped = append(*job.skipped, conflictEntryForPath(job.target, job.t.manifest, reason))
}

func conflictEntryForPath(target string, m *workdir.Manifest, reason string) report.Entry {
	return report.Entry{
		Family:         m.Family,
		Subfamily:      m.Subfamily,
		Key:            m.Key,
		Version:        m.VersionString,
		ShortHash:      fonts.ShortHash(m.SHA256),
		Path:           target,
		HasOther:       true,
		OtherLabel:     "repo:",
		OtherVersion:   m.VersionString,
		OtherShortHash: fonts.ShortHash(m.SHA256),
		OtherPath:      m.RepoPath,
		Reason:         reason,
	}
}

func removeTempQuietly(path string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		_ = err
	}
}
