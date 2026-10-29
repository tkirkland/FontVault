// Package fonts implements scanning and metadata extraction for .ttf and
// .otf font files.
package fonts

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
)

// Font is a scanned local font file with extracted metadata.
type Font struct {
	Path     string // path as found during the scan
	RealPath string // symlink-resolved path (dedup identity)
	Ext      string // "ttf" or "otf", lowercase
	SHA256   string // hex sha256 of the file bytes
	Size     int64  // file size in bytes

	// Key is PostScriptName + "|" + VendorID. Comparison is exact.
	Key            string
	KeyHash        string // hex sha256 of Key
	PostScriptName string
	VendorID       string // OS/2 achVendID, spaces and NULs trimmed; may be empty
	FullName       string // name ID 4
	Family         string // name ID 16, falling back to ID 1
	Subfamily      string // name ID 2
	VersionString  string // name ID 5
	FontRevision   string // head.fontRevision as a decimal string
	Copyright      string // name ID 0
	LicenseDesc    string // name ID 13
	LicenseURL     string // name ID 14
}

// KeyOf builds the font identity key: PostScript name + "|" + vendor ID.
func KeyOf(postScriptName, vendorID string) string {
	return postScriptName + "|" + vendorID
}

// HashKey returns the hex sha256 of a key string.
func HashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// ShortHash returns the first 8 hex characters of a sha256 hex string.
func ShortHash(sha string) string {
	if len(sha) < 8 {
		return sha
	}
	return sha[:8]
}

// SanitizeComponent maps a name to ASCII [A-Za-z0-9._-], replacing every
// other byte with '_', and caps the result at maxLen bytes. An empty result
// becomes "_".
func SanitizeComponent(s string, maxLen int) string {
	var b strings.Builder
	for i := 0; i < len(s) && b.Len() < maxLen; i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9', c == '.', c == '_', c == '-':
			b.WriteByte(c)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "_"
	}
	return b.String()
}

const (
	familyMaxLen = 100
	// The filename component is "<psname>-<keyhash8>.<ext>" and must be at
	// most 100 characters: 100 - 4 (".ttf"/".otf") - 9 ("-"+keyhash8) = 87.
	psNameMaxLen = 87
)

// RepoPath returns the stable repository path for the font:
// fonts/<family>/<psname>-<keyhash8>.<ext>, lowercased, with every
// component sanitized and capped. The path depends only on the key and
// metadata-derived names, never on the content hash.
func (f *Font) RepoPath() string {
	family := strings.ToLower(SanitizeComponent(f.Family, familyMaxLen))
	name := strings.ToLower(SanitizeComponent(f.PostScriptName, psNameMaxLen))
	return filepath.ToSlash(filepath.Join("fonts", family,
		name+"-"+f.KeyHash[:8]+"."+f.Ext))
}

// ManifestPath returns the manifest path for the font's key:
// manifest/<keyhash[0:2]>/<keyhash>.json.
func (f *Font) ManifestPath() string {
	return ManifestPathForKey(f.KeyHash)
}

// ManifestPathForKey builds a manifest path from a key hash.
func ManifestPathForKey(keyHash string) string {
	return "manifest/" + keyHash[:2] + "/" + keyHash + ".json"
}

// RepoFileName returns the filename (last path element) of RepoPath.
func (f *Font) RepoFileName() string {
	return filepath.Base(f.RepoPath())
}
