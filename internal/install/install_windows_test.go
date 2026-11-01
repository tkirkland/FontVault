//go:build windows

package install

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fontvault/internal/fsutil"
)

type fakeWinOps struct {
	values     map[string]string
	adds       []string
	removes    []string
	broadcasts int
	failAdd    error
	failRemove error
	failWrite  error
}

func (f *fakeWinOps) ValueExists(name string) (bool, error) {
	_, ok := f.values[name]
	return ok, nil
}

func (f *fakeWinOps) WriteValue(name, data string) error {
	if f.failWrite != nil {
		return f.failWrite
	}
	f.values[name] = data
	return nil
}

func (f *fakeWinOps) RemoveValueByName(name string) error {
	delete(f.values, name)
	return nil
}

func (f *fakeWinOps) RemoveValueByData(data string) error {
	for k, v := range f.values {
		if strings.EqualFold(strings.TrimSpace(v), strings.TrimSpace(data)) {
			delete(f.values, k)
		}
	}
	return nil
}

func (f *fakeWinOps) AddFontResource(path string) error {
	if f.failAdd != nil {
		return f.failAdd
	}
	f.adds = append(f.adds, path)
	return nil
}

func (f *fakeWinOps) RemoveFontResource(path string) error {
	if f.failRemove != nil {
		return f.failRemove
	}
	f.removes = append(f.removes, path)
	return nil
}

func (f *fakeWinOps) BroadcastFontChange() { f.broadcasts++ }

func newFakeInstaller(t *testing.T, system bool) (*windowsInstaller, *fakeWinOps, string) {
	t.Helper()
	dir := t.TempDir()
	ops := &fakeWinOps{values: map[string]string{}}
	return &windowsInstaller{system: system, installDir: dir, ops: ops}, ops, dir
}

func writeTemp(t *testing.T, dir, content string) string {
	t.Helper()
	f, err := os.CreateTemp(dir, "fontvault-test-*")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	return f.Name()
}

func TestWindowsInstall(t *testing.T) {
	inst, ops, dir := newFakeInstaller(t, false)
	tmp := writeTemp(t, dir, "font-bytes")
	meta := Meta{Filename: "MyFont-Regular.ttf", FullName: "My Font Regular", Ext: "ttf"}
	path, err := inst.Install(context.Background(), tmp, meta)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(dir, meta.Filename) {
		t.Errorf("installed path = %q", path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("file not moved: %v", err)
	}
	wantName := "My Font Regular (TrueType)"
	if ops.values[wantName] == "" {
		t.Errorf("registry value %q not written: %v", wantName, ops.values)
	}
	// HKCU stores the full path (in \?\ long form).
	want := fsutil.LongPath(path)
	if !strings.EqualFold(ops.values[wantName], want) {
		t.Errorf("HKCU data = %q, want full path %q", ops.values[wantName], want)
	}
	if len(ops.adds) != 1 || ops.broadcasts != 1 {
		t.Errorf("adds=%v broadcasts=%d", ops.adds, ops.broadcasts)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Error("temp file must be renamed away")
	}
}

func TestWindowsInstallSystemUsesFilenameOnly(t *testing.T) {
	inst, ops, dir := newFakeInstaller(t, true)
	tmp := writeTemp(t, dir, "font-bytes")
	meta := Meta{Filename: "Sys.otf", FullName: "Sys Open", Ext: "otf"}
	if _, err := inst.Install(context.Background(), tmp, meta); err != nil {
		t.Fatal(err)
	}
	if got := ops.values["Sys Open (OpenType)"]; got != "Sys.otf" {
		t.Errorf("HKLM data = %q, want filename only", got)
	}
}

func TestWindowsInstallRegistryCollisionRollsBack(t *testing.T) {
	inst, ops, dir := newFakeInstaller(t, false)
	ops.values["Taken (TrueType)"] = "something"
	tmp := writeTemp(t, dir, "font-bytes")
	meta := Meta{Filename: "taken.ttf", FullName: "Taken", Ext: "ttf"}
	_, err := inst.Install(context.Background(), tmp, meta)
	if !errors.Is(err, ErrRegistryNameCollision) {
		t.Fatalf("err = %v, want collision", err)
	}
	if _, err := os.Stat(filepath.Join(dir, meta.Filename)); !os.IsNotExist(err) {
		t.Error("copied file must be removed on collision")
	}
	if ops.values["Taken (TrueType)"] != "something" {
		t.Error("existing registry value must not be overwritten")
	}
}

func TestWindowsNameCollision(t *testing.T) {
	inst, ops, _ := newFakeInstaller(t, false)
	ops.values["X (TrueType)"] = "v"
	ok, err := inst.NameCollision(Meta{Filename: "x.ttf", FullName: "X", Ext: "ttf"})
	if err != nil || !ok {
		t.Fatalf("collision = %v, %v", ok, err)
	}
	ok, err = inst.NameCollision(Meta{Filename: "y.ttf", FullName: "Y", Ext: "ttf"})
	if err != nil || ok {
		t.Fatalf("no-collision = %v, %v", ok, err)
	}
}

func TestWindowsRemoveFontDeletesAndUnregisters(t *testing.T) {
	inst, ops, dir := newFakeInstaller(t, false)
	path := filepath.Join(dir, "old.ttf")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	ops.values["Old (TrueType)"] = path
	if err := inst.RemoveFont(context.Background(), path, Meta{Filename: "old.ttf", FullName: "Old", Ext: "ttf"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("file must be deleted")
	}
	if _, ok := ops.values["Old (TrueType)"]; ok {
		t.Error("registry value must be removed")
	}
	if len(ops.removes) != 1 {
		t.Errorf("RemoveFontResourceExW calls = %d", len(ops.removes))
	}
}

func TestWindowsRemoveFontFailureLeavesFile(t *testing.T) {
	inst, ops, dir := newFakeInstaller(t, false)
	path := filepath.Join(dir, "old.ttf")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	ops.failRemove = errors.New("gdi failure")
	err := inst.RemoveFont(context.Background(), path, Meta{Filename: "old.ttf", FullName: "Old", Ext: "ttf"})
	if err == nil {
		t.Fatal("expected error")
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Error("file must survive a failed unregister")
	}
}

func TestWindowsRegisterFont(t *testing.T) {
	inst, ops, dir := newFakeInstaller(t, false)
	path := filepath.Join(dir, "old.ttf")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := inst.RegisterFont(context.Background(), path, Meta{Filename: "old.ttf", FullName: "Old", Ext: "ttf"}); err != nil {
		t.Fatal(err)
	}
	if ops.values["Old (TrueType)"] == "" || len(ops.adds) != 1 || ops.broadcasts != 1 {
		t.Errorf("re-register incomplete: %+v", ops)
	}
}

func TestRegistryValueName(t *testing.T) {
	if got := registryValueName(Meta{Filename: "a.ttf", FullName: "A", Ext: "ttf"}); got != "A (TrueType)" {
		t.Errorf("ttf name = %q", got)
	}
	if got := registryValueName(Meta{Filename: "a.otf", FullName: "A", Ext: "otf"}); got != "A (OpenType)" {
		t.Errorf("otf name = %q", got)
	}
	if got := registryValueName(Meta{Filename: "fallback.ttf", Ext: "ttf"}); got != "fallback (TrueType)" {
		t.Errorf("fallback name = %q", got)
	}
}
