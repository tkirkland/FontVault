// Package report renders fontvault's deterministic plain-text report.
package report

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"fontvault/internal/fsutil"
)

// Section names, in the canonical order.
const (
	SectionLocalOnly        = "local only"
	SectionRepoOnly         = "repo only"
	SectionIdentical        = "identical"
	SectionDiffers          = "differs"
	SectionAdded            = "added to repo"
	SectionReplacedRepo     = "replaced in repo"
	SectionInstalled        = "installed locally"
	SectionReplacedLocal    = "replaced locally"
	SectionConflictsSkipped = "conflicts skipped"
	SectionExcluded         = "excluded locally"
	SectionSkippedFormats   = "skipped formats"
	SectionErrors           = "errors"
)

// SectionOrder is the fixed report section order.
var SectionOrder = []string{
	SectionLocalOnly,
	SectionRepoOnly,
	SectionIdentical,
	SectionDiffers,
	SectionAdded,
	SectionReplacedRepo,
	SectionInstalled,
	SectionReplacedLocal,
	SectionConflictsSkipped,
	SectionExcluded,
	SectionSkippedFormats,
	SectionErrors,
}

// Entry is one line in a section.
type Entry struct {
	Family    string
	Subfamily string
	Key       string
	Version   string
	ShortHash string
	Path      string

	// Reason is used by the conflicts-skipped and excluded sections.
	Reason string

	// The other side of a conflict (repo copy for a local entry, or vice
	// versa). When HasOther is set the entry renders as a two-sided block.
	HasOther       bool
	SideLabel      string
	OtherLabel     string
	OtherVersion   string
	OtherShortHash string
	OtherPath      string
	OtherFamily    string
	OtherSubfamily string
}

// Section is one report section.
type Section struct {
	Name    string
	Entries []Entry
	// Counts is used by the skipped-formats section (extension -> count).
	Counts map[string]int
}

// Header carries the report header fields.
type Header struct {
	Timestamp time.Time
	Version   string
	Mode      string
	OS        string
	Source    string
	FontDirs  []string
	Repo      string // "<login>/fontvault"
	Branch    string
	BaseSHA   string
	// NewSHA is printed for push/sync. Empty means "(no new commit)".
	NewSHA string
}

// Report is the full run report.
type Report struct {
	Header   Header
	Sections []*Section
}

// AddSection appends a section.
func (r *Report) AddSection(name string, entries []Entry) {
	entries = append([]Entry(nil), entries...)
	sortEntries(entries)
	r.Sections = append(r.Sections, &Section{Name: name, Entries: entries})
}

// AddCountSection appends a skipped-formats style section.
func (r *Report) AddCountSection(name string, counts map[string]int) {
	r.Sections = append(r.Sections, &Section{Name: name, Counts: counts})
}

func sortEntries(es []Entry) {
	sort.SliceStable(es, func(i, j int) bool {
		a, b := es[i], es[j]
		if a.Family != b.Family {
			return a.Family < b.Family
		}
		if a.Subfamily != b.Subfamily {
			return a.Subfamily < b.Subfamily
		}
		if a.Key != b.Key {
			return a.Key < b.Key
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Reason < b.Reason
	})
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// Filename returns the report file name for a local timestamp:
// fontvault_sync_YYYY-MM-DD_HHMM.txt (24-hour local time). The same name is
// used for every mode and is overwritten in the same minute.
func Filename(now time.Time) string {
	return "fontvault_sync_" + now.Format("2006-01-02_1504") + ".txt"
}

// Render produces the exact report text. The same text is printed to
// stdout.
func (r *Report) Render() string {
	var b strings.Builder
	r.renderHeader(&b)
	byName := sectionsByName(r.Sections)
	renderSummary(&b, byName)
	renderSections(&b, byName)
	return b.String()
}

// renderHeader prints the report header block.
func (r *Report) renderHeader(b *strings.Builder) {
	b.WriteString("fontvault report\n\n")
	_, _ = fmt.Fprintf(b, "timestamp: %s\n", r.Header.Timestamp.Format("2006-01-02 15:04:05 -0700"))
	_, _ = fmt.Fprintf(b, "version: %s\n", dash(r.Header.Version))
	_, _ = fmt.Fprintf(b, "mode: %s\n", dash(r.Header.Mode))
	_, _ = fmt.Fprintf(b, "os: %s\n", dash(r.Header.OS))
	_, _ = fmt.Fprintf(b, "source: %s\n", dash(r.Header.Source))
	if len(r.Header.FontDirs) == 0 {
		b.WriteString("font dirs: (none)\n")
	} else {
		b.WriteString("font dirs:\n")
		for _, d := range r.Header.FontDirs {
			_, _ = fmt.Fprintf(b, "  - %s\n", d)
		}
	}
	_, _ = fmt.Fprintf(b, "repository: %s\n", dash(r.Header.Repo))
	_, _ = fmt.Fprintf(b, "branch: %s\n", dash(r.Header.Branch))
	if r.Header.BaseSHA == "" {
		_, _ = fmt.Fprintf(b, "commit compared: (empty repository)\n")
	} else {
		_, _ = fmt.Fprintf(b, "commit compared: %s\n", r.Header.BaseSHA)
	}
	if r.Header.Mode == "push" || r.Header.Mode == "sync" {
		newSHA := r.Header.NewSHA
		if newSHA == "" {
			newSHA = "(no new commit)"
		}
		_, _ = fmt.Fprintf(b, "new commit: %s\n", newSHA)
	}
}

// renderSummary prints the summary block: present sections in canonical
// order.
func renderSummary(b *strings.Builder, byName map[string]*Section) {
	b.WriteString("\nsummary\n")
	summaryWritten := false
	for _, name := range SectionOrder {
		s, ok := byName[name]
		if !ok {
			continue
		}
		summaryWritten = true
		if name == SectionSkippedFormats {
			_, _ = fmt.Fprintf(b, "  %s: %s\n", name, formatCounts(s.Counts))
		} else {
			_, _ = fmt.Fprintf(b, "  %s: %d\n", name, len(s.Entries))
		}
	}
	if !summaryWritten {
		b.WriteString("  (nothing to report)\n")
	}
}

// sectionsByName indexes sections by name; last write wins, as before.
func sectionsByName(ss []*Section) map[string]*Section {
	m := map[string]*Section{}
	for _, s := range ss {
		m[s.Name] = s
	}
	return m
}

// renderSections prints each present section body in canonical order.
func renderSections(b *strings.Builder, byName map[string]*Section) {
	for _, name := range SectionOrder {
		s, ok := byName[name]
		if !ok {
			continue
		}
		_, _ = fmt.Fprintf(b, "\n%s\n", name)
		if name == SectionSkippedFormats {
			renderCountsBody(b, s)
		} else if len(s.Entries) == 0 {
			b.WriteString("  (none)\n")
		} else {
			for _, e := range s.Entries {
				renderEntry(b, name, e)
			}
		}
	}
}

// renderCountsBody lists the skipped-formats extensions in sorted order.
func renderCountsBody(b *strings.Builder, s *Section) {
	extensions := make([]string, 0, len(s.Counts))
	for e := range s.Counts {
		extensions = append(extensions, e)
	}
	sort.Strings(extensions)
	if len(extensions) == 0 {
		b.WriteString("  (none)\n")
	}
	for _, e := range extensions {
		_, _ = fmt.Fprintf(b, "  %s: %d\n", e, s.Counts[e])
	}
}

// renderEntry prints one entry: the errors section has its own shape, two-sided
// conflicts form a block, everything else is the plain one-line form.
func renderEntry(b *strings.Builder, name string, e Entry) {
	if name == SectionErrors {
		_, _ = fmt.Fprintf(b, "  %s: %s\n", dash(e.Path), e.Reason)
		return
	}
	if e.HasOther {
		renderConflictEntry(b, e)
		return
	}
	_, _ = fmt.Fprintf(b, "  %s | %s | %s | %s | %s\n",
		dash(e.Family), dash(e.Subfamily), dash(e.Version), dash(e.ShortHash), dash(e.Path))
	if e.Reason != "" {
		_, _ = fmt.Fprintf(b, "    reason: %s\n", e.Reason)
	}
}

// renderConflictEntry prints a two-sided conflict block.
func renderConflictEntry(b *strings.Builder, e Entry) {
	side := e.SideLabel
	if side == "" {
		side = "local:"
	}
	other := e.OtherLabel
	if other == "" {
		other = "repo:"
	}
	_, _ = fmt.Fprintf(b, "  %s | %s\n", dash(e.Family), dash(e.Subfamily))
	_, _ = fmt.Fprintf(b, "    %-8s %s | %s | %s\n", side, dash(e.Version), dash(e.ShortHash), dash(e.Path))
	_, _ = fmt.Fprintf(b, "    %-8s %s | %s | %s\n", other, dash(e.OtherVersion), dash(e.OtherShortHash), dash(e.OtherPath))
	_, _ = fmt.Fprintf(b, "    reason: %s\n", dash(e.Reason))
}

func formatCounts(counts map[string]int) string {
	if len(counts) == 0 {
		return "(none)"
	}
	extensions := make([]string, 0, len(counts))
	for e := range counts {
		extensions = append(extensions, e)
	}
	sort.Strings(extensions)
	parts := make([]string, 0, len(extensions))
	for _, e := range extensions {
		parts = append(parts, fmt.Sprintf("%s: %d", e, counts[e]))
	}
	return strings.Join(parts, ", ")
}

// Write atomically writes the report text to path (mode 0644).
func Write(path, text string) error {
	return fsutil.AtomicWrite(path, []byte(text), 0o644)
}
