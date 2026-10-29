package app

import (
	"context"
	"fmt"

	"fontvault/internal/report"
)

// runSync runs push, then pull against the post-push repo state. A fatal
// push skips the pull phase entirely.
func (a *App) runSync(ctx context.Context, st *runState) (int, *report.Report) {
	pushCode, pushRep, res := a.runPushPhase(ctx, st, true)
	if pushCode == ExitFatal || pushCode == ExitLockHeld {
		// Fatal push: no pull, no report.
		return pushCode, nil
	}
	if res == nil {
		// Preflight-style failure inside the phase; nothing to report.
		return pushCode, nil
	}

	pullCode, pullRep := a.runPull(ctx, st, res.useRepo)
	if pullCode == ExitFatal {
		return ExitFatal, nil
	}

	// Merge the two phase reports into one sync report.
	rep := a.baseReport("sync")
	rep.Header.BaseSHA = pushRep.Header.BaseSHA
	rep.Header.NewSHA = pushRep.Header.NewSHA
	for _, s := range pushRep.Sections {
		rep.Sections = append(rep.Sections, s)
	}
	for _, s := range pullRep.Sections {
		rep.Sections = append(rep.Sections, s)
	}
	if a.Opts.DryRun {
		fmt.Fprintf(a.Stdout, "dry-run: sync would run push then pull\n")
	}
	if pullCode == ExitCompleted || pushCode == ExitCompleted {
		return ExitCompleted, rep
	}
	return ExitOK, rep
}
