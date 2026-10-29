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
	want := "fonts/cool_family_name/cool_font_pro_plus_-" + f1.KeyHash[:8] + ".ttf"
	if p1 != want {
		t.Errorf("repo path = %q, want %q", p1, want)
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

func TestRepoFileNameCap(t *testing.T) {
	key := KeyOf(strings.Repeat("p", 200), "")
	f := &Font{
		PostScriptName: strings.Repeat("p", 200),
		Family:         strings.Repeat("f", 200),
		Ext:            "otf",
		Key:            key,
		KeyHash:        HashKey(key),
	}
	base := f.RepoFileName()
	if len(base) != 100 {
		t.Errorf("filename component length = %d, want exactly 100", len(base))
	}
	if !strings.HasSuffix(base, "-"+f.KeyHash[:8]+".otf") {
		t.Errorf("filename does not keep keyhash8+ext: %q", base)
	}
	if !strings.HasPrefix(base, strings.Repeat("p", 87)) {
		t.Errorf("filename should start with 87 psname bytes: %q", base)
	}
}

func TestManifestPathForKey(t *testing.T) {
	keyHash := strings.Repeat("ab", 32)
	if got := ManifestPathForKey(keyHash); got != "manifest/ab/"+keyHash+".json" {
		t.Errorf("manifest path = %q", got)
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
