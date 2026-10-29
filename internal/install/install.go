// Package install installs fonts pulled from the repository into the local
// font directories. Platform differences (Windows registry + GDI vs Linux
// fc-cache) live behind the Installer interface so the pull logic can be
// tested on every platform.
package install

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"fontvault/internal/fsutil"
)

// Meta describes a font being installed.
type Meta struct {
	Filename string // target filename (the repo filename)
	FullName string // used for the Windows registry value name
	Ext      string // "ttf" or "otf"
}

// ErrRegistryNameCollision means the Windows registry value name for the
// font already exists and is not overwritten.
var ErrRegistryNameCollision = errors.New("registry value name already exists")

// Installer installs and unregisters fonts on one platform.
type Installer interface {
	// Install moves tempPath into the install dir under meta.Filename and
	// registers the font. The caller has already verified that the target
	// does not exist. On a registry name collision the moved file is
	// removed and ErrRegistryNameCollision is returned.
	Install(ctx context.Context, tempPath string, meta Meta) (installedPath string, err error)
	// NameCollision reports whether the platform's registration name for
	// meta is already taken (the Windows registry value name; always false
	// on Linux). Callers use it before replacing an existing font.
	NameCollision(meta Meta) (bool, error)
	// RemoveFont unregisters and deletes an installed font. If any step
	// fails after unregistering, the caller re-registers the font.
	RemoveFont(ctx context.Context, path string, meta Meta) error
	// RegisterFont re-registers an existing font file (rollback path after
	// a failed RemoveFont).
	RegisterFont(ctx context.Context, path string, meta Meta) error
	// Finish runs once after all installs (fc-cache on Linux).
	Finish(ctx context.Context) error
}

// New returns the platform installer for the given source. installDir must
// already exist.
func New(system bool, installDir string) Installer {
	return newPlatformInstaller(system, installDir)
}

// CheckSystemPrereqs verifies elevation requirements for `-s system` pull:
// Windows needs an elevated token; Linux needs write access to
// /usr/local/share/fonts. It is a preflight check (exit 3 on failure).
func CheckSystemPrereqs() error {
	return checkSystemPrereqs()
}

// moveIntoTarget renames tempPath to installDir/meta.Filename, refusing to
// overwrite an existing file.
func moveIntoTarget(tempPath, installDir string, meta Meta) (string, error) {
	target := filepath.Join(installDir, meta.Filename)
	if _, err := os.Lstat(target); err == nil {
		return "", fmt.Errorf("install %s: target file already exists", target)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("check target %s: %w", target, err)
	}
	if err := os.Rename(tempPath, target); err != nil {
		return "", fmt.Errorf("rename %s to %s: %w", tempPath, target, err)
	}
	return target, nil
}

// removeQuietly removes a file, ignoring "already gone" errors.
func removeQuietly(path string) {
	if err := os.Remove(fsutil.LongPath(path)); err != nil && !os.IsNotExist(err) {
		_ = err // best effort rollback
	}
}
