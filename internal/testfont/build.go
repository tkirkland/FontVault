// Package testfont builds small but structurally valid TTF and OTF files
// for tests. The TrueType files are synthesized from scratch; the OpenType
// files reuse the tiny CFF table from the BSD-licensed CFFTest.otf fixture in
// golang.org/x/image (https://golang.org/LICENSE) so that sfnt.Parse accepts
// them, while every metadata table (name, OS/2, head) is synthesized with the
// requested values.
package testfont

import (
	"encoding/base64"
	"encoding/binary"

	"sort"
	"testing"
)

// Spec describes a test font.
type Spec struct {
	PostScriptName string // name ID 6; omitted when empty
	FullName       string // name ID 4; omitted when empty
	Family         string // name ID 16; omitted when empty
	FamilyID1      string // name ID 1 fallback; omitted when empty
	Subfamily      string // name ID 2; omitted when empty
	Version        string // name ID 5; omitted when empty
	Copyright      string // name ID 0; omitted when empty
	License        string // name ID 13; omitted when empty
	LicenseURL     string // name ID 14; omitted when empty
	Vendor         string // OS/2 achVendID, space-padded to 4 bytes
	Revision       uint32 // head.fontRevision as a 16.16 fixed value
	// Filler is stored in an extra table so two fonts with identical
	// metadata can still have different content hashes.
	Filler []byte
	// OTTO selects an OpenType (CFF) font instead of a TrueType font.
	OTTO bool
}

// Build returns the bytes of the requested font. It fails the test if the
// result does not parse with sfnt.Parse.
func Build(t testing.TB, s Spec) []byte {
	t.Helper()
	return build(s)
}

func build(s Spec) []byte {
	var tables [][]byte
	add := func(tag string, data []byte) { tables = append(tables, append([]byte(tag), data...)) }

	add("cmap", cmapBytes())
	add("head", headBytes(s))
	if s.OTTO {
		addOTTOTables(add)
	} else {
		addTTFTables(add)
	}
	add("OS/2", os2Bytes(s))
	add("post", postBytes())
	add("name", nameTable(s))
	if len(s.Filler) > 0 {
		add("FILL", s.Filler)
	}
	return assemble(tables, s.OTTO)
}

// cmapBytes: format 4, a single segment mapping U+FFFF to glyph 1 (out of
// range for a 1-glyph font, so lookups yield "not found").
func cmapBytes() []byte {
	cmap := make([]byte, 4+8+24)
	binary.BigEndian.PutUint16(cmap[0:], 0) // version
	binary.BigEndian.PutUint16(cmap[2:], 1) // numTables
	binary.BigEndian.PutUint16(cmap[4:], 3) // platform: Windows
	binary.BigEndian.PutUint16(cmap[6:], 1) // encoding: BMP
	binary.BigEndian.PutUint32(cmap[8:], 12)
	sub := cmap[12:]
	binary.BigEndian.PutUint16(sub[0:], 4)  // format
	binary.BigEndian.PutUint16(sub[2:], 24) // length
	binary.BigEndian.PutUint16(sub[4:], 0)  // language
	binary.BigEndian.PutUint16(sub[6:], 2)  // segCountX2
	binary.BigEndian.PutUint16(sub[8:], 2)  // searchRange
	binary.BigEndian.PutUint16(sub[10:], 0) // entrySelector
	binary.BigEndian.PutUint16(sub[12:], 0) // rangeShift
	binary.BigEndian.PutUint16(sub[14:], 0xFFFF)
	binary.BigEndian.PutUint16(sub[18:], 0xFFFF)
	binary.BigEndian.PutUint16(sub[20:], 1) // idDelta -> glyph 1
	return cmap
}

// headBytes: sfnt version, the spec's fontRevision, and fixed sane metrics.
func headBytes(s Spec) []byte {
	head := make([]byte, 54)
	binary.BigEndian.PutUint32(head[0:], 0x00010000)
	binary.BigEndian.PutUint32(head[4:], s.Revision) // fontRevision 16.16
	binary.BigEndian.PutUint32(head[12:], 0x5F0F3CF5)
	binary.BigEndian.PutUint16(head[18:], 1000) // unitsPerEm
	binary.BigEndian.PutUint16(head[46:], 8)    // lowestRecPPEM
	binary.BigEndian.PutUint16(head[48:], 2)    // fontDirectionHint
	// indexToLocFormat = 0 (short), glyphDataFormat = 0
	return head
}

// addOTTOTables adds the tables an OpenType (CFF) font needs beyond cmap/head.
func addOTTOTables(add func(tag string, data []byte)) {
	maxp := make([]byte, 6)
	binary.BigEndian.PutUint32(maxp[0:], 0x00005000)
	binary.BigEndian.PutUint16(maxp[4:], 5) // matches the CFF table
	add("maxp", maxp)

	add("hhea", hheaBytes(5))

	hmtx := make([]byte, 5*4)
	for i := range 5 {
		binary.BigEndian.PutUint16(hmtx[i*4:], 500)
	}
	add("hmtx", hmtx)

	cff, err := base64.StdEncoding.DecodeString(cffTestTable)
	if err != nil {
		panic("testfont: bad embedded CFF: " + err.Error())
	}
	add("CFF ", cff)
}

// addTTFTables adds the tables a TrueType font needs beyond cmap/head.
func addTTFTables(add func(tag string, data []byte)) {
	maxp := make([]byte, 32)
	binary.BigEndian.PutUint32(maxp[0:], 0x00010000)
	binary.BigEndian.PutUint16(maxp[4:], 1) // numGlyphs
	add("maxp", maxp)

	add("hhea", hheaBytes(1))

	hmtx := make([]byte, 4)
	binary.BigEndian.PutUint16(hmtx[0:], 500)
	add("hmtx", hmtx)

	loca := make([]byte, 4) // short format, [0, 0]: glyph 0 is empty
	add("loca", loca)
	add("glyf", nil)
}

// os2Bytes carries the spec's achVendID plus fixed sane metrics.
func os2Bytes(s Spec) []byte {
	os2 := make([]byte, 96)
	binary.BigEndian.PutUint16(os2[0:], 2)       // version 2
	binary.BigEndian.PutUint16(os2[2:], 500)     // xAvgCharWidth
	binary.BigEndian.PutUint16(os2[4:], 400)     // usWeightClass
	binary.BigEndian.PutUint16(os2[6:], 5)       // usWidthClass
	binary.BigEndian.PutUint16(os2[8:], 0)       // fsType
	binary.BigEndian.PutUint16(os2[30:], 8)      // sFamilyClass
	copy(os2[58:], pad4(s.Vendor))               // achVendID
	binary.BigEndian.PutUint16(os2[62:], 64)     // fsSelection
	binary.BigEndian.PutUint16(os2[64:], 32)     // usFirstCharIndex
	binary.BigEndian.PutUint16(os2[66:], 0xFF)   // usLastCharIndex
	binary.BigEndian.PutUint16(os2[68:], 800)    // sTypoAscender
	binary.BigEndian.PutUint16(os2[70:], 0xFFE2) // sTypoDescender (-30)
	binary.BigEndian.PutUint16(os2[72:], 200)    // sTypoLineGap
	binary.BigEndian.PutUint16(os2[74:], 1000)   // usWinAscent
	binary.BigEndian.PutUint16(os2[76:], 200)    // usWinDescent
	binary.BigEndian.PutUint16(os2[86:], 500)    // sxHeight
	binary.BigEndian.PutUint16(os2[88:], 700)    // sCapHeight
	return os2
}

// postBytes is the minimal v3.0 post table (no glyph names needed for 1 glyph).
func postBytes() []byte {
	post := make([]byte, 32)
	binary.BigEndian.PutUint32(post[0:], 0x00030000)
	return post
}

// assemble sorts the tagged tables by tag, writes the sfnt header and table
// directory (with the numTables search math), then the 4-byte-aligned bodies.
func assemble(tables [][]byte, otto bool) []byte {
	sort.SliceStable(tables, func(i, j int) bool {
		return string(tables[i][:4]) < string(tables[j][:4])
	})
	n := len(tables)
	out := make([]byte, 12+16*n)
	if otto {
		binary.BigEndian.PutUint32(out[0:], 0x4F54544F)
	} else {
		binary.BigEndian.PutUint32(out[0:], 0x00010000)
	}
	binary.BigEndian.PutUint16(out[4:], uint16(n))
	searchRange := uint16(1)
	for searchRange*2 <= uint16(n) {
		searchRange *= 2
	}
	searchRange *= 16
	entrySelector := uint16(0)
	for 1<<(entrySelector+1) <= n {
		entrySelector++
	}
	binary.BigEndian.PutUint16(out[6:], searchRange)
	binary.BigEndian.PutUint16(out[8:], entrySelector)
	binary.BigEndian.PutUint16(out[10:], uint16(16*n)-searchRange)
	offset := 12 + 16*n
	for i, t := range tables {
		rec := out[12+16*i:]
		copy(rec[0:], t[:4])
		binary.BigEndian.PutUint32(rec[8:], uint32(offset))
		binary.BigEndian.PutUint32(rec[12:], uint32(len(t)-4))
		offset += len(t) - 4
		if pad := offset % 4; pad != 0 {
			offset += 4 - pad
		}
	}
	for _, t := range tables {
		out = append(out, t[4:]...)
		for len(out)%4 != 0 {
			out = append(out, 0)
		}
	}
	return out
}

func hheaBytes(numHMetrics uint16) []byte {
	h := make([]byte, 36)
	binary.BigEndian.PutUint32(h[0:], 0x00010000)
	binary.BigEndian.PutUint16(h[4:], 800)           // ascent
	binary.BigEndian.PutUint16(h[6:], 0xFF00+0x00C8) // descent (-200)
	binary.BigEndian.PutUint16(h[10:], 1000)         // advanceWidthMax
	binary.BigEndian.PutUint16(h[18:], 1)            // caretSlopeRise
	binary.BigEndian.PutUint16(h[34:], numHMetrics)
	return h
}

func pad4(s string) []byte {
	b := []byte(s)
	for len(b) < 4 {
		b = append(b, ' ')
	}
	return b[:4]
}

func nameTable(s Spec) []byte {
	type rec struct {
		id    uint16
		value string
	}
	var recs []rec
	add := func(id uint16, v string) {
		if v != "" {
			recs = append(recs, rec{id, v})
		}
	}
	add(0, s.Copyright)
	add(1, s.FamilyID1)
	add(2, s.Subfamily)
	add(4, s.FullName)
	add(5, s.Version)
	add(6, s.PostScriptName)
	add(13, s.License)
	add(14, s.LicenseURL)
	add(16, s.Family)
	sort.Slice(recs, func(i, j int) bool { return recs[i].id < recs[j].id })

	count := len(recs)
	stringOffset := 6 + 12*count
	out := make([]byte, 0, stringOffset+64)
	var hdr [6]byte
	binary.BigEndian.PutUint16(hdr[0:], 0) // format
	binary.BigEndian.PutUint16(hdr[2:], uint16(count))
	binary.BigEndian.PutUint16(hdr[4:], uint16(stringOffset))
	out = append(out, hdr[:]...)
	var strings []byte
	for _, r := range recs {
		var entry [12]byte
		binary.BigEndian.PutUint16(entry[0:], 3) // Windows
		binary.BigEndian.PutUint16(entry[2:], 1) // Unicode BMP
		binary.BigEndian.PutUint16(entry[4:], 0x0409)
		binary.BigEndian.PutUint16(entry[6:], r.id)
		binary.BigEndian.PutUint16(entry[8:], uint16(len(r.value)*2))
		binary.BigEndian.PutUint16(entry[10:], uint16(len(strings)))
		out = append(out, entry[:]...)
		for _, c := range r.value {
			strings = append(strings, byte(c>>8), byte(c))
		}
	}
	out = append(out, strings...)
	return out
}

// cffTestTable is the CFF table of golang.org/x/image's BSD-licensed
// CFFTest.otf (5 glyphs); see https://golang.org/LICENSE.
const cffTestTable = "AQAEBAABAQEIQ0ZGVGVzdAABAgABADH4DwD4HAH4HQL4HgP4GAQe4Rol/wwC74v5/Pm0BRwBAw8c" +
	"AAAQHAEMERwAHRwB+hIABAIAAQAIAKAApwCudW5pNEUyRENvcHlyaWdodCAyMDE2IFRoZSBHbyBB" +
	"dXRob3JzLiBBbGwgcmlnaHRzIHJlc2VydmVkLgpVc2Ugb2YgdGhpcyBmb250IGlzIGdvdmVybmVk" +
	"IGJ5IGEgQlNELXN0eWxlIGxpY2Vuc2UgdGhhdCBjYW4gYmUgZm91bmQgYXQgaHR0cHM6Ly9nb2xh" +
	"bmcub3JnL0xJQ0VOU0UuQ0ZGVGVzdENGRlRlc3QAAAAAEQASADIBhwAFAQEkaHq+5vdji734Rb0B" +
	"vb33wL0DvRb4JPip/CQGvfx3FfhF98D8RQcOi+/47O8B79v3hNsD98D5UBXbs/sMO/sqbfuOMTtj" +
	"9wzb9yqp947lH+8EJyf7cPtI+0jv+3Dv7+/3cPdI90gn93AnHw72oHYB7/dcA+8W91z5tPtcBg75" +
	"V/kl94EV/AT3Kvd693gF96X8UhXW6cb3g/sb9zf7JvdD+52C+yz7Mvsx+ziy+6r3GPse9xP7GPek" +
	"xNzWCNsxydsFDveJ4QP3IficFYf7OPcAt+H8CqP4GPcAn4H3TvsEb4H3OjOFg/tSBQ747BT3uRV3" +
	"n/muiwaLDArvCu8MDPdcC9uR9wYMDQ=="
