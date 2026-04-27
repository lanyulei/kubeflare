package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

type Encryptor interface {
	Encrypt(value string) (string, error)
	Decrypt(value string) (string, error)
}

type NoopEncryptor struct{}

func IsNoopEncryptor(encryptor Encryptor) bool {
	switch encryptor.(type) {
	case NoopEncryptor, *NoopEncryptor:
		return true
	default:
		return false
	}
}

func (NoopEncryptor) Encrypt(value string) (string, error) {
	return value, nil
}

func (NoopEncryptor) Decrypt(value string) (string, error) {
	return value, nil
}

type AESGCMEncryptor struct {
	aead cipher.AEAD
}

const cipherPrefix = "enc:v1:"

func NewAESGCMEncryptor(hexKey string) (*AESGCMEncryptor, error) {
	key, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, fmt.Errorf("decode encryption key: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("invalid encryption key length: got %d, want 32 bytes", len(key))
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}

	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create gcm: %w", err)
	}

	return &AESGCMEncryptor{aead: aead}, nil
}

func (e *AESGCMEncryptor) Encrypt(value string) (string, error) {
	if e == nil {
		return "", fmt.Errorf("encryptor is nil")
	}

	nonce := make([]byte, e.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("read nonce: %w", err)
	}

	ciphertext := e.aead.Seal(nonce, nonce, []byte(value), nil)
	return cipherPrefix + hex.EncodeToString(ciphertext), nil
}

func (e *AESGCMEncryptor) Decrypt(value string) (string, error) {
	if e == nil {
		return "", fmt.Errorf("encryptor is nil")
	}
	if value == "" {
		return "", nil
	}
	if !strings.HasPrefix(value, cipherPrefix) {
		return value, nil
	}

	ciphertext, err := hex.DecodeString(strings.TrimPrefix(value, cipherPrefix))
	if err != nil {
		return "", fmt.Errorf("decode ciphertext: %w", err)
	}

	nonceSize := e.aead.NonceSize()
	if len(ciphertext) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}

	plaintext, err := e.aead.Open(nil, ciphertext[:nonceSize], ciphertext[nonceSize:], nil)
	if err != nil {
		return "", fmt.Errorf("decrypt ciphertext: %w", err)
	}

	return string(plaintext), nil
}
