package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fontvault/internal/cli"
	"fontvault/internal/config"
	"fontvault/internal/exclude"
	"fontvault/internal/github"
	"fontvault/internal/gitops"
	"fontvault/internal/install"
	"fontvault/internal/report"
)

// Exit codes.
const (
	ExitOK        = 0
	ExitFatal     = 1
	ExitCompleted = 2
	ExitPreflight = 3
	ExitLockHeld  = 4
	ExitUsage     = 5
	ExitCancelled = 6
)

// FontSources describes where local fonts are read from.
type FontSources struct {
	Dirs      []string
	Recursive bool
	// ExtraFiles are individual files scanned in addition to Dirs, e.g.
	// registry-registered font files outside the font directories.
	ExtraFiles []string
}

// Platform abstracts the OS-specific pieces.
type Platform interface {
	OSInfo() string
	FontSources(source string) (FontSources, error)
	// InstallDir returns the pull target directory (which may not exist
	// yet) for the source.
	InstallDir(source string) (string, error)
	Classifier() exclude.OSClassifier
	NewInstaller(system bool, installDir string) install.Installer
	// CheckPullPrereqs verifies elevation (Windows system) and install dir
	// writability. Preflight failure (exit 3).
	CheckPullPrereqs(source, installDir string) error
}

// App is one fontvault run.
type App struct {
	Opts    cli.Options
	Version string

	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader
	Log    *slog.Logger

	// Seams replaced by tests.
	Platform Platform
	GitHub   github.Client
	GhToken  func(ctx context.Context) (string, error)
	Env      func(string) (string, bool)
	Clock    func() time.Time
	TTY      func() bool

	// remoteURL is the origin remote; tests override it with a local bare
	// repository.
	remoteURL string

	// appDirOverride replaces the executable directory (tests only).
	appDirOverride string

	// Run state.
	appDir      string
	cfgPath     string
	reportPath  string
	login       string
	branch      string
	workdir     string
	token       string
	tokenEnv    string
	tokenSource string
	exclude     string
	xf          *exclude.ExclusionFile
	cfg         *config.File
	cfgLoaded   bool

	fontSources FontSources
	installDir  string

	git       *gitops.Runner
	lock      interface{ Release() error }
	tempFiles []string

	// housekeep overrides the git prune/gc calls in tests.
	housekeep housekeepOps

	pushPhaseOK bool
	pullPhaseOK bool

	prompter *prompter
}

// housekeepOps is the subset of gitops used by housekeeping.
type housekeepOps interface {
	LFSPrune(ctx context.Context, dir string) (string, error)
	GCAuto(ctx context.Context, dir string) (string, error)
}

// New builds an App with production defaults.
func New(opts cli.Options, version string, stdin io.Reader, stdout, stderr io.Writer) *App {
	level := slog.LevelInfo
	if opts.Verbose {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))
	a := &App{
		Opts:     opts,
		Version:  version,
		Stdout:   stdout,
		Stderr:   stderr,
		Stdin:    stdin,
		Log:      logger,
		Platform: newPlatform(),
		Env:      os.LookupEnv,
		Clock:    time.Now,
		GhToken:  ghAuthToken,
	}
	a.TTY = func() bool { return stdinIsTTY(stdin) }
	return a
}

func ghAuthToken(ctx context.Context) (string, error) {
	return runGhAuthToken(ctx)
}

// SetRemoteURL overrides the origin remote URL (integration tests).
func (a *App) SetRemoteURL(url string) { a.remoteURL = url }

// SetAppDir overrides the directory holding cfg and reports (tests only).
func (a *App) SetAppDir(dir string) { a.appDirOverride = dir }

func (a *App) repoName() string { return "fontvault" }

func (a *App) remoteFor(login string) string {
	if a.remoteURL != "" {
		return a.remoteURL
	}
	return fmt.Sprintf("https://github.com/%s/fontvault.git", login)
}

// usageFail prints a usage error (exit 5).
func (a *App) usageFail(format string, args ...any) int {
	fmt.Fprintf(a.Stderr, "fontvault: %s\nTry 'fontvault --help' for usage.\n", fmt.Sprintf(format, args...))
	return ExitUsage
}

// fail prints a fatal error message (exit 1).
func (a *App) fail(format string, args ...any) int {
	fmt.Fprintf(a.Stderr, "fontvault: %s\n", fmt.Sprintf(format, args...))
	return ExitFatal
}

// preflightFail prints a preflight error (exit 3).
func (a *App) preflightFail(format string, args ...any) int {
	fmt.Fprintf(a.Stderr, "fontvault: %s\n", fmt.Sprintf(format, args...))
	return ExitPreflight
}

// addTemp registers a temp file for cleanup.
func (a *App) addTemp(path string) { a.tempFiles = append(a.tempFiles, path) }

// cleanupTemps removes every registered temp file.
func (a *App) cleanupTemps() {
	for _, p := range a.tempFiles {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			a.Log.Warn("remove temp file failed", "path", p, "error", err)
		}
	}
	a.tempFiles = nil
}

// Run executes the full run and returns the exit code.
func (a *App) Run(ctx context.Context) (code int) {
	a.prompter = newPrompter(a.Stdin, a.Stdout)
	defer func() { a.housekeeping(&code) }()

	// TTY requirement: -y is required when stdin is not a terminal.
	if !a.Opts.Yes && !a.TTY() {
		return a.usageFail("-y/--yes is required when stdin is not a terminal (non-interactive run); conflicts and confirmations cannot be prompted")
	}

	if code := a.preflight(ctx); code != ExitOK {
		return code
	}

	fmt.Fprintln(a.Stdout, "Only TrueType (.ttf) and OpenType (.otf) fonts are processed.")
	if code := a.confirmSettings(); code != ExitOK {
		return code
	}

	code, rep := a.runModes(ctx)
	if (code == ExitOK || code == ExitCompleted) && !a.Opts.DryRun {
		text := rep.Render()
		fmt.Fprint(a.Stdout, text)
		if !strings.HasSuffix(text, "\n") {
			fmt.Fprintln(a.Stdout)
		}
		if err := report.Write(a.reportPath, text); err != nil {
			a.Log.Error("write report", "path", a.reportPath, "error", err)
			return ExitFatal
		}
		if config.AnySupplied(a.Opts.Supplied) {
			merged := config.MergeForSave(a.cfg, a.Opts.Supplied, config.BranchWorkdirToken{
				Branch:      a.Opts.Branch,
				Workdir:     a.Opts.Workdir,
				Token:       a.Opts.Token,
				ExcludeFile: a.Opts.ExcludeFile,
			}, a.tokenEnv)
			if err := config.Save(a.cfgPath, merged); err != nil {
				a.Log.Error("save config", "path", a.cfgPath, "error", err)
				return ExitFatal
			}
		}
	}
	return code
}

// housekeeping runs at the very end of every run: prune/gc only after
// successful phases, temp file removal and lock release always.
func (a *App) housekeeping(code *int) {
	if a.lock != nil {
		if err := a.lock.Release(); err != nil {
			a.Log.Warn("release lock", "error", err)
		}
		a.lock = nil
	}
	a.cleanupTemps()

	var ops housekeepOps
	if a.housekeep != nil {
		ops = a.housekeep
	} else if a.git != nil {
		ops = a.git
	}
	if ops == nil {
		return
	}
	// After a failed run (exit 1, 3, 4, 5 or 6), prune and gc nothing so
	// unpushed commits and their objects survive.
	success := *code == ExitOK || *code == ExitCompleted
	if !success {
		return
	}
	if a.pushPhaseOK || a.pullPhaseOK {
		if _, err := ops.LFSPrune(context.Background(), a.workdir); err != nil {
			a.Log.Warn("git lfs prune", "error", err)
		}
	}
	if _, err := ops.GCAuto(context.Background(), a.workdir); err != nil {
		a.Log.Warn("git gc --auto", "error", err)
	}
}

// preflight implements the documented preflight order.
func (a *App) preflight(ctx context.Context) int {
	// 1. switch validation happens in cli.Parse (exit 5).
	// 2. non-TTY check happens in Run (exit 5).

	// 3. app dir writable.
	if a.appDirOverride != "" {
		a.appDir = a.appDirOverride
	} else {
		exe, err := os.Executable()
		if err != nil {
			return a.preflightFail("resolve executable path: %v", err)
		}
		exe, err = filepath.EvalSymlinks(exe)
		if err != nil {
			return a.preflightFail("resolve executable symlinks: %v", err)
		}
		a.appDir = filepath.Dir(exe)
	}
	if err := checkDirWritable(a.appDir); err != nil {
		return a.preflightFail("app dir %s is not writable; the cfg and reports are stored there: %v", a.appDir, err)
	}
	a.cfgPath = filepath.Join(a.appDir, "fontvault.cfg")
	a.reportPath = filepath.Join(a.appDir, report.Filename(a.Clock()))

	// 4. cfg readable and decryptable.
	cfg, err := config.Load(a.cfgPath)
	if err != nil {
		return a.preflightFail("%v", err)
	}
	a.cfg = cfg
	a.cfgLoaded = true

	// 5+6. git and git-lfs present.
	if err := gitops.LookPath("git"); err != nil {
		return a.preflightFail("%v", err)
	}
	if err := gitops.LookPath("git-lfs"); err != nil {
		return a.preflightFail("%v", err)
	}

	// 7. git lfs install --skip-repo succeeds.
	a.git = gitops.New("", a.Log) // token set after resolution
	if _, err := a.git.LFSInstall(ctx); err != nil {
		return a.preflightFail("git lfs install --skip-repo failed: %v", err)
	}

	// 8. token resolved.
	var cfgToken, cfgTokenEnv string
	if cfg != nil {
		cfgToken = cfg.Token
		cfgTokenEnv = cfg.TokenEnv
	}
	a.tokenEnv = config.ResolveTokenEnv(a.Opts.TokenEnv, cfgTokenEnv)
	ghToken := func() (string, error) { return a.GhToken(ctx) }
	token, source, err := config.ResolveToken(a.Opts.Token, a.tokenEnv, a.Env, cfgToken, ghToken)
	if err != nil {
		return a.preflightFail("%v", err)
	}
	a.token = token
	a.tokenSource = source
	a.git = gitops.New(a.token, a.Log)

	// 9. GET /user.
	if a.GitHub == nil {
		a.GitHub = github.New(a.token, a.Version)
	}
	login, err := a.GitHub.CurrentUser(ctx)
	if err != nil {
		return a.preflightFail("resolve GitHub account: %v", err)
	}
	a.login = login

	// 10. repo exists or is created.
	exists, err := a.GitHub.RepoExists(ctx, login, a.repoName())
	if err != nil {
		return a.preflightFail("check repository %s/%s: %v", login, a.repoName(), err)
	}
	if !exists {
		if a.Opts.Compare || a.Opts.DryRun {
			fmt.Fprintf(a.Stdout, "Repository %s/%s does not exist.\n", login, a.repoName())
			return ExitPreflight
		}
		if a.Opts.Yes {
			if err := a.createRepo(ctx); err != nil {
				return ExitPreflight
			}
		} else {
			fmt.Fprintf(a.Stdout, "Repository %s/%s does not exist. Create it as public? [y/N] ", login, a.repoName())
			yes, err := a.prompter.confirm("")
			if err != nil {
				return a.fail("read answer: %v", err)
			}
			if !yes {
				return ExitCancelled
			}
			if err := a.createRepo(ctx); err != nil {
				return ExitPreflight
			}
		}
	}

	// Resolve the remaining persisted settings.
	var cfgBranch, cfgWorkdir, cfgExclude string
	if cfg != nil {
		cfgBranch = cfg.Branch
		cfgWorkdir = cfg.Workdir
		cfgExclude = cfg.ExcludeFile
	}
	a.branch = config.ResolveBranch(a.Opts.Branch, cfgBranch)
	a.workdir, err = config.ResolveWorkdir(a.Opts.Workdir, cfgWorkdir, login)
	if err != nil {
		return a.preflightFail("%v", err)
	}
	a.exclude = config.ResolveExcludeFile(a.Opts.ExcludeFile, cfgExclude)

	// Exclusions file.
	if a.exclude != "" {
		f, err := os.Open(a.exclude)
		if err != nil {
			return a.preflightFail("open exclusions file %s: %v", a.exclude, err)
		}
		xf, err := exclude.ParseExclusionFile(f)
		f.Close()
		if err != nil {
			return a.preflightFail("parse exclusions file %s: %v", a.exclude, err)
		}
		a.xf = xf
	}

	// 11. workdir writable.
	if err := os.MkdirAll(a.workdir, 0o755); err != nil {
		return a.preflightFail("create workdir %s: %v", a.workdir, err)
	}
	if err := checkDirWritable(a.workdir); err != nil {
		return a.preflightFail("workdir %s is not writable: %v", a.workdir, err)
	}

	// 12. source dirs readable.
	srcs, err := a.Platform.FontSources(a.Opts.Source)
	if err != nil {
		return a.preflightFail("%v", err)
	}
	a.fontSources = srcs
	for _, dir := range srcs.Dirs {
		if err := checkDirReadable(dir); err != nil {
			return a.preflightFail("source font dir %s is not readable: %v", dir, err)
		}
	}

	// 13. pull/sync install dir writable and elevation.
	if a.Opts.Direction == cli.DirectionPull || a.Opts.Direction == cli.DirectionSync {
		installDir, err := a.Platform.InstallDir(a.Opts.Source)
		if err != nil {
			return a.preflightFail("%v", err)
		}
		a.installDir = installDir
		if err := a.Platform.CheckPullPrereqs(a.Opts.Source, installDir); err != nil {
			return a.preflightFail("%v", err)
		}
	}
	return ExitOK
}

// createRepo creates the repository and explains permission failures.
func (a *App) createRepo(ctx context.Context) error {
	err := a.GitHub.CreateRepo(ctx, a.repoName(), false)
	if err == nil {
		fmt.Fprintf(a.Stdout, "Created repository %s/%s.\n", a.login, a.repoName())
		return nil
	}
	var ce *github.CreateError
	if errors.As(err, &ce) && (ce.StatusCode == 403 || ce.StatusCode == 404) {
		if ce.AcceptedPermissions != "" {
			fmt.Fprintf(a.Stderr, "fontvault: X-Accepted-GitHub-Permissions: %s\n", ce.AcceptedPermissions)
		}
		fmt.Fprintf(a.Stderr, "fontvault: the token cannot create repositories. Classic tokens need the public_repo or repo scope; fine-grained tokens need the \"Administration\" (write) or \"Repository creation\" (write) repository permission.\n")
		return fmt.Errorf("create repository %s/%s: %w", a.login, a.repoName(), err)
	}
	return fmt.Errorf("create repository %s/%s: %w", a.login, a.repoName(), err)
}

// confirmSettings prints the non-secret settings and prompts Proceed?.
func (a *App) confirmSettings() int {
	mode := a.Opts.ModeName()
	if a.Opts.DryRun {
		mode += " (dry-run)"
	}
	fmt.Fprintf(a.Stdout, "fontvault %s\n", a.Version)
	fmt.Fprintf(a.Stdout, "mode: %s\n", mode)
	fmt.Fprintf(a.Stdout, "github: %s/fontvault\n", a.login)
	fmt.Fprintf(a.Stdout, "branch: %s\n", a.branch)
	fmt.Fprintf(a.Stdout, "workdir: %s\n", a.workdir)
	fmt.Fprintf(a.Stdout, "source: %s\n", a.Opts.Source)
	fmt.Fprintln(a.Stdout, "font dirs:")
	for _, d := range a.fontSources.Dirs {
		fmt.Fprintf(a.Stdout, "  - %s\n", d)
	}
	if len(a.fontSources.Dirs) == 0 {
		fmt.Fprintln(a.Stdout, "  (none)")
	}
	if a.exclude != "" {
		fmt.Fprintf(a.Stdout, "exclusions: %s\n", a.exclude)
	} else {
		fmt.Fprintln(a.Stdout, "exclusions: (none)")
	}
	fmt.Fprintf(a.Stdout, "token source: %s\n", a.tokenSource)
	fmt.Fprintf(a.Stdout, "cfg: %s\n", a.cfgPath)
	fmt.Fprintf(a.Stdout, "report: %s\n", a.reportPath)

	if a.Opts.Yes {
		return ExitOK
	}
	yes, err := a.prompter.confirm("Proceed? [y/N] ")
	if err != nil {
		return a.fail("read answer: %v", err)
	}
	if !yes {
		return ExitCancelled
	}
	return ExitOK
}

// checkDirWritable verifies a directory accepts a temp file.
func checkDirWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".fontvault-write-test-*")
	if err != nil {
		return err
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}

// checkDirReadable verifies a directory can be listed. Missing directories
// are treated as empty (normal for fresh machines).
func checkDirReadable(dir string) error {
	_, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return nil
}
