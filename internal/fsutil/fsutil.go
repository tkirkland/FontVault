// Package fsutil provides small filesystem helpers shared across fontvault.
package fsutil

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// HashAndSize returns the hex SHA-256 and byte size of a file. It streams,
// so a family archive of any size is hashed without being read into memory.
func HashAndSize(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, fmt.Errorf("open %s: %w", path, err)
	}
	sum := sha256.New()
	size := int64(0)
	buf := make([]byte, 1<<16)
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			// this toolchain's hash.Hash accumulates via Write (io.Writer),
			// not Update
			if _, err := sum.Write(buf[:n]); err != nil {
				_ = f.Close()
				return "", 0, fmt.Errorf("read %s: %w", path, err)
			}
			size += int64(n)
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				break
			}
			_ = f.Close()
			return "", 0, fmt.Errorf("read %s: %w", path, rerr)
		}
	}
	if err := f.Close(); err != nil {
		return "", 0, fmt.Errorf("close %s: %w", path, err)
	}
	return hex.EncodeToString(sum.Sum(nil)), size, nil
}

// AtomicWrite writes data to path atomically: it writes a temporary file in
// the same directory, fsyncs it, applies mode and renames it over path.
func AtomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".fontvault-tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	cleanup := func() {
		// Best effort: the caller's write/sync/chmod error is the reported
		// cause; a failed cleanup here is silent by design.
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("sync %s: %w", tmpName, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		cleanup()
		return fmt.Errorf("chmod %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		// Best effort: the close failure is the reported cause.
		_ = os.Remove(tmpName)
		return fmt.Errorf("close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		// Best effort: the rename failure is the reported cause.
		_ = os.Remove(tmpName)
		return fmt.Errorf("rename %s to %s: %w", tmpName, path, err)
	}
	return nil
}
