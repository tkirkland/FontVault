# Task: Build `fontvault` — a cross-platform font archiver that syncs TTF/OTF fonts between the local machine and a GitHub repo via Git LFS

## Role and constraints

You are writing production-quality Go. Implement exactly what this spec describes. Do not add features, flags or behaviors that are not specified. If something in the spec is ambiguous or contradictory, stop and ask before choosing. Do not silently pick an interpretation.

## Goal

A CLI tool that works with TrueType (`.ttf`) and OpenType (`.otf`) fonts installed on the local machine (Windows or Linux) and a GitHub repository named `fontvault`, owned by the authenticated account, used as a font archive stored in Git LFS.

Modes:

- **push:** archive local fonts to the repo, excluding fonts that ship with the OS.
- **pull:** install repo fonts that are missing locally.
- **sync:** push, then pull.
- **compare:** read-only comparison report. This is the default when no direction is given.

## Language, build and dependencies

- Go (current stable). `CGO_ENABLED=0`. The output is a single static executable.
- Targets: `windows/amd64`, `windows/arm64`, `linux/amd64`, `linux/arm64`.
- **Allowed dependencies:** the standard library, plus these modules:
  - `golang.org/x/sys` (Windows ACLs, registry, gdi32/user32 via lazy DLL loading)
  - `golang.org/x/image/font/sfnt` (font parsing)
  - `golang.org/x/term` (TTY detection)
  - `github.com/spf13/pflag` (short and long switches)
- Any other dependency must be justified first and must not break single-binary static output.
- Use `_windows.go` / `_linux.go` files or build tags for platform-specific code. No runtime `GOOS` switches for logic that needs platform APIs.

## External runtime dependencies

- `git` and `git-lfs` must be on PATH. The tool shells out to them. It does not use go-git.
- `gh` is optional and is used only as a token fallback.
- `fc-cache` is optional on Linux. If it is absent, warn but do not fail.

## CLI (pflag; every switch has a short and long form)

fontvault -s <system|user> [-d <push|pull|sync> | -c] [flags]

| Short | Long             | Value                      | Persisted in cfg | Notes                                                                                                    |
| ----- | ---------------- | -------------------------- | ---------------- | -------------------------------------------------------------------------------------------------------- |
| `-s`  | `--source`       | `system` \| `user`         | no               | Required on every run. Selects the local font dir that is read (push/compare) and installed into (pull). |
| `-d`  | `--direction`    | `push` \| `pull` \| `sync` | no               |                                                                                                          |
| `-c`  | `--compare`      | —                          | no               | Same as omitting `-d`.                                                                                   |
| `-b`  | `--branch`       | name                       | yes              | Default `main`.                                                                                          |
| `-w`  | `--workdir`      | path                       | yes              | Default: `os.UserCacheDir()` + `/fontvault/<login>_fontvault`.                                           |
| `-t`  | `--token`        | token                      | yes              |                                                                                                          |
| `-e`  | `--token-env`    | env var name               | yes              | Default `GITHUB_TOKEN`.                                                                                  |
| `-x`  | `--exclude-file` | path                       | yes              |                                                                                                          |
| `-n`  | `--dry-run`      | —                          | no               |                                                                                                          |
| `-y`  | `--yes`          | —                          | no               |                                                                                                          |
| `-v`  | `--verbose`      | —                          | no               |                                                                                                          |
| `-h`  | `--help`         | —                          | no               |                                                                                                          |
| `-V`  | `--version`      | —                          | no               |                                                                                                          |

### Switch behavior

- `-d` together with `-c` is a usage error.
- `-h/--help`: full usage, including a description of every switch and these examples at minimum:
  - first run with a token
  - compare
  - push from the system dir
  - pull into the user dir
  - sync with `-y` for unattended use
  - dry-run
- `-V/--version` prints the version injected at build time (`-X main.version`).
- `-n/--dry-run` performs every step up to the first write and prints exactly what would be done. It makes no commits, pushes, file copies, installs, registry writes or repo creation, and writes neither the report nor the cfg.
- `-y/--yes`:
  - Answers yes to the settings confirmation and to repo creation.
  - Does not resolve conflicts. Conflicts are skipped (see Conflicts).
- **Non-TTY stdin without `-y`:** exit with a usage error explaining that `-y` is required for non-interactive runs.

### Exclusions file (`-x`)

- One entry per line.
- Each entry is either a SHA-256 hash or a case-insensitive family name.
- `#` starts a comment.

## Repository

- **Owner:** the authenticated account, resolved with `GET https://api.github.com/user` (`login`).
- **Name:** always `fontvault`. It is not configurable.
- **Remote:** `https://github.com/<login>/fontvault.git`.
- **Existence check** in preflight: `GET /repos/<login>/fontvault`.
  - On 200, proceed.
  - On 404:
    - In compare mode or with `-n`, report that the repo does not exist and exit 3. Never create it in these cases.
    - Otherwise, prompt `Repository <login>/fontvault does not exist. Create it as public? [y/N]`. `-y` answers yes.
    - If the answer is no, exit 6.
    - If yes, `POST /user/repos` with `{"name":"fontvault","private":false}`.
  - **Create fails with 403 or 404:**
    - Print the value of the `X-Accepted-GitHub-Permissions` response header if present.
    - Also print: classic tokens need the `public_repo` or `repo` scope; fine-grained tokens need the "Administration" (write) or "Repository creation" (write) repository permission.
    - Exit 3.

## Token resolution (in order)

1. `-t/--token`
2. The env var named by `-e/--token-env`
3. The cfg
4. `gh auth token`, if `gh` is installed and authenticated. A token obtained this way is never written to the cfg.
5. Otherwise, exit 3 with a message listing all four sources.

### Rules

- Supply the token to git via a temporary `GIT_ASKPASS` helper or `-c http.extraHeader`.
- Never place it in a URL or in git config, and never write it in plaintext to disk.
- Never print it in logs, errors, reports or `-v` output.
- Delete the askpass helper on exit.

## Configuration (`fontvault.cfg`)

- **Location:** the directory containing the executable (`os.Executable()`, symlinks resolved), referred to as the **app dir**.
- **Contents:** `branch`, `workdir`, `token`, `token_env`, `exclude_file`, `format_version`.
- **Precedence per value:** CLI switch, then env (token only), then cfg, then default.
- **Saving:**
  - Any persisted switch supplied on the command line is written to the cfg at the end of a successful (exit 0 or 2) run.
  - Not with `-n`.
  - Values not supplied keep their cfg value.
- **Encryption:**
  - JSON, encrypted with AES-256-GCM.
  - Key: HKDF-SHA256 over a static secret compiled into the binary, with an app-specific info string. No user password.
  - Random 12-byte nonce per write.
  - File format: magic `FVC1`, then 1-byte format version, then nonce, then ciphertext and tag.
- **Writes:** atomic (temp file in the same dir, `fsync`, rename). On Linux, mode `0600`.
- **Unreadable cfg** (bad magic, auth tag failure, bad JSON, unknown version): exit 3 with an error naming the file and the reason. Never auto-overwrite or delete it.

## App dir write check (always, in preflight)

- Check that the app dir is writable by creating and removing a temp file in it.
- If it is not writable, print a message naming the app dir and explaining that the cfg and reports are stored there, then exit 3.

## Settings confirmation (before any action)

After preflight and before any repo or local operation, print the non-secret settings:

- version
- mode (and `dry-run` if set)
- GitHub account and repo (`<login>/fontvault`)
- branch
- workdir
- source and the resolved local font dirs
- exclusions file
- token source (switch / env var name / cfg / gh), never the value
- cfg path
- report path

Then prompt `Proceed? [y/N]`. `-y` answers yes. If the answer is no, exit 6.

## Font scope

- Only `.ttf` and `.otf` (case-insensitive extension) are processed.
- At startup, print: `Only TrueType (.ttf) and OpenType (.otf) fonts are processed.`
- During scanning, count files with other font extensions by extension: `.ttc .otc .woff .woff2 .fon .fnt .pfb .pfm .afm .pcf .bdf .otb .dfont`. Report these counts as "skipped formats". They are never read beyond the directory listing.
- Validate each `.ttf`/`.otf` by sfnt version tag (`0x00010000`, `true` or `OTTO`) and by a successful `sfnt` parse. Skip and log invalid files. One bad file never aborts the run.
- Follow symlinks, tracking resolved real paths to avoid loops and duplicates.

## Local font dirs

### Windows

- `system`:
  - Read from `%WINDIR%\Fonts`, plus registered files outside that dir from `HKLM\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Fonts`.
  - Pull installs into `%WINDIR%\Fonts`.
- `user`:
  - Read from `%LOCALAPPDATA%\Microsoft\Windows\Fonts`, plus registered files outside that dir from `HKCU\...\CurrentVersion\Fonts`.
  - Pull installs into `%LOCALAPPDATA%\Microsoft\Windows\Fonts`.

### Linux

- `system`:
  - Read from `/usr/share/fonts` and `/usr/local/share/fonts` (recursive).
  - Pull installs into `/usr/local/share/fonts/fontvault/`.
- `user`:
  - Read from `$XDG_DATA_HOME/fonts` (fallback `~/.local/share/fonts`) and `~/.fonts` (recursive).
  - Pull installs into `<user fonts dir>/fontvault/`.

## Exclusion of default OS fonts (push, and classification in reports)

Exclusion fails closed: if the tool cannot determine whether a font is an OS default, it excludes the font and records the reason.

### Windows

- A system-dir font is an OS default if its file owner SID is `NT SERVICE\TrustedInstaller` (read with `GetNamedSecurityInfo`).
- If the owner cannot be read, exclude the font.

### Linux

- A system-dir font is an OS default if it belongs to an installed package.
- Detect the package manager in this order: dpkg, rpm, pacman, apk.
- For dpkg, read `/var/lib/dpkg/info/*.list` directly. For the others, batch the queries.
- If no package manager is detected or a query fails, exclude all system-dir fonts and log one warning.

### User source

- Exclude any user-dir font whose SHA-256 matches a system-dir OS-default font.

### `-x` entries

- Apply them on top of the rules above.

### Protection of OS-default fonts

- OS-default fonts are never modified, replaced or removed by any mode.

## Font identity

- **Key:** PostScript name (name ID 6) + `|` + OS/2 `achVendID`, with the vendor ID trimmed of spaces and NULs (it may be empty). If name ID 6 is absent, use the full name (name ID 4). If both are absent, the font is invalid. Key comparison is exact.
- **Content hash:** SHA-256 of the file bytes. This is also the LFS OID.
- **Comparing local against repo:**
  - **Same key, same hash:** identical.
  - **Same key, different hash:** a conflict.
  - **Key on one side only:** missing on the other side.
- **Two local files with the same key:**
  - Same hash: treat as one font.
  - Different hashes: a local conflict, where the user picks which local file represents the key, or skips the key. Under `-y` or non-interactive, skip.

## Repository layout

fonts/<family>/<psname>-<keyhash8>.<ext>
manifest/<keyhash[0:2]>/<keyhash>.json
.gitattributes

- `keyhash` = SHA-256 of the key string. `keyhash8` = its first 8 hex characters. The path is stable per key and does not change when the content changes.
- `<family>` comes from name ID 16, falling back to 1. `<psname>` is the key's name part.
  - Sanitize both to ASCII `[A-Za-z0-9._-]` and replace everything else with `_`.
  - Lowercase the entire path.
  - Cap each component at 100 characters.
- `.gitattributes` routes `*.ttf` and `*.otf` through LFS (`filter=lfs diff=lfs merge=lfs -text`). If it is missing or incomplete, add or fix it in its own commit before any font commit. This happens in push only.

### Manifest JSON (one per key)

- `key`, `postscript_name`, `vendor_id`, `full_name`, `family`, `subfamily`
- `version_string` (name ID 5), `font_revision` (`head.fontRevision` as a decimal string)
- `copyright` (ID 0), `license_description` (ID 13), `license_url` (ID 14), with fields that are absent omitted
- `sha256`, `size`, `ext`, `repo_path`
- `first_archived`, `last_updated` (RFC 3339 UTC)
- No source, path, hostname or user information of any kind.

## Workdir (sparse, blobs fetched on demand)

- **Clone:**
  - Run `GIT_LFS_SKIP_SMUDGE=1 git clone --sparse`, then `git sparse-checkout set manifest` (cone mode).
  - The working tree holds only `manifest/` and the root files (`.gitattributes`).
  - If the repo is empty (push only), initialize the branch with `.gitattributes`.
- **Existing workdir:**
  - Verify that its remote URL equals `https://github.com/<login>/fontvault.git`.
  - Fetch and fast-forward the branch.
  - If it is dirty or has diverged, exit 1 with a clear error. Never reset or discard.
- Load all manifests into an index keyed by key.

## Conflicts (same key, different hash)

- **What the prompt shows for each side:** version string, font revision, size, short hash, and path (local file path / repo path).
- **Options depend on the mode:**
  - **push:** `[r]eplace repo copy with local` / `[s]kip`
  - **pull:** `[l]ocal replaced by repo copy` / `[s]kip`
  - **sync:** `use [l]ocal` (replace the repo copy) / `use [r]epo` (replace the local copy) / `[s]kip`
    - The choice is made once, during the push phase. A "use repo" choice is applied during the pull phase.
- **Local copy is an OS-default font:** the only option is skip, and the conflict is reported.
- **Under `-y` or non-interactive:** every conflict is skipped automatically and listed in the report and summary.
- **Compare:** never prompts. Conflicts are listed under "Differs".

## Mode: compare

This mode is read-only: no commits, no LFS downloads, no local changes. It can write the report, and the cfg if persisted switches were supplied.

1. Prepare the workdir and scan `-s`, applying exclusions.
2. Classify:
   - **Local only:** included local fonts whose key is not in the repo.
   - **Repo only:** repo keys not present locally. Excluded local fonts count as present.
   - **Identical**
   - **Differs:** same key, different hash.
   - **Excluded locally**, with the reason.
   - **Skipped formats**, counts by extension.
   - **Errors**
3. Print the report and save it (see Reports).

## Mode: push

1. Prepare the workdir, scan and apply exclusions.
2. For each included local font:
   - **Key not in the repo:**
     - Write the file to its repo path (staged with `git add --sparse`) and write its manifest.
     - Before writing, assert that the path does not exist.
   - **Identical:** no-op.
   - **Conflict:** resolve per Conflicts.
     - **Replace repo copy:** write the new file to the same repo path, update the manifest (keep `first_archived`, update `last_updated` and the content fields). The previous LFS object remains in git history.
3. Never delete any file under `fonts/` or `manifest/`.
4. **Pre-commit verification:**
   - Use `git lfs status --porcelain` plus a staged-diff check to confirm that every staged font is staged as an LFS pointer.
   - If any font would be committed as a raw blob, abort without committing.
5. **Commit message:** a summary line with counts (added, replaced), then a body listing the affected families. Author and committer come from git config. If git config is unset, exit 1 with an instruction to set it.
6. **Push:**
   - On a non-fast-forward rejection, fetch and rebase, then redo steps 2–4 against the new state.
     - Conflicts already answered in this run keep their answer.
     - New conflicts are prompted (or skipped under `-y`).
   - Retry up to 5 times with exponential backoff and jitter, then exit 1. Local commits are retained.
   - Push LFS objects before the ref, and verify the push succeeded.
7. `git sparse-checkout reapply`, so that font files leave the working tree.

## Mode: pull

1. Prepare the workdir and scan `-s`. Exclusions are not applied, but OS-default protection is.
2. **Targets:**
   - repo keys missing locally
   - conflicts the user resolved as "replace local"
3. **Elevation:**
   - Windows `-s system` requires an elevated token. Check this in preflight and exit 3 if it is not elevated.
   - Linux `-s system` requires write access to `/usr/local/share/fonts`. Check this in preflight.
4. **Fetch content without checkout:**
   - For each target, run `git cat-file blob <ref>:<path> | git lfs smudge <path>`, writing to a temp file in the install dir.
   - Verify that the SHA-256 equals the manifest hash and that the file parses. On failure, skip it and report it.
5. **Install a missing font:**
   - **Target filename:** the repo filename.
     - If the target path exists with the same hash, skip it.
     - If it exists with a different hash, report a conflict and skip.
   - Rename the temp file into place.
   - **Windows registration:**
     - Registry value name: `<full name> (TrueType)` or `(OpenType)`.
       - For `-s system`, register the value under `HKLM`, with the filename only as its data.
       - For `-s user`, register the value under `HKCU`, with the full path as its data.
     - If the value name already exists, do not overwrite it. Remove the file copied in this run and report a conflict.
     - Then call `AddFontResourceExW` and broadcast `WM_FONTCHANGE`.
   - **Linux registration:** run `fc-cache -f <install dir>` once after all installs.
6. **Replace a local font (user-chosen conflicts only):**
   1. Windows: unregister the old font (`RemoveFontResourceExW`, then remove its registry value).
   2. Delete the old file.
      - If deletion fails (locked, permission denied), re-register the old font, report the error, and leave both copies unchanged.
   3. Install the new file as in step 5.
7. **Per-font failure:** roll back only what this run did for that font (copied file, registry value), then continue.
8. Pull never commits or pushes.

## Mode: sync

1. Run push.
   - If push ends fatally, do not run pull. Exit 1.
   - If push completes, possibly with per-file errors, continue.
2. Run pull against the post-push repo state, applying "use repo" conflict choices from the push phase.

## Reports

- **When written:**
  - Every run that completes with exit code 0 or 2, in any mode, writes the report.
  - Runs that fail (exit 1, 3, 4, 5 or 6) print to the console only and write no report.
  - `-n` writes no report.
- **Path:** `<app dir>/fontvault_sync_YYYY-MM-DD_HHMM.txt`, in local time and 24-hour time.
  - The same filename is used for every mode.
  - If the file exists, it is overwritten.
  - Write atomically.
- **Format:** plain-text UTF-8. The same text is printed to stdout.
- **Header:**
  - local timestamp
  - fontvault version
  - mode
  - OS and version
  - source and resolved dirs
  - `<login>/fontvault`, branch, and the commit SHA compared against (plus the new commit SHA for push/sync)
- **Summary counts:** one line per section.
- **Sections** (as applicable to the mode). Each lists fonts with family/subfamily, version string, short hash and path. Empty sections print `(none)`.
  - Local only
  - Repo only
  - Identical
  - Differs
  - Added to repo
  - Replaced in repo
  - Installed locally
  - Replaced locally
  - Conflicts skipped (each with the reason: user skip / `-y` / OS default / registry name collision)
  - Excluded locally (with the reason)
  - Skipped formats (counts by extension)
  - Errors (path and error)
- **Sorting:** deterministic, by family, then subfamily, then key.

## Housekeeping (end of every run)

- **Every run, success or failure:** remove temp files, the askpass helper and the lock.
- **After a successful push** (including the push phase of sync): `git sparse-checkout reapply`, `git lfs prune --verify-remote`.
- **After a successful pull:** `git lfs prune --verify-remote` (removes the LFS objects fetched for installation).
- **After any successful run:** `git gc --auto`.
- **After a failed run:** prune and gc nothing, so unpushed commits and their objects survive.
- **Never** delete reports or the cfg.

## Error handling and robustness

- **Preflight, in this order.** Each check fails with a specific, actionable message (exit 3 unless noted):
  1. switch validation (exit 5)
  2. non-TTY without `-y` (exit 5)
  3. app dir writable
  4. cfg readable and decryptable if present
  5. git present
  6. git-lfs present
  7. `git lfs install --skip-repo` succeeds
  8. token resolved
  9. `GET /user` succeeds
  10. repo exists or is created
  11. workdir writable
  12. source dirs readable
  13. (pull/sync) install dir writable and elevation
- **Single-instance lock:** an exclusive lock file in the workdir, with stale-lock detection by PID liveness (exit 4).
- **Per-file errors** are collected, do not abort the run, and appear in the summary and report.
- **Fatal errors abort** with no partial commit and no half-installed font: workdir state errors, invariant violations, a failed pre-commit verification, exhausted push retries.
- **Windows paths:** handle long paths (`\\?\` prefix where needed) and non-ASCII filenames.
- **Shutdown:** handle SIGINT/SIGTERM and Windows Ctrl+C gracefully. Finish or roll back the current font, leave nothing half-written, run the always-run housekeeping, release the lock.
- **Exit codes:**
  - 0: success
  - 1: fatal error
  - 2: completed with per-file errors or automatically skipped conflicts (user-chosen skips alone do not cause exit 2)
  - 3: preflight failure
  - 4: lock held
  - 5: usage error
  - 6: cancelled by the user (declined the settings confirmation or repo creation)
- Every error message names the operation, the path or URL involved, and the underlying error. Wrap errors with `%w`.

## Logging

- Use `log/slog` to stderr, at info level by default and debug with `-v`.
- The report/summary goes to stdout.

## Testing

- Unit tests for:
  - key extraction (ID 6/4 fallback, vendor trimming)
  - TTF/OTF validation and skipped-format counting
  - path sanitization, and stability of a key's path when its content changes
  - manifest create/update (`first_archived` preserved)
  - local duplicate-key handling
  - exclusion logic, with owner/package lookups behind interfaces
  - OS-default protection in conflicts
  - exclusions file parsing
  - classification and report formatting (golden files)
  - report filename and same-minute overwrite
  - cfg encrypt/decrypt round-trip, tamper detection, bad magic, unknown version
  - config precedence
  - token resolution order, with `gh` stubbed
  - `-y` and non-TTY behavior
  - conflict option sets per mode
- Integration tests against a local bare repo with git-lfs (GitHub API calls behind an interface with a fake):
  - repo-missing create flow (yes, no, 403)
  - first push to an empty repo
  - a repeat push being a no-op
  - a push with a conflict under `-y` (skipped, exit 2)
  - push "replace repo" (old object remains in history)
  - non-fast-forward resolved by retry
  - pull installing only missing fonts
  - pull "replace local", including rollback when deleting the old file fails
  - pull rollback on an injected install failure
  - sync ordering, and pull skipped after a fatal push
  - compare output for a fixture set
  - sparse working tree contains no fonts after push
  - prune is not run after a failed run
- Windows registry, GDI and ACL calls sit behind interfaces. Platform code is tested on its own platform via build tags.
- CI runs on `ubuntu-latest` and `windows-latest`.

## Deliverables

- Go source, `go.mod`, `go.sum`
- `README.md` covering:
  - purpose and TTF/OTF-only scope
  - install
  - every switch (short and long) and mode, with examples
  - repo naming and ownership
  - token sources and order, and the required token permissions:
    - push/pull: classic `public_repo` or `repo`; fine-grained Contents read/write plus Metadata read
    - repo creation: classic `public_repo` or `repo`; fine-grained Administration (write) or Repository creation (write)
  - cfg location, precedence and encryption
  - exclusion rules per OS
  - identity and conflict behavior
  - report location, name and contents
  - housekeeping
  - elevation requirements
  - exit codes
- `.gitignore`: must ignore `fontvault.cfg` and `fontvault_sync_*.txt`
- `.editorconfig`
- `.github/workflows/ci.yml`: build and test on push and PR, on both OSes
- `.github/workflows/release.yml`: on a `v*` tag, test on both OSes, cross-compile all four targets with `-trimpath -ldflags "-s -w -X main.version=<tag>"`, generate SHA-256 checksums, and attach binaries and checksums to a GitHub Release

## Before writing code

Reply first with:

1. A brief package/file layout.
2. Any spec points you consider ambiguous or technically problematic, with your proposed resolution.

Wait for confirmation before implementing.
