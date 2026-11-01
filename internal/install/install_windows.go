//go:build windows

package install

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"fontvault/internal/fsutil"
)

const (
	fontsRegPath  = `SOFTWARE\Microsoft\Windows NT\CurrentVersion\Fonts`
	wmFontChange  = 0x001D
	hwndBroadcast = 0xFFFF
)

var (
	procAddFontResourceExW    = windows.NewLazySystemDLL("gdi32.dll").NewProc("AddFontResourceExW")
	procRemoveFontResourceExW = windows.NewLazySystemDLL("gdi32.dll").NewProc("RemoveFontResourceExW")
	procPostMessageW          = windows.NewLazySystemDLL("user32.dll").NewProc("PostMessageW")
)

// winOps keeps Windows registry and GDI calls behind an interface so the
// installation logic can be unit-tested on Windows without touching the real
// system state.
type winOps interface {
	ValueExists(name string) (bool, error)
	WriteValue(name, data string) error
	RemoveValueByName(name string) error
	RemoveValueByData(data string) error
	AddFontResource(path string) error
	RemoveFontResource(path string) error
	BroadcastFontChange()
}

type realWinOps struct {
	hive   registry.Key
	system bool
}

func newRealWinOps(system bool) (*realWinOps, error) {
	var hive registry.Key
	if system {
		hive = registry.LOCAL_MACHINE
	} else {
		hive = registry.CURRENT_USER
	}
	k, err := registry.OpenKey(hive, fontsRegPath, registry.QUERY_VALUE)
	if err != nil {
		// The key always exists on real Windows; recreate logic stays out
		// of scope because fontvault never deletes it.
		return nil, fmt.Errorf("open registry key %s: %w", fontsRegPath, err)
	}
	// Probe only: realWinOps reopens the key per op, so close this handle now.
	_ = k.Close()
	return &realWinOps{hive: hive, system: system}, nil
}

func (r *realWinOps) open(access uint32) (registry.Key, error) {
	return registry.OpenKey(r.hive, fontsRegPath, access)
}

func (r *realWinOps) ValueExists(name string) (bool, error) {
	k, err := r.open(registry.QUERY_VALUE)
	if err != nil {
		return false, err
	}
	defer func() { _ = k.Close() }() // registry handle; close error is not actionable
	_, _, err = k.GetStringValue(name)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (r *realWinOps) WriteValue(name, data string) error {
	k, err := r.open(registry.SET_VALUE | registry.QUERY_VALUE)
	if err != nil {
		return err
	}
	defer func() { _ = k.Close() }() // registry handle; close error is not actionable
	return k.SetStringValue(name, data)
}

func (r *realWinOps) RemoveValueByName(name string) error {
	k, err := r.open(registry.SET_VALUE | registry.QUERY_VALUE)
	if err != nil {
		return err
	}
	defer func() { _ = k.Close() }() // registry handle; close error is not actionable
	if err := k.DeleteValue(name); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}

func (r *realWinOps) RemoveValueByData(data string) error {
	k, err := r.open(registry.SET_VALUE | registry.QUERY_VALUE)
	if err != nil {
		return err
	}
	defer func() { _ = k.Close() }() // registry handle; close error is not actionable
	names, err := k.ReadValueNames(0)
	if err != nil {
		return err
	}
	for _, n := range names {
		v, _, err := k.GetStringValue(n)
		if err != nil {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(v), strings.TrimSpace(data)) {
			if err := k.DeleteValue(n); err != nil && !errors.Is(err, registry.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}

func callFontResource(proc *windows.LazyProc, path string) error {
	p, err := windows.UTF16PtrFromString(fsutil.LongPath(path))
	if err != nil {
		return err
	}
	ret, _, callErr := proc.Call(uintptr(unsafe.Pointer(p)), 0, 0)
	// This SDK's Call yields nil on success; a non-nil callErr is never equal
	// to the ERROR_SUCCESS sentinel, so the nil check alone decides.
	if callErr != nil {
		return callErr
	}
	if ret == 0 {
		return windows.GetLastError()
	}
	return nil
}

func (r *realWinOps) AddFontResource(path string) error {
	return callFontResource(procAddFontResourceExW, path)
}

func (r *realWinOps) RemoveFontResource(path string) error {
	return callFontResource(procRemoveFontResourceExW, path)
}

func (r *realWinOps) BroadcastFontChange() {
	// Best effort: the GDI broadcast result is advisory; failures are not
	// actionable (the font data is already updated when this runs).
	_, _, _ = procPostMessageW.Call(hwndBroadcast, wmFontChange, 0, 0)
}

type windowsInstaller struct {
	system     bool
	installDir string
	ops        winOps
}

func newPlatformInstaller(system bool, installDir string) Installer {
	ops, err := newRealWinOps(system)
	if err != nil {
		// The registry key is required; surface the failure on first use.
		ops = nil
	}
	in := &windowsInstaller{system: system, installDir: installDir}
	if ops != nil {
		in.ops = ops
	}
	return in
}

// registryValueName builds the registry value name for a font:
// "<full name> (TrueType)" or "<full name> (OpenType)".
func registryValueName(meta Meta) string {
	kind := "TrueType"
	if meta.Ext == "otf" {
		kind = "OpenType"
	}
	name := meta.FullName
	if name == "" {
		name = strings.TrimSuffix(meta.Filename, "."+meta.Ext)
	}
	return name + " (" + kind + ")"
}

// registryValueData is the filename only for HKLM, the full path for HKCU.
func (w *windowsInstaller) registryValueData(path string) string {
	if w.system {
		return filepathBase(path)
	}
	return fsutil.LongPath(path)
}

func filepathBase(path string) string {
	if i := strings.LastIndexAny(path, `\/`); i >= 0 {
		return path[i+1:]
	}
	return path
}

func (w *windowsInstaller) NameCollision(meta Meta) (bool, error) {
	if w.ops == nil {
		return false, fmt.Errorf("registry key %s unavailable", fontsRegPath)
	}
	return w.ops.ValueExists(registryValueName(meta))
}

func (w *windowsInstaller) Install(_ context.Context, tempPath string, meta Meta) (string, error) {
	if w.ops == nil {
		return "", fmt.Errorf("registry key %s unavailable", fontsRegPath)
	}
	target, err := moveIntoTarget(tempPath, w.installDir, meta)
	if err != nil {
		return "", err
	}
	name := registryValueName(meta)
	rollback := func() {
		removeQuietly(target)
	}
	if exists, err := w.ops.ValueExists(name); err != nil {
		rollback()
		return "", fmt.Errorf("check registry value %q: %w", name, err)
	} else if exists {
		rollback()
		return "", ErrRegistryNameCollision
	}
	if err := w.ops.WriteValue(name, w.registryValueData(target)); err != nil {
		_ = w.ops.RemoveValueByName(name) // best-effort rollback; write error is the reported cause
		rollback()
		return "", fmt.Errorf("write registry value %q: %w", name, err)
	}
	if err := w.ops.AddFontResource(target); err != nil {
		_ = w.ops.RemoveValueByName(name) // best-effort rollback; the AddFontResource error is the reported cause
		rollback()
		return "", fmt.Errorf("AddFontResourceExW %s: %w", target, err)
	}
	w.ops.BroadcastFontChange()
	return target, nil
}

// RemoveFont unregisters the font (RemoveFontResourceExW, then its registry
// value) and deletes the file. If unregistration fails, the file is left in
// place and the caller reports the error with both copies unchanged.
func (w *windowsInstaller) RemoveFont(_ context.Context, path string, meta Meta) error {
	if w.ops == nil {
		return fmt.Errorf("registry key %s unavailable", fontsRegPath)
	}
	if err := w.ops.RemoveFontResource(path); err != nil {
		return fmt.Errorf("RemoveFontResourceExW %s: %w", path, err)
	}
	if err := w.ops.RemoveValueByName(registryValueName(meta)); err != nil {
		return fmt.Errorf("remove registry value for %s: %w", path, err)
	}
	// Also remove any value whose data points at this file (covers value
	// names that differ from the derived one).
	if err := w.ops.RemoveValueByData(w.registryValueData(path)); err != nil {
		return fmt.Errorf("remove registry value data for %s: %w", path, err)
	}
	if err := os.Remove(fsutil.LongPath(path)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete %s: %w", path, err)
	}
	return nil
}

// RegisterFont re-registers an existing font file (rollback after a failed
// RemoveFont).
func (w *windowsInstaller) RegisterFont(_ context.Context, path string, meta Meta) error {
	if w.ops == nil {
		return fmt.Errorf("registry key %s unavailable", fontsRegPath)
	}
	if err := w.ops.WriteValue(registryValueName(meta), w.registryValueData(path)); err != nil {
		return fmt.Errorf("write registry value for %s: %w", path, err)
	}
	if err := w.ops.AddFontResource(path); err != nil {
		return fmt.Errorf("AddFontResourceExW %s: %w", path, err)
	}
	w.ops.BroadcastFontChange()
	return nil
}

func (w *windowsInstaller) Finish(_ context.Context) error { return nil }

// checkSystemPrereqs verifies that the process token is elevated.
func checkSystemPrereqs() error {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token); err != nil {
		return fmt.Errorf("open process token: %w", err)
	}
	defer func() { _ = token.Close() }() // process token handle; close error is not actionable
	var elevation uint32
	var returned uint32
	if err := windows.GetTokenInformation(token, windows.TokenElevation,
		(*byte)(unsafe.Pointer(&elevation)), uint32(unsafe.Sizeof(elevation)), &returned); err != nil {
		return fmt.Errorf("query token elevation: %w", err)
	}
	if elevation == 0 {
		return fmt.Errorf("pull with -s system requires an elevated token; run fontvault as administrator")
	}
	return nil
}
