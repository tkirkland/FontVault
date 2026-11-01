//go:build linux

package install

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"

	"fontvault/internal/fsutil"
)

type linuxInstaller struct {
	installDir string
	log        *slog.Logger
}

func newPlatformInstaller(system bool, installDir string) Installer {
	return &linuxInstaller{installDir: installDir, log: slog.Default()}
}

func (l *linuxInstaller) Install(_ context.Context, tempPath string, meta Meta) (string, error) {
	target, err := moveIntoTarget(tempPath, l.installDir, meta)
	if err != nil {
		return "", err
	}
	if err := os.Chmod(target, 0o644); err != nil {
		removeQuietly(target)
		return "", fmt.Errorf("chmod %s: %w", target, err)
	}
	return target, nil
}

func (l *linuxInstaller) NameCollision(_ Meta) (bool, error) {
	return false, nil
}

func (l *linuxInstaller) RemoveFont(_ context.Context, path string, _ Meta) error {
	if err := os.Remove(fsutil.LongPath(path)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete %s: %w", path, err)
	}
	return nil
}

func (l *linuxInstaller) RegisterFont(_ context.Context, _ string, _ Meta) error {
	// Linux has no registration step; fc-cache runs in Finish.
	return nil
}

// Finish rebuilds the font cache for the installation dir. fc-cache is optional:
// when it is missing, a warning is logged and the run continues.
func (l *linuxInstaller) Finish(_ context.Context) error {
	fc, err := exec.LookPath("fc-cache")
	if err != nil {
		l.log.Warn("fc-cache not found; skipping font cache rebuild", "error", err)
		return nil
	}
	cmd := exec.Command(fc, "-f", l.installDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("fc-cache -f %s: %w: %s", l.installDir, err, string(out))
	}
	return nil
}

// checkSystemPrereqs requires write access to /usr/local/share/fonts,
// verified by creating and removing a temp file inside it.
func checkSystemPrereqs() error {
	dir := "/usr/local/share/fonts"
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return fmt.Errorf("install dir %s is not a directory: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, ".fontvault-write-test-*")
	if err != nil {
		return fmt.Errorf("cannot write to %s: pull with -s system needs write access to %s: %w", dir, dir, err)
	}
	name := f.Name()
	f.Close()
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("remove write test file %s: %w", name, err)
	}
	return nil
}
