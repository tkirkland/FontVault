// Package workdir manages the sparse git workdir holding the fontvault
// repository's manifest directory.
package workdir

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Manifest is one font entry inside a family manifest. RepoPath names the
// family archive the entry is stored in; Member names the entry inside that
// archive. SHA256 is the hash of the font's own bytes, not of the archive.
type Manifest struct {
	Key            string `json:"key"`
	PostScriptName string `json:"postscript_name"`
	VendorID       string `json:"vendor_id"`
	FullName       string `json:"full_name"`
	Family         string `json:"family"`
	Subfamily      string `json:"subfamily"`
	VersionString  string `json:"version_string"`
	FontRevision   string `json:"font_revision"`
	Copyright      string `json:"copyright,omitempty"`
	LicenseDesc    string `json:"license_description,omitempty"`
	LicenseURL     string `json:"license_url,omitempty"`
	SHA256         string `json:"sha256"`
	Size           int64  `json:"size"`
	Ext            string `json:"ext"`
	RepoPath       string `json:"repo_path"`
	Member         string `json:"member"`
	FirstArchived  string `json:"first_archived"`
	LastUpdated    string `json:"last_updated"`
}

// FamilyManifest is one manifest file: manifest/<family>-<hash8>.json. It
// describes the family's archive and every font stored inside it.
type FamilyManifest struct {
	Family        string     `json:"family"`
	RepoPath      string     `json:"repo_path"`
	ArchiveSHA256 string     `json:"archive_sha256,omitempty"`
	ArchiveSize   int64      `json:"archive_size"`
	FirstArchived string     `json:"first_archived"`
	LastUpdated   string     `json:"last_updated"`
	Fonts         []Manifest `json:"fonts"`

	// Path is the repository path of this manifest file. It is not part of the
	// JSON: the loader fills it from the file it read, and push fills it when
	// it creates a new one.
	Path string `json:"-"`
}

// Load reads one family manifest file. A corrupt manifest is a per-file
// error for the caller to record.
func Load(path string) (*FamilyManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest %s: %w", path, err)
	}
	var m FamilyManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest %s: %w", path, err)
	}
	if m.RepoPath == "" {
		return nil, fmt.Errorf("parse manifest %s: missing repo_path", path)
	}
	for i := range m.Fonts {
		if err := checkEntry(&m.Fonts[i], path, i); err != nil {
			return nil, err
		}
	}
	m.Path = path
	return &m, nil
}

// checkEntry validates the fields an entry must carry: identity, content
// hash, and the member name inside the archive.
func checkEntry(e *Manifest, path string, i int) error {
	if e.Key == "" {
		return fmt.Errorf("parse manifest %s: fonts[%d]: missing key", path, i)
	}
	if e.SHA256 == "" {
		return fmt.Errorf("parse manifest %s: fonts[%d]: missing sha256", path, i)
	}
	if e.Member == "" {
		return fmt.Errorf("parse manifest %s: fonts[%d]: missing member", path, i)
	}
	return nil
}

// Index is the set of all repo manifests: every font entry by key, and every
// family by archive path.
type Index struct {
	ByKey map[string]*Manifest
	// ByPath maps a family archive path to the manifest describing it.
	ByPath map[string]*FamilyManifest
}

// NewIndex builds an empty index.
func NewIndex() *Index {
	return &Index{ByKey: map[string]*Manifest{}, ByPath: map[string]*FamilyManifest{}}
}

// FamilyFor returns the manifest describing the archive at repoPath (nil when
// the family is not in the repository).
func (l *Index) FamilyFor(repoPath string) *FamilyManifest {
	return l.ByPath[repoPath]
}

// LoadIndex walks workdir/manifest and loads every manifest. A corrupt
// manifest is a per-file error and does not abort the load.
func LoadIndex(workdir string) (*Index, []error) {
	root := filepath.Join(workdir, "manifest")
	load := &indexLoad{idx: NewIndex()}
	if _, err := os.ReadDir(root); err != nil {
		if os.IsNotExist(err) {
			return load.idx, nil
		}
		load.dirUnreadable(root, err)
		return load.idx, load.errs
	}
	load.walk(root)
	return load.idx, load.errs
}

// indexLoad accumulates one LoadIndex run: the index being built plus the
// errors (unreadable directory, corrupt manifest) that were recorded
// instead of aborting the walk.
type indexLoad struct {
	idx  *Index
	errs []error
}

// walk reads one manifest directory: subdirectories recurse, everything
// else goes through addManifest. An unreadable directory is recorded and
// skipped, its subtree included.
func (l *indexLoad) walk(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		l.dirUnreadable(dir, err)
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			l.walk(filepath.Join(dir, e.Name()))
			continue
		}
		l.addManifest(dir, e.Name())
	}
}

// addManifest loads and indexes one manifest file. A non-JSON name is
// ignored; a manifest that fails to load is recorded and skipped. Every
// entry is indexed under ByKey with its archive path filled in, so callers
// keep seeing one record per font.
func (l *indexLoad) addManifest(dir, name string) {
	if !strings.EqualFold(filepath.Ext(name), ".json") {
		return
	}
	path := filepath.Join(dir, name)
	m, err := Load(path)
	if err != nil {
		l.errs = append(l.errs, err)
		return
	}
	l.idx.ByPath[m.RepoPath] = m
	for i := range m.Fonts {
		// The family record is authoritative for where its fonts live, so
		// the entry's archive path comes from it rather than from the entry's
		// own (possibly stale or absent) field.
		m.Fonts[i].RepoPath = m.RepoPath
		l.idx.ByKey[m.Fonts[i].Key] = &m.Fonts[i]
	}
}

// dirUnreadable records the failure to list a manifest directory.
func (l *indexLoad) dirUnreadable(dir string, err error) {
	l.errs = append(l.errs, fmt.Errorf("read manifest dir %s: %w", dir, err))
}

// NewFamilyManifest starts the manifest for a family whose archive is at
// repoPath, at time now (UTC).
func NewFamilyManifest(family string, repoPath string, now time.Time) *FamilyManifest {
	stamp := now.UTC().Format(time.RFC3339)
	return &FamilyManifest{
		Family:        family,
		RepoPath:      repoPath,
		FirstArchived: stamp,
		LastUpdated:   stamp,
		Fonts:         []Manifest{},
	}
}

// Find returns the entry for a font key (nil when the family does not hold
// that key).
func (l *FamilyManifest) Find(key string) *Manifest {
	for i := range l.Fonts {
		if l.Fonts[i].Key == key {
			return &l.Fonts[i]
		}
	}
	return nil
}

// PutEntry inserts or replaces the entry for key and stamps the family's
// last_updated. It returns the entry now stored, so the caller can keep using
// it (Index.ByKey must point at the same record).
func (l *FamilyManifest) PutEntry(e *Manifest, now time.Time) *Manifest {
	stamp := now.UTC().Format(time.RFC3339)
	for i := range l.Fonts {
		if l.Fonts[i].Key == e.Key {
			l.Fonts[i] = *e
			l.LastUpdated = stamp
			return &l.Fonts[i]
		}
	}
	l.Fonts = append(l.Fonts, *e)
	l.LastUpdated = stamp
	if l.FirstArchived == "" {
		l.FirstArchived = stamp
	}
	return &l.Fonts[len(l.Fonts)-1]
}

// SetArchive records the content hash and size of the family archive, as
// written by this run. It is what a later run fetches and verifies against.
func (l *FamilyManifest) SetArchive(sha256 string, size int64) {
	l.ArchiveSHA256 = sha256
	l.ArchiveSize = size
}

// NewFromFont stamps a new entry's archive dates at time now (UTC).
func NewFromFont(m *Manifest, now time.Time) *Manifest {
	m.FirstArchived = now.UTC().Format(time.RFC3339)
	m.LastUpdated = m.FirstArchived
	return m
}

// UpdateContent stamps the entry of a replacement font, preserving its
// first_archived (the caller copies it over before calling).
func UpdateContent(m *Manifest, now time.Time) {
	m.LastUpdated = now.UTC().Format(time.RFC3339)
}

// Marshal renders a family manifest's JSON deterministically.
func Marshal(m *FamilyManifest) ([]byte, error) {
	return json.MarshalIndent(m, "", "  ")
}
