package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"fontvault/internal/cli"
	"fontvault/internal/exclude"
	"fontvault/internal/fonts"
	"fontvault/internal/lockfile"
	"fontvault/internal/report"
	"fontvault/internal/workdir"
)

// runState carries everything the modes need.
type runState struct {
	workdir      *workdir.State
	idx          *workdir.Index
	manifestErrs []report.Entry
	scanRes      fonts.ScanResult
	// decisions for push/compare (exclusions applied); nil for pull.
	decisions []exclude.Decision
	// system font scan support for user-source runs.
	sysHashes     map[string]bool
	sysScanFailed bool
	sysScanErrs   []report.Entry
	// local representatives after duplicate resolution.
	reps map[string]*fonts.Font
	// every local key (included and excluded) counts as present.
	presentKeys map[string]bool
	// entries generated while resolving duplicates.
	localConflictSkips []report.Entry
	notChosen          []report.Entry
}

// runModes prepares the workdir, scans fonts and dispatches to the mode.
func (a *App) runModes(ctx context.Context) (int, *report.Report) {
	st := &runState{}

	// Workdir, repo index and local fonts.
	code := a.prepareWorkdir(ctx, st)
	if code != 0 {
		return code, nil
	}
	a.loadRepoIndex(st)
	a.scanFontSource(st)

	mode := a.Opts.ModeName()
	usesExclusions := mode == cli.DirectionPush || mode == "compare"
	if usesExclusions {
		a.applyExclusions(st)
	}

	// Local duplicate keys and which keys exist locally.
	st.reps, st.localConflictSkips, st.notChosen = a.resolveDuplicates(a.includedFonts(st), mode)
	a.collectPresentKeys(st, usesExclusions)

	switch mode {
	case cli.DirectionPush:
		return a.runPush(ctx, st)
	case cli.DirectionPull:
		return a.runPull(ctx, st, nil)
	case cli.DirectionSync:
		return a.runSync(ctx, st)
	default:
		return a.runCompare(st)
	}
}

// prepareWorkdir makes the workdir ready and takes its lock. It returns 0
// when the run may go on, otherwise the exit code to return.
func (a *App) prepareWorkdir(ctx context.Context, st *runState) int {
	mgr := workdir.NewManager(a.git, a.Log)
	state, lock, err := mgr.EnsureReady(ctx, a.workdir, a.remoteFor(a.login), a.branch)
	if err != nil {
		// A lock held by another live instance prints as-is and exits 4.
		if _, ok := errors.AsType[*lockfile.HeldError](err); ok {
			_, _ = fmt.Fprintf(a.Stderr, "fontvault: %v\n", err)
			return ExitLockHeld
		}
		return a.fail("%v", err)
	}
	a.lock = lock
	st.workdir = state
	if ctx.Err() != nil {
		return a.fail("interrupted")
	}
	return 0
}

// loadRepoIndex reads the repo index; unreadable manifests become report
// entries and do not stop the run.
func (a *App) loadRepoIndex(st *runState) {
	idx, manifestErrs := workdir.LoadIndex(a.workdir)
	st.idx = idx
	for _, e := range manifestErrs {
		st.manifestErrs = append(st.manifestErrs, report.Entry{Path: "manifest", Reason: e.Error()})
	}
}

// scanFontSource scans the font dirs selected for the run.
func (a *App) scanFontSource(st *runState) {
	st.scanRes = fonts.Scan(fonts.ScanOptions{
		Dirs:       a.fontSources.Dirs,
		Recursive:  a.fontSources.Recursive,
		ExtraFiles: a.fontSources.ExtraFiles,
	})
}

// applyExclusions gathers the OS-default hashes for a user source and
// applies the exclusion rules to the scanned fonts.
func (a *App) applyExclusions(st *runState) {
	classifier := a.Platform.Classifier()
	if a.Opts.Source == cli.SourceUser {
		st.sysHashes, st.sysScanFailed, st.sysScanErrs = a.systemOSDefaultHashes()
	}
	st.decisions = exclude.Apply(st.scanRes.Fonts, a.Opts.Source == cli.SourceSystem,
		st.sysHashes, st.sysScanFailed, classifier, a.xf)
}

// collectPresentKeys marks every key that exists locally: the
// representatives plus, for push/compare, the excluded fonts; for pull,
// every scanned font.
func (a *App) collectPresentKeys(st *runState, usesExclusions bool) {
	st.presentKeys = map[string]bool{}
	for _, f := range st.reps {
		st.presentKeys[f.Key] = true
	}
	if usesExclusions {
		for _, d := range st.decisions {
			if d.Excluded {
				st.presentKeys[d.Font.Key] = true
			}
		}
		return
	}
	// Pull: every scanned font counts as present.
	for _, f := range st.scanRes.Fonts {
		st.presentKeys[f.Key] = true
	}
}

// includedFonts returns the fonts that take part in the operation:
// push/compare use non-excluded fonts; pull uses everything scanned.
func (a *App) includedFonts(st *runState) []*fonts.Font {
	if a.Opts.Direction == cli.DirectionPull {
		return st.scanRes.Fonts
	}
	var out []*fonts.Font
	for _, d := range st.decisions {
		if !d.Excluded {
			out = append(out, d.Font)
		}
	}
	return out
}

// systemOSDefaultHashes scans the system font dirs and returns the content
// hashes of fonts classified as OS defaults. The result fails closed: any
// scan or classification problem makes the whole user-source run exclude
// every font.
func (a *App) systemOSDefaultHashes() (map[string]bool, bool, []report.Entry) {
	var errs []report.Entry
	failed := false
	srcs, err := a.Platform.FontSources(cli.SourceSystem)
	if err != nil {
		failed = true
		errs = append(errs, report.Entry{Path: "system font dirs", Reason: err.Error()})
		return nil, true, errs
	}
	res := fonts.Scan(fonts.ScanOptions{
		Dirs:       srcs.Dirs,
		Recursive:  srcs.Recursive,
		ExtraFiles: srcs.ExtraFiles,
	})
	for _, e := range res.Errors {
		errs = append(errs, report.Entry{Path: e.Path, Reason: e.Error()})
		failed = true
	}
	for _, e := range res.DirErrors {
		errs = append(errs, report.Entry{Path: e.Path, Reason: e.Error()})
		failed = true
	}
	classifier := a.Platform.Classifier()
	if classifier == nil {
		failed = true
		errs = append(errs, report.Entry{Path: "system font dirs", Reason: "no OS-default classifier available"})
		return nil, true, errs
	}
	hashes := map[string]bool{}
	for _, f := range res.Fonts {
		osDefault, err := classifier.IsOSDefault(f.RealPath)
		if err != nil {
			failed = true
			errs = append(errs, report.Entry{Path: f.Path, Reason: fmt.Sprintf("cannot determine OS-default status: %v", err)})
			continue
		}
		if osDefault {
			hashes[f.SHA256] = true
		}
	}
	if failed {
		return nil, true, errs
	}
	return hashes, false, errs
}

// resolveDuplicates picks one representative per key. Interactive pick is
// used in push/pull/sync; compare never prompts and skips the key; -y skips
// everywhere.
func (a *App) resolveDuplicates(list []*fonts.Font, mode string) (map[string]*fonts.Font, []report.Entry, []report.Entry) {
	reps := map[string]*fonts.Font{}
	var skips, notChosen []report.Entry

	byKey := map[string][]*fonts.Font{}
	for _, f := range list {
		byKey[f.Key] = append(byKey[f.Key], f)
	}
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, key := range keys {
		group := byKey[key]
		sort.Slice(group, func(i, j int) bool { return group[i].Path < group[j].Path })
		if len(group) == 1 {
			reps[key] = group[0]
			continue
		}
		sameHash := true
		for _, f := range group[1:] {
			if f.SHA256 != group[0].SHA256 {
				sameHash = false
				break
			}
		}
		if sameHash {
			reps[key] = group[0]
			continue
		}

		entry := localConflictEntry(group)
		if mode == "compare" {
			entry.Reason = "local conflict"
			skips = append(skips, entry)
			continue
		}
		if a.Opts.Yes {
			entry.Reason = "-y"
			skips = append(skips, entry)
			continue
		}
		// Interactive pick.
		fmt.Fprintf(a.Stdout, "Local conflict: %d files with key %q have different content\n", len(group), key)
		for i, f := range group {
			fmt.Fprintf(a.Stdout, "  [%d] %s (version %s, sha %s)\n", i+1, f.Path,
				orDash(f.VersionString), fonts.ShortHash(f.SHA256))
		}
		valid := map[string]string{}
		for i := range group {
			valid[fmt.Sprintf("%d", i+1)] = fmt.Sprintf("%d", i+1)
		}
		valid["s"] = "s"
		answer, err := a.prompter.choice(fmt.Sprintf("Choice [1-%d/s]: ", len(group)), valid)
		if err != nil {
			entry.Reason = "user skip"
			skips = append(skips, entry)
			a.Log.Warn("read local conflict choice", "error", err)
			continue
		}
		if answer == "s" {
			entry.Reason = "user skip"
			skips = append(skips, entry)
			continue
		}
		idx := int(answer[0]-'0') - 1
		chosen := group[idx]
		reps[key] = chosen
		for _, f := range group {
			if f == chosen {
				continue
			}
			notChosen = append(notChosen, fontEntry(f, exclude.ReasonNotChosenDuplicate))
		}
	}
	return reps, skips, notChosen
}

// localConflictEntry renders a two-local-file conflict entry.
func localConflictEntry(group []*fonts.Font) report.Entry {
	e := fontEntry(group[0], "")
	e.HasOther = true
	e.SideLabel = "local 1:"
	e.OtherLabel = "local 2:"
	e.OtherVersion = group[1].VersionString
	e.OtherShortHash = fonts.ShortHash(group[1].SHA256)
	e.OtherPath = group[1].Path
	return e
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// fontEntry builds a single-font report entry.
func fontEntry(f *fonts.Font, reason string) report.Entry {
	return report.Entry{
		Family:    f.Family,
		Subfamily: f.Subfamily,
		Key:       f.Key,
		Version:   f.VersionString,
		ShortHash: fonts.ShortHash(f.SHA256),
		Path:      f.Path,
		Reason:    reason,
	}
}

// conflictEntry builds a local-vs-repo conflict entry.
func conflictEntry(local *fonts.Font, repo *workdir.Manifest, reason string) report.Entry {
	return report.Entry{
		Family:         local.Family,
		Subfamily:      local.Subfamily,
		Key:            local.Key,
		Version:        local.VersionString,
		ShortHash:      fonts.ShortHash(local.SHA256),
		Path:           local.Path,
		HasOther:       true,
		SideLabel:      "local:",
		OtherLabel:     "repo:",
		OtherVersion:   repo.VersionString,
		OtherShortHash: fonts.ShortHash(repo.SHA256),
		OtherPath:      repo.RepoPath,
		Reason:         reason,
	}
}

// baseReport builds the shared report skeleton.
func (a *App) baseReport(mode string) *report.Report {
	return &report.Report{Header: report.Header{
		Timestamp: a.Clock(),
		Version:   a.Version,
		Mode:      mode,
		OS:        a.Platform.OSInfo(),
		Source:    a.Opts.Source,
		FontDirs:  append([]string{}, a.fontSources.Dirs...),
		Repo:      a.login + "/fontvault",
		Branch:    a.branch,
	}}
}

// errorEntries converts per-file scan/dir errors into report entries.
func scanErrorEntries(res fonts.ScanResult) []report.Entry {
	var out []report.Entry
	for _, e := range res.Errors {
		out = append(out, report.Entry{Path: e.Path, Reason: e.Err.Error()})
	}
	for _, e := range res.DirErrors {
		out = append(out, report.Entry{Path: e.Path, Reason: e.Err.Error()})
	}
	return out
}

// finalCode computes 0 vs 2 from collected per-file errors and
// automatically skipped conflicts.
func finalCode(errs int, autoSkips int) int {
	if errs > 0 || autoSkips > 0 {
		return ExitCompleted
	}
	return ExitOK
}

// isAutoSkipReason reports whether a skip counts toward exit code 2.
func isAutoSkipReason(reason string) bool {
	return reason == "-y" || strings.HasPrefix(reason, "OS default") ||
		reason == "registry name collision" || reason == "local conflict"
}

// ensureInstallDir creates the install directory (post-confirmation).
func (a *App) ensureInstallDir() error {
	if a.installDir == "" {
		dir, err := a.Platform.InstallDir(a.Opts.Source)
		if err != nil {
			return err
		}
		a.installDir = dir
	}
	return os.MkdirAll(a.installDir, 0o755)
}
