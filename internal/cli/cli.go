// Package cli defines fontvault's command line interface.
package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/pflag"
)

// Options are the parsed command line options.
type Options struct {
	Source      string
	Direction   string // "push", "pull", "sync", or "" for compare
	Compare     bool
	Branch      string
	Workdir     string
	Token       string
	TokenEnv    string
	ExcludeFile string
	DryRun      bool
	Yes         bool
	Verbose     bool
	Help        bool
	Version     bool
	// Supplied records which persisted switches were given on the command
	// line (used when saving the cfg).
	Supplied map[string]bool
}

// UsageError is a command line usage problem (exit code 5).
type UsageError struct{ msg string }

func (e *UsageError) Error() string { return e.msg }

func usageErrorf(format string, args ...any) error {
	return &UsageError{msg: fmt.Sprintf(format, args...)}
}

const (
	SourceSystem = "system"
	SourceUser   = "user"

	DirectionPush = "push"
	DirectionPull = "pull"
	DirectionSync = "sync"
)

// Parse parses args (without the program name) and validates them.
func Parse(args []string, errOut io.Writer) (Options, error) {
	var o Options
	o.Supplied = map[string]bool{}
	fs := pflag.NewFlagSet("fontvault", pflag.ContinueOnError)
	fs.SetOutput(errOut)
	fs.Usage = func() { Usage(fs.Output()) }

	fs.StringVarP(&o.Source, "source", "s", "", "font source: system or user (required)")
	fs.StringVarP(&o.Direction, "direction", "d", "", "direction: push, pull or sync")
	fs.BoolVarP(&o.Compare, "compare", "c", false, "compare only (default when -d is omitted)")
	fs.StringVarP(&o.Branch, "branch", "b", "", "repository branch (default: main)")
	fs.StringVarP(&o.Workdir, "workdir", "w", "", "local git workdir path")
	fs.StringVarP(&o.Token, "token", "t", "", "GitHub token")
	fs.StringVarP(&o.TokenEnv, "token-env", "e", "", "environment variable holding the GitHub token (default: GITHUB_TOKEN)")
	fs.StringVarP(&o.ExcludeFile, "exclude-file", "x", "", "exclusions file: one SHA-256 hash or case-insensitive family name per line")
	fs.BoolVarP(&o.DryRun, "dry-run", "n", false, "print what would be done without writing anything")
	fs.BoolVarP(&o.Yes, "yes", "y", false, "answer yes to confirmations; conflicts are skipped")
	fs.BoolVarP(&o.Verbose, "verbose", "v", false, "verbose (debug) logging to stderr")
	fs.BoolVarP(&o.Help, "help", "h", false, "show this help and exit")
	fs.BoolVarP(&o.Version, "version", "V", false, "print the version and exit")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			o.Help = true
			return o, nil
		}
		return Options{}, &UsageError{msg: err.Error()}
	}
	if fs.NArg() > 0 {
		return Options{}, usageErrorf("unexpected argument %q", fs.Arg(0))
	}

	for _, name := range []string{"branch", "workdir", "token", "token-env", "exclude-file"} {
		if fs.Changed(name) {
			o.Supplied[name] = true
		}
	}

	if o.Help || o.Version {
		return o, nil
	}
	if err := validate(&o); err != nil {
		return Options{}, err
	}
	return o, nil
}

func validate(o *Options) error {
	if o.Source == "" {
		return usageErrorf("the -s/--source flag is required: choose %q or %q", SourceSystem, SourceUser)
	}
	if o.Source != SourceSystem && o.Source != SourceUser {
		return usageErrorf("invalid -s/--source value %q: choose %q or %q", o.Source, SourceSystem, SourceUser)
	}
	if o.Direction != "" && o.Direction != DirectionPush && o.Direction != DirectionPull && o.Direction != DirectionSync {
		return usageErrorf("invalid -d/--direction value %q: choose %q, %q or %q", o.Direction, DirectionPush, DirectionPull, DirectionSync)
	}
	if o.Compare && o.Direction != "" {
		return usageErrorf("-c/--compare and -d/--direction cannot be used together")
	}
	if o.Branch != "" && strings.TrimSpace(o.Branch) == "" {
		return usageErrorf("invalid -b/--branch value: branch must not be blank")
	}
	if o.Workdir != "" && strings.TrimSpace(o.Workdir) == "" {
		return usageErrorf("invalid -w/--workdir value: path must not be blank")
	}
	if o.TokenEnv != "" && strings.TrimSpace(o.TokenEnv) == "" {
		return usageErrorf("invalid -e/--token-env value: environment variable name must not be blank")
	}
	return nil
}

// ModeName returns the human-readable mode name for the options.
func (o Options) ModeName() string {
	switch o.Direction {
	case DirectionPush:
		return "push"
	case DirectionPull:
		return "pull"
	case DirectionSync:
		return "sync"
	default:
		return "compare"
	}
}

const usageTemplate = `fontvault - archive TTF/OTF fonts in a GitHub repository via Git LFS

Only TrueType (.ttf) and OpenType (.otf) fonts are processed.

Usage:
  fontvault -s <system|user> [-d <push|pull|sync> | -c] [flags]

Switches:
  -s, --source <system|user>   Selects the local font dir that is read (push/compare)
                               and installed into (pull). Required on every run.
  -d, --direction <push|pull|sync>
                               Direction of the sync. push archives local fonts to the
                               repo; pull installs repo fonts missing locally; sync runs
                               push and then pull. Exclusive with -c.
  -c, --compare                Read-only comparison report. This is the default when no
                               direction is given. Exclusive with -d.
  -b, --branch <name>          Repository branch. Default: main. Persisted in the cfg.
  -w, --workdir <path>         Local git workdir. Default:
                               <user cache dir>/fontvault/FontVault_fonts.
                               Persisted in the cfg.
  -t, --token <token>          GitHub token. Persisted (encrypted) in the cfg. Other
                               sources, in order: the env var named by -e, the cfg, and
                               'gh auth token' (never persisted).
  -e, --token-env <name>       Environment variable holding the GitHub token.
                               Default: GITHUB_TOKEN. Persisted in the cfg.
  -x, --exclude-file <path>    Exclusions file: one entry per line, either a SHA-256 hash
                               or a case-insensitive family name; '#' starts a comment.
                               Persisted in the cfg.
  -n, --dry-run                Perform every step up to the first write and print exactly
                               what would be done. Makes no commits, pushes, file copies,
                               installs, registry writes or repo creation, and writes
                               neither the report nor the cfg.
  -y, --yes                    Answer yes to the settings confirmation and to repo
                               creation. Conflicts are never prompted; they are skipped
                               and listed in the report.
  -v, --verbose                Verbose (debug) logging on stderr.
  -h, --help                   Show this help and exit.
  -V, --version                Print the version and exit.

Examples:
  # First run with a token
  fontvault -s user -t ghp_... -y

  # Compare the local fonts with the repository (read-only)
  fontvault -s user -c

  # Push fonts from the system directory
  fontvault -s system -d push

  # Pull repository fonts into the user directory
  fontvault -s user -d pull

  # Full sync, unattended
  fontvault -s user -d sync -y

  # See what a push would do without changing anything
  fontvault -s user -d push -n
`

// Usage writes the full help text.
func Usage(w io.Writer) {
	_, _ = fmt.Fprint(w, usageTemplate)
}
