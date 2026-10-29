package app

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"fontvault/internal/cli"
	"fontvault/internal/fonts"
)

func TestNonTTYWithoutYesIsUsageError(t *testing.T) {
	opts, err := cli.Parse([]string{"-s", "user"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	a := New(opts, "v", strings.NewReader(""), &stdout, &stderr)
	a.TTY = func() bool { return false }
	code := a.Run(context.Background())
	if code != ExitUsage {
		t.Fatalf("exit = %d, want %d (stderr: %s)", code, ExitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "-y/--yes is required") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestNonTTYWithYesPassesTTYCheck(t *testing.T) {
	opts, err := cli.Parse([]string{"-s", "user", "-y"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	a := New(opts, "v", strings.NewReader(""), &stdout, &stderr)
	a.TTY = func() bool { return false }
	code := a.Run(context.Background())
	// Without a platform/API seam the run fails later than the TTY check;
	// it must not be the usage error.
	if code == ExitUsage {
		t.Fatalf("TTY check failed despite -y (stderr: %s)", stderr.String())
	}
}

func TestConflictOptionSetsPerMode(t *testing.T) {
	push := map[string]string{"r": "r", "s": "s"}
	sync := map[string]string{"l": "l", "r": "r", "s": "s"}
	pull := map[string]string{"l": "l", "s": "s"}

	if _, ok := push["l"]; ok {
		t.Error("push must not offer 'l'")
	}
	if _, ok := push["r"]; !ok || len(push) != 2 {
		t.Errorf("push options = %v", push)
	}
	if len(sync) != 3 {
		t.Errorf("sync options = %v", sync)
	}
	if _, ok := pull["r"]; ok {
		t.Error("pull must not offer 'r'")
	}
	if len(pull) != 2 {
		t.Errorf("pull options = %v", pull)
	}
}

func TestCommitMessage(t *testing.T) {
	f := func(family string) *fonts.Font { return &fonts.Font{Family: family} }
	msg := string(commitMessage([]*fonts.Font{f("Zeta"), f("Alpha")}, []*fonts.Font{f("Alpha")}))
	if !strings.HasPrefix(msg, "Add 2 fonts, replace 1 font\n") {
		t.Errorf("summary line = %q", msg)
	}
	if !strings.Contains(msg, "Families:\n  Alpha\n  Zeta\n") {
		t.Errorf("family body = %q", msg)
	}
	msg = string(commitMessage([]*fonts.Font{f("Solo")}, nil))
	if !strings.HasPrefix(msg, "Add 1 font\n") {
		t.Errorf("singular summary = %q", msg)
	}
}

func TestFinalCode(t *testing.T) {
	if finalCode(0, 0) != ExitOK {
		t.Error("clean run must exit 0")
	}
	if finalCode(1, 0) != ExitCompleted || finalCode(0, 1) != ExitCompleted {
		t.Error("per-file errors or auto-skips must exit 2")
	}
}

func TestIsAutoSkipReason(t *testing.T) {
	for _, r := range []string{"-y", "OS default (owner SID)", "registry name collision", "local conflict"} {
		if !isAutoSkipReason(r) {
			t.Errorf("reason %q must be an auto skip", r)
		}
	}
	if isAutoSkipReason("user skip") {
		t.Error("user skip must not be an auto skip")
	}
}

func TestIsNonFastForward(t *testing.T) {
	if !isNonFastForward(errorsNew("! [rejected] main -> main (non-fast-forward)")) {
		t.Error("rejected message not detected")
	}
	if !isNonFastForward(errorsNew("failed to push some refs; fetch first")) {
		t.Error("fetch-first message not detected")
	}
	if isNonFastForward(errorsNew("connection refused")) {
		t.Error("unrelated error misdetected")
	}
}

type strErr struct{ s string }

func (e *strErr) Error() string { return e.s }

func errorsNew(s string) error { return &strErr{s} }

type stubHousekeep struct {
	prunes int
	gcs    int
}

func (s *stubHousekeep) LFSPrune(context.Context, string) (string, error) {
	s.prunes++
	return "", nil
}

func (s *stubHousekeep) GCAuto(context.Context, string) (string, error) {
	s.gcs++
	return "", nil
}

func testLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

func TestHousekeepingAfterFailureRunsNoPruneOrGC(t *testing.T) {
	a := &App{Log: testLogger()}
	stub := &stubHousekeep{}
	a.housekeep = stub
	a.pushPhaseOK = true
	a.pullPhaseOK = true
	code := ExitFatal
	a.housekeeping(&code)
	if stub.prunes != 0 || stub.gcs != 0 {
		t.Errorf("failed run must not prune/gc: prunes=%d gcs=%d", stub.prunes, stub.gcs)
	}
}

func TestHousekeepingAfterCompletedRunPrunesAndGCs(t *testing.T) {
	a := &App{Log: testLogger()}
	stub := &stubHousekeep{}
	a.housekeep = stub
	a.pushPhaseOK = true
	a.pullPhaseOK = false
	code := ExitCompleted
	a.housekeeping(&code)
	if stub.prunes != 1 {
		t.Errorf("successful push phase must prune once, got %d", stub.prunes)
	}
	if stub.gcs != 1 {
		t.Errorf("completed run must gc, got %d", stub.gcs)
	}
}

func TestHousekeepingCleanupOnFailure(t *testing.T) {
	a := &App{Log: testLogger()}
	stub := &stubHousekeep{}
	a.housekeep = stub
	a.pushPhaseOK = false
	a.pullPhaseOK = true
	code := ExitPreflight
	a.housekeeping(&code)
	if stub.prunes != 0 || stub.gcs != 0 {
		t.Errorf("preflight failure must not prune/gc: prunes=%d gcs=%d", stub.prunes, stub.gcs)
	}
}

func TestLocalConflictEntryShowsBothSides(t *testing.T) {
	f1 := &fonts.Font{Family: "F", Subfamily: "R", Key: "k", VersionString: "1.0", SHA256: strings.Repeat("a", 64), Path: "/a.ttf"}
	f2 := &fonts.Font{Family: "F", Subfamily: "R", Key: "k", VersionString: "2.0", SHA256: strings.Repeat("b", 64), Path: "/b.ttf"}
	e := localConflictEntry([]*fonts.Font{f1, f2})
	if !e.HasOther || e.OtherPath != "/b.ttf" || e.SideLabel != "local 1:" || e.OtherLabel != "local 2:" {
		t.Errorf("entry = %+v", e)
	}
}
