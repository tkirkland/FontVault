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

	"fontvault/internal/archive7z"
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
	// ExtraFiles are individual files scanned in addition to Dirs, e.g.,
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
	tempDirs  []string

	// sevenz runs the 7z tool that stores the repository's family archives.
	sevenz archive7z.Tool

	// pullArchives caches the family archives this run has fetched, so
	// several fonts of one family download the archive once.
	pullArchives map[string]pullArchive

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

		pullArchives: map[string]pullArchive{},
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

func (a *App) repoName() string { return "FontVault_fonts" }

func (a *App) remoteFor(login string) string {
	if a.remoteURL != "" {
		return a.remoteURL
	}
	return fmt.Sprintf("https://github.com/%s/%s.git", login, a.repoName())
}

// usageFail prints a usage error (exit 5).
func (a *App) usageFail(format string, args ...any) int {
	_, _ = fmt.Fprintf(a.Stderr, "fontvault: %s\nTry 'fontvault --help' for usage.\n", fmt.Sprintf(format, args...))
	return ExitUsage
}

// fail prints a fatal error message (exit 1).
func (a *App) fail(format string, args ...any) int {
	_, _ = fmt.Fprintf(a.Stderr, "fontvault: %s\n", fmt.Sprintf(format, args...))
	return ExitFatal
}

// preflightFail prints a preflight error (exit 3).
func (a *App) preflightFail(format string, args ...any) int {
	_, _ = fmt.Fprintf(a.Stderr, "fontvault: %s\n", fmt.Sprintf(format, args...))
	return ExitPreflight
}

// addTemp registers a temp file for cleanup.
func (a *App) addTemp(path string) { a.tempFiles = append(a.tempFiles, path) }

// addTempDir registers a temp directory for removal at the end of the run.
func (a *App) addTempDir(path string) { a.tempDirs = append(a.tempDirs, path) }

// newTempDir creates a scratch directory (registered with addTempDir). It is
// where 7z staging files live, so a member's name inside an archive can be
// chosen without touching the repository's working tree.
func (a *App) newTempDir(label string) (string, error) {
	dir, err := os.MkdirTemp(os.TempDir(), "fontvault-"+label+"-")
	if err != nil {
		return "", fmt.Errorf("create temp dir: %w", err)
	}
	a.addTempDir(dir)
	return dir, nil
}

// cleanupTemps removes every registered temp file and directory.
func (a *App) cleanupTemps() {
	for _, p := range a.tempFiles {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			a.Log.Warn("remove temp file failed", "path", p, "error", err)
		}
	}
	for _, p := range a.tempDirs {
		if err := os.RemoveAll(p); err != nil && !os.IsNotExist(err) {
			a.Log.Warn("remove temp dir failed", "path", p, "error", err)
		}
	}
	a.tempFiles = nil
	a.tempDirs = nil
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

	_, _ = fmt.Fprintln(a.Stdout, "Only TrueType (.ttf) and OpenType (.otf) fonts are processed.")
	if code := a.confirmSettings(); code != ExitOK {
		return code
	}

	code, rep := a.runModes(ctx)
	if (code == ExitOK || code == ExitCompleted) && !a.Opts.DryRun {
		if code := a.writeReport(rep); code != ExitOK {
			return code
		}
		if code := a.persistSettings(); code != ExitOK {
			return code
		}
	}
	return code
}

// writeReport prints the rendered report and stores it in the app dir.
func (a *App) writeReport(rep *report.Report) int {
	text := rep.Render()
	_, _ = fmt.Fprint(a.Stdout, text)
	if !strings.HasSuffix(text, "\n") {
		_, _ = fmt.Fprintln(a.Stdout)
	}
	if err := report.Write(a.reportPath, text); err != nil {
		a.Log.Error("write report", "path", a.reportPath, "error", err)
		return ExitFatal
	}
	return ExitOK
}

// persistSettings saves the cfg when any persisted switch was supplied on
// the command line (already filtered for success and non-dry-run).
func (a *App) persistSettings() int {
	if !config.AnySupplied(a.Opts.Supplied) {
		return ExitOK
	}
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
	return ExitOK
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
	// non-pushed commits and their objects survive.
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
	// 2. a non-TTY check happens in Run (exit 5).

	// 3. app dir writable.
	if code := a.resolveAppDir(); code != ExitOK {
		return code
	}

	// 4. cfg readable and decryptable.
	if code := a.loadCfg(); code != ExitOK {
		return code
	}

	// 5+6. git and git-lfs present.
	if code := a.checkTools(); code != ExitOK {
		return code
	}

	// 7. git lfs install --skip-repo succeeds.
	if code := a.installLfs(ctx); code != ExitOK {
		return code
	}

	// 8. token resolved.
	if code := a.resolveToken(ctx); code != ExitOK {
		return code
	}

	// 9. GET /user.
	if code := a.resolveUser(ctx); code != ExitOK {
		return code
	}

	// 10. a repo exists or is created.
	if code := a.ensureRepo(ctx); code != ExitOK {
		return code
	}

	// Resolve the remaining persisted settings and load the
	// exclusion file.
	if code := a.resolvePersisted(); code != ExitOK {
		return code
	}

	// 11. workdir writable.
	if code := a.checkWorkdir(); code != ExitOK {
		return code
	}

	// 12. source dirs readable.
	if code := a.checkSourceDirs(); code != ExitOK {
		return code
	}

	// 13. pull/sync install dir writable and elevation.
	if code := a.checkInstallDir(); code != ExitOK {
		return code
	}
	return ExitOK
}

// resolveAppDir sets appDir from the executable location (or the test
// override) and verifies it is writable (preflight step 3).
func (a *App) resolveAppDir() int {
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
	a.cfgPath = config.DefaultPath(a.appDir)
	a.reportPath = filepath.Join(a.appDir, report.Filename(a.Clock()))
	return ExitOK
}

// resolveToken runs the preflight token step: resolve the token and rebuild the
// git runner with it.
func (a *App) resolveToken(ctx context.Context) int {
	var cfgToken, cfgTokenEnv string
	if a.cfg != nil {
		cfgToken = a.cfg.Token
		cfgTokenEnv = a.cfg.TokenEnv
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
	return ExitOK
}

// ensureRepo checks the repository exists, creating it when allowed and
// prompting otherwise (preflight step 10).
func (a *App) ensureRepo(ctx context.Context) int {
	exists, err := a.GitHub.RepoExists(ctx, a.login, a.repoName())
	if err != nil {
		return a.preflightFail("check repository %s/%s: %v", a.login, a.repoName(), err)
	}
	if exists {
		return ExitOK
	}
	if a.Opts.Compare || a.Opts.DryRun {
		_, _ = fmt.Fprintf(a.Stdout, "Repository %s/%s does not exist.\n", a.login, a.repoName())
		return ExitPreflight
	}
	if a.Opts.Yes {
		if err := a.createRepo(ctx); err != nil {
			return ExitPreflight
		}
	} else {
		_, _ = fmt.Fprintf(a.Stdout, "Repository %s/%s does not exist. Create it as public? [y/N] ", a.login, a.repoName())
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
	return ExitOK
}

// resolvePersisted applies the cfg fallbacks for branch, workdir, and the
// exclusion file, then parses the exclusion file.
func (a *App) resolvePersisted() int {
	// Resolve the remaining persisted settings.
	var cfgBranch, cfgWorkdir, cfgExclude string
	if a.cfg != nil {
		cfgBranch = a.cfg.Branch
		cfgWorkdir = a.cfg.Workdir
		cfgExclude = a.cfg.ExcludeFile
	}
	a.branch = config.ResolveBranch(a.Opts.Branch, cfgBranch)
	wd, err := config.ResolveWorkdir(a.Opts.Workdir, cfgWorkdir, a.login)
	if err != nil {
		return a.preflightFail("%v", err)
	}
	a.workdir = wd
	a.exclude = config.ResolveExcludeFile(a.Opts.ExcludeFile, cfgExclude)

	// Exclusions file.
	if a.exclude != "" {
		f, err := os.Open(a.exclude)
		if err != nil {
			return a.preflightFail("open exclusions file %s: %v", a.exclude, err)
		}
		xf, err := exclude.ParseExclusionFile(f)
		if err := f.Close(); err != nil {
			return a.preflightFail("close exclusions file %s: %v", a.exclude, err)
		}
		if err != nil {
			return a.preflightFail("parse exclusions file %s: %v", a.exclude, err)
		}
		a.xf = xf
	}
	return ExitOK
}

// loadCfg reads and decrypts the cfg (preflight step 4).
func (a *App) loadCfg() int {
	cfg, err := config.Load(a.cfgPath)
	if err != nil {
		return a.preflightFail("%v", err)
	}
	a.cfg = cfg
	a.cfgLoaded = true
	return ExitOK
}

// checkTools verifies git, git-lfs and 7z are available (preflight steps
// 5+6). The repository stores each font family as one 7z archive, so the 7z
// tool is required exactly as git and git-lfs are.
func (a *App) checkTools() int {
	if err := gitops.LookPath("git"); err != nil {
		return a.preflightFail("%v", err)
	}
	if err := gitops.LookPath("git-lfs"); err != nil {
		return a.preflightFail("%v", err)
	}
	if err := archive7z.Available(); err != nil {
		return a.preflightFail("%v", err)
	}
	a.sevenz = archive7z.New(a.Log)
	return ExitOK
}

// installLfs runs git lfs install --skip-repo (preflight step 7).
func (a *App) installLfs(ctx context.Context) int {
	a.git = gitops.New("", a.Log) // token set after resolution
	if _, err := a.git.LFSInstall(ctx); err != nil {
		return a.preflightFail("git lfs install --skip-repo failed: %v", err)
	}
	return ExitOK
}

// resolveUser resolves the GitHub account from the token (preflight step 9).
func (a *App) resolveUser(ctx context.Context) int {
	if a.GitHub == nil {
		a.GitHub = github.New(a.token, a.Version)
	}
	login, err := a.GitHub.CurrentUser(ctx)
	if err != nil {
		return a.preflightFail("resolve GitHub account: %v", err)
	}
	a.login = login
	return ExitOK
}

// checkWorkdir creates the workdir and verifies it is writable (preflight step 11).
func (a *App) checkWorkdir() int {
	if err := os.MkdirAll(a.workdir, 0o755); err != nil {
		return a.preflightFail("create workdir %s: %v", a.workdir, err)
	}
	if err := checkDirWritable(a.workdir); err != nil {
		return a.preflightFail("workdir %s is not writable: %v", a.workdir, err)
	}
	return ExitOK
}

// checkSourceDirs resolves the font sources and verifies the dirs are readable (preflight step 12).
func (a *App) checkSourceDirs() int {
	sources, err := a.Platform.FontSources(a.Opts.Source)
	if err != nil {
		return a.preflightFail("%v", err)
	}
	a.fontSources = sources
	for _, dir := range sources.Dirs {
		if err := checkDirReadable(dir); err != nil {
			return a.preflightFail("source font dir %s is not readable: %v", dir, err)
		}
	}
	return ExitOK
}

// checkInstallDir verifies pull/sync install dir prereqs: writability and
// elevation on a Windows system (preflight step 13).
func (a *App) checkInstallDir() int {
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
		_, _ = fmt.Fprintf(a.Stdout, "Created repository %s/%s.\n", a.login, a.repoName())
		return nil
	}
	var ce *github.CreateError
	if errors.As(err, &ce) && (ce.StatusCode == 403 || ce.StatusCode == 404) {
		if ce.AcceptedPermissions != "" {
			_, _ = fmt.Fprintf(a.Stderr, "fontvault: X-Accepted-GitHub-Permissions: %s\n", ce.AcceptedPermissions)
		}
		_, _ = fmt.Fprintf(a.Stderr, "fontvault: the token cannot create repositories. Classic tokens need the public_repo or repo scope; fine-grained tokens need the \"Administration\" (write) or \"Repository creation\" (write) repository permission.\n")
		return fmt.Errorf("create repository %s/%s: %w", a.login, a.repoName(), err)
	}
	return fmt.Errorf("create repository %s/%s: %w", a.login, a.repoName(), err)
}

// confirmSettings prints the non-secret settings and prompts Proceed.
func (a *App) confirmSettings() int {
	mode := a.Opts.ModeName()
	if a.Opts.DryRun {
		mode += " (dry-run)"
	}
	_, _ = fmt.Fprintf(a.Stdout, "fontvault %s\n", a.Version)
	_, _ = fmt.Fprintf(a.Stdout, "mode: %s\n", mode)
	_, _ = fmt.Fprintf(a.Stdout, "github: %s/%s\n", a.login, a.repoName())
	_, _ = fmt.Fprintf(a.Stdout, "branch: %s\n", a.branch)
	_, _ = fmt.Fprintf(a.Stdout, "workdir: %s\n", a.workdir)
	_, _ = fmt.Fprintf(a.Stdout, "source: %s\n", a.Opts.Source)
	_, _ = fmt.Fprintln(a.Stdout, "font dirs:")
	for _, d := range a.fontSources.Dirs {
		_, _ = fmt.Fprintf(a.Stdout, "  - %s\n", d)
	}
	if len(a.fontSources.Dirs) == 0 {
		_, _ = fmt.Fprintln(a.Stdout, "  (none)")
	}
	if a.exclude != "" {
		_, _ = fmt.Fprintf(a.Stdout, "exclusions: %s\n", a.exclude)
	} else {
		_, _ = fmt.Fprintln(a.Stdout, "exclusions: (none)")
	}
	_, _ = fmt.Fprintf(a.Stdout, "token source: %s\n", a.tokenSource)
	_, _ = fmt.Fprintf(a.Stdout, "cfg: %s\n", a.cfgPath)
	_, _ = fmt.Fprintf(a.Stdout, "report: %s\n", a.reportPath)

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
	cloErr := f.Close()
	remErr := os.Remove(name)
	if remErr != nil {
		return remErr
	}
	return cloErr
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
