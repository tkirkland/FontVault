package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	in := &File{
		Branch:      "main",
		Workdir:     "/tmp/work",
		Token:       "secret-token",
		TokenEnv:    "GITHUB_TOKEN",
		ExcludeFile: "/tmp/excl.txt",
	}
	data, err := Encrypt(in)
	if err != nil {
		t.Fatal(err)
	}
	// The ciphertext must not contain the plaintext token.
	if strings.Contains(string(data), "secret-token") {
		t.Fatal("ciphertext contains plaintext")
	}
	if string(data[:4]) != "FVC1" {
		t.Fatalf("magic = %q", data[:4])
	}
	if data[4] != 1 {
		t.Fatalf("version byte = %d", data[4])
	}
	out, err := Decrypt(data)
	if err != nil {
		t.Fatal(err)
	}
	if out.Branch != in.Branch || out.Token != in.Token || out.FormatVersion != 1 {
		t.Fatalf("round trip mismatch: %+v", out)
	}
}

func TestDecryptTamperDetection(t *testing.T) {
	data, err := Encrypt(&File{Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	tampered := append([]byte{}, data...)
	tampered[len(tampered)-1] ^= 0xFF
	_, err = Decrypt(tampered)
	var re *ReadError
	if !errors.As(err, &re) || re.Kind != KindAuthFailure {
		t.Fatalf("err = %v, want auth failure", err)
	}
}

func TestDecryptBadMagic(t *testing.T) {
	data, err := Encrypt(&File{Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	data[0] = 'X'
	_, err = Decrypt(data)
	var re *ReadError
	if !errors.As(err, &re) || re.Kind != KindBadMagic {
		t.Fatalf("err = %v, want bad magic", err)
	}
}

func TestDecryptUnknownVersion(t *testing.T) {
	data, err := Encrypt(&File{Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	data[4] = 9
	_, err = Decrypt(data)
	var re *ReadError
	if !errors.As(err, &re) || re.Kind != KindBadVersion {
		t.Fatalf("err = %v, want bad version", err)
	}
}

func TestDecryptBadJSON(t *testing.T) {
	// Re-encrypt a JSON value that does not match the File schema: encrypt
	// bytes directly using the same primitives by calling Encrypt with a
	// File is impossible, so craft it through the internal helpers.
	data, err := encryptJSON([]byte(`{"format_version": "not-an-int"}`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = Decrypt(data)
	var re *ReadError
	if !errors.As(err, &re) || re.Kind != KindBadJSON {
		t.Fatalf("err = %v, want bad JSON", err)
	}
}

func TestDecryptUnknownFormatVersionField(t *testing.T) {
	data, err := encryptJSON([]byte(`{"format_version": 2}`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = Decrypt(data)
	var re *ReadError
	if !errors.As(err, &re) || re.Kind != KindBadVersion {
		t.Fatalf("err = %v, want unknown format version", err)
	}
}

func TestSaveLoadAtomicAndMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fontvault.cfg")
	if err := Save(path, &File{Branch: "main", Token: "abc"}); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "linux" {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("cfg mode = %o, want 0600", fi.Mode().Perm())
		}
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Branch != "main" || got.Token != "abc" {
		t.Fatalf("loaded = %+v", got)
	}
	// No temp files may remain after an atomic write.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), "tmp") {
			t.Errorf("leftover temp file %s", e.Name())
		}
	}
	// Overwrite works.
	if err := Save(path, &File{Branch: "dev", TokenEnv: "GH_PAT"}); err != nil {
		t.Fatal(err)
	}
	got, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Branch != "dev" || got.TokenEnv != "GH_PAT" || got.Token != "" {
		t.Fatalf("overwritten = %+v", got)
	}
}

func TestLoadMissingFileIsNil(t *testing.T) {
	f, err := Load(filepath.Join(t.TempDir(), "nope.cfg"))
	if err != nil || f != nil {
		t.Fatalf("missing file: f=%v err=%v", f, err)
	}
}

func TestLoadUnreadableNamesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fontvault.cfg")
	if err := os.WriteFile(path, []byte("not a cfg"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v, want ReadError", err)
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error does not name the file: %v", err)
	}
}
