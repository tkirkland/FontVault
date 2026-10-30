// Package lockfile implements fontvault's single-instance lock.
package lockfile

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// HeldError means another live instance holds the lock (exit code 4).
type HeldError struct {
	Path string
	PID  int
}

func (e *HeldError) Error() string {
	if e.PID > 0 {
		return fmt.Sprintf("another fontvault instance (pid %d) is running; lock file %s", e.PID, e.Path)
	}
	return fmt.Sprintf("lock file %s is held by another process", e.Path)
}

// Lock is an acquired single-instance lock.
type Lock struct {
	path string
	file *os.File
}

// Path returns the lock file path.
func (l *Lock) Path() string { return l.path }

// Acquire creates and holds an exclusive lock file in dir. A lock whose
// recorded PID is not alive is considered stale and is replaced.
func Acquire(dir string) (*Lock, error) {
	path := filepath.Join(dir, ".fontvault.lock")
	for range 2 {
		lock, err, stale := tryAcquire(path)
		if stale {
			// A stale lock was removed: create it on the next attempt.
			continue
		}
		if lock != nil {
			return lock, nil
		}
		return nil, err
	}
	return nil, &HeldError{Path: path}
}

// tryAcquire attempts one acquisition: it creates the lock file and records
// the pid, or inspects an existing one. stale is true when the lock of a
// dead process was removed, so the caller should create it again.
func tryAcquire(path string) (*Lock, error, bool) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err == nil {
		lock, werr := writeLockFile(path, f)
		return lock, werr, false
	}
	if !os.IsExist(err) {
		return nil, fmt.Errorf("create lock file %s: %w", path, err), false
	}
	pid, readErr := readPID(path)
	if readErr != nil {
		// Unreadable lock: treat as held by an unknown process.
		return nil, &HeldError{Path: path}, false
	}
	if pid > 0 && processAlive(pid) {
		return nil, &HeldError{Path: path, PID: pid}, false
	}
	// Stale lock: remove it so the next attempt can create it.
	if err := os.Remove(path); err != nil {
		return nil, fmt.Errorf("remove stale lock file %s: %w", path, err), false
	}
	return nil, nil, true
}

// writeLockFile records the pid in the freshly created lock file. On a
// failed write or sync the half-written file is closed and removed so a
// later run is not blocked by a lock with no pid in it.
func writeLockFile(path string, f *os.File) (*Lock, error) {
	if _, err := fmt.Fprintf(f, "%d\n", os.Getpid()); err != nil {
		discardLock(path, f)
		return nil, fmt.Errorf("write lock file %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		discardLock(path, f)
		return nil, fmt.Errorf("sync lock file %s: %w", path, err)
	}
	return &Lock{path: path, file: f}, nil
}

// discardLock closes and removes a lock file that could not be written.
func discardLock(path string, f *os.File) {
	f.Close()
	os.Remove(path)
}

func readPID(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("lock file %s has no valid pid", path)
	}
	return pid, nil
}

// PID reads the pid recorded in a lock file.
func PID(path string) (int, error) { return readPID(path) }

// ProcessAlive reports whether a process with the given pid is alive on
// this host.
func ProcessAlive(pid int) bool { return processAlive(pid) }

// Release closes and removes the lock file.
func (l *Lock) Release() error {
	if l == nil {
		return nil
	}
	var first error
	if l.file != nil {
		if err := l.file.Close(); err != nil {
			first = fmt.Errorf("close lock file %s: %w", l.path, err)
		}
		l.file = nil
	}
	if err := os.Remove(l.path); err != nil && !os.IsNotExist(err) {
		if first == nil {
			first = fmt.Errorf("remove lock file %s: %w", l.path, err)
		}
	}
	return first
}
