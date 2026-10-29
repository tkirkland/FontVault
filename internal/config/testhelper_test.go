package config

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"testing"
)

// encryptJSON encrypts arbitrary plaintext with the production key and
// format, for testing corrupt-config cases.
func encryptJSON(plain []byte) ([]byte, error) {
	block, err := aes.NewCipher(key())
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := append([]byte(fileMagic), fileVersion)
	out = append(out, nonce...)
	return gcm.Seal(out, nonce, plain, nil), nil
}

var _ = testing.TB(nil)
