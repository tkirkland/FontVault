package workdir

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGitAttributesComplete(t *testing.T) {
	if !GitAttributesComplete(GitAttributesContent) {
		t.Error("canonical content must be complete")
	}
	if GitAttributesComplete("") {
		t.Error("empty content is incomplete")
	}
	if GitAttributesComplete("*.ttf filter=lfs diff=lfs merge=lfs -text\n") {
		t.Error("missing *.otf rule is incomplete")
	}
	if GitAttributesComplete("*.ttf filter=lfs diff=lfs -text\n*.otf filter=lfs diff=lfs merge=lfs -text\n") {
		t.Error("missing merge=lfs for ttf is incomplete")
	}
	// Extra unrelated lines are fine.
	withExtra := GitAttributesContent + "*.png filter=lfs diff=lfs merge=lfs -text\n"
	if !GitAttributesComplete(withExtra) {
		t.Error("extra rules must not make it incomplete")
	}
}

func TestLoadIndexRoundTrip(t *testing.T) {
	dir := t.TempDir()
	manifest := &Manifest{
		Key: "ps|VEND", PostScriptName: "ps", VendorID: "VEND",
		FullName: "Full", Family: "Fam", Subfamily: "Reg",
		SHA256: "abc", Size: 12, Ext: "ttf", RepoPath: "fonts/fam/ps-abc.ttf",
	}
	data, err := Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "manifest", "ab")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "key.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	idx, errs := LoadIndex(dir)
	if len(errs) != 0 {
		t.Fatalf("errs = %v", errs)
	}
	got := idx.ByKey["ps|VEND"]
	if got == nil || got.RepoPath != manifest.RepoPath {
		t.Fatalf("loaded = %+v", got)
	}
}

func TestLoadIndexCorruptManifestIsPerFileError(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "manifest", "ab")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "bad.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	idx, errs := LoadIndex(dir)
	if len(errs) != 1 || len(idx.ByKey) != 0 {
		t.Fatalf("idx=%v errs=%v", idx.ByKey, errs)
	}
}

func TestManifestFirstArchivedPreserved(t *testing.T) {
	m := &Manifest{SHA256: "old"}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	NewFromFont(m, now)
	first := m.FirstArchived
	later := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	UpdateContent(m, later)
	if m.FirstArchived != first {
		t.Errorf("first_archived changed: %q -> %q", first, m.FirstArchived)
	}
	if m.LastUpdated == first {
		t.Error("last_updated must be refreshed")
	}
}

func TestLoadIndexMissingDirIsEmpty(t *testing.T) {
	idx, errs := LoadIndex(t.TempDir())
	if len(errs) != 0 || len(idx.ByKey) != 0 {
		t.Fatalf("idx=%v errs=%v", idx.ByKey, errs)
	}
}
