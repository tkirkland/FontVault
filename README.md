# fontvault

fontvault is a cross-platform CLI font archiver. It keeps TrueType (`.ttf`)
and OpenType (`.otf`) fonts installed on the local machine in sync with a
GitHub repository named `FontVault_fonts` (owned by the authenticated account)
that stores the fonts through Git LFS.

Only TrueType (`.ttf`) and OpenType (`.otf`) fonts are processed. Other font
formats (`.ttc`, `.otc`, `.woff`, `.woff2`, `.fon`, `.fnt`, `.pfb`, `.pfm`,
`.afm`, `.pcf`, `.bdf`, `.otb`, `.dfont`) are counted as "skipped formats"
and are never read beyond the directory listing.

## Modes

| Mode      | What it does                                                                  |
| --------- | ----------------------------------------------------------------------------- |
| `push`    | Archives local fonts into the repo, excluding fonts that ship with the OS.    |
| `pull`    | Installs repo fonts that are missing locally (exclusions are not applied).    |
| `sync`    | Runs push, then pull against the post-push repo state.                        |
| `compare` | Read-only comparison report. This is the default when no direction is given.  |

## Requirements

- Go (to build from source)
- `git` and `git-lfs` on PATH (fontvault shells out to them; it does not use go-git)
- A GitHub account with a token (see below)
- `fc-cache` is optional on Linux (a missing one warns but does not fail)

## Install

Build the static binary for your platform (no cgo):

```console
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(git describe --tags --always)" .
```

The `fontvault.cfg` and the run reports are stored in the directory that
contains the executable (the "app dir", symlinks resolved). That directory
must be writable; fontvault verifies this on every run.

## Usage

```text
fontvault -s <system|user> [-d <push|pull|sync> | -c] [flags]
```

### Switches

Every switch has a short and a long form.

| Short | Long             | Value                      | Persisted in cfg | Notes                                                                                                    |
|-------|------------------|----------------------------|------------------|----------------------------------------------------------------------------------------------------------|
| `-s`  | `--source`       | `system` \| `user`         | no               | Required on every run. Selects the local font dir that is read (push/compare) and installed into (pull). |
| `-d`  | `--direction`    | `push` \| `pull` \| `sync` | no               |                                                                                                          |
| `-c`  | `--compare`      | —                          | no               | Same as omitting `-d`. Exclusive with `-d`.                                                              |
| `-b`  | `--branch`       | name                       | yes              | Default `main`.                                                                                          |
| `-w`  | `--workdir`      | path                       | yes              | Default: `<user cache dir>/fontvault/<login>_fontvault`.                                                 |
| `-t`  | `--token`        | token                      | yes              |                                                                                                          |
| `-e`  | `--token-env`    | env var name               | yes              | Default `GITHUB_TOKEN`.                                                                                  |
| `-x`  | `--exclude-file` | path                       | yes              | One entry per line: a SHA-256 hash or a case-insensitive family name; `#` starts a comment.              |
| `-n`  | `--dry-run`      | —                          | no               | Performs every step up to the first write and prints exactly what would be done.                         |
| `-y`  | `--yes`          | —                          | no               | Answers yes to the settings confirmation and repo creation; conflicts are skipped and listed.            |
| `-v`  | `--verbose`      | —                          | no               | Debug logging to stderr.                                                                                 |
| `-h`  | `--help`         | —                          | no               | Full usage.                                                                                              |
| `-V`  | `--version`      | —                          | no               | Prints the version injected at build time.                                                               |

### Examples

```console
# First run with a token
fontvault -s user -t ghp_... -y

# Compare the local fonts with the repository (read-only)
fontvault -s user -c

# Push fonts from the system directory
fontvault -s system -d push

# Pull repository fonts into the user directory
fontvault -s user -d pull

# Full sync, unattended
fontvault -s user -d sync -y

# See what a push would do without changing anything
fontvault -s user -d push -n
```

## Repository

- **Owner:** the authenticated account (resolved with `GET /user`).
- **Name:** always `FontVault_fonts`. It is not configurable.
- **Remote:** `https://github.com/<login>/FontVault_fonts.git`.

If the repository does not exist, push/pull/sync ask
`Repository <login>/FontVault_fonts does not exist. Create it as public? [y/N]`
(`-y` answers yes). Compare mode and `-n` never create it.

Repository layout:

```text
fonts/<family>/<family>-<familyhash8>.tar.br
manifest/<family>-<familyhash8>.json
.gitattributes
```

Every font family is **one archive**, a tar stream compressed with brotli (`.tar.br`), even a family with a single font;
fonts are never stored in the
repository as loose files. Inside the archive each font is stored as
`<psname>-<keyhash8>.<ext>`. fontvault writes and reads the archives itself,
with no external tool (see [Why tar + brotli](#why-tar--brotli) below).

`keyhash` is the SHA-256 of the font key (PostScript name + `|` + vendor ID) and
`familyhash` is the SHA-256 of the family string; the 8-character prefixes keep
names that sanitize alike apart. The paths are stable per key and per family and
do not change when the content changes.

The manifest holds one record for the family (its archive path, the archive's
SHA-256 and size, and its dates) plus one entry per font inside the archive (each with its own `sha256`, `size`, `ext`
and member name). The `.gitattributes`
file routes `*.tar.br` through LFS and is fixed in its own commit before any font
commit (push only).

Replacing one font rewrites the family's whole archive, so a new LFS object for
that family enters git history (the previous one remains there), and every
font of the family is re-uploaded at that point.

### Why tar + brotli

The archive format had to satisfy four constraints at once: the tool is a
single static binary (`CGO_ENABLED=0`, so no C compression libraries), the
repository lives in Git LFS where storage is limited, so the compression ratio
matters, every family must be one object, and nothing external should have to
be installed beyond `git` and `git-lfs`.

The first version shelled out to 7-Zip (LZMA2), which compresses TrueType data
very well but added an install requirement on every machine and in CI, and
produced archive bytes that depended on the 7-Zip build that wrote them. No
pure-Go library can *write* 7z archives (only read them), so keeping the
container meant keeping the external tool. The candidates that Go can write
natively were measured on real font families from a Windows installation (archive size as a percentage of the raw fonts,
lower is better):

| Codec                            | Calibri (8.1 MB) | Corbel (1.5 MB) | Segoe UI (4.7 MB) |
|----------------------------------|------------------|-----------------|-------------------|
| 7-Zip LZMA2 (previous, external) | 31.5%            | 29.2%           | 38.8%             |
| **brotli, quality 11 (chosen)**  | **32.2%**        | **29.5%**       | **39.4%**         |
| xz (pure-Go encoder)             | 37.4%            | 34.4%           | 44.9%             |
| zstd, best level                 | 39.2%            | 37.7%           | 47.2%             |
| zip, Deflate level 9 (stdlib)    | 50.2%            | 54.2%           | 58.2%             |

Brotli at its highest quality lands within one or two percent of 7-Zip. That is
not a coincidence: brotli was designed around font data and ships a built-in
dictionary tuned for it, which is why the WOFF2 web font format uses it. The
pure-Go xz encoder is a weaker match finder than the real LZMA2 and costs about
a fifth more space; Deflate, the only option in the standard library, costs
more than half again as much.

The archive is a **tar** stream so that one brotli stream covers the whole
family ("solid" compression): the weights of a family share most of their
glyph outlines and compress far better together than one file at a time.
Extracting one font means decoding the whole family, but decoding is fast (well under a tenth of a second for a large
family), so pull does not notice.

The trade is encode time. Quality 11 runs at roughly four to five seconds per
megabyte of fonts, single-threaded, and a single brotli stream cannot use more
than one core. Push therefore encodes only the families that changed, and
encodes them in parallel, one goroutine per family up to the number of CPU
cores. A normal push re-encodes a few megabytes; the first push of a large font
library is the slow case. The quality is a single constant in
`internal/archive`, where quality 10 halves the time for one to two percent
more space and quality 9 is an order of magnitude faster for five percent more.

Two further properties fell out of writing the archives in Go. The output is **deterministic**: members are written in
sorted order with fixed metadata, so
the same fonts produce byte-identical archives on every machine, and the
`archive_sha256` in a manifest no longer depends on which tool wrote the file.
And the dependency is one pure-Go module, `github.com/andybalholm/brotli`
(MIT), which keeps the static single-binary build.

To open a family archive by hand, decompress it with the `brotli` command-line
tool (or NanaZip on Windows) and then extract the tar with any tar tool; 7-Zip
proper does not read brotli.

## Token sources and required permissions

The token is resolved in this order:

1. `-t/--token`
2. The environment variable named by `-e/--token-env` (default `GITHUB_TOKEN`)
3. The cfg
4. `gh auth token` (if `gh` is installed and authenticated; never persisted to the cfg)

If no source provides a token the run exits 3 with a message listing all four.

The token is supplied to git/git-lfs per invocation through
`-c http.extraHeader=AUTHORIZATION: basic …`. It is never placed in a URL or
in git config, never written to disk in plaintext, and never printed in logs,
errors, reports or `-v` output.

Required token permissions:

- **push/pull:** classic `public_repo` or `repo` scope; fine-grained
  **Contents: read/write** plus **Metadata: read**.
- **repo creation:** classic `public_repo` or `repo` scope; fine-grained
  **Administration (write)** or **Repository creation (write)**.

## Configuration (`fontvault.cfg`)

- **Location:** the app dir (directory containing the executable, symlinks resolved).
- **Contents:** `branch`, `workdir`, `token`, `token_env`, `exclude_file`, `format_version`.
- **Precedence per value:** CLI switch, then env (token only), then cfg, then default.
- **Saving:** persisted switches supplied on the command line are written at
  the end of a run that exits 0 or 2 (never with `-n`); values not supplied
  keep their cfg value.
- **Encryption:** JSON encrypted with AES-256-GCM. The key is derived with
  HKDF-SHA256 from a static secret compiled into the binary and an
  app-specific info string (no user password). File format: magic `FVC1`,
  1-byte format version, 12-byte nonce, ciphertext and tag. This is
  obfuscation, not protection against someone who holds the binary.
- **Writes:** atomic (temp file in the same dir, fsync, rename); mode `0600`
  on Linux.
- An unreadable cfg (bad magic, auth tag failure, bad JSON, unknown version)
  exits 3 naming the file and the reason. It is never auto-overwritten.

## Local font dirs

### Windows

- `system`: reads `%WINDIR%\Fonts` plus registered files outside it from
  `HKLM\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Fonts`; pull installs
  into `%WINDIR%\Fonts`.
- `user`: reads `%LOCALAPPDATA%\Microsoft\Windows\Fonts` plus registered
  files outside it from `HKCU\...\CurrentVersion\Fonts`; pull installs into
  `%LOCALAPPDATA%\Microsoft\Windows\Fonts`.

### Linux

- `system`: reads `/usr/share/fonts` and `/usr/local/share/fonts`
  (recursive); pull installs into `/usr/local/share/fonts/fontvault/`.
- `user`: reads `$XDG_DATA_HOME/fonts` (fallback `~/.local/share/fonts`) and
  `~/.fonts` (recursive); pull installs into `<user fonts dir>/fontvault/`.

## Exclusion of default OS fonts

Exclusion fails closed: if the tool cannot determine whether a font is an OS
default, the font is excluded and the reason is recorded.

- **Windows:** a system-dir font is an OS default when its file owner SID is
  `NT SERVICE\TrustedInstaller` (read with `GetNamedSecurityInfo`). An
  unreadable owner excludes the font.
- **Linux:** a system-dir font is an OS default when it belongs to an
  installed package. The package manager is detected in the order dpkg, rpm,
  pacman, apk. For dpkg, `/var/lib/dpkg/info/*.list` is read directly; the
  others are queried in batches. No package manager or a failed query
  excludes all system-dir fonts with one warning.
- **User source:** user-dir fonts whose SHA-256 matches a system-dir OS
  default are excluded; if the system dirs cannot be scanned, all user fonts
  are excluded (fail closed).
- **`-x` entries** are applied on top of the rules above.

OS-default fonts are never modified, replaced or removed by any mode.

## Identity and conflicts

- **Key:** PostScript name (name ID 6) + `|` + OS/2 `achVendID` (spaces and
  NULs trimmed; may be empty). Name ID 4 is the fallback when ID 6 is
  absent; without either, the font is invalid. Key comparison is exact.
- **Content hash:** SHA-256 of the font's own bytes, recorded per font in its
  family manifest. The LFS object is the family archive, so its OID is the
  archive's hash, not the font's.
- **Same key, same hash:** identical. **Same key, different hash:** a
  conflict. **Key on one side only:** missing on the other side.
- Two local files with the same key and different hashes are a local
  conflict: the user picks which file represents the key or skips it.
  Compare mode never prompts (the key is skipped); `-y` skips everywhere.

Conflict prompts show version string, font revision, size, short hash and
path for each side, with options per mode:

- **push:** `[r]eplace repo copy with local` / `[s]kip`
- **pull:** `[l]ocal replaced by repo copy` / `[s]kip`
- **sync:** `use [l]ocal` / `use [r]epo` / `[s]kip` (decided once during the
  push phase)

A local copy that is an OS-default font can only be skipped. Under `-y` or
non-interactive runs every conflict is skipped and listed in the report.

## Report

Every run that completes with exit code 0 or 2 writes a plain-text UTF-8
report; the same text is printed to stdout. Runs that fail (exit 1, 3, 4, 5
or 6) print to the console only. `-n` writes no report.

- **Path:** `<app dir>/fontvault_sync_YYYY-MM-DD_HHMM.txt` (local time,
  24-hour clock). The same filename is used for every mode and is
  overwritten within the same minute. Writes are atomic.
- **Header:** local timestamp, fontvault version, mode, OS and version,
  source and resolved dirs, `<login>/FontVault_fonts`, branch, and the commit SHA
  compared against (plus the new commit SHA for push/sync).
- **Summary:** one count line per section.
- **Sections** (as applicable to the mode): Local only, Repo only,
  Identical, Differs, Added to repo, Replaced in repo, Installed locally,
  Replaced locally, Conflicts skipped (each with its reason), Excluded
  locally (with the reason), Skipped formats (counts by extension), Errors
  (path and error). Empty sections print `(none)`. Entries are sorted by
  family, then subfamily, then key.

## Housekeeping

- Every run, success or failure: remove temp files and the lock.
- After a successful push (including the push phase of sync):
  `git sparse-checkout reapply`, `git lfs prune --verify-remote`.
- After a successful pull: `git lfs prune --verify-remote`.
- After any successful run (exit 0 or 2): `git gc --auto`.
- After a failed run: prune and gc nothing, so un-pushed commits and their
  objects survive.
- Reports and the cfg are never deleted.

## Elevation requirements

- **Windows `-s system` pull:** requires an elevated token; checked in
  preflight (exit 3 when not elevated).
- **Linux `-s system` pull:** requires write access to
  `/usr/local/share/fonts`; checked in preflight (exit 3 when not writable).

## Exit codes

| Code | Meaning                                                                     |
|------|-----------------------------------------------------------------------------|
| 0    | success                                                                     |
| 1    | fatal error                                                                 |
| 2    | completed with per-file errors or automatically skipped conflicts           |
| 3    | preflight failure                                                           |
| 4    | lock held by another live instance                                          |
| 5    | usage error (including non-TTY stdin without `-y`)                          |
| 6    | cancelled by the user (declined the settings confirmation or repo creation) |

## Development

```console
go test ./...   # unit + integration tests (integration needs git and git-lfs)
```

The CI runs on `ubuntu-latest` and `windows-latest`; releases are built for
`windows/amd64`, `windows/arm64`, `linux/amd64` and `linux/arm64` with
`CGO_ENABLED=0`.

---

## Licensing

**Licensing:** FontVault stores font files. It does not read, track or enforce
font licenses. Fonts you push may be commercial or otherwise restricted, and the
repository is created as public. You are responsible for what you archive and
for who can access the repository.
