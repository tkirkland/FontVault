//go:build windows

package fsutil

import (
	"path/filepath"
	"strings"
)

// LongPath returns path with a \\?\ prefix so Windows file APIs are not
// limited to MAX_PATH. Relative paths and already-prefixed paths are
// returned unchanged.
func LongPath(path string) string {
	if path == "" {
		return path
	}
	if strings.HasPrefix(path, `\\?\`) || strings.HasPrefix(path, `\\.\`) {
		return path
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if len(path) >= 2 && path[1] == ':' {
		return `\\?\` + path
	}
	return path
}
