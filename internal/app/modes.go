package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
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

// runModes prepares the workdir, scans fonts, and dispatches to the mode.
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
	sources, err := a.Platform.FontSources(cli.SourceSystem)
	if err != nil {
		failed = true
		errs = append(errs, report.Entry{Path: "system font dirs", Reason: err.Error()})
		return nil, true, errs
	}
	res := fonts.Scan(fonts.ScanOptions{
		Dirs:       sources.Dirs,
		Recursive:  sources.Recursive,
		ExtraFiles: sources.ExtraFiles,
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

// resolveDuplicates picks one representative per key. Files that share a key
// with the same content are one font; files that share a key with different
// content are a local conflict, where push/pull/sync ask the user which file
// stands for the key and compare or -y skip the key. It returns the
// representatives, one entry per skipped key, and one entry per file the user
// did not choose. Byte-identical copies are reported nowhere: they never
// needed a decision.
func (a *App) resolveDuplicates(list []*fonts.Font, mode string) (map[string]*fonts.Font,
	[]report.Entry, []report.Entry) {
	// note: a key with no representative is absent from reps, so no mode acts
	// on it; skips and notChosen are only reported.
	// (push walks sortedReps(reps); pull decides from presentKeys and reads
	// neither entry list)
	// (nothing here writes, deletes or installs, so -n needs no special case)
	reps := map[string]*fonts.Font{}
	var skips, notChosen []report.Entry

	byKey := groupByKey(list)
	// note: sorted keys keep the prompt order stable; map iteration would ask
	// about the groups in a random order on every run. The report sorts its
	// own sections, so this order is only about the prompts.
	// (a key is visited once per run, and resolveDuplicates runs before the
	// mode dispatch, so a local pick cannot change during sync)
	for _, key := range sortedGroupKeys(byKey) {
		// note: path order fixes the prompt numbering and the two files the
		// conflict entry pairs; the same sorted slice feeds the printed list,
		// the answer parse and the entry, so a printed number always names a
		// file this run can use.
		// note: sortedByPath copies the group, so nothing below mutates it.
		group := sortedByPath(byKey[key])
		// note: sameContent is true for a single file, so the old length check is
		// gone; an empty group cannot be built here. The extra copies of a
		// byte-identical key are not errors, skips or exclusions: the key has
		// one identity, so nothing was given up.
		// (any copy works: identical bytes give identical metadata and hash)
		if sameContent(group) {
			reps[key] = group[0]
			continue
		}

		// note: a skipped key stays out of reps, so no mode acts on it,
		// and its files on disk stay untouched. reason says why the key was
		// skipped.
		// (compare and push keep that key out of the repo; pull does not
		// install its repo copy either, because collectPresentKeys marks
		// every scanned font for pull)
		// note: reason is read by isAutoSkipReason, so its wording stays
		// literal: compare yields "local conflict", -y yields "-y", and a
		// declined or unreadable prompt yields "user skip".
		// (a read failure is a skip, never a guess: a file the user never
		// chose must not be picked, and one failed prompt must not abort
		// the run)
		chosen, reason := a.chooseLocalFont(key, group, mode)
		// (only push, pull and sync reach a prompt; compare and -y answer
		// without asking, which is what chooseLocalFont checks)
		if chosen == nil {
			// A skipped key is reported as a two-sided conflict entry whose
			// reason is printed under "Conflicts skipped".
			// note: reason is never empty here, because every skip path in
			// chooseLocalFont supplies one.
			// (the group has at least two files, since the sameContent just
			// returned false; a larger group shows only its first two paths,
			// which is all the entry has room for)
			entry := localConflictEntry(group)
			entry.Reason = reason
			// note: the reason belongs to both the report text and the exit-code
			// rule, so the strings must not be renamed in one place only.
			// (isAutoSkipReason counts "-y" and "local conflict" because nothing
			// could be asked; a user-chosen skip does not raise exit code 2)
			// (one entry per key: a group is visited once and this pass never
			// re-runs, so no keyed dedup map is needed, unlike push's
			// rebase retries)
			skips = append(skips, entry)
			// (compare and push list these under Conflicts skipped and count
			// the automatic ones for exit code 2; pull builds its own skipped
			// list and reads neither list here)
			continue
		}

		// The file the user picked is the key's representative.
		reps[key] = chosen
		// note: the files the user left out become "Excluded locally" entries,
		// so the report says why they were not archived. They are not skips,
		// so they do not change the exit code.
		// (compare.go and push.go append notChosen to their excluded section;
		// pull never reads it, and pull archives nothing)
		for _, f := range group {
			// note: Font pointers, so this compares identity: a group holds one
			// Font per scanned file and never the same one twice.
			if f != chosen {
				// (the reason constant comes from the exclude package, which owns
				// that section's wording)
				notChosen = append(notChosen, fontEntry(f, exclude.ReasonNotChosenDuplicate))
			}
		}
	}
	return reps, skips, notChosen
}

// chooseLocalFont decides what to do with local files that share a key but
// differ in content: it returns the file that represents the key, or nil with
// the reason the key was skipped.
func (a *App) chooseLocalFont(key string, group []*fonts.Font, mode string) (*fonts.Font, string) {
	// note: compare is read-only and -y answers confirmations without resolving
	// conflicts, so both skip the key. The two reasons are automatic skips,
	// which isAutoSkipReason counts toward exit code 2.
	// (Run forces a non-TTY run to -y, so it takes the second branch)
	// (the key is absent from reps either way, so no mode touches it)
	if mode == "compare" {
		return nil, "local conflict"
	}
	if a.Opts.Yes {
		return nil, "-y"
	}

	// Otherwise ask which file stands for the key.
	// note: the numbers promptLocalChoice prints are positions in this very
	// slice, which the caller sorted by path, so the answer and the index
	// below cannot disagree.
	// note: an unreadable answer (EOF, closed stdin) is a user skip rather
	// than a fatal error: a file the user never chose must not be picked,
	// and one failed prompt must not abort the run.
	// (pushConflict and the pull conflict code treat a read failure the same
	// way)
	// (the key keeps no partial decision: it is simply absent from reps)
	// (prompter.choice reports an error only when it read no line at all,
	// so no answer at all reaches the parse below)
	answer, err := a.promptLocalChoice(key, group)
	if err != nil {
		// (the log keeps the failure visible on stderr, where -v shows it too)
		// (the reason matches a declined prompt: either way the user decided,
		// and a user-chosen skip alone does not raise exit code 2)
		a.Log.Warn("read local conflict choice", "error", err)
		// (the caller's loop moves on to the next key)
		return nil, "user skip"
	}
	// note: "s" skips the key, the same letter the local-vs.-repo conflict
	// prompts use. No file was chosen, so none is rejected: the whole key
	// is reported once, as a skipped conflict.
	if answer == "s" {
		return nil, "user skip"
	}
	// note: the whole answer is parsed rather than only its first character, so
	// a group with more than nine files can be chosen. One character would
	// map the choice "10" to file 1.
	// (promptLocalChoice offers only these options and re-asks anything else,
	// so an unusable answer here would be a bug elsewhere: skipping is safer
	// than guessing, and an out-of-range index would abort the run)
	// (its bound comes from the same group length that promptLocalChoice
	// numbered, so an accepted choice is always in range)
	// (a group cannot change between the prompt and the parse: nothing here
	// mutates it, and sortedByPath handed out a copy)
	// (so a number the user typed always names a file that exists here,
	// and a malformed one can be skipped without risking a panic)
	idx := localChoiceIndex(answer, len(group))
	if idx < 0 {
		// A malformed answer cannot be turned into a file: skip the key.
		// (no log: the answer was read fine, it was just unusable)
		// (the reason follows the other declined prompts, so exit code 2 stays
		// reserved for automatic skips)
		// (the key gets one skipped-conflict entry, not one per file: nothing
		// was chosen and nothing was rejected)
		return nil, "user skip"
	}
	// note: the prompt numbers start at 1 and the slice starts at 0. An empty
	// reason means the key was resolved, not skipped: the caller reads reason
	// only when the font is nil.
	// (the caller reports the other files of the group as unchosen duplicates)
	// (push, pull and sync act on the returned representative)
	// note: localChoiceIndex returns -1 or an index inside the group, so the
	// access below is always in range.
	return group[idx], ""
}

// promptLocalChoice prints the conflicting files and asks which one
// represents the key. The answer is a printed number or "s".
func (a *App) promptLocalChoice(key string, group []*fonts.Font) (string, error) {
	_, _ = fmt.Fprintf(a.Stdout, "Local conflict: %d files with key %q have different content\n", len(group), key)
	// note: these numbers are also the answers accepted below, and chooseLocalFont
	// maps them back into this same slice, so a number always names what the
	// user just saw.
	// (the version string and the short hash are the differences the user can
	// act on, so both are shown orDash covers a font with no version string, which is valid)
	// (compare and -y never print this list, so their skip entries only carry
	// the first two paths, see localConflictEntry, which pairs them)
	for i, f := range group {
		_, _ = fmt.Fprintf(a.Stdout, "  [%d] %s (version %s, sha %s)\n", i+1, f.Path,
			orDash(f.VersionString), fonts.ShortHash(f.SHA256))
	}
	// note: every answer maps to itself, because prompter.choice validates the
	// keys and hands the choice back unchanged.
	// (a local group has no letter options, unlike a local-vs.-repo conflict:
	// the files have no meaning per mode, only a position)
	// ("s" skips the key, the same letter the other conflict prompts use)
	// (a skipped key is absent from reps, which is the same outcome for every
	// mode: nothing is archived, replaced or installed for it)
	answers := map[string]string{"s": "s"}
	// note: one option per file, so a group with more than nine files asks and
	// answers with two digits.
	// (the prompt range below is printed from the same group size, so the
	// shown choices and the accepted options cannot disagree)
	for i := range group {
		choice := fmt.Sprintf("%d", i+1)
		answers[choice] = choice
	}
	// note: prompter.choice re-asks anything outside this set, so the answer
	// returned here is always one of the printed options or "s".
	// (a read failure is returned as an error, which skips the key)
	// (see chooseLocalFont, which maps the answer to a group position)
	// (and localChoiceIndex, which does the parsing)
	// (nothing is written here: printing only)
	return a.prompter.choice(fmt.Sprintf("Choice [1-%d/s]: ", len(group)), answers)
}

// localChoiceIndex turns a local-conflict answer into an index in the group,
// or -1 when the answer is a skip or unusable.
func localChoiceIndex(answer string, n int) int {
	// ("s" skips the key; the other answers are 1-based positions.)
	// note: an unusable answer is treated like a decline, because a bad answer
	// must never pick the wrong file.
	// (the caller's map -1 to a skipped key: see chooseLocalFont and
	// resolveDuplicates)
	if answer == "s" {
		return -1
	}
	// note: the whole answer is parsed, not just its first character, so a
	// two-digit choice works. One character would turn the choice "10" into
	// file 1.
	// (prompter.choice never offers anything else, so a parse failure here
	// would be a bug elsewhere: skip rather than guess)
	// (an empty answer fails to parse and skips, which is what a truncated
	// read would otherwise produce)
	// (Atoi rejects anything that is not a plain decimal number that covers
	// every answer that could not be offered)
	v, err := strconv.Atoi(answer)
	// note: a negative index reaching a caller would panic the group access,
	// so -1 is the safe answer.
	if err != nil {
		// (a parse failure is not fatal: the run continues with the next key)
		// (no file of the key is chosen, so nothing is archived for it)
		// (the caller reports one skipped-conflict entry for the whole group)
		return -1
	}
	// note: the printed positions run 1…n, so anything outside that range is
	// unusable.
	// (an out-of-range choice is skipped rather than clamped or wrapped: both
	// would silently pick a file the user did not name)
	// (n is the group length, the same length promptLocalChoice numbered, so
	// a valid choice is always inside)
	if v < 1 || v > n {
		// (the caller reports the whole group as one skipped conflict)
		return -1
	}
	// note: the printed numbers start at 1, the slice starts at 0.
	// (the callers read group[idx] as the representative: see resolveDuplicates)
	// note: a group of one never comes here, since the sameContent accepted it
	// first, and an empty group cannot be built, so v-1 is a real index.
	return v - 1
}

// groupByKey groups local files by font key.
func groupByKey(list []*fonts.Font) map[string][]*fonts.Font {
	g := map[string][]*fonts.Font{}
	// note: each group is appended in scan order; the callers sort every group
	// they use, see sortedByPath.
	// note: a key is never empty here, so an empty group cannot be built.
	// (groupByKey only buckets what the run already validated: every font in
	// a list has a key, since a font without a name is invalid)
	for _, f := range list {
		g[f.Key] = append(g[f.Key], f)
	}
	return g
}

// sortedGroupKeys returns the keys of a group map in a fixed order.
func sortedGroupKeys(groups map[string][]*fonts.Font) []string {
	keys := make([]string, 0, len(groups))
	// note: map iteration is randomized, so the keys are sorted before use.
	// (this order decides which group the user is prompted for first)
	// (the report sorts its own sections, so it is not affected)
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// sortedByPath orders one group by file path.
func sortedByPath(group []*fonts.Font) []*fonts.Font {
	// note: the copy keeps the caller's slice untouched: the group map is
	// reused by the loops that read it.
	// (sort.Slice is not stable, so files with the same path may swap; the
	// prompt list, the answer parse and the conflict entry read this copy,
	// so they still agree)
	out := append([]*fonts.Font{}, group...)
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// the sameContent reports whether every file in the group has the content hash of
// the first one, which means the key stands for a single font.
func sameContent(group []*fonts.Font) bool {
	// note: hashing was done while scanning, so hashes are compared rather
	// than bytes.
	// (which file is the reference does not change the result: a group is
	// uniform iff every other copy matches it)
	// (a single file is trivially uniform, so the callers need no length
	// check)
	// (a group whose hashes differ is the local-conflict rule: same key,
	// different content)
	for _, f := range group[1:] {
		if f.SHA256 != group[0].SHA256 {
			return false
		}
	}
	// note: an empty group is reported as uniform; the callers cannot build one.
	// (see groupByKey above, which only creates non-empty groups)
	return true
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

// conflictEntry builds a local-vs.-repo conflict entry.
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

// finalCode computes 0 vs. 2 from collected per-file errors and
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

// countAutoSkips counts the skipped entries that raise exit code 2.
// note: the strings isAutoSkipReason matches are shared with the report text,
// so a reason renamed in one place only would silently change exit codes.
func countAutoSkips(skipped []report.Entry) int {
	n := 0
	for _, e := range skipped {
		if isAutoSkipReason(e.Reason) {
			n++
		}
	}
	return n
}

// ensureInstallDir creates the installation directory (post-confirmation).
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
