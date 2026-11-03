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
	// The archive member name is "<psname>-<keyhash8>.<ext>" and must be at
	// most 100 characters: 100 - 4 (".ttf"/".otf") - 9 ("-"+keyhash8) = 87.
	psNameMaxLen = 87
	// The archive name is "<family>-<familyhash8>.7z" and must stay within
	// 100 characters: 100 - 3 (".7z") - 9 ("-"+familyhash8) = 88.
	archiveNameMaxLen = 88
)

// RepoPath returns the repository path of the family archive holding this
// font: fonts/<family>/<family>-<familyhash8>.7z, lowercased, with every
// component sanitized and capped. Fonts are never stored as loose files: a
// family with one font still gets an archive. The path depends only on the
// family name, never on the content hash.
//
// note: the hash suffix is what the manifest name uses for the same reason:
// two family names can sanitize to the same component ("Cool/Family" and
// "Cool Family"), and without it, they would silently share one archive
// while their manifests, which are named the same way, stayed separate.
func (f *Font) RepoPath() string {
	fam := familyComponent(f.Family)
	return filepath.ToSlash(filepath.Join("fonts", fam, archiveName(f.Family)))
}

// archiveName returns "<sanitized family>-<familyhash8>.7z".
func archiveName(family string) string {
	return strings.ToLower(SanitizeComponent(family, archiveNameMaxLen)) + "-" + HashKey(family)[:8] + ".7z"
}

// MemberName returns the name this font is stored under inside its family
// archive: <psname>-<keyhash8>.<ext>. The key hash keeps two keys whose names
// sanitize to the same string apart, which is normal for the vendor-ID-only
// differences patched fonts carry.
func (f *Font) MemberName() string {
	name := strings.ToLower(SanitizeComponent(f.PostScriptName, psNameMaxLen))
	return name + "-" + f.KeyHash[:8] + "." + f.Ext
}

// ManifestPath returns the manifest path for the font's family:
// manifest/<family>-<familyhash8>.json.
func (f *Font) ManifestPath() string {
	return FamilyManifestPath(f.Family)
}

// FamilyManifestPath builds the manifest path for a family name. The key-hash
// suffix makes the name unique even when two family names sanitize alike.
func FamilyManifestPath(family string) string {
	short := strings.ToLower(SanitizeComponent(family, familyMaxLen))
	return "manifest/" + short + "-" + HashKey(family)[:8] + ".json"
}

// RepoFileName returns the filename (last path element) of RepoPath, the
// family archive name.
func (f *Font) RepoFileName() string {
	return filepath.Base(f.RepoPath())
}

// familyComponent returns the lowercased, sanitized family directory name.
func familyComponent(family string) string {
	return strings.ToLower(SanitizeComponent(family, familyMaxLen))
}
