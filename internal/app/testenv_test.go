package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"fontvault/internal/cli"
	"fontvault/internal/config"
	"fontvault/internal/exclude"
	"fontvault/internal/install"
	"fontvault/internal/testfont"
)

// ---- test environment ------------------------------------------------------

type testEnv struct {
	t        *testing.T
	root     string
	barePath string
	workdir  string
	appDir   string
	fontDir  string
	sysDir   string
	install  string
	clock    time.Time
	identity bool
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	requireGit(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	gitconfig := filepath.Join(home, ".gitconfig")
	cfg := "[user]\n\tname = Font Vault Test\n\temail = fontvault-test@example.invalid\n"
	if err := os.WriteFile(gitconfig, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	env := &testEnv{
		t:        t,
		root:     root,
		barePath: filepath.Join(root, "remote.git"),
		workdir:  filepath.Join(root, "work"),
		appDir:   filepath.Join(root, "appdir"),
		fontDir:  filepath.Join(root, "userfonts"),
		sysDir:   filepath.Join(root, "sysfonts"),
		install:  filepath.Join(root, "installdir"),
		clock:    time.Date(2026, 10, 29, 14, 5, 7, 0, time.FixedZone("CET", 3600)),
		identity: true,
	}
	git(t, root, "init", "--bare", env.barePath)
	for _, d := range []string{env.appDir, env.fontDir, env.sysDir, env.install} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return env
}

// clearIdentity removes user.name/user.email so commit identity checks fail.
func (e *testEnv) clearIdentity() {
	e.t.Helper()
	home := os.Getenv("HOME")
	cfg := "[core]\n\tautocrlf = false\n"
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(cfg), 0o600); err != nil {
		e.t.Fatal(err)
	}
	e.identity = false
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	if _, err := exec.LookPath("git-lfs"); err != nil {
		t.Skip("git-lfs not available")
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %s: %v\nstderr: %s", strings.Join(args, " "), err, errb.String())
	}
	return strings.TrimSpace(out.String())
}

// bare runs git with --git-dir pointing at the bare repository.
func (e *testEnv) bare(args ...string) string {
	e.t.Helper()
	full := append([]string{"--git-dir=" + e.barePath}, args...)
	return git(e.t, e.root, full...)
}

// bareRefExists reports whether a ref exists in the bare repo (non-fatal).
func (e *testEnv) bareRefExists(ref string) bool {
	e.t.Helper()
	cmd := exec.Command("git", "--git-dir="+e.barePath, "rev-parse", "--verify", ref)
	cmd.Dir = e.root
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	var errb bytes.Buffer
	cmd.Stderr = &errb
	err := cmd.Run()
	return err == nil
}

func (e *testEnv) head() string {
	e.t.Helper()
	out := e.bare("rev-parse", "refs/heads/main")
	if out == "" {
		return ""
	}
	return out
}

func (e *testEnv) show(commit, path string) string {
	e.t.Helper()
	return e.bare("show", commit+":"+path)
}

// writeFont writes a fixture font into dir.
func (e *testEnv) writeFont(dir, name string, spec testfont.Spec) string {
	e.t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, testfont.Build(e.t, spec), 0o644); err != nil {
		e.t.Fatal(err)
	}
	return path
}

func (e *testEnv) lsLFSObjects() []string {
	e.t.Helper()
	var out []string
	root := filepath.Join(e.workdir, ".git", "lfs", "objects")
	// The visitor swallows per-entry errors (a missing objects dir simply means
	// "no LFS objects"), so a non-nil result from Walk is a real failure and
	// the listing would be incomplete: fail the test instead of asserting
	// against a partial answer.
	walkErr := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	if walkErr != nil {
		e.t.Fatalf("walk %s: %v", root, walkErr)
	}
	return out
}

// ---- fakes -----------------------------------------------------------------

type fakeGitHub struct {
	login       string
	exists      bool
	userErr     error
	existsErr   error
	createErr   error
	createCalls int
}

func (f *fakeGitHub) CurrentUser(ctx context.Context) (string, error) {
	if f.userErr != nil {
		return "", f.userErr
	}
	return f.login, nil
}

func (f *fakeGitHub) RepoExists(ctx context.Context, owner, name string) (bool, error) {
	if f.existsErr != nil {
		return false, f.existsErr
	}
	return f.exists, nil
}

func (f *fakeGitHub) CreateRepo(ctx context.Context, name string, private bool) error {
	f.createCalls++
	return f.createErr
}

type fakeClassifier struct{ defaults map[string]bool }

func (f *fakeClassifier) Name() string { return "fake" }

func (f *fakeClassifier) IsOSDefault(path string) (bool, error) {
	return f.defaults[path], nil
}

// fakeInstaller installs into a directory and records every call.
type fakeInstaller struct {
	dir         string
	installed   []string
	removed     []string
	registered  []string
	finishes    int
	failInstall map[string]error
	failRemove  map[string]error
	collisions  map[string]bool
}

func (f *fakeInstaller) Install(_ context.Context, tempPath string, meta install.Meta) (string, error) {
	if err := f.failInstall[meta.Filename]; err != nil {
		return "", err
	}
	if f.collisions[meta.Filename] {
		return "", install.ErrRegistryNameCollision
	}
	target := filepath.Join(f.dir, meta.Filename)
	if err := os.Rename(tempPath, target); err != nil {
		return "", err
	}
	f.installed = append(f.installed, meta.Filename)
	return target, nil
}

func (f *fakeInstaller) NameCollision(meta install.Meta) (bool, error) {
	return f.collisions[meta.Filename], nil
}

func (f *fakeInstaller) RemoveFont(_ context.Context, path string, _ install.Meta) error {
	if err := f.failRemove[filepath.Base(path)]; err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	f.removed = append(f.removed, path)
	return nil
}

func (f *fakeInstaller) RegisterFont(_ context.Context, path string, _ install.Meta) error {
	f.registered = append(f.registered, path)
	return nil
}

func (f *fakeInstaller) Finish(_ context.Context) error {
	f.finishes++
	return nil
}

type fakePlatform struct {
	env        *testEnv
	classifier *fakeClassifier
	installer  *fakeInstaller
}

func (p *fakePlatform) OSInfo() string { return "TestOS 1.0" }

func (p *fakePlatform) FontSources(source string) (FontSources, error) {
	switch source {
	case cli.SourceSystem:
		return FontSources{Dirs: []string{p.env.sysDir}, Recursive: false}, nil
	case cli.SourceUser:
		return FontSources{Dirs: []string{p.env.fontDir}, Recursive: false}, nil
	}
	return FontSources{}, fmt.Errorf("bad source %q", source)
}

func (p *fakePlatform) InstallDir(source string) (string, error) {
	return p.env.install, nil
}

func (p *fakePlatform) Classifier() exclude.OSClassifier { return p.classifier }

func (p *fakePlatform) NewInstaller(system bool, installDir string) install.Installer {
	return p.installer
}

func (p *fakePlatform) CheckPullPrereqs(source, installDir string) error { return nil }

// ---- app runner ------------------------------------------------------------

type runResult struct {
	code   int
	stdout string
	stderr string
}

func (e *testEnv) runApp(t *testing.T, args []string, stdin string, gh *fakeGitHub, plat *fakePlatform) runResult {
	t.Helper()
	// Ensure a token and a workdir are always available; the fake GitHub
	// ignores the token, and the workdir must live under the test root.
	hasToken := false
	hasWorkdir := false
	for _, a := range args {
		if a == "-t" || a == "--token" {
			hasToken = true
		}
		if a == "-w" || a == "--workdir" {
			hasWorkdir = true
		}
	}
	if !hasToken {
		args = append(append([]string{}, args...), "-t", "test-token")
	}
	if !hasWorkdir {
		args = append(append([]string{}, args...), "-w", e.workdir)
	}
	opts, err := cli.Parse(args, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	var out, errb bytes.Buffer
	a := New(opts, "test-version", strings.NewReader(stdin), &out, &errb)
	a.TTY = func() bool { return true }
	a.SetRemoteURL(filepath.ToSlash(e.barePath))
	a.SetAppDir(e.appDir)
	a.GitHub = gh
	a.Platform = plat
	a.GhToken = func(ctx context.Context) (string, error) { return "", errors.New("gh unavailable") }
	a.Env = func(string) (string, bool) { return "", false }
	a.Clock = func() time.Time { return e.clock }
	code := a.Run(context.Background())
	return runResult{code: code, stdout: out.String(), stderr: errb.String()}
}

func (e *testEnv) newPlat(t *testing.T) *fakePlatform {
	t.Helper()
	return &fakePlatform{
		env:        e,
		classifier: &fakeClassifier{defaults: map[string]bool{}},
		installer: &fakeInstaller{
			dir:         e.install,
			failInstall: map[string]error{},
			failRemove:  map[string]error{},
			collisions:  map[string]bool{},
		},
	}
}

func ghExists(login string) *fakeGitHub {
	return &fakeGitHub{login: login, exists: true}
}

// ---- fixtures --------------------------------------------------------------

func fontSpecA(extra ...byte) testfont.Spec {
	return testfont.Spec{
		PostScriptName: "Alpha-Regular",
		FullName:       "Alpha Regular",
		Family:         "Alpha",
		Subfamily:      "Regular",
		Version:        "1.000",
		Vendor:         "ABCD",
		Revision:       0x00010000,
		Filler:         extra,
	}
}

func fontSpecB() testfont.Spec {
	return testfont.Spec{
		PostScriptName: "Bravo-Bold",
		FullName:       "Bravo Bold",
		Family:         "Bravo",
		Subfamily:      "Bold",
		Version:        "2.000",
		Vendor:         "EFGH",
		Revision:       0x00020000,
	}
}

func fontSpecC() testfont.Spec {
	return testfont.Spec{
		PostScriptName: "Charlie-Regular",
		FullName:       "Charlie Regular",
		Family:         "Charlie",
		Subfamily:      "Regular",
		Version:        "3.000",
		Vendor:         "IJKL",
		Revision:       0x00030000,
	}
}

func pushArgs(source string) []string {
	return []string{"-s", source, "-d", "push", "-y"}
}

func pullArgs(source string) []string {
	return []string{"-s", source, "-d", "pull", "-y"}
}

// seededEnv returns a fresh env and plat whose user font dir holds fonts
// (file name → spec, written in sorted order) already pushed once, seeding
// the repo.
func seededEnv(t *testing.T, fonts map[string]testfont.Spec) (*testEnv, *fakePlatform) {
	t.Helper()
	env := newTestEnv(t)
	plat := env.newPlat(t)
	names := make([]string, 0, len(fonts))
	for name := range fonts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		env.writeFont(env.fontDir, name, fonts[name])
	}
	if res := env.runApp(t, pushArgs("user"), "", ghExists("alice"), plat); res.code != ExitOK {
		t.Fatalf("seed push: %d (%s)", res.code, res.stderr)
	}
	return env, plat
}

// readReport decrypts nothing; returns the report text.
func (e *testEnv) reportText() string {
	e.t.Helper()
	entries, err := os.ReadDir(e.appDir)
	if err != nil {
		e.t.Fatal(err)
	}
	for _, ent := range entries {
		if strings.HasPrefix(ent.Name(), "fontvault_sync_") {
			data, err := os.ReadFile(filepath.Join(e.appDir, ent.Name()))
			if err != nil {
				e.t.Fatal(err)
			}
			return string(data)
		}
	}
	return ""
}

// cfgFile loads the cfg written by a successful run. config.Load returns
// (nil, nil) when the file is absent, so the helper fails the test there
// instead of handing callers a nil *config.File to dereference.
func (e *testEnv) cfgFile() *config.File {
	e.t.Helper()
	f, err := config.Load(filepath.Join(e.appDir, "fontvault.cfg"))
	if err != nil {
		e.t.Fatal(err)
	}
	if f == nil {
		e.t.Fatal("cfg file was not written")
	}
	return f
}
