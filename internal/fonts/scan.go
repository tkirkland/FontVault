package fonts

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SkippedExtensions are the font-related extensions that are counted as
// "skipped formats" during a scan but never read.
var SkippedExtensions = []string{
	".ttc", ".otc", ".woff", ".woff2", ".fon", ".fnt",
	".pfb", ".pfm", ".afm", ".pcf", ".bdf", ".otb", ".dfont",
}

func isSkippedExt(name string) (string, bool) {
	ext := strings.ToLower(filepath.Ext(name))
	for _, e := range SkippedExtensions {
		if ext == e {
			return e, true
		}
	}
	return "", false
}

// PathError is a per-file error with the path it refers to.
type PathError struct {
	Path string
	Err  error
}

func (e PathError) Error() string { return e.Path + ": " + e.Err.Error() }
func (e PathError) Unwrap() error { return e.Err }

// ScanOptions describes what to scan.
type ScanOptions struct {
	// Dirs are scanned (recursively only when Recursive is set).
	Dirs []string
	// Recursive controls whether subdirectories of Dirs are walked.
	Recursive bool
	// ExtraFiles are individual font files scanned directly, e.g. files
	// registered in the Windows registry outside the font directories.
	ExtraFiles []string
}

// ScanResult is the outcome of a scan.
type ScanResult struct {
	// Fonts holds the validated .ttf/.otf files, deduplicated by resolved
	// real path (symlink loops and duplicates are ignored).
	Fonts []*Font
	// Skipped counts files with other font extensions, keyed by lowercase
	// extension including the dot. Those files are never opened.
	Skipped map[string]int
	// Errors holds per-file errors; one bad file never aborts a scan.
	Errors []PathError
	// DirErrors holds per-directory read errors.
	DirErrors []PathError
}

// Scan scans the given directories and extra files.
func Scan(opts ScanOptions) ScanResult {
	res := ScanResult{Skipped: map[string]int{}}
	visitedDirs := map[string]bool{}
	visitedFiles := map[string]bool{}
	queue := append([]string{}, opts.Dirs...)
	for _, dir := range queue {
		scanDir(dir, opts.Recursive, visitedDirs, visitedFiles, &res)
	}
	for _, f := range opts.ExtraFiles {
		scanFile(f, visitedFiles, &res)
	}
	return res
}

func scanDir(dir string, recursive bool, visitedDirs, visitedFiles map[string]bool, res *ScanResult) {
	realPath, err := filepath.EvalSymlinks(dir)
	if err != nil {
		res.DirErrors = append(res.DirErrors, PathError{Path: dir, Err: fmt.Errorf("resolve font dir: %w", err)})
		return
	}
	if visitedDirs[realPath] {
		return
	}
	visitedDirs[realPath] = true

	entries, err := os.ReadDir(dir)
	if err != nil {
		res.DirErrors = append(res.DirErrors, PathError{Path: dir, Err: fmt.Errorf("read font dir: %w", err)})
		return
	}
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		info, err := os.Stat(p) // follows symlinks
		if err != nil {
			res.Errors = append(res.Errors, PathError{Path: p, Err: fmt.Errorf("stat: %w", err)})
			continue
		}
		if info.IsDir() {
			if recursive {
				scanDir(p, recursive, visitedDirs, visitedFiles, res)
			}
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		scanFile(p, visitedFiles, res)
	}
}

func scanFile(path string, visitedFiles map[string]bool, res *ScanResult) {
	name := filepath.Base(path)
	if ext, ok := isSkippedExt(name); ok {
		res.Skipped[ext]++
		return
	}
	ext := strings.ToLower(filepath.Ext(name))
	if ext != ".ttf" && ext != ".otf" {
		return
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		res.Errors = append(res.Errors, PathError{Path: path, Err: fmt.Errorf("resolve symlink: %w", err)})
		return
	}
	if visitedFiles[realPath] {
		return
	}
	visitedFiles[realPath] = true
	f, err := Inspect(path)
	if err != nil {
		res.Errors = append(res.Errors, PathError{Path: path, Err: fmt.Errorf("invalid font: %w", err)})
		return
	}
	f.RealPath = realPath
	res.Fonts = append(res.Fonts, f)
}
