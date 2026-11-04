//go:build linux

package osinfo

// Package OS info reports the host OS name and version.

import (
	"bufio"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// String returns a human-readable OS name and version, e.g.
// "Ubuntu 24.04.1 LTS". It prefers /etc/os-release and falls back to uname.
func String() string {
	pretty := prettyName()
	if pretty != "" {
		return pretty
	}
	fallback := unameString()
	if fallback != "" {
		return fallback
	}
	return "Linux"
}

// prettyName returns the PRETTY_NAME value from /etc/os-release, or "" when
// the file is absent or carries no non-empty PRETTY_NAME.
func prettyName() string {
	f, err := os.Open("/etc/os-release")
	if err != nil {
		return ""
	}
	defer closeQuietly(f)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "PRETTY_NAME=") {
			continue
		}
		v := strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), `"`)
		if v != "" {
			return v
		}
	}
	return ""
}

// unameString returns "<sysname> <release>" (or just "<sysname>") from uname,
// or "" when uname is unavailable or reports an empty sysname.
func unameString() string {
	var uts unix.Utsname
	if err := unix.Uname(&uts); err != nil {
		return ""
	}
	sys := strings.TrimRight(string(uts.Sysname[:]), "\x00")
	if sys == "" {
		return ""
	}
	rel := strings.TrimRight(string(uts.Release[:]), "\x00")
	if rel == "" {
		return sys
	}
	return sys + " " + rel
}

// closeQuietly closes an os-release handle: the scan already produced its
// answer, so a close failure changes nothing (give the best effort).
func closeQuietly(f *os.File) {
	_ = f.Close()
}
