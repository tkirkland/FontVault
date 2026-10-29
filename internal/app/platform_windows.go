//go:build windows

package app

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows/registry"

	"fontvault/internal/exclude"
	"fontvault/internal/install"
	"fontvault/internal/osinfo"
)

type windowsPlatform struct{}

func newPlatform() Platform { return windowsPlatform{} }

func (windowsPlatform) OSInfo() string { return osinfo.String() }

const fontsRegPath = `SOFTWARE\Microsoft\Windows NT\CurrentVersion\Fonts`

func (windowsPlatform) FontSources(source string) (FontSources, error) {
	var dirs []string
	var hive registry.Key
	switch source {
	case "system":
		windir := os.Getenv("WINDIR")
		if windir == "" {
			return FontSources{}, fmt.Errorf("WINDIR is not set")
		}
		dirs = []string{filepath.Join(windir, "Fonts")}
		hive = registry.LOCAL_MACHINE
	case "user":
		local := os.Getenv("LOCALAPPDATA")
		if local == "" {
			return FontSources{}, fmt.Errorf("LOCALAPPDATA is not set")
		}
		dirs = []string{filepath.Join(local, "Microsoft", "Windows", "Fonts")}
		hive = registry.CURRENT_USER
	default:
		return FontSources{}, fmt.Errorf("invalid source %q", source)
	}

	var extra []string
	if k, err := registry.OpenKey(hive, fontsRegPath, registry.QUERY_VALUE); err == nil {
		names, err := k.ReadValueNames(0)
		k.Close()
		if err == nil {
			for _, name := range names {
				kk, err := registry.OpenKey(hive, fontsRegPath, registry.QUERY_VALUE)
				if err != nil {
					continue
				}
				data, _, err := kk.GetStringValue(name)
				kk.Close()
				if err != nil || data == "" {
					continue
				}
				extra = append(extra, registryFontFile(data, dirs)...)
			}
		}
	}
	return FontSources{Dirs: dirs, Recursive: false, ExtraFiles: extra}, nil
}

// registryFontFile turns a registry value data into scannable files.
// Absolute paths are scanned directly; bare filenames live inside the font
// directory and are covered by the directory scan, unless the file does not
// exist there (then nothing to scan).
func registryFontFile(data string, dirs []string) []string {
	if filepath.IsAbs(data) {
		return []string{data}
	}
	if len(dirs) == 0 {
		return nil
	}
	p := filepath.Join(dirs[0], data)
	if _, err := os.Stat(p); err == nil {
		// Already covered by the directory scan; return it anyway so the
		// extension counting stays accurate without duplicates (scan
		// dedupes by real path).
		return []string{p}
	}
	return nil
}

func (windowsPlatform) InstallDir(source string) (string, error) {
	switch source {
	case "system":
		windir := os.Getenv("WINDIR")
		if windir == "" {
			return "", fmt.Errorf("WINDIR is not set")
		}
		return filepath.Join(windir, "Fonts"), nil
	case "user":
		local := os.Getenv("LOCALAPPDATA")
		if local == "" {
			return "", fmt.Errorf("LOCALAPPDATA is not set")
		}
		dir := filepath.Join(local, "Microsoft", "Windows", "Fonts")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("create font dir %s: %w", dir, err)
		}
		return dir, nil
	}
	return "", fmt.Errorf("invalid source %q", source)
}

func (windowsPlatform) Classifier() exclude.OSClassifier {
	return exclude.Classifier(nil)
}

func (windowsPlatform) NewInstaller(system bool, installDir string) install.Installer {
	return install.New(system, installDir)
}

// CheckPullPrereqs: system pulls require an elevated token; user pulls
// require a writable install dir.
func (windowsPlatform) CheckPullPrereqs(source, installDir string) error {
	if source == "system" {
		if err := install.CheckSystemPrereqs(); err != nil {
			return err
		}
	}
	if err := checkDirWritable(installDir); err != nil {
		return fmt.Errorf("install dir %s is not writable: %w", installDir, err)
	}
	return nil
}
