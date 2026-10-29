package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func parse(t *testing.T, args ...string) (Options, error) {
	t.Helper()
	return Parse(args, &bytes.Buffer{})
}

func TestParseDefaultsToCompare(t *testing.T) {
	o, err := parse(t, "-s", "user")
	if err != nil {
		t.Fatal(err)
	}
	if o.ModeName() != "compare" || o.Compare {
		t.Errorf("mode = %q, compare flag = %v", o.ModeName(), o.Compare)
	}
}

func TestParseAllFlags(t *testing.T) {
	o, err := parse(t, "-s", "system", "-d", "sync", "-b", "dev", "-w", "/tmp/w",
		"-t", "tok", "-e", "MY_TOK", "-x", "/tmp/x.txt", "-n", "-y", "-v")
	if err != nil {
		t.Fatal(err)
	}
	if o.Source != "system" || o.Direction != "sync" || o.Branch != "dev" ||
		o.Workdir != "/tmp/w" || o.Token != "tok" || o.TokenEnv != "MY_TOK" ||
		o.ExcludeFile != "/tmp/x.txt" || !o.DryRun || !o.Yes || !o.Verbose {
		t.Fatalf("options = %+v", o)
	}
	for _, f := range PersistedFlagNames() {
		if !o.Supplied[f] {
			t.Errorf("flag %q not recorded as supplied", f)
		}
	}
}

// PersistedFlagNames mirrors the persisted switches for the test.
func PersistedFlagNames() []string {
	return []string{"branch", "workdir", "token", "token-env", "exclude-file"}
}

func TestParseLongForms(t *testing.T) {
	o, err := parse(t, "--source", "user", "--direction", "pull", "--branch", "main",
		"--workdir", "w", "--token", "t", "--token-env", "E", "--exclude-file", "x",
		"--dry-run", "--yes", "--verbose")
	if err != nil {
		t.Fatal(err)
	}
	if o.Source != "user" || o.Direction != "pull" || !o.DryRun || !o.Yes || !o.Verbose {
		t.Fatalf("options = %+v", o)
	}
}

func TestUsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"missing source", nil, "-s/--source flag is required"},
		{"bad source", []string{"-s", "banana"}, "invalid -s/--source"},
		{"bad direction", []string{"-s", "user", "-d", "sideways"}, "invalid -d/--direction"},
		{"compare with direction", []string{"-s", "user", "-c", "-d", "push"}, "cannot be used together"},
		{"unexpected arg", []string{"-s", "user", "extra"}, `unexpected argument "extra"`},
		{"unknown flag", []string{"-s", "user", "--frobnicate"}, "unknown flag"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parse(t, tt.args...)
			var ue *UsageError
			if !errors.As(err, &ue) {
				t.Fatalf("err = %v, want UsageError", err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %q, want substring %q", err, tt.want)
			}
		})
	}
}

func TestHelpAndVersionBypassValidation(t *testing.T) {
	o, err := parse(t, "-h")
	if err != nil || !o.Help {
		t.Fatalf("-h: o=%+v err=%v", o, err)
	}
	o, err = parse(t, "--help")
	if err != nil || !o.Help {
		t.Fatalf("--help: o=%+v err=%v", o, err)
	}
	o, err = parse(t, "-V")
	if err != nil || !o.Version {
		t.Fatalf("-V: o=%+v err=%v", o, err)
	}
}

func TestUsageContainsEverySwitchAndExamples(t *testing.T) {
	var buf bytes.Buffer
	Usage(&buf)
	text := buf.String()
	for _, want := range []string{
		"-s, --source", "-d, --direction", "-c, --compare", "-b, --branch",
		"-w, --workdir", "-t, --token", "-e, --token-env", "-x, --exclude-file",
		"-n, --dry-run", "-y, --yes", "-v, --verbose", "-h, --help", "-V, --version",
		"First run with a token", "Compare the local fonts", "Push fonts from the system",
		"Pull repository fonts into the user", "unattended", "without changing anything",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("usage missing %q", want)
		}
	}
}

func TestSuppliedOnlyForGivenFlags(t *testing.T) {
	o, err := parse(t, "-s", "user", "-b", "dev")
	if err != nil {
		t.Fatal(err)
	}
	if !o.Supplied["branch"] || o.Supplied["token"] {
		t.Errorf("supplied = %v", o.Supplied)
	}
}
