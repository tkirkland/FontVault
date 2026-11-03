// Package exclude implements the exclusion rules applied during a push:
// OS-default fonts, user fonts matching OS defaults, and the user's -x
// exclusions file.
package exclude

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"fontvault/internal/fonts"
)

// Reasons used across reports and summaries.
const (
	ReasonOSDefault             = "OS default"
	ReasonOSDefaultUnreadable   = "cannot determine OS-default status"
	ReasonMatchesOSDefault      = "matches an OS-default system font"
	ReasonSystemScanUnavailable = "system font scan unavailable"
	ReasonExcludedByFile        = "excluded by exclusions file"
	ReasonNotChosenDuplicate    = "local duplicate not chosen"
)

// ExclusionFile holds -x entries: content hashes and case-insensitive
// family names.
type ExclusionFile struct {
	Hashes   map[string]bool
	Families map[string]bool
}

// ParseExclusionFile reads one entry per line. '#' starts a comment.
// An entry is either an SHA-256 hash (64 hex characters) or a case-insensitive
// family name.
func ParseExclusionFile(r io.Reader) (*ExclusionFile, error) {
	xf := &ExclusionFile{Hashes: map[string]bool{}, Families: map[string]bool{}}
	sc := bufio.NewScanner(r)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if isSHA256(line) {
			xf.Hashes[strings.ToLower(line)] = true
			continue
		}
		xf.Families[strings.ToLower(line)] = true
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read exclusions file: %w", err)
	}
	return xf, nil
}

func isSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// OSClassifier decides whether a font in a system font directory is an OS
// default (Windows: owner SID; Linux: installed package).
type OSClassifier interface {
	// Name describes the mechanism, e.g. "owner SID" or "dpkg".
	Name() string
	// IsOSDefault reports whether the file at path is an OS default. An
	// error means the status cannot be determined (fail closed).
	IsOSDefault(path string) (bool, error)
}

// Classifier returns an OSClassifier for the current platform, or nil when
// no mechanism is available (fail closed for system fonts).
func Classifier(logf func(format string, args ...any)) OSClassifier {
	return newOSClassifier(logf)
}

// Decision records the outcome for one local font.
type Decision struct {
	Font     *fonts.Font
	Excluded bool
	Reason   string
	// Protected means the font is an OS default and must never be modified,
	// replaced or removed by any mode (used by pull).
	Protected bool
}

// Apply classifies local fonts for a push/compare scan.
//
//   - systemSource: fonts in system dirs are excluded when they are OS
//     defaults; an undeterminable status excludes the font (fail closed).
//   - userSource: fonts whose content hash matches a system-dir OS default
//     are excluded. When sysScanFailed is true the system dirs could not be
//     scanned, so every user font is excluded (fail closed).
//   - The -x file is applied on top of the rules above.
//   - Protected is set for fonts that are OS-default files themselves; it is
//     meaningful even when the font is not excluded (pull).
func Apply(fontList []*fonts.Font, systemSource bool, sysOSDefaultHashes map[string]bool, sysScanFailed bool, classifier OSClassifier, xf *ExclusionFile) []Decision {
	out := make([]Decision, 0, len(fontList))
	for _, f := range fontList {
		d := Decision{Font: f}
		if systemSource {
			classifySystemFont(&d, f, classifier)
		} else {
			classifyUserFont(&d, f, sysOSDefaultHashes, sysScanFailed)
		}
		applyExclusionFile(&d, f, xf)
		out = append(out, d)
	}
	return out
}

// classifySystemFont applies the OS-default rule to a system-source font:
// excluded when it is an OS default (and protected), or fail closed when the
// status cannot be determined.
func classifySystemFont(d *Decision, f *fonts.Font, classifier OSClassifier) {
	if classifier == nil {
		d.Excluded = true
		d.Reason = ReasonOSDefault + " (no package manager or owner check available)"
		return
	}
	osDefault, err := classifier.IsOSDefault(f.RealPath)
	if err != nil {
		d.Excluded = true
		d.Reason = fmt.Sprintf("%s: %v", ReasonOSDefaultUnreadable, err)
	} else if osDefault {
		d.Excluded = true
		d.Protected = true
		d.Reason = fmt.Sprintf("%s (%s)", ReasonOSDefault, classifier.Name())
	}
}

// classifyUserFont applies the user-source rule: excluded when the system scan
// failed (fail closed) or the font's hash matches an OS-default system font.
func classifyUserFont(d *Decision, f *fonts.Font, sysOSDefaultHashes map[string]bool, sysScanFailed bool) {
	if sysScanFailed {
		d.Excluded = true
		d.Reason = ReasonSystemScanUnavailable
	} else if sysOSDefaultHashes[f.SHA256] {
		d.Excluded = true
		d.Reason = ReasonMatchesOSDefault
	}
}

// applyExclusionFile layers the -x file rule on top, only for fonts the source
// rules did not already exclude.
func applyExclusionFile(d *Decision, f *fonts.Font, xf *ExclusionFile) {
	if d.Excluded || xf == nil {
		return
	}
	if xf.Hashes[f.SHA256] || xf.Families[strings.ToLower(f.Family)] {
		d.Excluded = true
		d.Reason = ReasonExcludedByFile
	}
}

// ProtectionStatus reports whether a local font must be protected from
// modification during pull. When the status cannot be determined the font
// is treated as protected (fail closed).
func ProtectionStatus(f *fonts.Font, systemSource bool, classifier OSClassifier) (protected bool, reason string) {
	if !systemSource {
		return false, ""
	}
	if classifier == nil {
		return true, "OS-default status unknown"
	}
	osDefault, err := classifier.IsOSDefault(f.RealPath)
	if err != nil {
		return true, fmt.Sprintf("OS-default status unknown: %v", err)
	}
	if osDefault {
		return true, fmt.Sprintf("OS default (%s)", classifier.Name())
	}
	return false, ""
}
