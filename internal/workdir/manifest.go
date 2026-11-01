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

// Manifest is one font manifest (manifest/<hh>/<keyhash>.json).
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
	FirstArchived  string `json:"first_archived"`
	LastUpdated    string `json:"last_updated"`
}

// Load reads a manifest file.
func Load(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest %s: %w", path, err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest %s: %w", path, err)
	}
	if m.Key == "" {
		return nil, fmt.Errorf("parse manifest %s: missing key", path)
	}
	if m.SHA256 == "" {
		return nil, fmt.Errorf("parse manifest %s: missing sha256", path)
	}
	if m.RepoPath == "" {
		return nil, fmt.Errorf("parse manifest %s: missing repo_path", path)
	}
	return &m, nil
}

// Index is the set of all repo manifests keyed by font key.
type Index struct {
	ByKey map[string]*Manifest
	// ByPath allows duplicate detection on repo paths.
	ByPath map[string]*Manifest
}

// NewIndex builds an empty index.
func NewIndex() *Index {
	return &Index{ByKey: map[string]*Manifest{}, ByPath: map[string]*Manifest{}}
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
// ignored; a manifest that fails to load is recorded and skipped.
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
	l.idx.ByKey[m.Key] = m
	l.idx.ByPath[m.RepoPath] = m
}

// dirUnreadable records the failure to list a manifest directory.
func (l *indexLoad) dirUnreadable(dir string, err error) {
	l.errs = append(l.errs, fmt.Errorf("read manifest dir %s: %w", dir, err))
}

// NewFromFont builds a manifest for a local font at time now (UTC).
func NewFromFont(m *Manifest, now time.Time) *Manifest {
	m.FirstArchived = now.UTC().Format(time.RFC3339)
	m.LastUpdated = m.FirstArchived
	return m
}

// UpdateContent updates the content fields of an existing manifest for a
// replacement font, preserving first_archived.
func UpdateContent(m *Manifest, now time.Time) {
	m.LastUpdated = now.UTC().Format(time.RFC3339)
}

// Marshal renders the manifest JSON deterministically.
func Marshal(m *Manifest) ([]byte, error) {
	return json.MarshalIndent(m, "", "  ")
}
