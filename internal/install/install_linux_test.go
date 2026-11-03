//go:build Linux

package install

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLinuxInstallMovesFile(t *testing.T) {
	dir := t.TempDir()
	inst := &linuxInstaller{installDir: dir}
	tmp, err := os.CreateTemp(dir, "tmp-*")
	if err != nil {
		t.Fatal(err)
	}
	tmp.WriteString("font")
	tmp.Close()
	path, err := inst.Install(context.Background(), tmp.Name(), Meta{Filename: "x.ttf", FullName: "X", Ext: "ttf"})
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(dir, "x.ttf") {
		t.Errorf("path = %q", path)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("stat = %v %v", fi, err)
	}
}

func TestLinuxInstallRefusesExistingTarget(t *testing.T) {
	dir := t.TempDir()
	inst := &linuxInstaller{installDir: dir}
	if err := os.WriteFile(filepath.Join(dir, "x.ttf"), []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	tmp, _ := os.CreateTemp(dir, "tmp-*")
	tmp.Close()
	if _, err := inst.Install(context.Background(), tmp.Name(), Meta{Filename: "x.ttf", FullName: "X", Ext: "ttf"}); err == nil {
		t.Fatal("expected error for existing target")
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "x.ttf")); string(data) != "existing" {
		t.Error("existing file must not be overwritten")
	}
}

func TestLinuxRemoveFont(t *testing.T) {
	dir := t.TempDir()
	inst := &linuxInstaller{installDir: dir}
	p := filepath.Join(dir, "old.ttf")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := inst.RemoveFont(context.Background(), p, Meta{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Error("file must be deleted")
	}
}

func TestLinuxNameCollisionNever(t *testing.T) {
	inst := &linuxInstaller{}
	ok, err := inst.NameCollision(Meta{Filename: "x.ttf"})
	if err != nil || ok {
		t.Fatalf("collision = %v, %v", ok, err)
	}
}

func TestLinuxFinishWithoutFcCacheWarnsAndSucceeds(t *testing.T) {
	inst := &linuxInstaller{}
	// fc-cache presence depends on the host; the contract is that a missing
	// binary never fails the run. Test the missing case by pointing PATH at
	// an empty dir.
	t.Setenv("PATH", t.TempDir())
	if err := inst.Finish(context.Background()); err != nil {
		t.Fatalf("missing fc-cache must not fail: %v", err)
	}
}
