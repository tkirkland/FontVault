// Package archive7z stores and reads the fontvault repository's family archives
// by shelling out to the 7z command line tool, the same way fontvault shells
// out to git and git-lfs. Fonts are never held in the repository as loose
// files: one archive per font family, even when the family has a single font.
//
// No Go compression library is used: the 7z container (LZMA2) is not
// implemented in the Go standard library, and calling the external tool keeps
// the single static binary that `CGO_ENABLED=0` produces.
package archive7z

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Tool runs the 7z command line tool.
type Tool struct {
	// Exe is the resolved executable; empty means "look it up when needed".
	Exe string
	Log *slog.Logger
}

// New returns a Tool that resolves the executable on first use.
func New(log *slog.Logger) Tool {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return Tool{Log: log}
}

// ExeNames are the accepted 7z executable names, in preference order. 7za
// and 7zr are the standalone variants 7-Zip also installs on some systems.
var exeNames = []string{"7z", "7za", "7zr"}

// Resolve returns the 7z executable path, or an error explaining that 7-Zip
// must be installed. PATH is searched first; the Windows default install
// directory is a fallback, because 7-Zip there is not always on PATH.
func Resolve() (string, error) {
	for _, name := range exeNames {
		found, err := exec.LookPath(name)
		if err == nil {
			return found, nil
		}
	}
	for _, dir := range programFilesDirs() {
		if dir == "" {
			continue
		}
		candidate := joinPath(joinPath(dir, "7-Zip"), "7z.exe")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("7z is not on PATH: install 7-Zip (it provides the 7z command)")
}

// programFilesDirs are the Windows program-files roots, either of which can
// hold a default 7-Zip installation. Empty strings are ignored (the variable
// is unset).
func programFilesDirs() []string {
	return []string{
		os.Getenv("PROGRAMFILES"),
		os.Getenv("PROGRAMFILES(X86)"),
	}
}

// Available verifies that the 7z tool can be found (preflight).
func Available() error {
	_, err := Resolve()
	return err
}

// Add stores memberName's bytes from sourceFile into archive, creating the
// archive when it does not exist yet and replacing an existing member of the
// same name. The member path inside the archive is exactly memberName: 7z
// records the path of the input file relative to its working directory, so
// sourceFile's parent is used as the working directory and its basename must
// equal memberName.
//
// note: archive must be an absolute path: it is resolved against the staging
// directory, not the repository or the process's current directory.
func (t *Tool) Add(ctx context.Context, archive, memberName, sourceFile string) error {
	if baseName(sourceFile) != memberName {
		return fmt.Errorf("7z add %s: staging file %s is not named %s",
			archive, sourceFile, memberName)
	}
	return t.run(ctx, dirName(sourceFile), createArgs(archive, memberName)...)
}

// Extract returns the bytes stored under memberName in archive, or an error
// when the archive has no such member.
func (t *Tool) Extract(ctx context.Context, archive, memberName string) ([]byte, error) {
	return t.output(ctx, "", extractArgs(archive, memberName)...)
}

// List returns the member names stored in archive, sorted.
func (t *Tool) List(ctx context.Context, archive string) ([]string, error) {
	out, err := t.output(ctx, "", listArgs(archive)...)
	if err != nil {
		return nil, err
	}
	var names []string
	for line := range strings.SplitSeq(string(out), "\n") {
		l := strings.TrimRight(line, "\r")
		if !strings.HasPrefix(l, "Path = ") {
			continue
		}
		names = append(names, strings.TrimPrefix(l, "Path = "))
	}
	sort.Strings(names)
	return names, nil
}

// createArgs are the store/update switches: -t7z picks the container, LZMA2
// is the codec, -mx=5 is the default level made explicit, -bd skips the
// progress display and -y answers 7z's "create it?" prompt.
//
// note: -mt (thread count) is deliberately absent. NanaZip's 7z rejects
// "-mt=1" with "System ERROR: The parameter is incorrect", and a single
// thread is what the default already is.
func createArgs(archive, memberName string) []string {
	return []string{"a", "-t7z", "-m0=lzma2", "-mx=5", "-bd", "-y", archive, memberName}
}

// extractArgs send one member's bytes to stdout (-so) with no progress display
// and no prompts.
func extractArgs(archive, memberName string) []string {
	return []string{"x", "-so", "-bd", "-y", archive, memberName}
}

// listArgs lists members in machine-readable form (-ba drops headers, -slt
// gives "Path = " lines instead of a table).
func listArgs(archive string) []string {
	return []string{"l", "-ba", "-slt", archive}
}

// run executes 7z with args in dir and discards stdout.
func (t *Tool) run(ctx context.Context, dir string, args ...string) error {
	_, err := t.exec(ctx, dir, args...)
	return err
}

// output executes 7z with args and returns raw stdout bytes.
func (t *Tool) output(ctx context.Context, dir string, args ...string) ([]byte, error) {
	out, err := t.exec(ctx, dir, args...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// exec runs the resolved 7z executable. C-locale output keeps `-l -slt`
// parsing stable across machines.
func (t *Tool) exec(ctx context.Context, dir string, args ...string) ([]byte, error) {
	exe := t.Exe
	if exe == "" {
		resolved, err := Resolve()
		if err != nil {
			return nil, err
		}
		exe = resolved
	}
	env := append(os.Environ(), "LC_ALL=C", "LANG=C")
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Dir = dir
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	t.Log.Debug("7z", "args", strings.Join(args, " "), "dir", dir)
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("7z %s: %w", strings.Join(args, " "), ctx.Err())
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("7z %s: %w: %s", strings.Join(args, " "), err, msg)
	}
	return stdout.Bytes(), nil
}

// baseName, dirName and joinPath are this toolchain's filepath helpers, kept
// here so the package reads the same way everywhere.
func baseName(p string) string { return filepath.Base(p) }

func dirName(p string) string { return filepath.Dir(p) }

func joinPath(a, b string) string { return filepath.Join(a, b) }
