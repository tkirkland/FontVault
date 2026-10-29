package app

import (
	"bytes"
	"strings"
	"testing"

	"fontvault/internal/cli"
	"fontvault/internal/fonts"
)

func mkFont(ps, family, sha string) *fonts.Font {
	return &fonts.Font{
		Path: "/f/" + ps + ".ttf", RealPath: "/f/" + ps + ".ttf",
		Ext: "ttf", SHA256: sha, PostScriptName: ps, Family: family,
		Key: ps + "|", KeyHash: fonts.HashKey(ps + "|"),
	}
}

func newPromptApp(t *testing.T, stdin string) *App {
	t.Helper()
	var out, errb bytes.Buffer
	opts := cli.Options{Source: "user", Yes: false, Supplied: map[string]bool{}}
	a := New(opts, "v", strings.NewReader(stdin), &out, &errb)
	a.Log = testLogger()
	a.TTY = func() bool { return true }
	a.prompter = newPrompter(a.Stdin, a.Stdout)
	return a
}

func TestResolveDuplicatesSameHashIsOneFont(t *testing.T) {
	a := newPromptApp(t, "")
	f1 := mkFont("A-Regular", "A", strings.Repeat("1", 64))
	f2 := mkFont("A-Regular", "A", strings.Repeat("1", 64))
	f2.Path = "/other/a.ttf"
	reps, skips, notChosen := a.resolveDuplicates([]*fonts.Font{f1, f2}, "push")
	if len(reps) != 1 || len(skips) != 0 || len(notChosen) != 0 {
		t.Fatalf("reps=%d skips=%d notChosen=%d", len(reps), len(skips), len(notChosen))
	}
	if reps[f1.Key].Path != "/f/A-Regular.ttf" {
		t.Errorf("representative = %q", reps[f1.Key].Path)
	}
}

func TestResolveDuplicatesUnderYesSkips(t *testing.T) {
	a := newPromptApp(t, "")
	a.Opts.Yes = true
	f1 := mkFont("A-Regular", "A", strings.Repeat("1", 64))
	f2 := mkFont("A-Regular", "A", strings.Repeat("2", 64))
	reps, skips, notChosen := a.resolveDuplicates([]*fonts.Font{f1, f2}, "push")
	if len(reps) != 0 || len(skips) != 1 || len(notChosen) != 0 {
		t.Fatalf("reps=%d skips=%d notChosen=%d", len(reps), len(skips), len(notChosen))
	}
	if skips[0].Reason != "-y" || !skips[0].HasOther {
		t.Errorf("skip entry = %+v", skips[0])
	}
}

func TestResolveDuplicatesCompareNeverPrompts(t *testing.T) {
	a := newPromptApp(t, "")
	f1 := mkFont("A-Regular", "A", strings.Repeat("1", 64))
	f2 := mkFont("A-Regular", "A", strings.Repeat("2", 64))
	reps, skips, _ := a.resolveDuplicates([]*fonts.Font{f1, f2}, "compare")
	if len(reps) != 0 || len(skips) != 1 {
		t.Fatalf("reps=%d skips=%d", len(reps), len(skips))
	}
	if skips[0].Reason != "local conflict" {
		t.Errorf("reason = %q", skips[0].Reason)
	}
}

func TestResolveDuplicatesInteractivePick(t *testing.T) {
	// Choose [2], so the second file becomes the representative.
	a := newPromptApp(t, "2\n")
	f1 := mkFont("A-Regular", "A", strings.Repeat("1", 64))
	f2 := mkFont("A-Regular", "A", strings.Repeat("2", 64))
	reps, skips, notChosen := a.resolveDuplicates([]*fonts.Font{f1, f2}, "push")
	if len(reps) != 1 || len(skips) != 0 || len(notChosen) != 1 {
		t.Fatalf("reps=%d skips=%d notChosen=%d", len(reps), len(skips), len(notChosen))
	}
	if got := reps[f1.Key].SHA256; got != strings.Repeat("2", 64) {
		t.Errorf("chosen hash = %s", got)
	}
	if notChosen[0].Reason != "local duplicate not chosen" || notChosen[0].Path != f1.Path {
		t.Errorf("not chosen entry = %+v", notChosen[0])
	}
}

func TestResolveDuplicatesInteractiveSkip(t *testing.T) {
	a := newPromptApp(t, "s\n")
	f1 := mkFont("A-Regular", "A", strings.Repeat("1", 64))
	f2 := mkFont("A-Regular", "A", strings.Repeat("2", 64))
	reps, skips, _ := a.resolveDuplicates([]*fonts.Font{f1, f2}, "push")
	if len(reps) != 0 || len(skips) != 1 {
		t.Fatalf("reps=%d skips=%d", len(reps), len(skips))
	}
	if skips[0].Reason != "user skip" {
		t.Errorf("reason = %q", skips[0].Reason)
	}
}

func TestPromptChoiceReasksOnInvalidInput(t *testing.T) {
	a := newPromptApp(t, "zz\nr\n")
	got, err := a.prompter.choice("pick: ", map[string]string{"r": "r"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "r" {
		t.Errorf("answer = %q", got)
	}
	if !strings.Contains(a.Stdout.(*bytes.Buffer).String(), "Please answer one of") {
		t.Error("invalid answer must be re-asked")
	}
}

func TestFontEntryAndConflictEntry(t *testing.T) {
	f := mkFont("A-Regular", "A", strings.Repeat("1", 64))
	f.Subfamily = "Regular"
	f.VersionString = "1.0"
	e := fontEntry(f, "some reason")
	if e.Family != "A" || e.ShortHash != "11111111" || e.Reason != "some reason" {
		t.Errorf("entry = %+v", e)
	}
}
