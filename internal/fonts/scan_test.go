package fonts

import (
	"os"
	"path/filepath"
	"testing"

	"fontvault/internal/testfont"
)

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanSkipsFormatsWithoutReading(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "good.ttf"), testfont.Build(t, testfont.Spec{
		PostScriptName: "Good-Regular", FullName: "Good", Family: "Good",
	}))
	// A file with a skipped extension whose contents are garbage: if the
	// scanner opened it, it would error; it must only count the extension.
	writeFile(t, filepath.Join(dir, "collection.ttc"), []byte("not a real ttc"))
	writeFile(t, filepath.Join(dir, "web.woff2"), []byte("junk"))
	writeFile(t, filepath.Join(dir, "old.fon"), []byte("junk"))
	writeFile(t, filepath.Join(dir, "note.txt"), []byte("ignored entirely"))

	res := Scan(ScanOptions{Dirs: []string{dir}, Recursive: true})
	if len(res.Fonts) != 1 {
		t.Fatalf("fonts = %d, want 1 (errors: %v)", len(res.Fonts), res.Errors)
	}
	if res.Skipped[".ttc"] != 1 || res.Skipped[".woff2"] != 1 || res.Skipped[".fon"] != 1 {
		t.Errorf("skipped counts = %v", res.Skipped)
	}
	if res.Skipped[".txt"] != 0 {
		t.Errorf(".txt must not be counted: %v", res.Skipped)
	}
	if len(res.Errors) != 0 {
		t.Errorf("unexpected errors: %v", res.Errors)
	}
}

func TestScanInvalidFileIsPerFileError(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "good.ttf"), testfont.Build(t, testfont.Spec{
		PostScriptName: "Good-Regular", FullName: "Good", Family: "Good",
	}))
	writeFile(t, filepath.Join(dir, "bad.otf"), []byte("garbage otf"))

	res := Scan(ScanOptions{Dirs: []string{dir}})
	if len(res.Fonts) != 1 {
		t.Fatalf("fonts = %d, want 1", len(res.Fonts))
	}
	if len(res.Errors) != 1 {
		t.Fatalf("errors = %d, want 1", len(res.Errors))
	}
	if res.Errors[0].Path != filepath.Join(dir, "bad.otf") {
		t.Errorf("error path = %q", res.Errors[0].Path)
	}
}

func TestScanFollowsSymlinksAndDeduplicates(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	writeFile(t, filepath.Join(sub, "real.ttf"), testfont.Build(t, testfont.Spec{
		PostScriptName: "Real-Regular", FullName: "Real", Family: "Real",
	}))
	if err := os.Symlink(filepath.Join(sub, "real.ttf"), filepath.Join(dir, "link.ttf")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	// A directory symlink pointing back at the parent must not loop.
	if err := os.Symlink(dir, filepath.Join(sub, "loop")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	res := Scan(ScanOptions{Dirs: []string{dir}, Recursive: true})
	if len(res.Fonts) != 1 {
		t.Fatalf("fonts = %d, want 1 (dedupe failed), errors: %v", len(res.Fonts), res.Errors)
	}
}

func TestScanRecursiveFlag(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "top.ttf"), testfont.Build(t, testfont.Spec{
		PostScriptName: "Top-Regular", FullName: "Top", Family: "Top",
	}))
	writeFile(t, filepath.Join(dir, "nested", "inner.ttf"), testfont.Build(t, testfont.Spec{
		PostScriptName: "Inner-Regular", FullName: "Inner", Family: "Inner",
	}))

	res := Scan(ScanOptions{Dirs: []string{dir}, Recursive: false})
	if len(res.Fonts) != 1 || res.Fonts[0].PostScriptName != "Top-Regular" {
		t.Errorf("non-recursive scan got %d fonts, want only top-level", len(res.Fonts))
	}
	res = Scan(ScanOptions{Dirs: []string{dir}, Recursive: true})
	if len(res.Fonts) != 2 {
		t.Errorf("recursive scan got %d fonts, want 2", len(res.Fonts))
	}
}

func TestScanExtraFiles(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(dir, "outside", "extra.ttf")
	writeFile(t, outside, testfont.Build(t, testfont.Spec{
		PostScriptName: "Extra-Regular", FullName: "Extra", Family: "Extra",
	}))
	res := Scan(ScanOptions{Dirs: []string{filepath.Join(dir, "empty")}, ExtraFiles: []string{outside}})
	if len(res.Fonts) != 1 {
		t.Fatalf("fonts = %d, want 1", len(res.Fonts))
	}
	if res.Fonts[0].Path != outside {
		t.Errorf("path = %q", res.Fonts[0].Path)
	}
}
