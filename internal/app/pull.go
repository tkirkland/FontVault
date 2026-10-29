package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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

	var installed, replaced []report.Entry
	var skipped []report.Entry

	// Build the local lookup: every scanned font, deduplicated through the
	// reps map where possible.
	localByKey := map[string]*fonts.Font{}
	for k, f := range st.reps {
		localByKey[k] = f
	}

	targets := map[string]*pullTarget{}

	// 1. Repo keys missing locally.
	for _, m := range sortedManifests(st.idx) {
		if st.presentKeys[m.Key] {
			continue
		}
		targets[m.Key] = &pullTarget{manifest: m}
	}

	// 2. Repo keys present locally with a different hash: conflict
	// resolution (never prompted under -y; OS-default locals are skipped).
	classifier := a.Platform.Classifier()
	for _, m := range sortedManifests(st.idx) {
		local, ok := localByKey[m.Key]
		if !ok || local.SHA256 == m.SHA256 {
			continue
		}
		entry := conflictEntry(local, m, "")
		protected, protectReason := exclude.ProtectionStatus(local, a.Opts.Source == cli.SourceSystem, classifier)
		if protected {
			entry.Reason = protectReason
			skipped = append(skipped, entry)
			continue
		}
		if a.Opts.Yes {
			entry.Reason = "-y"
			skipped = append(skipped, entry)
			continue
		}
		fmt.Fprintf(a.Stdout,
			"Conflict: %s %s (key %s)\n  local: version %s | revision %s | %d bytes | sha %s | %s\n  repo:  version %s | revision %s | %d bytes | sha %s | %s\n",
			local.Family, local.Subfamily, local.Key,
			orDash(local.VersionString), orDash(local.FontRevision), local.Size, fonts.ShortHash(local.SHA256), local.Path,
			orDash(m.VersionString), orDash(m.FontRevision), m.Size, fonts.ShortHash(m.SHA256), m.RepoPath)
		answer, err := a.prompter.choice("[l]ocal replaced by repo copy / [s]kip: ", map[string]string{"l": "l", "s": "s"})
		if err != nil || answer == "s" {
			entry.Reason = "user skip"
			skipped = append(skipped, entry)
			continue
		}
		targets[m.Key] = &pullTarget{manifest: m, replace: true, local: local}
	}

	// 3. Sync "use repo" choices from the push phase. The manifest is
	// re-resolved against the post-push index so the installed content is
	// the final repo state.
	for key, m := range useRepo {
		if fresh, ok := st.idx.ByKey[key]; ok {
			m = fresh
		}
		targets[key] = &pullTarget{manifest: m, replace: true, local: localByKey[key]}
	}

	// Execute in deterministic order.
	keys := make([]string, 0, len(targets))
	for k := range targets {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	if a.Opts.DryRun {
		for _, k := range keys {
			t := targets[k]
			if t.replace {
				fmt.Fprintf(a.Stdout, "dry-run: would replace local font with %s\n", t.manifest.RepoPath)
			} else {
				fmt.Fprintf(a.Stdout, "dry-run: would install %s\n", t.manifest.RepoPath)
			}
		}
		rep.AddSection(report.SectionInstalled, installed)
		rep.AddSection(report.SectionReplacedLocal, replaced)
		rep.AddSection(report.SectionConflictsSkipped, skipped)
		rep.AddSection(report.SectionErrors, errs)
		rep.AddCountSection(report.SectionSkippedFormats, st.scanRes.Skipped)
		auto := 0
		for _, e := range skipped {
			if isAutoSkipReason(e.Reason) {
				auto++
			}
		}
		return finalCode(len(errs), auto), rep
	}

	// The installer needs the install dir to exist.
	if err := a.ensureInstallDir(); err != nil {
		return a.fail("%v", err), nil
	}
	installer := a.Platform.NewInstaller(a.Opts.Source == cli.SourceSystem, a.installDir)

	for _, k := range keys {
		if ctx.Err() != nil {
			a.Log.Warn("interrupted; rolling back current font")
			break
		}
		t := targets[k]
		err := a.pullOne(ctx, st, installer, t, &installed, &replaced, &skipped, &errs)
		if err != nil {
			errs = append(errs, report.Entry{Path: t.manifest.RepoPath, Reason: err.Error()})
			continue
		}
	}

	// fc-cache (Linux) runs once after all installs; missing fc-cache warns
	// without failing.
	if err := installer.Finish(ctx); err != nil {
		errs = append(errs, report.Entry{Path: a.installDir, Reason: err.Error()})
	}
	a.pullPhaseOK = true

	auto := 0
	for _, e := range skipped {
		if isAutoSkipReason(e.Reason) {
			auto++
		}
	}
	rep.AddSection(report.SectionInstalled, installed)
	rep.AddSection(report.SectionReplacedLocal, replaced)
	rep.AddSection(report.SectionConflictsSkipped, skipped)
	rep.AddSection(report.SectionErrors, errs)
	rep.AddCountSection(report.SectionSkippedFormats, st.scanRes.Skipped)
	return finalCode(len(errs), auto), rep
}

// pullOne fetches and installs one target, rolling back this run's partial
// work on failure.
func (a *App) pullOne(ctx context.Context, st *runState, installer install.Installer, t *pullTarget, installed, replaced, skipped *[]report.Entry, errs *[]report.Entry) error {
	m := t.manifest
	meta := install.Meta{
		Filename: filepath.Base(m.RepoPath),
		FullName: m.FullName,
		Ext:      m.Ext,
	}
	metaName := repoEntry(m)

	// Fetch content without checkout: cat-file the blob and let LFS smudge
	// it, then verify hash and parseability.
	blob, err := a.git.Pipe(ctx, st.workdir.Dir, nil,
		[]string{"cat-file", "blob", "HEAD:" + m.RepoPath},
		[]string{"lfs", "smudge", m.RepoPath})
	if err != nil {
		return fmt.Errorf("fetch %s: %w", m.RepoPath, err)
	}
	sum := sha256.Sum256(blob)
	if hex.EncodeToString(sum[:]) != m.SHA256 {
		return fmt.Errorf("verify %s: content hash mismatch (got %s, manifest has %s)", m.RepoPath, hex.EncodeToString(sum[:]), m.SHA256)
	}
	if _, err := fonts.Parse(blob); err != nil {
		return fmt.Errorf("verify %s: downloaded font does not parse: %w", m.RepoPath, err)
	}

	// Write the temp file inside the install dir so the final rename is
	// atomic.
	tmp, err := os.CreateTemp(a.installDir, ".fontvault-pull-*")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", a.installDir, err)
	}
	tmpName := tmp.Name()
	a.addTemp(tmpName)
	if _, err := tmp.Write(blob); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp file %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temp file %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file %s: %w", tmpName, err)
	}
	defer func() { removeTempQuietly(tmpName) }()

	target := filepath.Join(a.installDir, meta.Filename)
	if !t.replace {
		// Missing-font install: skip when the target appeared with the
		// same content; conflict when it appeared with different content.
		if existing, err := os.ReadFile(target); err == nil {
			esum := sha256.Sum256(existing)
			if hex.EncodeToString(esum[:]) == m.SHA256 {
				a.Log.Info("target already exists with same hash; skipping", "path", target)
				return nil
			}
			*skipped = append(*skipped, conflictEntryForPath(target, m, "target path exists with different content"))
			return nil
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("check target %s: %w", target, err)
		}
		path, err := installer.Install(ctx, tmpName, meta)
		if err != nil {
			if errors.Is(err, install.ErrRegistryNameCollision) {
				*skipped = append(*skipped, conflictEntryForPath(target, m, "registry name collision"))
				return nil
			}
			return fmt.Errorf("install %s: %w", target, err)
		}
		*installed = append(*installed, metaName)
		a.Log.Info("installed font", "path", path)
		return nil
	}

	// Replace-local flow: check the registration name first (so the old
	// font is not removed when the new name collides), then unregister and
	// delete the old font, then install the new one. On deletion failure,
	// re-register the old font and leave both copies unchanged.
	old := t.local
	if old == nil {
		return fmt.Errorf("replace %s: no local file recorded", m.Key)
	}
	if collision, err := installer.NameCollision(meta); err != nil {
		return fmt.Errorf("check registration name for %s: %w", m.RepoPath, err)
	} else if collision {
		*skipped = append(*skipped, conflictEntryForPath(target, m, "registry name collision"))
		return nil
	}
	oldMeta := install.Meta{Filename: filepath.Base(old.Path), FullName: old.FullName, Ext: old.Ext}
	if err := installer.RemoveFont(ctx, old.Path, oldMeta); err != nil {
		if rerr := installer.RegisterFont(ctx, old.Path, oldMeta); rerr != nil {
			a.Log.Warn("re-register old font after failed removal", "path", old.Path, "error", rerr)
		}
		*errs = append(*errs, report.Entry{Path: old.Path, Reason: fmt.Sprintf("replace failed; old font left in place: %v", err)})
		return nil
	}
	path, err := installer.Install(ctx, tmpName, meta)
	if err != nil {
		if errors.Is(err, install.ErrRegistryNameCollision) {
			*skipped = append(*skipped, conflictEntryForPath(target, m, "registry name collision"))
			return nil
		}
		// Roll back what this run did for the font: the new file (already
		// removed by the installer on failure) and the registry value.
		return fmt.Errorf("install replacement %s: %w", target, err)
	}
	*replaced = append(*replaced, metaName)
	a.Log.Info("replaced font", "path", path)
	return nil
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
