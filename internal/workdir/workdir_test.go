package workdir

import (
	"os"
	"path/filepath"
	"strings"
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
	if GitAttributesComplete("*.7z filter=lfs diff=lfs -text\n") {
		t.Error("missing merge=lfs is incomplete")
	}
	if GitAttributesComplete("*.7z filter=lfs diff=lfs merge=lfs\n") {
		t.Error("missing -text is incomplete")
	}
	// A repository still routing loose fonts is incomplete: push rewrites
	// the file in its own commit.
	legacy := "*.ttf filter=lfs diff=lfs merge=lfs -text\n*.otf filter=lfs diff=lfs merge=lfs -text\n"
	if GitAttributesComplete(legacy) {
		t.Error("the legacy per-font rules are incomplete for archives")
	}
	// Extra unrelated lines, and the legacy rules alongside the archive
	// rule, are fine.
	withExtra := legacy + GitAttributesContent + "*.png filter=lfs diff=lfs merge=lfs -text\n"
	if !GitAttributesComplete(withExtra) {
		t.Error("extra rules must not make it incomplete")
	}
}

func TestLoadIndexRoundTrip(t *testing.T) {
	dir, fam := roundTripFixture(t)
	idx, errs := LoadIndex(dir)
	requireNoLoadErrors(t, errs)
	got := checkRecordPerFont(t, idx)
	fam2 := requireFamilyForArchive(t, idx)
	checkSharedRecords(t, idx, fam2, got)
	checkFamilyFields(t, idx, fam2, fam)
}

// roundTripFixture builds a two-font family manifest, marshals it, and writes
// it into <dir>/manifest/fam-dfb39c.json for LoadIndex to find.
func roundTripFixture(t *testing.T) (string, *FamilyManifest) {
	dir := t.TempDir()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fam := NewFamilyManifest("Fam", "fonts/fam/fam.7z", now)
	fam.SetArchive(strings.Repeat("a", 64), 70000)
	fam.PutEntry(&Manifest{Key: "ps|VEND", SHA256: "abc", Member: "ps-abc.ttf", Family: "Fam"}, now)
	fam.PutEntry(&Manifest{Key: "ps2|VEND", SHA256: "def", Member: "ps2-def.ttf", Family: "Fam"}, now)
	data, err := Marshal(fam)
	if err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "manifest")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "fam-dfb39c.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, fam
}

// requireNoLoadErrors fails the run when the loader reported a manifest it
// could not read.
func requireNoLoadErrors(t *testing.T, errs []error) {
	t.Helper()
	if len(errs) != 0 {
		t.Fatalf("errs = %v", errs)
	}
}

// checkRecordPerFont: one record per font, with the archive path filled in
// from the family. It hands back the first record for the identity checks.
func checkRecordPerFont(t *testing.T, idx *Index) *Manifest {
	t.Helper()
	if len(idx.ByKey) != 2 {
		t.Fatalf("ByKey has %d entries, want 2", len(idx.ByKey))
	}
	got := idx.ByKey["ps|VEND"]
	if got == nil || got.RepoPath != "fonts/fam/fam.7z" || got.Member != "ps-abc.ttf" {
		t.Fatalf("loaded entry = %+v", got)
	}
	return got
}

// requireFamilyForArchive resolves the loaded family by its archive path.
func requireFamilyForArchive(t *testing.T, idx *Index) *FamilyManifest {
	t.Helper()
	fam2 := idx.FamilyFor("fonts/fam/fam.7z")
	if fam2 == nil {
		t.Fatal("the family record must be reachable by archive path")
	}
	return fam2
}

// checkSharedRecords: ByKey must map to the record inside the loaded family's
// own Fonts vector, so a later PutEntry through the family reaches what push.go
// reads back out of the index.
func checkSharedRecords(t *testing.T, idx *Index, fam2 *FamilyManifest, got *Manifest) {
	t.Helper()
	if idx.ByKey["ps2|VEND"] != fam2.Find("ps2|VEND") {
		t.Error("ByKey must point at the family's own entry record")
	}
	if fam2.Find("ps|VEND") != got {
		t.Error("ByKey and Find must agree on one record")
	}
}

// checkFamilyFields asserts the family's archive fields, dates and manifest
// path survived the round trip, and that an unknown archive resolves to nil.
func checkFamilyFields(t *testing.T, idx *Index, fam2 *FamilyManifest, fam *FamilyManifest) {
	t.Helper()
	if fam2.ArchiveSHA256 != strings.Repeat("a", 64) || fam2.ArchiveSize != 70000 {
		t.Errorf("archive fields lost: %q %d", fam2.ArchiveSHA256, fam2.ArchiveSize)
	}
	if fam2.FirstArchived != fam.FirstArchived || fam2.LastUpdated != fam.LastUpdated {
		t.Error("family dates lost across the round trip")
	}
	if fam2.Path == "" {
		t.Error("the loader must record which file the family manifest came from")
	}
	if idx.FamilyFor("fonts/other/other.7z") != nil {
		t.Error("unknown family must be nil")
	}
}

func TestLoadIndexValidatesEntries(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "manifest")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	// an entry without its member name cannot be extracted from the archive
	if err := os.WriteFile(filepath.Join(sub, "memberless.json"),
		[]byte(`{"family":"F","repo_path":"fonts/f/f.7z","fonts":[{"key":"k","sha256":"s"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// nor can one without a content hash to compare
	if err := os.WriteFile(filepath.Join(sub, "nohash.json"),
		[]byte(`{"family":"F","repo_path":"fonts/f/f2.7z","fonts":[{"key":"k","member":"k.ttf"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// nor the family record itself without the archive it describes
	if err := os.WriteFile(filepath.Join(sub, "norepo.json"),
		[]byte(`{"fonts":[{"key":"k","sha256":"s","member":"k.ttf"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	idx, errs := LoadIndex(dir)
	if len(errs) != 3 {
		t.Fatalf("errs = %v, want three per-file errors", errs)
	}
	if len(idx.ByKey) != 0 || len(idx.ByPath) != 0 {
		t.Fatalf("idx=%v, want empty", idx.ByKey)
	}
}

func TestFamilyManifestFindAndPutEntry(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fam := NewFamilyManifest("Fam", "fonts/fam/fam.7z", now)
	if fam.Find("ps|VEND") != nil {
		t.Error("empty family must not find a key")
	}
	first := fam.PutEntry(&Manifest{Key: "ps|VEND", SHA256: "old", Member: "ps-old.ttf", Family: "Fam"}, now)
	if fam.Find("ps|VEND") != first {
		t.Error("PutEntry must make the key findable")
	}
	if len(fam.Fonts) != 1 {
		t.Errorf("fonts = %d, want 1 (same key replaces)", len(fam.Fonts))
	}
	later := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	replaced := fam.PutEntry(&Manifest{Key: "ps|VEND", SHA256: "new", Member: "ps-new.ttf", Family: "Fam"}, later)
	found := fam.Find("ps|VEND")
	if found == nil {
		t.Fatal("PutEntry must keep the replaced key findable")
	}
	if found.SHA256 != "new" {
		t.Error("PutEntry must replace the entry")
	}
	if found.Member != "ps-new.ttf" {
		t.Error("PutEntry must replace the member name too")
	}
	if replaced != found {
		t.Error("PutEntry must return the record now in the vector")
	}
	if fam.FirstArchived == fam.LastUpdated {
		t.Error("last_updated must move when a font is replaced")
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
