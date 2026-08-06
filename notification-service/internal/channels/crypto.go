package channels

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"sync"
)

// Channel secrets (webhook URLs, SMTP passwords, PagerDuty/Opsgenie keys) are
// encrypted at rest with AES-256-GCM. The key comes from CHANNEL_ENCRYPTION_KEY
// (32 bytes as hex-64 or base64); if it isn't set we refuse to store secrets
// rather than persist them in the clear or under a weak default key — a
// misconfigured deployment should fail closed, not silently leak credentials.

var (
	aeadOnce sync.Once
	aead     cipher.AEAD
	aeadErr  error
)

// ErrNoEncryptionKey is returned when a secret must be encrypted but no key is
// configured. Callers surface this as a clear "configure CHANNEL_ENCRYPTION_KEY"
// error rather than storing plaintext.
var ErrNoEncryptionKey = errors.New("CHANNEL_ENCRYPTION_KEY is not set — cannot store channel secrets securely")

func loadAEAD() (cipher.AEAD, error) {
	aeadOnce.Do(func() {
		raw := os.Getenv("CHANNEL_ENCRYPTION_KEY")
		if raw == "" {
			aeadErr = ErrNoEncryptionKey
			return
		}
		key := decodeKey(raw)
		block, err := aes.NewCipher(key)
		if err != nil {
			aeadErr = err
			return
		}
		aead, aeadErr = cipher.NewGCM(block)
	})
	return aead, aeadErr
}

// decodeKey accepts a 32-byte key as hex-64 or base64; anything else is hashed
// to 32 bytes with SHA-256 so an operator-supplied passphrase still yields a
// valid AES-256 key deterministically.
func decodeKey(raw string) []byte {
	if b, err := hex.DecodeString(raw); err == nil && len(b) == 32 {
		return b
	}
	if b, err := base64.StdEncoding.DecodeString(raw); err == nil && len(b) == 32 {
		return b
	}
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

// Encrypt returns base64(nonce||ciphertext) for the given plaintext.
func Encrypt(plaintext string) (string, error) {
	gcm, err := loadAEAD()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt reverses Encrypt.
func Decrypt(ciphertext string) (string, error) {
	gcm, err := loadAEAD()
	if err != nil {
		return "", err
	}
	raw, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("ciphertext too short")
	}
	nonce, body := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, body, nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

// EncryptionConfigured reports whether a key is available (used to fail create
// requests early with a clear message).
func EncryptionConfigured() bool {
	_, err := loadAEAD()
	return err == nil
}
