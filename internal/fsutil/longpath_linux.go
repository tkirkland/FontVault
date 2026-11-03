//go:build Linux

package fsutil

// LongPath is a no-op on Linux.
func LongPath(path string) string {
	return path
}
