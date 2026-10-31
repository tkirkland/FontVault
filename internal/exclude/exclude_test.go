package exclude

import (
	"errors"
	"strings"
	"testing"

	"fontvault/internal/fonts"
)

type stubClassifier struct {
	results map[string]bool
	errs    map[string]error
	name    string
}

func (s *stubClassifier) Name() string {
	if s.name == "" {
		return "stub"
	}
	return s.name
}

func (s *stubClassifier) IsOSDefault(path string) (bool, error) {
	if s.errs != nil {
		if err, ok := s.errs[path]; ok {
			return false, err
		}
	}
	return s.results[path], nil
}

func testFont(ps, family, sha string) *fonts.Font {
	return &fonts.Font{
		Path:           "/fonts/" + ps + ".ttf",
		RealPath:       "/fonts/" + ps + ".ttf",
		Ext:            "ttf",
		SHA256:         sha,
		PostScriptName: ps,
		Family:         family,
	}
}

func TestExclusionFileParsing(t *testing.T) {
	input := `# comment line
ABC123abc123abc123abc123abc123abc123abc123abc123abc123abc123ab12

  My Family
`
	xf, err := ParseExclusionFile(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if !xf.Hashes["abc123abc123abc123abc123abc123abc123abc123abc123abc123abc123ab12"] {
		t.Error("hash not parsed (case-insensitive)")
	}
	if !xf.Families["my family"] {
		t.Error("family not parsed (case-insensitive, trimmed)")
	}
	// Non-hex entries are family names even if they look hash-ish.
	xf2, err := ParseExclusionFile(strings.NewReader("not a hash at all"))
	if err != nil {
		t.Fatal(err)
	}
	if !xf2.Families["not a hash at all"] || len(xf2.Hashes) != 0 {
		t.Errorf("family fallback failed: %+v", xf2)
	}
}

func TestApplySystemSource(t *testing.T) {
	fontList := []*fonts.Font{
		testFont("OSFont", "OS Fam", "hash-os"),
		testFont("UserFont", "User Fam", "hash-user"),
		testFont("Unknown", "Unknown Fam", "hash-unknown"),
		testFont("Exc", "Exc Fam", "hash-exc"),
	}
	cls := &stubClassifier{results: map[string]bool{
		"/fonts/OSFont.ttf":   true,
		"/fonts/UserFont.ttf": false,
		"/fonts/Unknown.ttf":  false,
	}, errs: map[string]error{
		"/fonts/Unknown.ttf": errors.New("permission denied"),
	}}
	xf, _ := ParseExclusionFile(strings.NewReader("exc fam\n"))
	dec := Apply(fontList, true, nil, false, cls, xf)

	byPS := map[string]Decision{}
	for _, d := range dec {
		byPS[d.Font.PostScriptName] = d
	}
	if !byPS["OSFont"].Excluded || byPS["OSFont"].Reason != "OS default (stub)" {
		t.Errorf("OSFont = %+v", byPS["OSFont"])
	}
	if byPS["UserFont"].Excluded {
		t.Error("UserFont should be included")
	}
	if !byPS["Unknown"].Excluded || !strings.Contains(byPS["Unknown"].Reason, "cannot determine OS-default status") {
		t.Errorf("Unknown = %+v", byPS["Unknown"])
	}
	if !byPS["Exc"].Excluded || byPS["Exc"].Reason != "excluded by exclusions file" {
		t.Errorf("Exc = %+v", byPS["Exc"])
	}
	if !byPS["OSFont"].Protected {
		t.Error("OSFont should be protected")
	}
}

func TestApplyUserSource(t *testing.T) {
	sysHashes := map[string]bool{"hash-os": true}
	fontList := []*fonts.Font{
		testFont("Match", "A", "hash-os"),
		testFont("Other", "B", "hash-other"),
	}
	dec := Apply(fontList, false, sysHashes, false, nil, nil)
	if !dec[0].Excluded || dec[0].Reason != "matches an OS-default system font" {
		t.Errorf("Match = %+v", dec[0])
	}
	if dec[1].Excluded {
		t.Error("Other should be included")
	}
	// System scan failed: fail closed, exclude all user fonts.
	dec = Apply(fontList, false, nil, true, nil, nil)
	for _, d := range dec {
		if !d.Excluded || d.Reason != "system font scan unavailable" {
			t.Errorf("fail closed: %+v", d)
		}
	}
}

func TestApplyNoClassifierFailsClosed(t *testing.T) {
	dec := Apply([]*fonts.Font{testFont("A", "Fam", "h")}, true, nil, false, nil, nil)
	if !dec[0].Excluded {
		t.Error("system font without a classifier must be excluded")
	}
}

func TestProtectionStatus(t *testing.T) {
	cls := &stubClassifier{results: map[string]bool{"/fonts/OS.ttf": true, "/fonts/User.ttf": false},
		errs: map[string]error{"/fonts/Broken.ttf": errors.New("denied")}}

	if p, _ := ProtectionStatus(testFont("OS", "F", ""), true, cls); !p {
		t.Error("OS font should be protected")
	}
	if p, _ := ProtectionStatus(testFont("User", "F", ""), true, cls); p {
		t.Error("non-OS system font should not be protected")
	}
	if p, _ := ProtectionStatus(testFont("Broken", "F", ""), true, cls); !p {
		t.Error("undeterminable status must be protected (fail closed)")
	}
	if p, _ := ProtectionStatus(testFont("User", "F", ""), false, cls); p {
		t.Error("user-source fonts are not protected")
	}
	if p, _ := ProtectionStatus(testFont("User", "F", ""), true, nil); !p {
		t.Error("nil classifier must be protected (fail closed)")
	}
}
