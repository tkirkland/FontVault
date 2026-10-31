# Project memory — session of 2026-10-30

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
