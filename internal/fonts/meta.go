package fonts

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/image/font/sfnt"
)

const (
	sfntTrueType = 0x00010000
	sfntTrue     = 0x74727565 // 'true'
	sfntOTTO     = 0x4F54544F // 'OTTO'
)

// Inspect reads, validates and extracts metadata from a font file.
func Inspect(path string) (*Font, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read font file: %w", err)
	}
	meta, err := Parse(data)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	f := &Font{
		Path:           path,
		RealPath:       path,
		Ext:            ext,
		SHA256:         hex.EncodeToString(sum[:]),
		Size:           int64(len(data)),
		PostScriptName: meta.PostScriptName,
		VendorID:       meta.VendorID,
		FullName:       meta.FullName,
		Family:         meta.Family,
		Subfamily:      meta.Subfamily,
		VersionString:  meta.VersionString,
		FontRevision:   meta.FontRevision,
		Copyright:      meta.Copyright,
		LicenseDesc:    meta.LicenseDesc,
		LicenseURL:     meta.LicenseURL,
	}
	f.Key = KeyOf(f.PostScriptName, f.VendorID)
	f.KeyHash = HashKey(f.Key)
	return f, nil
}

// Meta is the raw metadata parsed from a font file.
type Meta struct {
	PostScriptName string
	VendorID       string
	FullName       string
	Family         string
	Subfamily      string
	VersionString  string
	FontRevision   string
	Copyright      string
	LicenseDesc    string
	LicenseURL     string
}

// Parse validates data as a TTF/OTF font (sfnt version tag plus a successful
// sfnt.Parse) and extracts the metadata used for identity and manifests.
func Parse(data []byte) (*Meta, error) {
	if len(data) < 12 {
		return nil, fmt.Errorf("invalid font: file too short (%d bytes)", len(data))
	}
	version := binary.BigEndian.Uint32(data[0:4])
	switch version {
	case sfntTrueType, sfntTrue, sfntOTTO:
	default:
		return nil, fmt.Errorf("invalid font: unknown sfnt version 0x%08X", version)
	}
	// The spec requires a successful sfnt parse in addition to the version
	// tag check.
	if _, err := sfnt.Parse(data); err != nil {
		return nil, fmt.Errorf("invalid font: sfnt parse: %w", err)
	}

	nameRecords, err := readNameTable(data)
	if err != nil {
		return nil, err
	}
	m := &Meta{}
	m.PostScriptName = nameRecords[6]
	m.FullName = nameRecords[4]
	if m.PostScriptName == "" {
		m.PostScriptName = m.FullName
	}
	if m.PostScriptName == "" {
		return nil, fmt.Errorf("invalid font: no PostScript name (name ID 6) and no full name (name ID 4)")
	}
	m.Family = nameRecords[16]
	if m.Family == "" {
		m.Family = nameRecords[1]
	}
	m.Subfamily = nameRecords[2]
	m.VersionString = nameRecords[5]
	m.Copyright = nameRecords[0]
	m.LicenseDesc = nameRecords[13]
	m.LicenseURL = nameRecords[14]

	m.VendorID = readVendorID(data)
	revision, err := readFontRevision(data)
	if err != nil {
		return nil, err
	}
	m.FontRevision = revision
	return m, nil
}

type table struct {
	offset, length uint32
}

func readTableDir(data []byte) (map[string]table, error) {
	numTables := int(binary.BigEndian.Uint16(data[4:6]))
	if 12+16*numTables > len(data) {
		return nil, fmt.Errorf("invalid font: table directory out of bounds")
	}
	tables := make(map[string]table, numTables)
	for i := range numTables {
		rec := data[12+16*i:]
		tag := string(rec[0:4])
		offset := binary.BigEndian.Uint32(rec[8:12])
		length := binary.BigEndian.Uint32(rec[12:16])
		if uint64(offset)+uint64(length) > uint64(len(data)) {
			return nil, fmt.Errorf("invalid font: table %q out of bounds", tag)
		}
		tables[tag] = table{offset, length}
	}
	return tables, nil
}

// namePriority assigns a higher score to more preferred platform/encoding
// combinations: Windows BMP (3,1), Windows UCS-4 (3,10), Unicode (0,x),
// Macintosh Roman (1,0), then anything else in table order.
func namePriority(platform, encoding uint16) int {
	switch {
	case platform == 3 && encoding == 1:
		return 400
	case platform == 3 && encoding == 10:
		return 300
	case platform == 0:
		return 200
	case platform == 1 && encoding == 0:
		return 100
	default:
		return 0
	}
}

func readNameTable(data []byte) (map[uint16]string, error) {
	h, err := nameTableHeader(data)
	if err != nil {
		return nil, err
	}
	best, err := bestNameCandidates(data, h)
	if err != nil {
		return nil, err
	}
	return nameValues(best), nil
}

// nameHeader is a validated name-table header: where its records start, how
// many there are, and where its string data starts.
type nameHeader struct {
	base, records, stringOffset int
}

// nameTableHeader locates the "name" table and validates its header.
func nameTableHeader(data []byte) (nameHeader, error) {
	tables, err := readTableDir(data)
	if err != nil {
		return nameHeader{}, err
	}
	t, ok := tables["name"]
	if !ok {
		// Fonts without a name table are invalid for our purposes: identity
		// cannot be established.
		return nameHeader{}, fmt.Errorf("invalid font: no name table")
	}
	if t.length < 6 {
		return nameHeader{}, fmt.Errorf("invalid font: name table too short")
	}
	base := int(t.offset)
	format := binary.BigEndian.Uint16(data[base : base+2])
	if format != 0 && format != 1 {
		return nameHeader{}, fmt.Errorf("invalid font: unsupported name table format %d", format)
	}
	h := nameHeader{
		base:         base,
		records:      int(binary.BigEndian.Uint16(data[base+2 : base+4])),
		stringOffset: int(binary.BigEndian.Uint16(data[base+4 : base+6])),
	}
	if h.base+6+12*h.records > len(data) {
		return nameHeader{}, fmt.Errorf("invalid font: name records out of bounds")
	}
	return h, nil
}

// nameRecord is one name-table record with the bytes of its string resolved.
// order is the record's position in the table, which breaks priority ties.
type nameRecord struct {
	platform, encoding, id uint16
	order                  int
	raw                    []byte
}

// nameRecordAt reads record i. A string that runs past the end of the file
// aborts the load rather than skipping the record.
func nameRecordAt(data []byte, h nameHeader, i int) (nameRecord, error) {
	rec := data[h.base+6+12*i : h.base+18+12*i]
	r := nameRecord{
		platform: binary.BigEndian.Uint16(rec[0:2]),
		encoding: binary.BigEndian.Uint16(rec[2:4]),
		id:       binary.BigEndian.Uint16(rec[6:8]),
		order:    i,
	}
	length := int(binary.BigEndian.Uint16(rec[8:10]))
	start := h.base + h.stringOffset + int(binary.BigEndian.Uint16(rec[10:12]))
	if start < 0 || start+length > len(data) {
		return nameRecord{}, fmt.Errorf("invalid font: name string out of bounds")
	}
	r.raw = data[start : start+length]
	return r, nil
}

// candidate is the value currently held for a name ID: the decoded string plus
// what made it win (priority, then table order).
type candidate struct {
	priority int
	order    int
	value    string
}

// bestNameCandidates walks the records in table order: a record whose string
// is out of bounds aborts the load, one that cannot be decoded is skipped.
func bestNameCandidates(data []byte, h nameHeader) (map[uint16]candidate, error) {
	best := make(map[uint16]candidate)
	for i := range h.records {
		r, err := nameRecordAt(data, h, i)
		if err != nil {
			return best, err
		}
		addCandidate(best, r)
	}
	return best, nil
}

// addCandidate decodes a record and records it when it beats what that name ID
// already holds.
func addCandidate(best map[uint16]candidate, r nameRecord) {
	value, ok := decodeNameString(r.platform, r.encoding, r.raw)
	if !ok {
		return
	}
	c := candidate{priority: namePriority(r.platform, r.encoding), order: r.order, value: value}
	prev, found := best[r.id]
	if !found || outranks(c, prev) {
		best[r.id] = c
	}
}

// outranks reports whether c wins over the candidate already held for a name ID:
// a higher priority, or the same priority from an earlier record.
func outranks(c candidate, prev candidate) bool {
	if c.priority != prev.priority {
		return c.priority > prev.priority
	}
	return c.order < prev.order
}

// nameValues projects the winning candidates to their strings.
func nameValues(best map[uint16]candidate) map[uint16]string {
	out := make(map[uint16]string, len(best))
	for id, c := range best {
		out[id] = c.value
	}
	return out
}

func decodeNameString(platform, encoding uint16, raw []byte) (string, bool) {
	switch {
	case platform == 0, platform == 3 && encoding == 10:
		// Unicode: UTF-16BE.
		if len(raw)%2 != 0 {
			return "", false
		}
		var b strings.Builder
		for i := 0; i+1 < len(raw); i += 2 {
			b.WriteRune(rune(binary.BigEndian.Uint16(raw[i:])))
		}
		return b.String(), true
	case platform == 3 && encoding == 1:
		// Windows BMP: UTF-16BE code units.
		if len(raw)%2 != 0 {
			return "", false
		}
		var b strings.Builder
		for i := 0; i+1 < len(raw); i += 2 {
			b.WriteRune(rune(binary.BigEndian.Uint16(raw[i:])))
		}
		return b.String(), true
	case platform == 1 && encoding == 0:
		// Macintosh Roman.
		var b strings.Builder
		for _, c := range raw {
			if c < 0x80 {
				b.WriteByte(c)
			} else if r := macRomanHigh[c-0x80]; r != 0 {
				b.WriteRune(r)
			} else {
				b.WriteRune(rune(c))
			}
		}
		return b.String(), true
	default:
		return "", false
	}
}

func readVendorID(data []byte) string {
	tables, err := readTableDir(data)
	if err != nil {
		return ""
	}
	t, ok := tables["OS/2"]
	if !ok || t.length < 62 {
		return ""
	}
	v := data[int(t.offset)+58 : int(t.offset)+62]
	return strings.TrimRight(string(v), " \x00")
}

// readFontRevision reads head.fontRevision (a 16.16 fixed value) and formats
// it as a decimal string, e.g. 1.00049.
func readFontRevision(data []byte) (string, error) {
	tables, err := readTableDir(data)
	if err != nil {
		return "", err
	}
	t, ok := tables["head"]
	if !ok || t.length < 54 {
		return "", fmt.Errorf("invalid font: no head table")
	}
	rev := binary.BigEndian.Uint32(data[int(t.offset)+4 : int(t.offset)+8])
	major := rev >> 16
	minor := rev & 0xFFFF
	return strconv.FormatUint(uint64(major), 10) + "." + fmt.Sprintf("%05d", minor), nil
}
