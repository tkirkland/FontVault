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
	fontDir, hive, err := fontDirFor(source)
	if err != nil {
		return FontSources{}, err
	}
	dirs := []string{fontDir}
	return FontSources{Dirs: dirs, Recursive: false, ExtraFiles: registryFontFiles(hive, dirs)}, nil
}

// fontDirFor maps a source to its font directory and the registry hive
// whose Fonts value lists the fonts installed for that source.
func fontDirFor(source string) (string, registry.Key, error) {
	switch source {
	case "system":
		windir := os.Getenv("WINDIR")
		if windir == "" {
			return "", 0, fmt.Errorf("WINDIR is not set")
		}
		return filepath.Join(windir, "Fonts"), registry.LOCAL_MACHINE, nil
	case "user":
		local := os.Getenv("LOCALAPPDATA")
		if local == "" {
			return "", 0, fmt.Errorf("LOCALAPPDATA is not set")
		}
		return filepath.Join(local, "Microsoft", "Windows", "Fonts"), registry.CURRENT_USER, nil
	}
	return "", 0, fmt.Errorf("invalid source %q", source)
}

// registryFontFiles collects the font files named by the registry Fonts
// value for a hive: one file per non-empty string value, resolved through
// registryFontFile.
func registryFontFiles(hive registry.Key, dirs []string) []string {
	var extra []string
	k, err := registry.OpenKey(hive, fontsRegPath, registry.QUERY_VALUE)
	if err != nil {
		return extra
	}
	defer func() { _ = k.Close() }() // read-only handle; close error is not actionable
	names, err := k.ReadValueNames(0)
	if err != nil {
		return extra
	}
	for _, name := range names {
		data, _, err := k.GetStringValue(name)
		if err != nil || data == "" {
			continue
		}
		extra = append(extra, registryFontFile(data, dirs)...)
	}
	return extra
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
	dir, _, err := fontDirFor(source)
	if err != nil {
		return "", err
	}
	if source == "user" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("create font dir %s: %w", dir, err)
		}
	}
	return dir, nil
}

func (windowsPlatform) Classifier() exclude.OSClassifier {
	return exclude.Classifier(nil)
}

func (windowsPlatform) NewInstaller(system bool, installDir string) install.Installer {
	return install.New(system, installDir)
}

// CheckPullPrereqs enforces pull prerequisites: system pulls require an
// elevated token; user pulls require a writable install dir.
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
