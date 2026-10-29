//go:build linux

// Package osinfo reports the host OS name and version.
package osinfo

import (
	"bufio"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// String returns a human-readable OS name and version, e.g.
// "Ubuntu 24.04.1 LTS". It prefers /etc/os-release and falls back to uname.
func String() string {
	if f, err := os.Open("/etc/os-release"); err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if strings.HasPrefix(line, "PRETTY_NAME=") {
				v := strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), `"`)
				if v != "" {
					return v
				}
			}
		}
	}
	var uts unix.Utsname
	if err := unix.Uname(&uts); err == nil {
		sys := strings.TrimRight(string(uts.Sysname[:]), "\x00")
		rel := strings.TrimRight(string(uts.Release[:]), "\x00")
		if sys != "" {
			if rel != "" {
				return sys + " " + rel
			}
			return sys
		}
	}
	return "Linux"
}
