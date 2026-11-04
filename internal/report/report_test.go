package report

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "update golden files")

func fullReport() *Report {
	r := &Report{Header: Header{
		Timestamp: time.Date(2026, 10, 29, 14, 5, 7, 0, time.FixedZone("CET", 3600)),
		Version:   "1.2.3",
		Mode:      "compare",
		OS:        "TestOS 1.0",
		Source:    "user",
		FontDirs:  []string{"/home/u/.local/share/fonts"},
		Repo:      "alice/FontVault_fonts",
		Branch:    "main",
		BaseSHA:   "0123456789abcdef",
	}}
	r.AddSection(SectionLocalOnly, []Entry{
		{Family: "Zulu", Subfamily: "Regular", Key: "z", Version: "1.0", ShortHash: "aaaaaaaa", Path: "/l/z.ttf"},
		{Family: "Alpha", Subfamily: "Bold", Key: "a", Version: "2.0", ShortHash: "bbbbbbbb", Path: "/l/a.ttf"},
	})
	r.AddSection(SectionRepoOnly, []Entry{
		{Family: "Alpha", Subfamily: "Regular", Key: "ar", Version: "1.1", ShortHash: "cccccccc", Path: "fonts/alpha/ar-cccccccc.ttf"},
	})
	r.AddSection(SectionIdentical, []Entry{
		{Family: "Same", Subfamily: "Regular", Key: "s", Version: "1.0", ShortHash: "dddddddd", Path: "/l/s.ttf"},
	})
	r.AddSection(SectionDiffers, []Entry{
		{Family: "Diff", Subfamily: "Regular", Key: "d", Version: "1.0", ShortHash: "eeeeeeee", Path: "/l/d.ttf"},
	})
	r.AddSection(SectionConflictsSkipped, []Entry{
		{
			Family: "Conflict", Subfamily: "Regular", Key: "c",
			Version: "1.0", ShortHash: "ffffffff", Path: "/l/c.ttf",
			HasOther: true, OtherVersion: "1.1", OtherShortHash: "11111111",
			OtherPath: "fonts/conflict/c-11111111.ttf",
			Reason:    "skipped with -y",
		},
	})
	r.AddSection(SectionExcluded, []Entry{
		{Family: "Excluded", Subfamily: "Regular", Key: "x", Version: "1.0", ShortHash: "22222222", Path: "/l/x.ttf", Reason: "OS default (owner SID)"},
	})
	r.AddSection(SectionErrors, []Entry{
		{Path: "/l/bad.ttf", Reason: "invalid font: sfnt parse: invalid font"},
	})
	r.AddCountSection(SectionSkippedFormats, map[string]int{".ttc": 2, ".fon": 1})
	return r
}

func TestRenderGolden(t *testing.T) {
	got := fullReport().Render()
	golden := filepath.Join("testdata", "compare_report.golden")
	if *update {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden file: %v (run with -update to create it)", err)
	}
	if got != string(want) {
		t.Errorf("report differs from golden file %s:\n--- got ---\n%s\n--- want ---\n%s", golden, got, want)
	}
}

func TestRenderEmptySections(t *testing.T) {
	r := &Report{Header: Header{Timestamp: time.Now(), Mode: "pull"}}
	r.AddSection(SectionInstalled, nil)
	r.AddSection(SectionErrors, nil)
	text := r.Render()
	if !strings.Contains(text, "\ninstalled locally\n  (none)\n") {
		t.Error("empty section must print (none)")
	}
	if !strings.Contains(text, "installed locally: 0") {
		t.Error("summary must contain the empty section count")
	}
}

func TestPushHeaderNewCommit(t *testing.T) {
	r := &Report{Header: Header{Timestamp: time.Now(), Mode: "push", BaseSHA: "abc", NewSHA: "def"}}
	text := r.Render()
	if !strings.Contains(text, "new commit: def") {
		t.Error("push header must print the new commit")
	}
	r.Header.NewSHA = ""
	text = r.Render()
	if !strings.Contains(text, "new commit: (no new commit)") {
		t.Error("push header without a commit must print (no new commit)")
	}
	r.Header.Mode = "compare"
	text = r.Render()
	if strings.Contains(text, "new commit") {
		t.Error("compare header must not print a new commit line")
	}
}

func TestFilenameAndSameMinuteOverwrite(t *testing.T) {
	now := time.Date(2026, 10, 29, 14, 5, 7, 0, time.FixedZone("CET", 3600))
	name := Filename(now)
	if name != "fontvault_sync_2026-10-29_1405.txt" {
		t.Errorf("filename = %q", name)
	}
	// Same minute, different second: identical filename, overwritten.
	if Filename(now.Add(time.Second)) != name {
		t.Error("same-minute run must reuse the filename")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := Write(path, "first"); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, "second"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "second" {
		t.Errorf("overwrite failed: %q", data)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), "tmp") {
			t.Errorf("leftover temp file %s", e.Name())
		}
	}
}

func TestSortDeterministic(t *testing.T) {
	a := fullReport().Render()
	b := fullReport().Render()
	if a != b {
		t.Error("rendering must be deterministic")
	}
}
