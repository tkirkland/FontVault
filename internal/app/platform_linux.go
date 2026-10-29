//go:build linux

package app

import (
	"fmt"
	"os"
	"path/filepath"

	"fontvault/internal/exclude"
	"fontvault/internal/install"
	"fontvault/internal/osinfo"
)

type linuxPlatform struct{}

func newPlatform() Platform { return linuxPlatform{} }

func (linuxPlatform) OSInfo() string { return osinfo.String() }

func userFontsDirs() []string {
	var dirs []string
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		dirs = append(dirs, filepath.Join(xdg, "fonts"))
	} else if home != "" {
		dirs = append(dirs, filepath.Join(home, ".local", "share", "fonts"))
	}
	if home != "" {
		dirs = append(dirs, filepath.Join(home, ".fonts"))
	}
	return dirs
}

func (linuxPlatform) FontSources(source string) (FontSources, error) {
	switch source {
	case "system":
		return FontSources{
			Dirs:      []string{"/usr/share/fonts", "/usr/local/share/fonts"},
			Recursive: true,
		}, nil
	case "user":
		return FontSources{Dirs: userFontsDirs(), Recursive: true}, nil
	}
	return FontSources{}, fmt.Errorf("invalid source %q", source)
}

func (linuxPlatform) InstallDir(source string) (string, error) {
	switch source {
	case "system":
		return "/usr/local/share/fonts/fontvault", nil
	case "user":
		dirs := userFontsDirs()
		if len(dirs) == 0 {
			return "", fmt.Errorf("cannot resolve user font dir")
		}
		dir := filepath.Join(dirs[0], "fontvault")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("create font dir %s: %w", dir, err)
		}
		return dir, nil
	}
	return "", fmt.Errorf("invalid source %q", source)
}

func (linuxPlatform) Classifier() exclude.OSClassifier {
	return exclude.Classifier(nil)
}

func (linuxPlatform) NewInstaller(system bool, installDir string) install.Installer {
	return install.New(system, installDir)
}

func (linuxPlatform) CheckPullPrereqs(source, installDir string) error {
	if source == "system" {
		if err := install.CheckSystemPrereqs(); err != nil {
			return err
		}
		// The real install dir is created later; verify the parent here.
		return checkDirWritable("/usr/local/share/fonts")
	}
	if err := checkDirWritable(installDir); err != nil {
		return fmt.Errorf("install dir %s is not writable: %w", installDir, err)
	}
	return nil
}
