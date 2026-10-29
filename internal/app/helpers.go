package app

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/term"
)

// stdinIsTTY reports whether the reader wraps a terminal.
func stdinIsTTY(r io.Reader) bool {
	if f, ok := r.(*os.File); ok {
		return term.IsTerminal(int(f.Fd()))
	}
	return false
}

// runGhAuthToken runs `gh auth token`, the last-resort token source. An
// error means gh is missing, unauthenticated or failed.
func runGhAuthToken(ctx context.Context) (string, error) {
	gh, err := exec.LookPath("gh")
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, gh, "auth", "token")
	var out strings.Builder
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return strings.TrimSpace(out.String()), nil
}
