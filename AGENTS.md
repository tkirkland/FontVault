# Project memory — session of 2026-11-01

## Project

Same `fontvault` project as before (see session logs of 2026-10-31 and 2026-10-30 below, kept for reference).

## Work done this session (2026-11-01)

- `internal/lockfile/` package (new since the 2026-10-31 notes): cleared the only GoLand diagnostic in
  `alive_windows.go` — the bare `windows.CloseHandle(h)` in `processAlive` (the "Unhandled error" inspection fires
  because this toolchain's `CloseHandle` return is error-typed, same family as `os.Remove`/`File.Close`). Now
  `_ = windows.CloseHandle(h)` with a `// best effort` comment — the handle only probes existence, close failure
  must not change the answer. `processAlive`'s signature and the Linux sibling untouched; `lockfile.go`/
  `alive_linux.go` showed no diagnostics.
- Verified: `gofmt -l -e` clean, `go vet ./internal/lockfile` clean, `go test -count=1 ./internal/lockfile` green;
  `mcp__ide__getDiagnostics` timed out three times afterward (the known post-test-run re-index flakiness), so the
  re-check fell back to gofmt/vet — the discard form is the AST shape the inspection accepts at every other site.
- `lockfile.go`: cleared its only two "Unhandled error" diagnostics — the bare `f.Close()` and `os.Remove(path)` in
  `discardLock` (the failure-path cleanup called from `writeLockFile`). Now `_ =` both with a best-effort comment,
  fsutil-style: the caller still reports the write/sync failure as the cause, control flow unchanged, and a
  leftover half-written lock is later handled by `tryAcquire`'s stale-PID replacement. `Release()` already handled
  both errors (first-error capture) — untouched. Verified: gofmt/vet clean, `go test -count=1 ./internal/lockfile`
  green, and this time `mcp__ide__getDiagnostics` answered: **0 diagnostics** for the file (asked before the test
  run to dodge the re-index timeout). LF endings preserved.
- `lockfile_test.go`: its two "Unhandled error" diagnostics were the bare `lock2.Release()` / `lock.Release()` at
  test-end in `TestAcquireRelease` and `TestStaleLockReplaced`. Handled with `t.Errorf` rather than discarded — unlike
  production best-effort cleanup, a `Release()` failure in a lock-removal test is test-relevant, and the sibling
  first-release site already checked it (same spirit as the `lsLFSObjects` walk fix: handle, don't suppress). Only
  behavioral delta: these tests now fail instead of passing if `Release` misbehaves at cleanup. Verified: gofmt/vet
  clean, `go test -count=1 ./internal/lockfile` green, IDE answered **0 diagnostics** (asked before the test run),
  LF preserved. `internal/lockfile` now has zero IDE diagnostics across all four files.
- DeepSource "high complexity" sweep continued: `internal/osinfo/osinfo_linux.go` `String()` (27 lines: nested
  open-block + scan loop + nested uname block) is now a flat 3-step body — `prettyName()` → `unameString()` →
  `"Linux"`, helpers immediately below (same split convention). `prettyName` keeps the "empty PRETTY_NAME means
  keep scanning" semantics via a guard-clause `continue`; `unameString` returns `""` for "uname failed" and
  "empty sysname" (which `String` turns into the `"Linux"` fallback — same outcomes as the original's nested ifs).
  The bare `defer f.Close()` became `defer closeQuietly(f)` (this toolchain's `File.Close` returns `error`;
  never-bare rule) — only real delta is fd-close ordering: it now closes when `prettyName` exits instead of at
  `String`'s return, unobservable since the scan is complete before `Uname` runs. `String()`'s signature untouched,
  so `platform_linux.go:19`/`platform_windows.go:21` are untouched. Note: the package has **no test file**, and the
  file is `//go:build linux` on a win32 host — verified via `gofmt -l -e` clean, `GOOS=linux go vet`/`go build
  ./internal/osinfo` clean, host `go build ./...` clean, IDE 0 diagnostics; behavior identical by construction,
  no runtime test possible here (same caveat as `pkg_linux.go`). Also kept the user's on-disk `werr`→`wErr`
  rename in `lockfile.go` (not reverted).
- `osinfo_windows.go`: cleared its one "Unhandled error" — bare `defer k.Close()` in `String()` (registry `Key.Close`
  is error-typed here) → `defer closeKeyQuietly(k)` + helper below (sibling of the linux `closeQuietly`); values are
  all read before the close, semantics unchanged. **Editor race:** GoLand's unsaved buffer flushed over this fix
  twice (reverted it while re-adding the user's own header edits); re-applied a third time and IDE then answered
  0 diagnostics. If the user keeps the file open unsaved in GoLand, disk edits keep losing — tell them to
  save/reload in the IDE before agent edits.
- **Reported, not reverted** (user's on-disk edits per session instructions): `osinfo_linux.go` now reads
  `//go:build Linux` — build-constraint identifiers are lowercase, so on Linux the file is excluded and
  `GOOS=linux go build ./internal/osinfo` fails "build constraints exclude all Go files" (verified, exit 1). Also
  both files now have `package osinfo` **before** their `// Package …` doc comment (doc comment detached from the
  package), and the windows one has the text doubled ("Package OS info Package OS info reports…"). gofmt accepts
  the layout; only the capital `Linux` is a hard build break.
- Verified: gofmt clean on both osinfo files, host `go build ./internal/osinfo` + `go vet` exit 0, IDE 0 diagnostics
  on windows file.
- `internal/report/report.go` (same session): cleared all 23 diagnostics in one pass — "high complexity 53" on
  `Render` plus 22 "Unhandled error" on its bare `fmt.Fprintf(&b, …)` calls. `Render` is now a 5-line body
  (`renderHeader` → `sectionsByName` → `renderSummary` → `renderSections`), helpers immediately below:
  `renderHeader` (method, uses `r.Header`), `renderSummary`, `sectionsByName` (same last-write-wins map),
  `renderSections` (loop dispatches to `renderCountsBody` / `(none)` / per-entry `renderEntry`),
  `renderEntry` (errors-shape, conflict block, plain line — original's `continue`s became `return`s),
  `renderConflictEntry`. Every `Fprintf` got the `_, _ =` discard during the rewrite; the `b.WriteString` calls
  are NOT flagged (this toolchain's `Builder.WriteString` returns nothing) so they stay bare — don't "fix" those.
  Helpers take `b *strings.Builder` and must call `fmt.Fprintf(b, …)` — passing `&b` yields `**strings.Builder`
  compile errors (hit this once, fixed with replace_all; the replace ate the space after the comma, one `gofmt -w`
  cleaned it). Byte-identity guarded by `report_test.go` (exact rendered text) plus app integration tests.
  Verified: IDE 0 diagnostics (asked before tests), `gofmt -l -e` clean, `go vet` clean, `go test -count=1 ./...`
  fully green (app 92.9s); LF preserved.
- `internal/workdir/prepare.go`: cleared all 9 diagnostics (complexity 20/42/46 on `GitAttributesComplete`,
  `EnsureReady`, `prepareExisting`, plus 6 "Unhandled error" in `EnsureReady`). Same split convention, all helpers
  immediately below their caller: `GitAttributesComplete` → `hasLFSRule(content, ext)` + `allLFSAttrs(joined)`
  (closure became file-level); `EnsureReady` → 3-line body `lockAndPrepare` / `cloneFresh` / `cloneFailure` — the
  four identical "acquire → prepare → release-on-error" blocks collapsed into `lockAndPrepare` (a fifth into
  `lockAndInit` for the empty-remote path); the leftover-lock scan lives in `cloneFresh`, the two recoverable
  clone-error recoveries in `cloneFailure` (all `fmt` messages byte-identical). Helpers: `lockFileLive(dir)` (plain
  fn), `releaseQuietly(lock)` (`_ = lock.Release()`, `removeTempQuietly` precedent) replaced the 5 bare
  `lock.Release()`, and the bare dead-leftover `os.Remove` got `_ =` + comment. `prepareExisting` →
  `checkRemote` + `checkClean` + `prepareUnborn` + `prepareBranch` (the first `RevParse` value was dead in both
  arms, so it's `if _, headErr := RevParse` in the caller; each branch re-parses HEAD as before). **Threshold note
  for future sweeps: the IDE flags cognitive complexity at 15, not 20** — `EnsureReady` still read 15 after the
  first split and needed the extra `cloneFresh`/`cloneFailure` extraction. No `os.DirEntry` named (helper keeps
  the loop inside `cloneFresh` per the DirEntry caution). Verified: IDE 0 diagnostics, `gofmt -l -e` clean,
  `go vet` clean, `go test -count=1 ./...` fully green (app 94.3s), LF preserved.

## Project memory — session of 2026-10-31

## Project

Same `fontvault` project as before (see session log of 2026-10-30 below, kept for reference).

## Work done this session (2026-10-31)

Goal: reduce the flagged "high complexity" of `TestFirstPushToEmptyRepo` in `internal/app/integration_test.go`
while preserving behavior.

- Rewrote the test body as a flat checklist and extracted six single-purpose `check*` helpers immediately below it (repo
  convention: `check*`/`require*` names, `t.Helper()`, same package, unexported):
    - `checkInitThenFontsCommits` — two-commit history, `t.Fatalf` on wrong commit count (preserved from original)
    - `checkLFSAttributes` — per-extension loop replaces the `||`-joined contains check (reports which pattern is
      missing)
    - `checkFontBlobsArePointers` — guard-clause `continue` instead of nested `if suffix { ... }`
    - `checkManifestFileCount`, `checkWorktreeHasNoFonts`, `checkPushReportWritten` (report check now a
      `for want := range []string{...}` loop like `TestCompareOutputForFixtureSet`'s)
- Replaced the `func() testfont.Spec { ... }()` IIFE for the OTTO Bravo fixture with three plain statements
  (`bravo := fontSpecB(); bravo.OTTO = true; ...`).
- All other tests untouched; helper names are generic in case `TestSparseWorktreeNoFontsAfterPush` etc. want to reuse
  them later (deliberately not rewired this session to keep the diff focused).
- Verified: `gofmt -l -e` clean, `go vet` clean, `go test ./...` all pass, LF endings preserved. The edit tool handled
  the multi-line Go payload fine this session (no Python patch scripts needed).

### `runPull` complexity (internal/app/pull.go)

- Same goal, same pattern: `runPull` (was ~138 lines: three decision passes + dry-run block + install loop + a
  report tail that existed twice) is now a flat body — local lookup → `a.pullTargets` → `sortedTargetKeys` → dry-run
  return → installer → `a.applyPullTargets` → `installer.Finish` → `a.finishPull`. All helpers sit immediately
  below it in `pull.go`:
    - `pullTargets(st, localByKey, useRepo) (map[string]*pullTarget, []report.Entry)` — the three passes (missing
      keys / local-vs.-repo conflict / sync "use repo"), returning targets plus the declined conflicts as
      skipped entries.
    - `a.pullConflict(local, m, classifier) (*pullTarget, string)` — one conflict: protection check → `-y` →
      prompt; `nil` + reason means skipped. The caller builds `conflictEntry(local, m, reason)` (the old
      pre-built `entry` plus `entry.Reason = …` mutation is gone — same entry, constructed once).
    - `sortedTargetKeys`, `a.printPullDryRun`, `a.applyPullTargets` (forwards `pullOne`'s existing pointer-list
      params), `a.finishPull(rep, st, …) int` — one shared tail for the dry-run and real paths.
- `countAutoSkips(skipped)` added in `modes.go` next to `isAutoSkipReason`; pull uses it, while `push.go`/`compare.go`
  keep their inline loops (not rewired this session, same "keep the diff focused" rule).
- `pullOne` (outside the selection) and `runPull`'s signature are untouched, so `sync.go:23` and `modes.go:67`
  are untouched.
- Behavior identical by construction: report section order preserved (matters for sync's merged report), nil
  `installed`/`replaced` in the dry-run path as before, `a.Platform.Classifier()` still taken once per run,
  `pullPhaseOK` and `finalCode` semantics unchanged. Verified: `gofmt -l -e` clean, `go vet ./...` clean,
  `go test ./internal/app -run 'Pull|Sync|Duplicate'` green, then full `go test ./...` green; LF endings preserved.

### `pullOne` complexity (internal/app/pull.go), same session

- Second pass on the file: `pullOne` (was ~110 lines: fetch+verify, temp-file write, missing-install branch,
  replace-local branch, four shared report lists) is now ~15 lines — `newPullJob` → `pullBlob` → `writePullTemp`
  → `defer removeTempQuietly` → `installMissingFont` / `replaceLocalFont`.
- Introduced `pullJob` (right below `pullOne`): `{t, meta, metaName, target, installed, replaced, skipped, errs}`
  bundles the target with the installer names and the four report lists, so the flows take
  `(ctx, installer, job, tmpName)` instead of nine parameters. The lists are held **by pointer** as before, so the
  job is passed by value and appends still reach `runPull`'s slices. `a.newPullJob` derives `meta.Filename` and
  `target` from one `filepath.Base(m.RepoPath)`, so a skipped entry's path and the installer's path cannot drift.
- `a.pullBlob` (cat-file | lfs smudge + hash + parse verify; hex digest computed once instead of twice),
  `a.writePullTemp` (create/addTemp/write/sync/close; still does NOT remove the file on failure — same as the
  original, where the `defer` was registered only after a successful close, so cleanup is `addTemp` at run end),
  `a.installMissingFont`, `a.replaceLocalFont`, and `recordSkipped(job, reason)` which replaces the four
  hand-written `*skipped = append(*skipped, conflictEntryForPath(target, m, "registry name collision"))` sites (the
  reason strings stay literal).
- `pullOne`'s own signature is unchanged, so `applyPullTargets`'s call is untouched; `conflictEntryForPath` and
  `removeTempQuietly` are untouched.
- Verified again: `gofmt -l -e` clean (used `gofmt -w` once to fix the `pullJob` field alignment), `go vet` clean,
  pull/sync tests green, full `go test ./...` green.

### DeepSource "unhandled error" in `writePullTemp`, same session

- The flagged spots were the bare `tmp.Close()` calls on the write-failure and sync-failure paths (pre-existing
  since the initial commit, surfaced by the split). Both now call `a.closeTempAfterFailure(tmp)`, which closes the
  handle and `a.Log.Warn`s a close failure instead of dropping it; the caller's write/sync error stays the reported
  cause, the close still happens exactly once per path, and the file is still left for `addTemp`'s cleanup at run
  end (app.go:186 logs those removals). Messages, control flow and exit codes unchanged.
- Repo convention the user restored by hand: writes to `a.Stdout`/`a.Stderr` use `_, _ = fmt.Fprintf(...)` — use that
  form in this package (push.go already does).
- Same kind of discard still present, deliberately not touched: `removeTempQuietly`'s `_ = err` (best-effort, the
  run's `addTemp` cleanup reports removal failures). `internal/install/install.go`'s `removeQuietly` has the same
  shape with a `// best effort rollback` comment.

### DeepSource "unhandled error" in `lsLFSObjects` (internal/app/testenv_test.go)

- The flagged spot was the bare `filepath.Walk(root, func(...) error {...})` call (testenv_test.go:153). Note for
  future sessions: this toolchain's `path/filepath` is not upstream-shaped — `Walk(root, WalkFunc)` **returns
  `error`** (non-nil stops the walk) and `WalkFunc` is `func(path, fs.FileInfo, err) error`, so discarding the call
  really is dropping an error, not just an int count.
- Fixed in place, behavior identical: the call result now goes to `walkErr`, and a non-nil value fails the test
  (`e.t.Fatalf("walk %s: %v", root, walkErr)`) instead of asserting against a truncated listing. The visitor still
  swallows per-entry errors, so a missing `workdir/.git/lfs/objects` root keeps returning the empty list as before (its
  only caller, integration_test.go:718, expects a non-empty list there anyway).
- Repo convention stays "handle, don't suppress" (see commit c8f76d4 turning a bare `fmt.Fprintf` into `_, _ =`);
  explicit discards (`entries, _ := os.ReadDir(dir)` in report/config/integration tests) are left alone.
- Verified: `gofmt -l -e` clean, `go vet ./internal/app` clean, `go test ./...` all green, LF endings preserved.

### GoLand diagnostics sweep: `testenv_test.go` fakes + `internal/cli/cli.go`, same session

- `testenv_test.go`: 12 "Unused parameter" warnings on the fakes (`fakeGitHub.CurrentUser/RepoExists/CreateRepo`,
  `fakePlatform.InstallDir/NewInstaller/CheckPullPrereqs`) — unused params renamed to `_`, same as `fakeInstaller`
  already did; interface signatures unchanged. `err.Is` was NOT used here (see Notes).
- `cli.go`: `err == pflag.ErrHelp` → `errors.Is(err, pflag.ErrHelp)` (added `"errors"` import; verified this SDK's
  `errors.Is` at `src/errors/wrap.go` is the standard unwrap-chain form). Bare `fmt.Fprint(w, usageTemplate)` →
  `_, _ = fmt.Fprint(...)` per the discard convention.
- Verified: `gofmt -l -e` clean, `go vet ./internal/cli` clean, `go test ./internal/cli` green, `go test ./...` green,
  IDE reports 0 diagnostics for both files; LF endings preserved.

### Diagnostics sweep continued (same session): config, exclude, fonts, fsutil

- `config.go`: deleted dead `KindNone` sentinel (`Load` signals "absent" as `(nil, nil)`); `err.(*ReadError)` →
  `var re *ReadError; errors.As(err, &re)` (the form `config_test.go` uses); unused `DefaultPath` wired up —
  `app.go:374`'s identical join now calls `config.DefaultPath(a.appDir)`, param renamed `exePath`→`exeDir`.
- `config_test.go`: complexity-16 `TestSaveLoadAtomicAndMode` → helpers `loadCfg`/`checkCfgMode`/`checkNoLeftoverTemps`
  below the test (the nil check in `loadCfg` also cleared 2 potential-nil-deref warnings).
- `resolve_test.go`: complexity-24 table test → named `tokenCase` struct + `checkTokenOutcome`/`checkTokenFailure`.
- `exclude.go`: complexity-24 `Apply` → flat loop + `classifySystemFont`/`classifyUserFont`/`applyExclusionFile`
  (Decision by pointer; reasons and fail-closed semantics verbatim).
- `owner_windows.go`: unused `logf` in `newOSClassifier` → `_` (signature shared with the Linux sibling).
- `pkg_linux.go`: `queryRpm`/`queryPacman`/`queryApk` (15/15/18) → shared `outputLines`/`ownedPath`/`requireAnswered`
  helpers; error strings parameterized by manager name, byte-identical output. File is `//go:build linux`, so verified
  with `GOOS=linux go build`/`vet` (host is win32) — no Linux runtime test possible here; parse regressions would
  surface
  as "query did not answer" on a real Linux box.
- `fonts/scan.go`: local `real` collided with an SDK builtin → `realPath` (the on-disk `scanDir` rename matches).
- `fsutil.go`: 4 cleanup best-effort calls got `_ =` + comment (`AtomicWrite` cleanup closure, post-Close and
  post-Rename removals); deleted uncalled `CopyFile` and `DirWritable` — app's own `checkDirWritable` (5 call sites) has
  different messages/close-precedence, so no rewiring. Dropped the then-unused `io` import.
- Verified after each step: gofmt clean (used `gofmt -w` once for a leftover blank line), `go vet ./...` clean, final
  `go test -count=1 ./...` fully green (app 88.5s).

### `LoadIndex` complexity (internal/workdir/manifest.go), same session

- `LoadIndex` (39 lines, recursive `var walk func(dir string); walk = func…` closure capturing `idx`/`errs`) is now an
  8-line body: join root → probe `os.ReadDir(root)` → `load.walk(root)` → return `load.idx, load.errs`. Helpers sit
  immediately below it (same convention as the `pull.go` split):
    - `indexLoad` struct `{idx *Index, errs []error}` — the accumulator the closure used to capture; built as
      `&indexLoad{idx: NewIndex()}`, `errs` stays nil until something fails (nil slices append fine, and the tests'
      `len(errs) != 0` checks read nil as "no errors" exactly as before).
    - `(l *indexLoad) walk(dir)` — read dir, recurse on subdirectories, everything else → `addManifest`.
    - `(l *indexLoad) addManifest(dir, name)` — `.json` guard, `Load`, then both index maps.
    - `(l *indexLoad) dirUnreadable(dir, err)` — one place builds `"read manifest dir %s: %w"`, so the root probe and
      the walk share the message text (previously duplicated).
- Behavior identical by construction: the separate root probe is what keeps "manifest/ absent → empty index, no errors"
  distinct from "unreadable root → one error" (`TestLoadIndexMissingDirIsEmpty` covers the first half,
  `TestLoadIndexCorruptManifestIsPerFileError` the per-file half); visit order is still `os.ReadDir` order with
  recursion inline, so index insert order and error order are unchanged. `LoadIndex`'s signature is untouched, so
  `modes.go:99` and `push.go:600` are untouched.
- Note for future sessions: **nothing in this repo names `os.DirEntry`** — entries are only ever used via `e.Name()` /
  `e.IsDir()`. Given this toolchain's non-upstream stdlib shape, keep helper parameters as plain `strings` (like
  `addManifest(dir, name)`) rather than naming that type; grep `DirEntry` before introducing it.
- Verified: `gofmt -l -e` clean, `go vet ./...` clean, `go test -count=1 ./internal/workdir` green, then full
  `go test -count=1 ./...` fully green (app 94.1s); LF endings preserved (0 CR bytes in the file).

### `readNameTable` complexity (internal/fonts/meta.go), same session

- Same shape a third time. `readNameTable` (58 lines: header validation + a 12-record byte-slicing loop + a
  priority-then-order merge written as a 3-clause `||` chain + a projection) is now 10 lines: `nameTableHeader` →
  `bestNameCandidates` → `nameValues`. Helpers immediately below it:
    - `nameHeader {base, records, stringOffset int}` + `nameTableHeader(data)` — table lookup, the
      `length < 6` guard, format 0/1 check, the `base+6+12*count > len(data)` check. All six error strings
      byte-identical,
      including `"invalid font: unsupported name table format %d"`; returns `nameHeader{}` alongside the error.
    - `nameRecord {platform, encoding, id uint16; order int; raw []byte}` + `nameRecordAt(data, h, i)` — the 12-byte
      record parse and the `"name string out of bounds"` abort in one place; `order` carries the record index so
      tie-breaking needs no extra parameter.
    - `nameRecord`-level `addCandidate(best, r)` — decodes, skips undecodable records, records when it wins.
    - `outranks(c, prev)` replaces the
      `c.priority > prev.priority || (c.priority == prev.priority && c.order < prev.order)`
      chain (enumerated: higher/lower/equal priority all match the original).
    - `candidate` moved from a function-local type to file scope (required so the helpers can name it).
- Order of operations per record is unchanged: bounds check before decode, so "abort" vs "skip" land on the same records
  as before; iteration is still table order `0..records-1`, so the same candidate wins. `readNameTable`'s signature is
  untouched, so `meta.go:88` in `Parse` is untouched.
- `gofmt -l -e` flagged only my `nameRecord` field alignment; fixed with `gofmt -w` (re-checked: file now gofmt-clean).
- Verified: `gofmt` clean, `go vet ./...` clean, `go test -count=1 ./internal/fonts` green, full
  `go test -count=1 ./...`
  fully green (app 92.0s); LF endings preserved.

## Session log — 2026-10-30

## Project

`fontvault` — cross-platform CLI font archiver (Go). Syncs local `.ttf`/`.otf` fonts with a GitHub repo (`fontvault`)
via Git LFS. Modes: `push`, `pull`, `sync`, `compare` (default). Code layout: root `main.go`-style entry plus
`internal/app` (modes, push, pull, prompt, report, compare, duplicates) and `internal/stdlib`.

## Work done this session

Goal: reduce the complexity of `resolveDuplicates` in `internal/app/modes.go` while preserving behavior and the
package/test API.

- Split the monolithic `resolveDuplicates` (was ~124 lines, deeply nested) into small single-purpose helpers, all added
  in `modes.go`:
    - `groupByKey` — bucket remote entries by key
    - `sortedGroupKeys` — deterministic iteration order for groups
    - `sortedByPath` — deterministic within-group ordering
    - `sameContent` — true when every file in a group has one content hash (also covers single-file groups)
    - `chooseLocalFont` — decision layer (auto-skip vs. pick)
    - `promptLocalChoice` — local conflict prompt
    - `localChoiceIndex` — parse the chosen index
- `resolveDuplicates` now reads as one flat loop: group → sort → decide via `a.chooseLocalFont(key, group, mode)` →
  bookkeeping (skips get `localConflictEntry` + `entry.Reason = reason`; unchosen files still get
  `fontEntry(f, exclude.ReasonNotChosenDuplicate)`).
- Merged the old `len(group) == 1` and `sameHash` branches into one `sameContent` check.
- Function signature kept identical so `modes.go:60` and the 5 call sites in `duplicates_test.go` are untouched.
- Latent bug fixed during refactor: selection index was parsed as `int(answer[0]-'0') - 1` (single digit only); replaced
  with a full-answer parse (`strconv.Atoi` in `localChoiceIndex`). Out-of-range or unparsable answers now return `-1`
  and are treated as "skip" instead of panicking or picking the wrong file.
- Added `"strconv"` to `internal/app/stdlib/__init__.go`'s import list (sorted: `"sort"`, `"strconv"`, `"strings"`).

### Report gap fix (push/sync), test-first

- `runPushPhase` (`push.go`) now merges `st.localConflictSkips` into the "Conflicts skipped" section **and** the exit-2
  auto-skip tally via a combined `skippedAll` slice, matching compare. Safe without dedup or pre-sorting: the two lists
  are disjoint by construction (skipped keys are absent from `st.reps`, and `res.skipped` only ever holds `st.reps`
  keys), `pushWithRetries` re-runs only apply/verify/commit (never `runPushPhase`), and `report.AddSection` sorts each
  section. Sync is covered automatically — it merges the push-phase sections and already ORs either phase's exit 2.
- Corrected the claim in this file's own "Open" item: the **excluded section was never a gap** — `push.go` has appended
  `st.notChosen` since the initial commit (`4e1cf5e`). Only the Conflicts-skipped half was real.
- Test: `TestPushLocalDuplicateUnderYesSkipsAndReports` in `integration_test.go` (RED confirmed: exit 0 where the spec
  says 2; GREEN after the fix). Updated the now-stale comment in `modes.go` ("compare and push list these…").

## Verification

- `modes.go` uses LF line endings; `gofmt -l -e` is clean, `go build ./...` and `go test ./...` pass (including
  `internal/app` with `duplicates_test.go`).
- No `go` on PATH by default. Working toolchain: `C:\Users\Tony\sdk\go1.27.3` (GOROOT per `.idea/workspace.xml`;
  `go.exe`/`gofmt.exe` in its `bin`). Example:
    - `& 'C:\Users\Tony\sdk\go1.27.3\bin\gofmt.exe' -l -e 'internal\app\modes.go'`
    - set `$env:GOROOT='C:\Users\Tony\sdk\go1.27.3'` and prepend its `bin` to PATH, then
      `go build ./... ; go test ./...`
- Edits had to be applied with byte-precise Python patch scripts (the edit tool mangled multi-line Go payloads). Temp
  scripts lived in `C:\Users\Tony\AppData\Local\Temp\opencode`.

## Open / unresolved (not changed, pending user decision)

1. **Pull-mode report gap (remaining)**: `pull` still builds "Conflicts skipped" only from its own repo-conflict list,
   so a
   `-y`/declined local-duplicate skip is neither listed nor counted for exit 2 in pull (the key also stays out of the
   repo
   because `collectPresentKeys` marks it present, so the user silently loses both copies). The `modes.go` comments call
   this deliberate; the identical merge would fix it if the user decides pull should honor "skipped and listed" too.
   The push/sync half is fixed — see "Report gap fix" under "Work done".
2. **Reason strings** (`"-y"`, `"user skip"`, `"local conflict"`) are hardcoded string literals in `modes.go`,
   `push.go`, `pull.go`, and `compare.go`'s `isAutoSkipReason` (which counts auto-skips for the exit-2 tally). A shared
   `reason.go` of constants would be tidier but would touch many files/exports; not done.
3. All changes are uncommitted — review with `git diff`.

## Notes for future sessions

- Prompt semantics: `prompter.choice` lowercases/trims, re-asks on invalid input, and only returns `ok=false` when no
  line could be read; an error is reported then treated as a skip.
- `compare.go` consumes `localConflictSkips`/`notChosen` entry lists; `push.go` consumes both for its report (skips and
  excluded); `pull.go` reads neither.
- No dedicated memory tool/plugin exists in this environment; this file is the project memory convention (`AGENTS.md` at
  repo root, read by future agent sessions).
- Toolchain shape (go1.27.3): the `error` interface has **no** `Is`/`Visit` methods — `err.Is(target)` is a compile
  error ("type error has no field or method Is"). Use `errors.Is`/`errors.AsType` (this SDK names it `AsType`, not
  `As`; `errors.Is` at `src/errors/wrap.go` has the usual unwrap-chain semantics). Same family as the `filepath.Walk`
  non-upstream shape noted above.
- SDK builtins collide with plain names: `real` is a builtin (GoLand warns "collides with the 'builtin' function").
  Prefer names like `realPath` for locals; check other flagged names (scan.go was fixed this way).
- `os.Remove`/`File.Close` return `error` in this toolchain, so bare calls are real dropped errors — silence
  deliberately with `_ =` + comment, never leave bare.
- GoLand `mcp__ide__getDiagnostics` intermittently times out (often right after a long `go test` run re-indexing);
  retry, or fall back to `gofmt -l -e` + `go vet` for verification and ask the user for the squiggle text.
