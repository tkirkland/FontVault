package app

import (
	"context"
	"fmt"
	"sort"

	"fontvault/internal/fonts"
	"fontvault/internal/report"
	"fontvault/internal/workdir"
)

// runCompare is read-only: it classifies and reports, making no commits,
// LFS downloads or local changes.
func (a *App) runCompare(ctx context.Context, st *runState) (int, *report.Report) {
	rep := a.baseReport("compare")
	rep.Header.BaseSHA = st.workdir.BaseSHA

	var localOnly, repoOnly, identical, differs []report.Entry
	var excluded []report.Entry

	// Excluded local fonts (with their reasons).
	for _, d := range st.decisions {
		if d.Excluded {
			excluded = append(excluded, fontEntry(d.Font, d.Reason))
		}
	}
	// Unchosen duplicates are also listed as excluded.
	excluded = append(excluded, st.notChosen...)

	for _, f := range sortedReps(st.reps) {
		repo := st.idx.ByKey[f.Key]
		switch {
		case repo == nil:
			localOnly = append(localOnly, fontEntry(f, ""))
		case repo.SHA256 == f.SHA256:
			identical = append(identical, fontEntry(f, ""))
		default:
			differs = append(differs, fontEntry(f, ""))
		}
	}
	// Repo keys with no local presence (excluded locals count as present).
	for _, m := range sortedManifests(st.idx) {
		if st.presentKeys[m.Key] {
			continue
		}
		repoOnly = append(repoOnly, repoEntry(m))
	}

	var errors_ []report.Entry
	errors_ = append(errors_, st.manifestErrs...)
	errors_ = append(errors_, scanErrorEntries(st.scanRes)...)
	errors_ = append(errors_, st.sysScanErrs...)

	rep.AddSection(report.SectionLocalOnly, localOnly)
	rep.AddSection(report.SectionRepoOnly, repoOnly)
	rep.AddSection(report.SectionIdentical, identical)
	rep.AddSection(report.SectionDiffers, differs)
	rep.AddSection(report.SectionConflictsSkipped, st.localConflictSkips)
	rep.AddSection(report.SectionExcluded, excluded)
	rep.AddSection(report.SectionErrors, errors_)
	rep.AddCountSection(report.SectionSkippedFormats, st.scanRes.Skipped)

	autoSkips := 0
	for _, e := range st.localConflictSkips {
		if isAutoSkipReason(e.Reason) {
			autoSkips++
		}
	}
	if a.Opts.DryRun {
		fmt.Fprintf(a.Stdout, "dry-run: would write report %s\n", a.reportPath)
	}
	return finalCode(len(errors_), autoSkips), rep
}

// repoEntry builds a report entry from a repo manifest.
func repoEntry(m *workdir.Manifest) report.Entry {
	return report.Entry{
		Family:    m.Family,
		Subfamily: m.Subfamily,
		Key:       m.Key,
		Version:   m.VersionString,
		ShortHash: fonts.ShortHash(m.SHA256),
		Path:      m.RepoPath,
	}
}

// sortedReps returns the local representatives in deterministic order.
func sortedReps(reps map[string]*fonts.Font) []*fonts.Font {
	out := make([]*fonts.Font, 0, len(reps))
	for _, f := range reps {
		out = append(out, f)
	}
	sortFonts(out)
	return out
}

// sortedManifests returns repo manifests in deterministic order.
func sortedManifests(idx *workdir.Index) []*workdir.Manifest {
	out := make([]*workdir.Manifest, 0, len(idx.ByKey))
	for _, m := range idx.ByKey {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Family != b.Family {
			return a.Family < b.Family
		}
		if a.Subfamily != b.Subfamily {
			return a.Subfamily < b.Subfamily
		}
		if a.Key != b.Key {
			return a.Key < b.Key
		}
		return a.RepoPath < b.RepoPath
	})
	return out
}

func sortFonts(list []*fonts.Font) {
	sort.Slice(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.Family != b.Family {
			return a.Family < b.Family
		}
		if a.Subfamily != b.Subfamily {
			return a.Subfamily < b.Subfamily
		}
		if a.Key != b.Key {
			return a.Key < b.Key
		}
		return a.Path < b.Path
	})
}
