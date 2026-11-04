//go:build linux

package exclude

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// packageClassifier decides whether a path is owned by an installed
// package, probing dpkg, rpm, pacman and apk in that order.
type packageClassifier struct {
	kind      string
	dpkgOwned map[string]bool
	logf      func(format string, args ...any)
}

func newOSClassifier(logf func(format string, args ...any)) OSClassifier {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if _, err := os.Stat("/var/lib/dpkg/info"); err == nil {
		c := &packageClassifier{kind: "dpkg", logf: logf}
		c.loadDpkgLists()
		return c
	}
	for _, kind := range []string{"rpm", "pacman", "apk"} {
		if _, err := exec.LookPath(kind); err == nil {
			return &packageClassifier{kind: kind, logf: logf}
		}
	}
	return nil
}

func (c *packageClassifier) Name() string { return c.kind + " package" }

// IsOSDefault reports whether the file belongs to an installed package.
func (c *packageClassifier) IsOSDefault(path string) (bool, error) {
	owned, err := c.owned(map[string]bool{path: true})
	if err != nil {
		return false, err
	}
	return owned[path], nil
}

func (c *packageClassifier) loadDpkgLists() {
	c.dpkgOwned = map[string]bool{}
	lists, err := filepath.Glob("/var/lib/dpkg/info/*.list")
	if err != nil {
		return
	}
	for _, l := range lists {
		f, err := os.Open(l)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			p := strings.TrimSpace(sc.Text())
			if p == "" {
				continue
			}
			// Package lists sometimes carry relative entries; only absolute
			// paths can match font files.
			if strings.HasPrefix(p, "/") {
				c.dpkgOwned[p] = true
			}
		}
		f.Close()
	}
}

// owned batches a query to the detected package manager.
func (c *packageClassifier) owned(paths map[string]bool) (map[string]bool, error) {
	out := map[string]bool{}
	if len(paths) == 0 {
		return out, nil
	}
	switch c.kind {
	case "dpkg":
		if c.dpkgOwned == nil {
			return nil, fmt.Errorf("dpkg package lists unavailable")
		}
		for p := range paths {
			out[p] = c.dpkgOwned[p]
		}
		return out, nil
	case "rpm":
		return c.queryRpm(paths)
	case "pacman":
		return c.queryPacman(paths)
	case "apk":
		return c.queryApk(paths)
	}
	return nil, fmt.Errorf("no package manager detected")
}

const batchSize = 100

func (c *packageClassifier) batch(paths map[string]bool) [][]string {
	var all []string
	for p := range paths {
		all = append(all, p)
	}
	var batches [][]string
	for len(all) > 0 {
		n := len(all)
		if n > batchSize {
			n = batchSize
		}
		batches = append(batches, all[:n])
		all = all[n:]
	}
	return batches
}

func (c *packageClassifier) run(args ...string) string {
	cmd := exec.Command(c.kind, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	// Query commands exit non-zero for unowned files; the output text is
	// what carries the per-file answers, so the exit status is ignored.
	out, _ := cmd.CombinedOutput()
	return string(out)
}

// queryRpm runs `rpm -qf <path>...`; output "file X is not owned by any
// package" means not owned.
func (c *packageClassifier) queryRpm(paths map[string]bool) (map[string]bool, error) {
	out := map[string]bool{}
	for _, batch := range c.batch(paths) {
		text := c.run(append([]string{"-qf"}, batch...)...)
		for _, line := range outputLines(text) {
			if idx := strings.Index(line, " is not owned by any package"); idx > 0 {
				p := strings.TrimSpace(strings.TrimPrefix(line[:idx], "file"))
				out[p] = false
				continue
			}
			// "<path> is owned by <pkg>-<version>"
			if p := ownedPath(line); p != "" {
				out[p] = true
			}
		}
	}
	return requireAnswered(out, paths, "rpm")
}

// queryPacman runs `pacman -Qo <path>...`; "error: No package owns <path>"
// means not owned.
func (c *packageClassifier) queryPacman(paths map[string]bool) (map[string]bool, error) {
	out := map[string]bool{}
	for _, batch := range c.batch(paths) {
		text := c.run(append([]string{"-Qo"}, batch...)...)
		for _, line := range outputLines(text) {
			if p := ownedPath(line); p != "" {
				out[p] = true
				continue
			}
			if idx := strings.Index(line, "error: No package owns "); idx >= 0 {
				out[strings.TrimSpace(line[idx+len("error: No package owns "):])] = false
			}
		}
	}
	return requireAnswered(out, paths, "pacman")
}

// queryApk runs `apk info --who-owns <path>...`.
func (c *packageClassifier) queryApk(paths map[string]bool) (map[string]bool, error) {
	out := map[string]bool{}
	for _, batch := range c.batch(paths) {
		text := c.run(append([]string{"info", "--who-owns"}, batch...)...)
		for _, line := range outputLines(text) {
			// "<path> is owned by <pkg>-<version>"
			if p := ownedPath(line); p != "" {
				out[p] = true
				continue
			}
			// "<path>: No such file or directory" (unowned)
			if idx := strings.Index(line, ": No such file or directory"); idx > 0 {
				out[strings.TrimSpace(line[:idx])] = false
				continue
			}
			// "ERROR: ..." is a failure, not an ownership answer.
			if strings.HasPrefix(line, "ERROR:") {
				return nil, fmt.Errorf("apk query failed: %s", line)
			}
		}
	}
	return requireAnswered(out, paths, "apk")
}

// outputLines trims command output and drops empty lines.
func outputLines(text string) []string {
	var ls []string
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		if l != "" {
			ls = append(ls, l)
		}
	}
	return ls
}

// ownedPath extracts the path from "<path> is owned by <pkg>-<version>" output
// lines; "" means the line does not have that shape.
func ownedPath(line string) string {
	if idx := strings.Index(line, " is owned by "); idx > 0 {
		return strings.TrimSpace(line[:idx])
	}
	return ""
}

// requireAnswered fails the whole batch when the package manager left any
// path unanswered (fail closed).
func requireAnswered(out, paths map[string]bool, manager string) (map[string]bool, error) {
	for p := range paths {
		if _, ok := out[p]; !ok {
			return nil, fmt.Errorf("%s query did not answer for %s", manager, p)
		}
	}
	return out, nil
}
