package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fontvault/internal/archive7z"
	"fontvault/internal/fonts"
	"fontvault/internal/github"
	"fontvault/internal/testfont"
)

// TestRepoMissingCreateFlowYes covers: repo missing, -y answers yes,
// CreateRepo is called once, and the run proceeds to push.
func TestRepoMissingCreateFlowYes(t *testing.T) {
	env := newTestEnv(t)
	plat := env.newPlat(t)
	env.writeFont(env.fontDir, "alpha.ttf", fontSpecA())

	gh := &fakeGitHub{login: "alice", exists: false}
	res := env.runApp(t, pushArgs("user"), "", gh, plat)

	if res.code != ExitOK {
		t.Fatalf("exit = %d (stdout: %s, stderr: %s)", res.code, res.stdout, res.stderr)
	}
	if gh.createCalls != 1 {
		t.Errorf("CreateRepo calls = %d, want 1", gh.createCalls)
	}
	if !strings.Contains(res.stdout, "Created repository alice/FontVault_fonts.") {
		t.Errorf("stdout missing creation message: %q", res.stdout)
	}
	// The push went through to the (local bare) remote.
	if env.head() == "" {
		t.Error("no commit pushed after repo creation")
	}
}

// TestRepoMissingCreateFlowNo covers the interactive decline (exit 6).
func TestRepoMissingCreateFlowNo(t *testing.T) {
	env := newTestEnv(t)
	plat := env.newPlat(t)
	gh := &fakeGitHub{login: "alice", exists: false}
	res := env.runApp(t, []string{"-s", "user", "-d", "push"}, "n\n", gh, plat)
	if res.code != ExitCancelled {
		t.Fatalf("exit = %d, want %d", res.code, ExitCancelled)
	}
	if gh.createCalls != 0 {
		t.Errorf("CreateRepo must not be called on decline")
	}
	if !strings.Contains(res.stdout, "does not exist. Create it as public?") {
		t.Errorf("stdout missing prompt: %q", res.stdout)
	}
}

// TestRepoMissingCreateFlow403 covers the permission error output (exit 3).
func TestRepoMissingCreateFlow403(t *testing.T) {
	env := newTestEnv(t)
	plat := env.newPlat(t)
	gh := &fakeGitHub{
		login:     "alice",
		exists:    false,
		createErr: &github.CreateError{StatusCode: 403, AcceptedPermissions: "contents=read"},
	}
	res := env.runApp(t, pushArgs("user"), "", gh, plat)
	if res.code != ExitPreflight {
		t.Fatalf("exit = %d, want %d", res.code, ExitPreflight)
	}
	if !strings.Contains(res.stderr, "X-Accepted-GitHub-Permissions: contents=read") {
		t.Errorf("stderr missing permissions header: %q", res.stderr)
	}
	if !strings.Contains(res.stderr, "public_repo or repo") || !strings.Contains(res.stderr, "Administration") {
		t.Errorf("stderr missing scope guidance: %q", res.stderr)
	}
}

// TestRepoMissingCompareExits3: compare (and -n) never create the repo.
func TestRepoMissingCompareExits3(t *testing.T) {
	for _, args := range [][]string{
		{"-s", "user", "-c", "-y"},
		{"-s", "user", "-d", "push", "-n", "-y"},
	} {
		env := newTestEnv(t)
		plat := env.newPlat(t)
		gh := &fakeGitHub{login: "alice", exists: false}
		res := env.runApp(t, args, "", gh, plat)
		if res.code != ExitPreflight {
			t.Fatalf("args %v: exit = %d, want %d", args, res.code, ExitPreflight)
		}
		if gh.createCalls != 0 {
			t.Errorf("args %v: CreateRepo must not be called", args)
		}
		if !strings.Contains(res.stdout, "does not exist") {
			t.Errorf("args %v: stdout missing message: %q", args, res.stdout)
		}
	}
}

// TestFirstPushToEmptyRepo: init commit with .gitattributes, then the font
// commit; fonts are LFS pointers; the sparse worktree holds no fonts.
func TestFirstPushToEmptyRepo(t *testing.T) {
	env := newTestEnv(t)
	plat := env.newPlat(t)
	env.writeFont(env.fontDir, "alpha.ttf", fontSpecA())
	bravo := fontSpecB()
	bravo.OTTO = true
	env.writeFont(env.fontDir, "bravo.otf", bravo)

	res := env.runApp(t, pushArgs("user"), "", ghExists("alice"), plat)
	if res.code != ExitOK {
		t.Fatalf("exit = %d (stderr: %s)", res.code, res.stderr)
	}

	checkInitThenFontsCommits(t, env, "Add 2 fonts")
	checkLFSAttributes(t, env, "*.7z")
	checkArchiveBlobsArePointers(t, env, 2)
	checkManifestFileCount(t, env, 2)
	checkWorktreeHasNoFonts(t, env)
	checkPushReportWritten(t, env)
}

// checkInitThenFontsCommits asserts the remote history is exactly two
// commits: the .gitattributes init, then the fonts commit addSubject.
func checkInitThenFontsCommits(t *testing.T, env *testEnv, addSubject string) {
	t.Helper()
	log := strings.Split(env.bare("log", "--format=%s", "refs/heads/main"), "\n")
	if len(log) != 2 {
		t.Fatalf("commits = %v", log)
	}
	if !strings.Contains(log[0], addSubject) || !strings.Contains(log[1], "Initialize fontvault") {
		t.Errorf("commit messages = %v", log)
	}
}

// checkLFSAttributes asserts .gitattributes routes each extension through LFS.
func checkLFSAttributes(t *testing.T, env *testEnv, extensions ...string) {
	t.Helper()
	attrs := env.show(env.head(), ".gitattributes")
	for _, ext := range extensions {
		want := ext + " filter=lfs diff=lfs merge=lfs -text"
		if !strings.Contains(attrs, want) {
			t.Errorf(".gitattributes missing %q: %q", want, attrs)
		}
	}
}

// checkArchiveBlobsArePointers asserts exactly want family archives are in the
// tree, every one of them an LFS pointer, and that no loose font file was
// written: fonts live inside the archive, never beside it.
func checkArchiveBlobsArePointers(t *testing.T, env *testEnv, want int) {
	t.Helper()
	head := env.head()
	tree := env.bare("ls-tree", "-r", "--name-only", "refs/heads/main", "--", "fonts")
	seen := 0
	for p := range strings.SplitSeq(tree, "\n") {
		if p == "" {
			continue
		}
		if strings.HasSuffix(p, ".ttf") || strings.HasSuffix(p, ".otf") {
			t.Errorf("loose font file in the tree: %s", p)
			continue
		}
		if !strings.HasSuffix(p, ".7z") {
			continue
		}
		seen++
		blob := env.show(head, p)
		if !strings.HasPrefix(blob, "version https://git-lfs.github.com/spec/v1") {
			t.Errorf("blob %s is not an LFS pointer: %q", p, blob)
		}
	}
	if seen != want {
		t.Errorf("family archives in tree = %d, want %d", seen, want)
	}
}

// archivePath returns the repository path of a family's archive, using the
// product's own derivation so the test cannot drift from it.
func archivePath(family string) string {
	f := fonts.Font{Family: family}
	return f.RepoPath()
}

// checkManifestFileCount asserts the manifest directory holds want files.
func checkManifestFileCount(t *testing.T, env *testEnv, want int) {
	t.Helper()
	manifests := env.bare("ls-tree", "-r", "--name-only", "refs/heads/main", "--", "manifest")
	if len(strings.Split(manifests, "\n")) != want {
		t.Errorf("manifest files = %q", manifests)
	}
}

// checkWorktreeHasNoFonts asserts the sparse worktree holds no font files.
func checkWorktreeHasNoFonts(t *testing.T, env *testEnv) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(env.workdir, "fonts")); !os.IsNotExist(err) {
		t.Error("worktree still contains fonts/ after push")
	}
}

// checkPushReportWritten asserts the report shows push mode and the new commit.
func checkPushReportWritten(t *testing.T, env *testEnv) {
	t.Helper()
	text := env.reportText()
	for _, want := range []string{"mode: push", "added to repo", "new commit: " + env.head()} {
		if !strings.Contains(text, want) {
			t.Errorf("report missing %q: %q", want, text)
		}
	}
}

// TestRepeatPushIsNoOp: a second push with identical fonts changes nothing.
func TestRepeatPushIsNoOp(t *testing.T) {
	env := newTestEnv(t)
	plat := env.newPlat(t)
	env.writeFont(env.fontDir, "alpha.ttf", fontSpecA())
	if res := env.runApp(t, pushArgs("user"), "", ghExists("alice"), plat); res.code != ExitOK {
		t.Fatalf("first push: %d (%s)", res.code, res.stderr)
	}
	before := env.head()

	res := env.runApp(t, pushArgs("user"), "", ghExists("alice"), plat)
	if res.code != ExitOK {
		t.Fatalf("second push: %d (%s)", res.code, res.stderr)
	}
	if env.head() != before {
		t.Error("repeat push must not create a commit")
	}
	if !strings.Contains(env.reportText(), "new commit: (no new commit)") {
		t.Error("report must show no new commit")
	}
}

// TestPushConflictUnderYesSkips: -y skips conflicts and exits 2.
func TestPushConflictUnderYesSkips(t *testing.T) {
	env := newTestEnv(t)
	plat := env.newPlat(t)
	env.writeFont(env.fontDir, "alpha.ttf", fontSpecA())
	if res := env.runApp(t, pushArgs("user"), "", ghExists("alice"), plat); res.code != ExitOK {
		t.Fatalf("first push: %d (%s)", res.code, res.stderr)
	}
	oldSHA := env.head()
	oldBlob := env.show(oldSHA, archivePath("Alpha"))

	// Same key, different content.
	if err := os.Remove(filepath.Join(env.fontDir, "alpha.ttf")); err != nil {
		t.Fatal(err)
	}
	env.writeFont(env.fontDir, "alpha.ttf", fontSpecA('x', 'y', 'z'))

	res := env.runApp(t, pushArgs("user"), "", ghExists("alice"), plat)
	if res.code != ExitCompleted {
		t.Fatalf("exit = %d, want %d (stderr: %s)", res.code, ExitCompleted, res.stderr)
	}
	if env.head() != oldSHA {
		t.Error("skipped conflict must not create a commit")
	}
	if env.show(env.head(), archivePath("Alpha")) != oldBlob {
		t.Error("repo copy changed despite skip")
	}
	text := env.reportText()
	if !strings.Contains(text, "conflicts skipped") || !strings.Contains(text, "reason: -y") {
		t.Errorf("report missing skipped conflict: %q", text)
	}
}

// TestPushLocalDuplicateUnderYesSkipsAndReports: -y skips a local duplicate
// key, lists it under Conflicts skipped and exits 2, like compare does.
func TestPushLocalDuplicateUnderYesSkipsAndReports(t *testing.T) {
	env := newTestEnv(t)
	plat := env.newPlat(t)
	env.writeFont(env.fontDir, "alpha.ttf", fontSpecA())
	if res := env.runApp(t, pushArgs("user"), "", ghExists("alice"), plat); res.code != ExitOK {
		t.Fatalf("first push: %d (%s)", res.code, res.stderr)
	}
	oldSHA := env.head()

	// Same key, different content: a local duplicate, skipped under -y.
	env.writeFont(env.fontDir, "alpha2.ttf", fontSpecA('x', 'y', 'z'))

	res := env.runApp(t, pushArgs("user"), "", ghExists("alice"), plat)
	if res.code != ExitCompleted {
		t.Fatalf("exit = %d, want %d (stderr: %s)", res.code, ExitCompleted, res.stderr)
	}
	if env.head() != oldSHA {
		t.Error("skipped local duplicate must not create a commit")
	}
	text := env.reportText()
	if !strings.Contains(text, "conflicts skipped") || !strings.Contains(text, "reason: -y") {
		t.Errorf("report missing skipped local duplicate: %q", text)
	}
}

// TestPushReplaceRepo: interactive "r" replaces the repo copy; the old LFS
// pointer remains in git history.
func TestPushReplaceRepo(t *testing.T) {
	env := newTestEnv(t)
	plat := env.newPlat(t)
	env.writeFont(env.fontDir, "alpha.ttf", fontSpecA())
	if res := env.runApp(t, pushArgs("user"), "", ghExists("alice"), plat); res.code != ExitOK {
		t.Fatalf("first push: %d (%s)", res.code, res.stderr)
	}
	oldSHA := env.head()
	repoPath := archivePath("Alpha")

	if err := os.Remove(filepath.Join(env.fontDir, "alpha.ttf")); err != nil {
		t.Fatal(err)
	}
	env.writeFont(env.fontDir, "alpha.ttf", fontSpecA('n', 'e', 'w'))

	res := env.runApp(t, []string{"-s", "user", "-d", "push"}, "y\nr\n", ghExists("alice"), plat)
	if res.code != ExitOK {
		t.Fatalf("exit = %d (stdout: %s, stderr: %s)", res.code, res.stdout, res.stderr)
	}
	if !strings.Contains(res.stdout, "[r]eplace repo copy with local / [s]kip") {
		t.Errorf("conflict prompt not shown: %q", res.stdout)
	}
	if env.head() == oldSHA {
		t.Fatal("replace must create a commit")
	}
	// New blob has the new oid; old object remains in history.
	newBlob := env.show(env.head(), repoPath)
	if !strings.Contains(newBlob, "oid sha256:") {
		t.Fatalf("new blob not a pointer: %q", newBlob)
	}
	oldBlob := env.show(oldSHA, repoPath)
	if !strings.Contains(oldBlob, "oid sha256:") || oldBlob == newBlob {
		t.Errorf("old object must remain in history (old=%q new=%q)", oldBlob, newBlob)
	}
	text := env.reportText()
	if !strings.Contains(text, "replaced in repo") {
		t.Errorf("report missing replaced section: %q", text)
	}
}

// TestNonFastForwardResolvedByRetry: the remote moves between commit and
// push; the push is rebased and retried until it succeeds. A pre-push hook
// moves the remote exactly once, on the first push attempt, making that
// attempt fail with a non-fast-forward rejection.
func TestNonFastForwardResolvedByRetry(t *testing.T) {
	env := newTestEnv(t)
	plat := env.newPlat(t)
	env.writeFont(env.fontDir, "alpha.ttf", fontSpecA())
	if res := env.runApp(t, pushArgs("user"), "", ghExists("alice"), plat); res.code != ExitOK {
		t.Fatalf("first push: %d (%s)", res.code, res.stderr)
	}

	// An external clone with one un-pushed commit.
	ext := filepath.Join(env.root, "external")
	git(t, env.root, "clone", filepath.ToSlash(env.barePath), ext)
	git(t, ext, "checkout", "main")
	git(t, ext, "config", "user.name", "External")
	git(t, ext, "config", "user.email", "external@example.invalid")
	if err := os.WriteFile(filepath.Join(ext, "other.txt"), []byte("external change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, ext, "add", "other.txt")
	git(t, ext, "commit", "-m", "external commit")

	// Install a pre-push hook that pushes the external commit on the first
	// invocation only, moving the remote underneath the app's push.
	marker := filepath.ToSlash(filepath.Join(env.root, "hook-ran"))
	hook := filepath.Join(env.workdir, ".git", "hooks", "pre-push")
	script := "#!/bin/sh\n" +
		"if [ ! -f \"" + marker + "\" ]; then\n" +
		"  touch \"" + marker + "\"\n" +
		"  git -C \"" + filepath.ToSlash(ext) + "\" push origin main >/dev/null 2>&1\n" +
		"fi\n" +
		"exit 0\n"
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	// Now push a new font: the first push attempt is rejected, the local
	// commit is rebased, and the retry succeeds.
	env.writeFont(env.fontDir, "bravo.ttf", fontSpecB())
	res := env.runApp(t, pushArgs("user"), "", ghExists("alice"), plat)
	if res.code != ExitOK {
		t.Fatalf("exit = %d (stdout: %s, stderr: %s)", res.code, res.stdout, res.stderr)
	}
	log := strings.Split(env.bare("log", "--format=%s", "refs/heads/main"), "\n")
	// History: the rebased font commit sits on top of the external commit
	// that moved the remote during the first push attempt.
	if len(log) != 4 {
		t.Fatalf("commits = %v", log)
	}
	if !strings.Contains(log[0], "Add 1 font") || log[1] != "external commit" {
		t.Errorf("history order wrong: %v", log)
	}
}

// TestNonFastForwardSameFamilyRecovers: two machines add different fonts to
// the same family. The second machine's commit lands on the remote during the
// first machine's push, so both commits rewrite the same family archive and
// manifest and a plain rebase conflicts. The run must still finish with all
// three fonts in the family and leave the workdir usable for the next run.
func TestNonFastForwardSameFamilyRecovers(t *testing.T) {
	env := newTestEnv(t)
	plat := env.newPlat(t)
	env.writeFont(env.fontDir, "alpha.ttf", fontSpecA())
	if res := env.runApp(t, pushArgs("user"), "", ghExists("alice"), plat); res.code != ExitOK {
		t.Fatalf("first push: %d (%s)", res.code, res.stderr)
	}
	base := env.head()

	// A second machine (its own workdir) adds Alpha Bold to the family, then
	// the remote is rewound so that commit is pending in its workdir only.
	bold := fontSpecA()
	bold.PostScriptName, bold.FullName, bold.Subfamily = "Alpha-Bold", "Alpha Bold", "Bold"
	boldPath := env.writeFont(env.fontDir, "alpha-bold.ttf", bold)
	ext := filepath.Join(env.root, "external")
	extArgs := append(pushArgs("user"), "-w", ext)
	if res := env.runApp(t, extArgs, "", ghExists("alice"), plat); res.code != ExitOK {
		t.Fatalf("external push: %d (%s)", res.code, res.stderr)
	}
	if err := os.Remove(boldPath); err != nil {
		t.Fatal(err)
	}
	env.bare("update-ref", "refs/heads/main", base)

	// The first machine's pre-push hook pushes the external commit on the
	// first invocation only, moving the remote underneath the app's push.
	marker := filepath.ToSlash(filepath.Join(env.root, "hook-ran"))
	hook := filepath.Join(env.workdir, ".git", "hooks", "pre-push")
	script := "#!/bin/sh\n" +
		"if [ ! -f \"" + marker + "\" ]; then\n" +
		"  touch \"" + marker + "\"\n" +
		"  git -C \"" + filepath.ToSlash(ext) + "\" push origin main >/dev/null 2>&1\n" +
		"fi\n" +
		"exit 0\n"
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	italic := fontSpecA()
	italic.PostScriptName, italic.FullName, italic.Subfamily = "Alpha-Italic", "Alpha Italic", "Italic"
	env.writeFont(env.fontDir, "alpha-italic.ttf", italic)
	res := env.runApp(t, pushArgs("user"), "", ghExists("alice"), plat)
	if res.code != ExitOK {
		t.Fatalf("exit = %d (stdout: %s, stderr: %s)", res.code, res.stdout, res.stderr)
	}
	log := strings.Split(env.bare("log", "--format=%s", "refs/heads/main"), "\n")
	if len(log) != 4 {
		t.Fatalf("commits = %v", log)
	}
	manifest := env.show("refs/heads/main", fonts.FamilyManifestPath("Alpha"))
	for _, want := range []string{"Alpha-Regular", "Alpha-Bold", "Alpha-Italic"} {
		if !strings.Contains(manifest, `"postscript_name": "`+want+`"`) && !strings.Contains(manifest, `"postscript_name":"`+want+`"`) {
			t.Errorf("family manifest lacks %s:\n%s", want, manifest)
		}
	}

	checkArchiveMembers(t, env, "Alpha", []string{"alpha-regular-", "alpha-bold-", "alpha-italic-"})

	// The workdir must not be left mid-rebase: a repeat push is a no-op.
	if res := env.runApp(t, pushArgs("user"), "", ghExists("alice"), plat); res.code != ExitOK {
		t.Errorf("repeat push after recovery: %d (stderr: %s)", res.code, res.stderr)
	}
}

// checkArchiveMembers lists the family archive the remote branch points at
// (through its LFS pointer and the bare repo's LFS store) and asserts it holds
// exactly one member per prefix in want.
func checkArchiveMembers(t *testing.T, env *testEnv, family string, want []string) {
	t.Helper()
	pointer := env.show("refs/heads/main", archivePath(family))
	var oid string
	for line := range strings.SplitSeq(pointer, "\n") {
		if rest, ok := strings.CutPrefix(line, "oid sha256:"); ok {
			oid = strings.TrimSpace(rest)
		}
	}
	if len(oid) != 64 {
		t.Fatalf("archive pointer for %s has no oid: %q", family, pointer)
	}
	object := filepath.Join(env.barePath, "lfs", "objects", oid[:2], oid[2:4], oid)
	tool := archive7z.New(nil)
	members, err := tool.List(context.Background(), object)
	if err != nil {
		t.Fatalf("list %s: %v", object, err)
	}
	if len(members) != len(want) {
		t.Errorf("archive members = %v, want %d", members, len(want))
	}
	for _, prefix := range want {
		found := false
		for _, m := range members {
			if strings.HasPrefix(m, prefix) {
				found = true
			}
		}
		if !found {
			t.Errorf("archive lacks a %s* member: %v", prefix, members)
		}
	}
}

// TestPullInstallsOnlyMissingFonts: repo has A and B; only B is present
// locally; pull installs only A.
func TestPullInstallsOnlyMissingFonts(t *testing.T) {
	env, plat := seededEnv(t, map[string]testfont.Spec{
		"alpha.ttf": fontSpecA(),
		"bravo.ttf": fontSpecB(),
	})

	// Locally, only Bravo remains.
	if err := os.Remove(filepath.Join(env.fontDir, "alpha.ttf")); err != nil {
		t.Fatal(err)
	}
	res := env.runApp(t, pullArgs("user"), "", ghExists("alice"), plat)
	if res.code != ExitOK {
		t.Fatalf("pull: %d (stderr: %s)", res.code, res.stderr)
	}
	if len(plat.installer.installed) != 1 {
		t.Fatalf("installed = %v, want only Alpha", plat.installer.installed)
	}
	if !strings.Contains(plat.installer.installed[0], "alpha-regular") {
		t.Errorf("installed = %v", plat.installer.installed)
	}
	if _, err := os.Stat(filepath.Join(env.install, filepath.Base(plat.installer.installed[0]))); err != nil {
		t.Errorf("installed file missing: %v", err)
	}
	text := env.reportText()
	if !strings.Contains(text, "installed locally") {
		t.Errorf("report missing installed section: %q", text)
	}
}

// TestPullReplaceLocalRollbackOnDeleteFailure: the user chooses "replace
// local", the delete fails, the old font is re-registered and both copies
// stay unchanged.
func TestPullReplaceLocalRollbackOnDeleteFailure(t *testing.T) {
	env, plat := seededEnv(t, map[string]testfont.Spec{"alpha.ttf": fontSpecA()})

	// Local copy changes to different content.
	localPath := filepath.Join(env.fontDir, "alpha.ttf")
	if err := os.Remove(localPath); err != nil {
		t.Fatal(err)
	}
	env.writeFont(env.fontDir, "alpha.ttf", fontSpecA('l', 'o', 'c', 'a', 'l'))
	before, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatal(err)
	}

	plat.installer.failRemove["alpha.ttf"] = errInjected
	res := env.runApp(t, []string{"-s", "user", "-d", "pull"}, "y\nl\n", ghExists("alice"), plat)

	if res.code != ExitCompleted {
		t.Fatalf("exit = %d, want %d (stdout: %s)", res.code, ExitCompleted, res.stdout)
	}
	after, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("old file must survive: %v", err)
	}
	if string(after) != string(before) {
		t.Error("old file content changed")
	}
	if len(plat.installer.registered) != 1 || plat.installer.registered[0] != localPath {
		t.Errorf("old font not re-registered: %v", plat.installer.registered)
	}
	if len(plat.installer.installed) != 0 {
		t.Errorf("nothing may be installed: %v", plat.installer.installed)
	}
	text := env.reportText()
	if !strings.Contains(text, "errors") || !strings.Contains(text, "left in place") {
		t.Errorf("report missing error entry: %q", text)
	}
}

// TestPullReplaceLocal: happy path replacement after an interactive "l".
func TestPullReplaceLocal(t *testing.T) {
	env, plat := seededEnv(t, map[string]testfont.Spec{"alpha.ttf": fontSpecA()})
	localPath := filepath.Join(env.fontDir, "alpha.ttf")
	if err := os.Remove(localPath); err != nil {
		t.Fatal(err)
	}
	env.writeFont(env.fontDir, "alpha.ttf", fontSpecA('l', 'o', 'c', 'a', 'l'))

	res := env.runApp(t, []string{"-s", "user", "-d", "pull"}, "y\nl\n", ghExists("alice"), plat)
	if res.code != ExitOK {
		t.Fatalf("exit = %d (stdout: %s, stderr: %s)", res.code, res.stdout, res.stderr)
	}
	if !strings.Contains(res.stdout, "[l]ocal replaced by repo copy / [s]kip") {
		t.Errorf("prompt missing: %q", res.stdout)
	}
	// The old local file is gone, and the new copy sits in the installation dir
	// with the original repo font bytes.
	if _, err := os.Stat(localPath); !os.IsNotExist(err) {
		t.Error("old local file must be deleted after replace")
	}
	installedName := "alpha-regular-" + fonts.HashKey("Alpha-Regular|ABCD")[:8] + ".ttf"
	installedData, err := os.ReadFile(filepath.Join(env.install, installedName))
	if err != nil {
		t.Fatal(err)
	}
	want := testfont.Build(t, fontSpecA())
	if string(installedData) != string(want) {
		t.Error("installed file does not match the repo font")
	}
	// A follow-up push must be a no-op now (nothing local to archive).
	res2 := env.runApp(t, pushArgs("user"), "", ghExists("alice"), plat)
	if res2.code != ExitOK || env.head() == "" {
		t.Fatalf("post-replace push: %d (%s)", res2.code, res2.stderr)
	}
	if !strings.Contains(env.reportText(), "new commit: (no new commit)") {
		t.Error("post-replace push must be a no-op")
	}
	if len(plat.installer.removed) != 1 || len(plat.installer.installed) != 1 {
		t.Errorf("removed=%v installed=%v", plat.installer.removed, plat.installer.installed)
	}
}

// TestPullRollbackOnInjectedInstallFailure: a failing Install must leave no
// file behind and be reported as a per-file error (exit 2).
func TestPullRollbackOnInjectedInstallFailure(t *testing.T) {
	env, plat := seededEnv(t, map[string]testfont.Spec{"alpha.ttf": fontSpecA()})
	if err := os.Remove(filepath.Join(env.fontDir, "alpha.ttf")); err != nil {
		t.Fatal(err)
	}
	plat.installer.failInstall["alpha-regular-"+fonts.HashKey("Alpha-Regular|ABCD")[:8]+".ttf"] = errInjected

	res := env.runApp(t, pullArgs("user"), "", ghExists("alice"), plat)
	if res.code != ExitCompleted {
		t.Fatalf("exit = %d, want %d", res.code, ExitCompleted)
	}
	entries, _ := os.ReadDir(env.install)
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".fontvault") {
			t.Errorf("unexpected file in install dir: %s", e.Name())
		}
	}
	text := env.reportText()
	if !strings.Contains(text, "errors") || !strings.Contains(text, "install") {
		t.Errorf("report missing install error: %q", text)
	}
}

// TestPullRegistryCollision: a colliding registration name is an
// auto-skipped conflict (exit 2) and installs nothing.
func TestPullRegistryCollision(t *testing.T) {
	env, plat := seededEnv(t, map[string]testfont.Spec{"alpha.ttf": fontSpecA()})
	if err := os.Remove(filepath.Join(env.fontDir, "alpha.ttf")); err != nil {
		t.Fatal(err)
	}
	name := "alpha-regular-" + fonts.HashKey("Alpha-Regular|ABCD")[:8] + ".ttf"
	plat.installer.collisions[name] = true

	res := env.runApp(t, pullArgs("user"), "", ghExists("alice"), plat)
	if res.code != ExitCompleted {
		t.Fatalf("exit = %d, want %d", res.code, ExitCompleted)
	}
	if len(plat.installer.installed) != 0 {
		t.Errorf("nothing may be installed: %v", plat.installer.installed)
	}
	text := env.reportText()
	if !strings.Contains(text, "registry name collision") {
		t.Errorf("report missing collision reason: %q", text)
	}
}

// TestSyncOrderingAndFatalPushSkipsPull covers both spec cases: the happy
// ordering (repo-only font installed after push), and pull skipped when the
// push phase fails fatally.
func TestSyncOrderingAndFatalPushSkipsPull(t *testing.T) {
	t.Run("ordering", func(t *testing.T) {
		env := newTestEnv(t)
		plat := env.newPlat(t)
		// Seed the repo with Charlie (pushed from a different "machine"
		// state), then sync Alpha locally.
		env.writeFont(env.fontDir, "charlie.ttf", fontSpecC())
		if res := env.runApp(t, pushArgs("user"), "", ghExists("alice"), plat); res.code != ExitOK {
			t.Fatalf("seed push: %d (%s)", res.code, res.stderr)
		}
		if err := os.Remove(filepath.Join(env.fontDir, "charlie.ttf")); err != nil {
			t.Fatal(err)
		}
		env.writeFont(env.fontDir, "alpha.ttf", fontSpecA())

		res := env.runApp(t, []string{"-s", "user", "-d", "sync", "-y"}, "", ghExists("alice"), plat)
		if res.code != ExitOK {
			t.Fatalf("sync: %d (stdout: %s, stderr: %s)", res.code, res.stdout, res.stderr)
		}
		// Pull ran after push and installed Charlie (missing locally).
		checkSyncInstalledCharlie(t, env, plat)
	})

	t.Run("fatal push skips pull", func(t *testing.T) {
		env := newTestEnv(t)
		plat := env.newPlat(t)
		env.writeFont(env.fontDir, "alpha.ttf", fontSpecA())
		env.clearIdentity()

		res := env.runApp(t, []string{"-s", "user", "-d", "sync", "-y"}, "", ghExists("alice"), plat)
		if res.code != ExitFatal {
			t.Fatalf("exit = %d, want %d (stderr: %s)", res.code, ExitFatal, res.stderr)
		}
		checkPullSkippedNoReport(t, env, plat)
	})
}

// checkSyncInstalledCharlie asserts the post-push pull phase ran: the
// repo-only Charlie was installed and the report covers both phases.
func checkSyncInstalledCharlie(t *testing.T, env *testEnv, plat *fakePlatform) {
	t.Helper()
	if len(plat.installer.installed) != 1 ||
		!strings.Contains(plat.installer.installed[0], "charlie-regular") {
		t.Errorf("installed = %v, want Charlie after the push", plat.installer.installed)
	}
	text := env.reportText()
	for _, want := range []string{"mode: sync", "added to repo", "installed locally"} {
		if !strings.Contains(text, want) {
			t.Errorf("sync report incomplete, missing %q: %q", want, text)
		}
	}
}

// checkPullSkippedNoReport asserts the pull phase never ran: nothing was
// installed and no report was written.
func checkPullSkippedNoReport(t *testing.T, env *testEnv, plat *fakePlatform) {
	t.Helper()
	if len(plat.installer.installed) != 0 {
		t.Errorf("pull must not run after fatal push: %v", plat.installer.installed)
	}
	if env.reportText() != "" {
		t.Error("failed run must write no report")
	}
}

// TestCompareOutputForFixtureSet drives a fixture classification.
func TestCompareOutputForFixtureSet(t *testing.T) {
	env, plat := seededEnv(t, map[string]testfont.Spec{
		"alpha.ttf": fontSpecA(),
		"bravo.ttf": fontSpecB(),
	})

	// Local state: Alpha identical, Bravo modified (differs), Charlie new,
	// a skipped-format file and an invalid font.
	if err := os.Remove(filepath.Join(env.fontDir, "bravo.ttf")); err != nil {
		t.Fatal(err)
	}
	bs := fontSpecB()
	bs.Filler = []byte("changed")
	env.writeFont(env.fontDir, "bravo.ttf", bs)
	env.writeFont(env.fontDir, "charlie.ttf", fontSpecC())
	if err := os.WriteFile(filepath.Join(env.fontDir, "collection.ttc"), []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(env.fontDir, "broken.otf"), []byte("not a font"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := env.runApp(t, []string{"-s", "user", "-c", "-y"}, "", ghExists("alice"), plat)
	if res.code != ExitCompleted {
		t.Fatalf("exit = %d, want %d (stdout: %s, stderr: %s)", res.code, ExitCompleted, res.stdout, res.stderr)
	}
	text := env.reportText()
	for _, want := range []string{
		"mode: compare",
		"local only", "Charlie",
		"identical", "Alpha",
		"differs", "Bravo",
		"skipped formats", ".ttc: 1",
		"errors", "broken.otf",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("report missing %q:\n%s", want, text)
		}
	}
	// Read-only: no new commits on the remote.
	if got := env.reportText(); !strings.Contains(got, "commit compared: "+env.head()) {
		t.Errorf("header missing compared sha: %q", got)
	}
}

// TestSparseWorktreeNoFontsAfterPush is covered inside
// TestFirstPushToEmptyRepo; this variant checks it after a repeat push too.
func TestSparseWorktreeNoFontsAfterPush(t *testing.T) {
	env := newTestEnv(t)
	plat := env.newPlat(t)
	env.writeFont(env.fontDir, "alpha.ttf", fontSpecA())
	for i := range 2 {
		if res := env.runApp(t, pushArgs("user"), "", ghExists("alice"), plat); res.code != ExitOK {
			t.Fatalf("push %d: %d (%s)", i, res.code, res.stderr)
		}
		if _, err := os.Stat(filepath.Join(env.workdir, "fonts")); !os.IsNotExist(err) {
			t.Fatalf("push %d: worktree still contains fonts/", i)
		}
	}
}

// TestPruneNotRunAfterFailedRun: an invariant violation aborts the push;
// the LFS objects staged before the abort survive (no prune), and no report
// is written.
func TestPruneNotRunAfterFailedRun(t *testing.T) {
	env := newTestEnv(t)
	plat := env.newPlat(t)

	// Seed the bare repo with an orphan font file that has no manifest.
	seed := filepath.Join(env.root, "seed")
	git(t, env.root, "clone", filepath.ToSlash(env.barePath), seed)
	git(t, seed, "checkout", "-b", "main")
	git(t, seed, "config", "user.name", "Seeder")
	git(t, seed, "config", "user.email", "seeder@example.invalid")
	orphanSpec := testfont.Spec{
		PostScriptName: "Zeta-Regular",
		FullName:       "Zeta Regular",
		Family:         "Zeta",
		Subfamily:      "Regular",
		Vendor:         "ZZZZ",
	}
	orphanPath := archivePath("Zeta")
	if err := os.MkdirAll(filepath.Dir(filepath.Join(seed, orphanPath)), 0o755); err != nil {
		t.Fatal(err)
	}
	// the bytes do not matter: the run refuses before reading the archive,
	// because no manifest describes the family it belongs to
	if err := os.WriteFile(filepath.Join(seed, orphanPath), testfont.Build(t, orphanSpec), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, seed, "add", "-f", orphanPath)
	git(t, seed, "commit", "-m", "orphan")
	git(t, seed, "push", "origin", "main")

	// Local: Alpha (sorts before Zeta, gets staged first) and Zeta (hits
	// the invariant violation).
	env.writeFont(env.fontDir, "alpha.ttf", fontSpecA())
	env.writeFont(env.fontDir, "zeta.ttf", orphanSpec)

	res := env.runApp(t, pushArgs("user"), "", ghExists("alice"), plat)
	if res.code != ExitFatal {
		t.Fatalf("exit = %d, want %d (stdout: %s)", res.code, ExitFatal, res.stdout)
	}
	if !strings.Contains(res.stderr, "invariant violation") {
		t.Errorf("stderr = %q", res.stderr)
	}
	// Alpha was staged before the abort: its LFS object must survive (no
	// prune after a failed run).
	objects := env.lsLFSObjects()
	if len(objects) == 0 {
		t.Error("LFS objects were pruned after a failed run")
	}
	if env.reportText() != "" {
		t.Error("failed run must write no report")
	}
}

// TestDryRunWritesNothing: -n leaves the remote, cfg and report untouched.
func TestDryRunWritesNothing(t *testing.T) {
	env := newTestEnv(t)
	plat := env.newPlat(t)
	env.writeFont(env.fontDir, "alpha.ttf", fontSpecA())

	res := env.runApp(t, []string{"-s", "user", "-d", "push", "-n", "-y"}, "", ghExists("alice"), plat)
	if res.code != ExitOK {
		t.Fatalf("exit = %d (stderr: %s)", res.code, res.stderr)
	}
	if !strings.Contains(res.stdout, "dry-run: would add") ||
		!strings.Contains(res.stdout, "dry-run: would push LFS objects") {
		t.Errorf("dry-run plan missing: %q", res.stdout)
	}
	// The remote has no branch: nothing was pushed.
	if env.bareRefExists("refs/heads/main") {
		t.Error("dry-run pushed a branch")
	}
	if env.reportText() != "" {
		t.Error("dry-run wrote a report")
	}
	if _, err := os.Stat(filepath.Join(env.appDir, "fontvault.cfg")); !os.IsNotExist(err) {
		t.Error("dry-run wrote the cfg")
	}
}

// TestCfgPersistedSwitches: -b is persisted into the encrypted cfg after a
// successful run; compare without persisted switches leaves it alone.
func TestCfgPersistedSwitches(t *testing.T) {
	env := newTestEnv(t)
	plat := env.newPlat(t)
	env.writeFont(env.fontDir, "alpha.ttf", fontSpecA())

	res := env.runApp(t, []string{"-s", "user", "-d", "push", "-y", "-b", "dev"}, "", ghExists("alice"), plat)
	if res.code != ExitOK {
		t.Fatalf("exit = %d (stderr: %s)", res.code, res.stderr)
	}
	cfg := env.cfgFile()
	if cfg.Branch != "dev" {
		t.Errorf("cfg branch = %q", cfg.Branch)
	}
	// A compare run without switches must keep the cfg value.
	res = env.runApp(t, []string{"-s", "user", "-c", "-y"}, "", ghExists("alice"), plat)
	if res.code != ExitOK {
		t.Fatalf("compare: %d (%s)", res.code, res.stderr)
	}
	if got := env.cfgFile(); got.Branch != "dev" {
		t.Errorf("cfg branch changed to %q", got.Branch)
	}
}

// TestLockHeldExits4: an existing live lock file (our own pid) makes the
// run exit 4.
func TestLockHeldExits4(t *testing.T) {
	env := newTestEnv(t)
	plat := env.newPlat(t)
	env.writeFont(env.fontDir, "alpha.ttf", fontSpecA())

	lockDir := filepath.Join(env.workdir)
	if err := os.MkdirAll(lockDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Simulate a live lock: our own pid.
	if err := os.WriteFile(filepath.Join(lockDir, ".fontvault.lock"),
		[]byte(fmt.Sprintf("%d", os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}

	res := env.runApp(t, pushArgs("user"), "", ghExists("alice"), plat)
	if res.code != ExitLockHeld {
		t.Fatalf("exit = %d, want %d (stderr: %s)", res.code, ExitLockHeld, res.stderr)
	}
	if !strings.Contains(res.stderr, "lock") {
		t.Errorf("stderr = %q", res.stderr)
	}
}

// TestPullOSDefaultProtection: a local OS-default font with a conflicting
// repo copy can only be skipped (exit 2, reason "OS default").
func TestPullOSDefaultProtection(t *testing.T) {
	env := newTestEnv(t)
	plat := env.newPlat(t)
	// Push Alpha from the user dir first.
	env.writeFont(env.fontDir, "alpha.ttf", fontSpecA())
	if res := env.runApp(t, pushArgs("user"), "", ghExists("alice"), plat); res.code != ExitOK {
		t.Fatalf("push: %d (%s)", res.code, res.stderr)
	}
	// A different Alpha copy lives in the system dir and is an OS default.
	sysPath := env.writeFont(env.sysDir, "alpha.ttf", func() testfont.Spec {
		s := fontSpecA()
		s.Filler = []byte("os-default")
		return s
	}())
	// The scan classifies the symlink-resolved path, and on Windows that
	// resolution also expands 8.3 short names (CI's temp dir is under
	// C:\Users\RUNNER~1), so the fake must be keyed by the resolved form.
	realSysPath, err := filepath.EvalSymlinks(sysPath)
	if err != nil {
		t.Fatal(err)
	}
	plat.classifier.defaults[realSysPath] = true

	res := env.runApp(t, []string{"-s", "system", "-d", "pull", "-y"}, "", ghExists("alice"), plat)
	if res.code != ExitCompleted {
		t.Fatalf("exit = %d, want %d (stdout: %s, stderr: %s)", res.code, ExitCompleted, res.stdout, res.stderr)
	}
	if len(plat.installer.installed) != 0 || len(plat.installer.removed) != 0 {
		t.Errorf("OS-default font must never be modified: installed=%v removed=%v",
			plat.installer.installed, plat.installer.removed)
	}
	text := env.reportText()
	if !strings.Contains(text, "conflicts skipped") || !strings.Contains(text, "OS default") {
		t.Errorf("report missing OS-default skip: %q", text)
	}
}

var errInjected = errors.New("injected failure")
