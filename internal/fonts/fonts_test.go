package fonts

import (
	"encoding/binary"
	"strings"
	"testing"

	"fontvault/internal/testfont"
)

func build(t *testing.T, s testfont.Spec) []byte {
	t.Helper()
	b := testfont.Build(t, s)
	if _, err := Parse(b); err != nil {
		t.Fatalf("fixture does not parse: %v", err)
	}
	return b
}

func TestKeyExtraction(t *testing.T) {
	tests := []struct {
		name    string
		spec    testfont.Spec
		wantKey string
		wantErr bool
	}{
		{
			name: "postscript name with vendor",
			spec: testfont.Spec{
				PostScriptName: "MyFont-Regular",
				FullName:       "My Font Regular",
				Family:         "My Font",
				Vendor:         "ABCD",
			},
			wantKey: "MyFont-Regular|ABCD",
		},
		{
			name: "id6 fallback to id4",
			spec: testfont.Spec{
				FullName: "Fallback Name",
				Family:   "Fallback",
			},
			wantKey: "Fallback Name|",
		},
		{
			name: "vendor trimmed of spaces",
			spec: testfont.Spec{
				PostScriptName: "Trimmed-Regular",
				FullName:       "Trimmed Regular",
				Vendor:         "AB",
			},
			// Vendor is padded to 4 bytes and then trimmed on read.
			wantKey: "Trimmed-Regular|AB",
		},
		{
			name: "missing both names is invalid",
			spec: testfont.Spec{
				Family: "NoNames",
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.wantErr {
				b := testfont.Build(t, tt.spec)
				if _, err := Parse(b); err == nil {
					t.Fatal("expected parse error, got nil")
				}
				return
			}
			b := build(t, tt.spec)
			m, err := Parse(b)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got := KeyOf(m.PostScriptName, m.VendorID); got != tt.wantKey {
				t.Errorf("key = %q, want %q", got, tt.wantKey)
			}
		})
	}
}

func TestTTFOTFValidation(t *testing.T) {
	t.Run("ttf valid", func(t *testing.T) {
		b := build(t, testfont.Spec{PostScriptName: "A-Regular", Family: "A", FullName: "A Regular"})
		if binary.BigEndian.Uint32(b[0:4]) != 0x00010000 {
			t.Fatalf("expected TTF sfnt version")
		}
	})
	t.Run("otf valid", func(t *testing.T) {
		b := build(t, testfont.Spec{PostScriptName: "B-Regular", Family: "B", FullName: "B Regular", OTTO: true})
		if binary.BigEndian.Uint32(b[0:4]) != 0x4F54544F {
			t.Fatalf("expected OTTO sfnt version")
		}
	})
	t.Run("garbage invalid", func(t *testing.T) {
		if _, err := Parse([]byte("not a font at all")); err == nil {
			t.Fatal("expected error for garbage")
		}
	})
	t.Run("wrong magic invalid", func(t *testing.T) {
		b := build(t, testfont.Spec{PostScriptName: "C-Regular", Family: "C"})
		binary.BigEndian.PutUint32(b[0:4], 0x12345678)
		if _, err := Parse(b); err == nil {
			t.Fatal("expected error for bad sfnt version")
		}
	})
	t.Run("corrupt table invalid", func(t *testing.T) {
		b := build(t, testfont.Spec{PostScriptName: "D-Regular", Family: "D"})
		b = b[:len(b)-40] // truncate table data
		if _, err := Parse(b); err == nil {
			t.Fatal("expected error for truncated font")
		}
	})
}

func TestParseAcceptsClampedKernOverflow(t *testing.T) {
	// The shape corbelb.ttf (and Microsoft's Cambria/Calibri) carry: one
	// format-0 Kern subtable with 14457 pairs whose true length (86750)
	// overflows the uint16 subtableLength field, which the font stores
	// clamped at 0xFFFF. The font is real and loads fine; only strict
	// kerning parsers reject it, and fontvault never reads kerning.
	b := build(t, testfont.Spec{PostScriptName: "KernOdd-Regular", FullName: "Kern Odd Regular"})
	m, err := Parse(withExtraTables(t, b, kernClampedOverflow()))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if m.PostScriptName != "KernOdd-Regular" {
		t.Errorf("PostScript name = %q, want %q", m.PostScriptName, "KernOdd-Regular")
	}
}

func TestParseAcceptsZeroCoverageFormatGPOS(t *testing.T) {
	// The shape the nerd-font patcher leaves in UbuntuSansNerdFont files:
	// a GPOS Extension lookup pointing at a PairPos format-2 subtable whose
	// Coverage table starts with format 0. Real shapers tolerate it; fontvault
	// never reads GPOS at all.
	b := build(t, testfont.Spec{PostScriptName: "GposOdd-Regular", FullName: "Gpos Odd Regular"})
	m, err := Parse(withExtraTables(t, b, gposZeroCoverageFormat()))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if m.PostScriptName != "GposOdd-Regular" {
		t.Errorf("PostScript name = %q, want %q", m.PostScriptName, "GposOdd-Regular")
	}
}

// withExtraTables rebuilds a built fixture font with additional raw tables
// (tag+payload each): it re-reads the table directory, then hands all tables
// to testfont.Assemble, which sorts by tag and rewrites the header, directory
// and 4-byte aligned bodies.
func withExtraTables(t *testing.T, b []byte, extras ...[]byte) []byte {
	t.Helper()
	if len(b) < 12 {
		t.Fatal("fixture font has no table directory")
	}
	num := int(binary.BigEndian.Uint16(b[4:6]))
	var tables [][]byte
	for i := range num {
		if 12+16*(i+1) > len(b) {
			t.Fatal("fixture table directory out of bounds")
		}
		rec := b[12+16*i:]
		off := int(binary.BigEndian.Uint32(rec[8:12]))
		length := int(binary.BigEndian.Uint32(rec[12:16]))
		if off+length > len(b) {
			t.Fatal("fixture table body out of bounds")
		}
		entry := append([]byte{}, rec[0:4]...)
		entry = append(entry, b[off:off+length]...)
		tables = append(tables, entry)
	}
	tables = append(tables, extras...)
	return testfont.Assemble(tables, binary.BigEndian.Uint32(b[0:4]))
}

// kernClampedOverflow is a Kern table in corbelb.ttf's shape: a single
// format-0 subtable whose pairs array outgrows the uint16 subtableLength,
// which the font stores clamped at 0xFFFF.
func kernClampedOverflow() []byte {
	const pairs = 14457
	table := []byte("kern")
	put16 := func(v uint16) {
		table = append(table, byte(v>>8), byte(v))
	}
	put16(0)                    // kern version
	put16(1)                    // numTables
	put16(0)                    // subtable version
	put16(0xFFFF)               // subtableLength, clamped (true value 86750 overflows)
	table = append(table, 0, 1) // format 0, horizontal-only coverage bits
	put16(pairs)
	table = append(table, make([]byte, 6*pairs)...)
	return table
}

// gposZeroCoverageFormat is a GPOS table in UbuntuSansNerdFont's shape:
// latn/DefaultLangSys -> Kern feature -> one Extension lookup whose single
// PairPos format-2 subtable has a Coverage table with format 0.
func gposZeroCoverageFormat() []byte {
	payload := make([]byte, 92)
	p16 := func(off int, v uint16) {
		binary.BigEndian.PutUint16(payload[off:], v)
	}
	p16(0, 1)
	p16(2, 0)  // GPOS version 1.0
	p16(4, 16) // scriptListOffset
	p16(6, 40) // featureListOffset
	p16(8, 56) // lookupListOffset
	p16(16, 1) // scriptCount
	copy(payload[18:], "latn")
	p16(22, 8)      // scriptTableOffset
	p16(24, 4)      // defaultLangSysOffset
	p16(26, 0)      // langSysCount
	p16(28, 0)      // LangSys lookupOrder
	p16(30, 0xFFFF) // no required feature
	p16(32, 1)      // featureIndexCount
	p16(34, 0)      // -> feature index 0
	p16(40, 1)      // featureCount
	copy(payload[42:], "kern")
	p16(46, 8)                                   // featureOffset
	p16(48, 0)                                   // featureFlags
	p16(50, 1)                                   // feature's lookup count
	p16(52, 0)                                   // -> lookup index 0
	p16(56, 1)                                   // lookupCount
	p16(58, 6)                                   // lookupTableOffset
	p16(62, 9)                                   // lookupType 9 (Extension)
	p16(64, 0)                                   // lookupFlag
	p16(66, 1)                                   // subTableCount
	p16(68, 8)                                   // subtableOffset
	p16(70, 1)                                   // Extension format
	p16(72, 2)                                   // inner lookupType 2 (PairPos)
	binary.BigEndian.PutUint32(payload[74:], 12) // inner offset
	p16(82, 2)                                   // PairPos format 2
	p16(84, 8)                                   // coverageOffset
	p16(90, 0)                                   // coverage format 0 (the shape the patcher emits)
	return append([]byte("GPOS"), payload...)
}

func TestFamilyFallback(t *testing.T) {
	b := build(t, testfont.Spec{
		PostScriptName: "Fam-Regular",
		FullName:       "Fam Regular",
		FamilyID1:      "Family One",
	})
	m, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if m.Family != "Family One" {
		t.Errorf("family = %q, want ID1 fallback %q", m.Family, "Family One")
	}
	if m.Subfamily != "" {
		t.Errorf("subfamily = %q, want empty", m.Subfamily)
	}
}

func TestMetadataExtraction(t *testing.T) {
	b := build(t, testfont.Spec{
		PostScriptName: "Meta-Regular",
		FullName:       "Meta Regular",
		Family:         "Meta Fam",
		Subfamily:      "Regular",
		Version:        "Version 2.5",
		Copyright:      "(c) someone",
		License:        "OFL",
		LicenseURL:     "https://example.invalid/license",
		Vendor:         "XYZZ",
		Revision:       0x00010005, // 1.00005
	})
	m, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if m.FullName != "Meta Regular" || m.Family != "Meta Fam" || m.Subfamily != "Regular" {
		t.Errorf("names wrong: %+v", m)
	}
	if m.VersionString != "Version 2.5" {
		t.Errorf("version = %q", m.VersionString)
	}
	if m.Copyright != "(c) someone" || m.LicenseDesc != "OFL" || m.LicenseURL != "https://example.invalid/license" {
		t.Errorf("license fields wrong: %+v", m)
	}
	if m.FontRevision != "1.00005" {
		t.Errorf("font revision = %q, want 1.00005", m.FontRevision)
	}
	if m.VendorID != "XYZZ" {
		t.Errorf("vendor = %q", m.VendorID)
	}
}

func TestSanitizeAndPathStability(t *testing.T) {
	key := KeyOf("Cool Font Pro/Plus!", "ABCD")
	f1 := &Font{
		PostScriptName: "Cool Font Pro/Plus!",
		VendorID:       "ABCD",
		Family:         "Cool Family Name",
		Ext:            "ttf",
		Key:            key,
		KeyHash:        HashKey(key),
	}
	// Same key, different content: paths must be identical.
	f2 := *f1
	f2.SHA256 = strings.Repeat("ab", 32)
	p1, p2 := f1.RepoPath(), f2.RepoPath()
	if p1 != p2 {
		t.Errorf("repo path changed when only content changed: %q vs %q", p1, p2)
	}
	fam := strings.ToLower(SanitizeComponent(f1.Family, 100))
	want := "fonts/" + fam + "/" + archiveName(f1.Family)
	if p1 != want {
		t.Errorf("repo path = %q, want %q", p1, want)
	}
	// the member name inside that archive keeps the per-font key hash, so
	// two fonts whose names sanitize alike stay distinguishable
	wantMember := "cool_font_pro_plus_" + "-" + f1.KeyHash[:8] + ".ttf"
	if got := f1.MemberName(); got != wantMember {
		t.Errorf("member name = %q, want %q", got, wantMember)
	}
	if !strings.HasSuffix(wantMember, "-"+f1.KeyHash[:8]+".ttf") {
		t.Error("member name must keep keyhash8 + ext")
	}
}

func TestSanitizeComponentCap(t *testing.T) {
	long := strings.Repeat("x", 300)
	got := SanitizeComponent(long, 100)
	if len(got) != 100 {
		t.Errorf("cap failed: len %d", len(got))
	}
	if SanitizeComponent("", 100) != "_" {
		t.Error("empty component should become _")
	}
	if SanitizeComponent("A b/c*d", 100) != "A_b_c_d" {
		t.Errorf("sanitize = %q", SanitizeComponent("A b/c*d", 100))
	}
}

func TestMemberNameCap(t *testing.T) {
	key := KeyOf(strings.Repeat("p", 200), "")
	f := &Font{
		PostScriptName: strings.Repeat("p", 200),
		Family:         strings.Repeat("f", 200),
		Ext:            "otf",
		Key:            key,
		KeyHash:        HashKey(key),
	}
	// the member name keeps the loose filename shape: capped at 100, with
	// keyhash8 + ext intact after the cap cut the name down
	base := f.MemberName()
	if len(base) != 100 {
		t.Errorf("member name length = %d, want exactly 100", len(base))
	}
	if !strings.HasSuffix(base, "-"+f.KeyHash[:8]+".otf") {
		t.Errorf("member name does not keep keyhash8+ext: %q", base)
	}
	if !strings.HasPrefix(base, strings.Repeat("p", 87)) {
		t.Errorf("member name should start with 87 psname bytes: %q", base)
	}
	// the family component is capped too, because the whole family shares
	// one archive whose name is derived from it: 88 family bytes + "-"
	// + familyhash8 + ".7z" = exactly 100
	archive := f.RepoFileName()
	if len(archive) != 100 {
		t.Errorf("archive name length = %d, want exactly 100", len(archive))
	}
	if !strings.HasSuffix(archive, "-"+HashKey(f.Family)[:8]+".7z") {
		t.Errorf("archive name = %q, want it to keep familyhash8 + .7z", archive)
	}
	// the manifest name follows the same 100-character rule: 86 family
	// bytes + "-" + familyhash8 + ".json" = exactly 100
	manifest := strings.TrimPrefix(f.ManifestPath(), "manifest/")
	if len(manifest) != 100 {
		t.Errorf("manifest name length = %d, want exactly 100", len(manifest))
	}
	if !strings.HasSuffix(manifest, "-"+HashKey(f.Family)[:8]+".json") {
		t.Errorf("manifest name = %q, want it to keep familyhash8 + .json", manifest)
	}
}

func TestFamilyManifestPath(t *testing.T) {
	// manifest name is <sanitized family>-<familyhash8>.json
	got := FamilyManifestPath("Cool Family")
	if !strings.HasPrefix(got, "manifest/cool_family-") || !strings.HasSuffix(got, ".json") {
		t.Errorf("manifest path = %q, want manifest/cool_family-<hash8>.json", got)
	}
	// the hash suffix separates two family names that sanitize alike, the
	// same role keyhash8 played in the per-font manifest names
	if FamilyManifestPath("Cool/Family") == FamilyManifestPath("Cool Family") {
		t.Error("family names that sanitize alike must still get separate manifests")
	}
	// and it is the family name that is hashed, not the font key: two fonts
	// of one family share a manifest
	if FamilyManifestPath("Cool Family") == FamilyManifestPath("Cool Family Pro") {
		t.Error("different families must get different manifests")
	}
}

func TestMacRomanDecoding(t *testing.T) {
	// 0x8A -> U+00E4 in MacRoman.
	got, ok := decodeNameString(1, 0, []byte{0x8A})
	if !ok || got != "ä" {
		t.Errorf("macroman 0x8A = %q, ok=%v, want ä", got, ok)
	}
	got, ok = decodeNameString(3, 1, []byte{0x00, 0xE4})
	if !ok || got != "ä" {
		t.Errorf("windows bmp = %q, ok=%v, want ä", got, ok)
	}
	got, ok = decodeNameString(0, 3, []byte{0x00, 0xE4})
	if !ok || got != "ä" {
		t.Errorf("unicode = %q, ok=%v, want ä", got, ok)
	}
	if _, ok := decodeNameString(2, 2, []byte{0x41}); ok {
		t.Error("unsupported platform should not decode")
	}
}

func TestShortHash(t *testing.T) {
	if got := ShortHash("abcdef0123456789"); got != "abcdef01" {
		t.Errorf("short hash = %q", got)
	}
}
