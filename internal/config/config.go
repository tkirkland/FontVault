// Package config implements fontvault's encrypted configuration file.
//
// The configuration is stored next to the executable as fontvault.cfg. The
// file is JSON encrypted with AES-256-GCM; the key is derived with
// HKDF-SHA256 from a static secret compiled into the binary. This is
// obfuscation only (anyone holding the binary can decrypt the file), not
// protection against a determined attacker.
package config

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"fontvault/internal/fsutil"
)

// staticSecret is compiled into the binary. It is intentionally not a
// secret: encryption of the cfg protects against casual disclosure only.
var staticSecret = []byte("fontvault-static-cfg-key-v1-0123456789abcdef")

const (
	fileMagic       = "FVC1"
	fileVersion     = 1
	hkdfInfo        = "fontvault cfg AES-256-GCM key v1"
	nonceSize       = 12
	minEncryptedLen = len(fileMagic) + 1 + nonceSize + aes.BlockSize
	cfgFileMode     = 0o600
)

// File is the persisted configuration content.
type File struct {
	Branch        string `json:"branch"`
	Workdir       string `json:"workdir"`
	Token         string `json:"token"`
	TokenEnv      string `json:"token_env"`
	ExcludeFile   string `json:"exclude_file"`
	FormatVersion int    `json:"format_version"`
}

// Kind classifies a configuration read failure.
type Kind int

const (
	// KindNone means the file is absent; reading it is not an error.
	KindNone Kind = iota
	KindBadMagic
	KindBadVersion
	KindAuthFailure
	KindBadJSON
	KindIO
)

// ReadError describes why a cfg file could not be read.
type ReadError struct {
	Kind   Kind
	Path   string
	Reason string
	Err    error
}

func (e *ReadError) Error() string {
	return fmt.Sprintf("unreadable config %s: %s", e.Path, e.Reason)
}

func (e *ReadError) Unwrap() error { return e.Err }

func key() []byte {
	k, err := hkdf.Key(sha256.New, staticSecret, nil, hkdfInfo, 32)
	if err != nil {
		panic("config: hkdf failed: " + err.Error())
	}
	return k
}

// Encrypt serializes f and encrypts it into the on-disk format:
// magic "FVC1", 1-byte format version, 12-byte nonce, ciphertext+tag.
func Encrypt(f *File) ([]byte, error) {
	f.FormatVersion = fileVersion
	plain, err := json.Marshal(f)
	if err != nil {
		return nil, fmt.Errorf("marshal config: %w", err)
	}
	block, err := aes.NewCipher(key())
	if err != nil {
		return nil, fmt.Errorf("new aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("new gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	out := make([]byte, 0, len(fileMagic)+1+nonceSize+len(plain)+gcm.Overhead())
	out = append(out, fileMagic...)
	out = append(out, fileVersion)
	out = append(out, nonce...)
	out = gcm.Seal(out, nonce, plain, nil)
	return out, nil
}

// Decrypt parses and decrypts the on-disk format, returning a typed error
// naming the file and the failure reason.
func Decrypt(data []byte) (*File, error) {
	if len(data) < minEncryptedLen {
		return nil, &ReadError{Kind: KindBadMagic, Reason: "file too short to be a fontvault config"}
	}
	if !bytes.Equal(data[:4], []byte(fileMagic)) {
		return nil, &ReadError{Kind: KindBadMagic, Reason: "bad magic bytes (not a fontvault config)"}
	}
	if v := data[4]; v != fileVersion {
		return nil, &ReadError{Kind: KindBadVersion, Reason: fmt.Sprintf("unknown format version %d", v)}
	}
	block, err := aes.NewCipher(key())
	if err != nil {
		return nil, &ReadError{Kind: KindIO, Reason: "cipher setup", Err: err}
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, &ReadError{Kind: KindIO, Reason: "gcm setup", Err: err}
	}
	if len(data) < len(fileMagic)+1+gcm.NonceSize() {
		return nil, &ReadError{Kind: KindBadMagic, Reason: "truncated nonce"}
	}
	nonce := data[len(fileMagic)+1 : len(fileMagic)+1+gcm.NonceSize()]
	ciphertext := data[len(fileMagic)+1+gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, &ReadError{Kind: KindAuthFailure, Reason: "decryption failed (tampered or corrupted file)", Err: err}
	}
	var f File
	if err := json.Unmarshal(plain, &f); err != nil {
		return nil, &ReadError{Kind: KindBadJSON, Reason: "invalid JSON after decryption", Err: err}
	}
	if f.FormatVersion != fileVersion {
		return nil, &ReadError{Kind: KindBadVersion, Reason: fmt.Sprintf("unknown format version %d", f.FormatVersion)}
	}
	return &f, nil
}

// Load reads and decrypts the cfg at path. A missing file returns
// (nil, nil).
func Load(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, &ReadError{Kind: KindIO, Path: path, Reason: "read failed", Err: err}
	}
	f, err := Decrypt(data)
	if err != nil {
		if re, ok := err.(*ReadError); ok && re.Path == "" {
			re.Path = path
		}
		return nil, err
	}
	return f, nil
}

// Save encrypts and atomically writes the cfg to path. On Linux the file is
// created with mode 0600.
func Save(path string, f *File) error {
	data, err := Encrypt(f)
	if err != nil {
		return err
	}
	if err := fsutil.AtomicWrite(path, data, cfgFileMode); err != nil {
		return fmt.Errorf("write config %s: %w", path, err)
	}
	return nil
}

// DefaultPath returns the cfg path in the directory containing the
// executable (symlinks resolved).
func DefaultPath(exePath string) string {
	return filepath.Join(exePath, "fontvault.cfg")
}
