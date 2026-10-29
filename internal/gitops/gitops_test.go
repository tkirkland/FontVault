package gitops

import (
	"strings"
	"testing"
)

func TestIsLFSPointer(t *testing.T) {
	good := "version https://git-lfs.github.com/spec/v1\n" +
		"oid sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n" +
		"size 123\n"
	if !IsLFSPointer([]byte(good), strings.Repeat("ab", 32)) && !IsLFSPointer([]byte(good), "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef") {
		t.Error("valid pointer with matching oid not detected")
	}
	if IsLFSPointer([]byte(good), strings.Repeat("ff", 32)) {
		t.Error("oid mismatch must not be a valid pointer")
	}
	if IsLFSPointer([]byte("binary font data"), "") {
		t.Error("raw blob detected as pointer")
	}
	if IsLFSPointer([]byte("version https://git-lfs.github.com/spec/v1\noid sha256:short\n"), "short") {
		t.Error("malformed pointer accepted")
	}
}

func TestRedaction(t *testing.T) {
	r := &Runner{Token: "supersecret"}
	out := r.redact("auth failed for Bearer supersecret while pushing")
	if strings.Contains(out, "supersecret") {
		t.Errorf("token leaked: %q", out)
	}
	if !strings.Contains(out, "<redacted>") {
		t.Errorf("redaction missing: %q", out)
	}
	// Base64 form must be redacted too.
	creds := "eC1hY2Nlc3MtdG9rZW46c3VwZXJzZWNyZXQ="
	if !strings.Contains(r.redact("header "+creds), "<redacted>") {
		t.Error("base64 form not redacted")
	}
}

func TestCloneSparseEmptyError(t *testing.T) {
	if !CloneSparseEmptyError(errorsNew("warning: You appear to have cloned an empty repository.")) {
		t.Error("empty repository message not detected")
	}
	if CloneSparseEmptyError(errorsNew("connection refused")) {
		t.Error("unrelated error misdetected as empty repo")
	}
}

type strErr struct{ s string }

func (e *strErr) Error() string { return e.s }

func errorsNew(s string) error { return &strErr{s} }
