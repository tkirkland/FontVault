package lockfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAcquireRelease(t *testing.T) {
	dir := t.TempDir()
	lock, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".fontvault.lock")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("lock file missing: %v", err)
	}
	// A second acquisition while held fails.
	if _, err := Acquire(dir); err == nil {
		t.Fatal("second Acquire must fail")
	} else {
		if _, ok := errors.AsType[*HeldError](err); !ok {
			t.Fatalf("err = %v, want HeldError", err)
		}
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("lock file must be removed on release")
	}
	// Re-acquire works after release.
	lock2, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := lock2.Release(); err != nil {
		t.Errorf("release after re-acquire: %v", err)
	}
}

func TestStaleLockReplaced(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".fontvault.lock")
	// A pid that is almost certainly not alive on this host.
	if err := os.WriteFile(path, []byte("999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lock, err := Acquire(dir)
	if err != nil {
		t.Fatalf("stale lock must be replaced: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Errorf("release after stale replace: %v", err)
	}
}

func TestLiveLockKept(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".fontvault.lock")
	if err := os.WriteFile(path, []byte(pidString(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Acquire(dir)
	var held *HeldError
	if !errors.As(err, &held) {
		t.Fatalf("err = %v, want HeldError", err)
	}
	if held.PID != os.Getpid() {
		t.Errorf("held pid = %d", held.PID)
	}
}

func pidString(pid int) string {
	return itoa(pid) + "\n"
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
