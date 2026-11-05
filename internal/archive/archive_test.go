package archive

import (
	"bytes"
	"strings"
	"testing"
)

func sample() map[string][]byte {
	return map[string][]byte{
		"zulu-11111111.ttf":  bytes.Repeat([]byte("glyph data z "), 400),
		"alpha-22222222.otf": bytes.Repeat([]byte("glyph data a "), 300),
		"mike-33333333.ttf":  []byte("tiny"),
	}
}

func TestRoundTrip(t *testing.T) {
	in := sample()
	data, err := Encode(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(in) {
		t.Fatalf("decoded %d members, want %d", len(out), len(in))
	}
	for name, want := range in {
		if !bytes.Equal(out[name], want) {
			t.Errorf("member %s differs after round trip", name)
		}
	}
}

func TestEncodeIsDeterministic(t *testing.T) {
	a, err := Encode(sample())
	if err != nil {
		t.Fatal(err)
	}
	b, err := Encode(sample())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Error("two encodes of the same members must be byte-identical")
	}
}

func TestEncodeCompresses(t *testing.T) {
	in := sample()
	raw := 0
	for _, b := range in {
		raw += len(b)
	}
	data, err := Encode(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) >= raw/4 {
		t.Errorf("encoded %d bytes for %d raw bytes of repetitive data; compression is not applied", len(data), raw)
	}
}

func TestMembersAreSorted(t *testing.T) {
	data, err := Encode(sample())
	if err != nil {
		t.Fatal(err)
	}
	names, err := Members(data)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"alpha-22222222.otf", "mike-33333333.ttf", "zulu-11111111.ttf"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("members = %v, want %v", names, want)
	}
}

func TestExtract(t *testing.T) {
	data, err := Encode(sample())
	if err != nil {
		t.Fatal(err)
	}
	got, err := Extract(data, "mike-33333333.ttf")
	if err != nil || string(got) != "tiny" {
		t.Fatalf("extract = %q, %v", got, err)
	}
	if _, err := Extract(data, "missing.ttf"); err == nil {
		t.Error("extracting a missing member must fail")
	}
}

func TestDecodeRejectsGarbage(t *testing.T) {
	if _, err := Decode([]byte("this is not a Brotli stream")); err == nil {
		t.Error("garbage must not decode")
	}
}

func TestEncodeRejectsEmptyAndBadNames(t *testing.T) {
	if _, err := Encode(map[string][]byte{}); err == nil {
		t.Error("an archive with no members must be refused")
	}
	if _, err := Encode(map[string][]byte{"dir/name.ttf": []byte("x")}); err == nil {
		t.Error("a member name with a path separator must be refused")
	}
}
