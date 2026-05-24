// Package crypto — шифрование чувствительных полей в БД (§5.5 ТЗ).
//
// Алгоритм: AES-256-GCM. Формат хранения:
//
//	<version>:<nonce_b64>:<ciphertext_b64>:<auth_tag_b64>
//
// где version — "v1", nonce 12 байт, auth_tag 16 байт.
// Версия закладывается в формат на случай будущей ротации алгоритма.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	formatVersion = "v1"
	keyLen        = 32 // AES-256
	nonceLen      = 12 // GCM standard
)

var (
	ErrInvalidKey     = errors.New("crypto: encryption key must be 32 bytes (base64-decoded)")
	ErrInvalidFormat  = errors.New("crypto: ciphertext has invalid format, expected v1:nonce:ct:tag")
	ErrInvalidVersion = errors.New("crypto: ciphertext has unsupported version, expected v1")
	ErrDecryption     = errors.New("crypto: decryption failed (wrong key or tampered data)")
)

// Cipher — стейтфул-структура: содержит AEAD-инстанс, готовый к Encrypt/Decrypt.
// Один Cipher используется на всё приложение, передаётся через DI.
type Cipher struct {
	aead cipher.AEAD
}

// NewCipher парсит base64-ключ из ENCRYPTION_KEY, проверяет длину
// (32 байта после декодирования), создаёт AES-256-GCM AEAD.
//
// Возвращает ErrInvalidKey при пустом/неверной длине ключе.
func NewCipher(base64Key string) (*Cipher, error) {
	if base64Key == "" {
		return nil, fmt.Errorf("%w: empty", ErrInvalidKey)
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(base64Key))
	if err != nil {
		return nil, fmt.Errorf("%w: base64 decode: %v", ErrInvalidKey, err)
	}
	if len(key) != keyLen {
		return nil, fmt.Errorf("%w: got %d bytes, want %d", ErrInvalidKey, len(key), keyLen)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes.NewCipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("cipher.NewGCM: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

// Encrypt шифрует plaintext и возвращает строку формата
// `v1:nonce_b64:ct_b64:tag_b64`. Пустой plaintext шифруется как
// пустая строка — это даёт возможность хранить «не задано»
// без специальной обработки.
func (c *Cipher) Encrypt(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	nonce := make([]byte, nonceLen)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("read nonce: %w", err)
	}
	// AEAD.Seal возвращает ciphertext+tag (tag в конце, 16 байт).
	sealed := c.aead.Seal(nil, nonce, []byte(plaintext), nil)
	ct := sealed[:len(sealed)-c.aead.Overhead()]
	tag := sealed[len(sealed)-c.aead.Overhead():]

	enc := base64.StdEncoding.EncodeToString
	return formatVersion + ":" + enc(nonce) + ":" + enc(ct) + ":" + enc(tag), nil
}

// Decrypt парсит строку формата `v1:nonce:ct:tag`, проверяет аутентификацию
// и возвращает plaintext. Пустая строка → пустой результат, ошибки нет.
func (c *Cipher) Decrypt(encoded string) (string, error) {
	if encoded == "" {
		return "", nil
	}
	parts := strings.Split(encoded, ":")
	if len(parts) != 4 {
		return "", ErrInvalidFormat
	}
	if parts[0] != formatVersion {
		return "", fmt.Errorf("%w: got %q", ErrInvalidVersion, parts[0])
	}
	dec := base64.StdEncoding.DecodeString
	nonce, err := dec(parts[1])
	if err != nil {
		return "", fmt.Errorf("%w: nonce: %v", ErrInvalidFormat, err)
	}
	ct, err := dec(parts[2])
	if err != nil {
		return "", fmt.Errorf("%w: ct: %v", ErrInvalidFormat, err)
	}
	tag, err := dec(parts[3])
	if err != nil {
		return "", fmt.Errorf("%w: tag: %v", ErrInvalidFormat, err)
	}
	if len(nonce) != nonceLen {
		return "", fmt.Errorf("%w: nonce len %d", ErrInvalidFormat, len(nonce))
	}
	sealed := append(ct, tag...)
	plain, err := c.aead.Open(nil, nonce, sealed, nil)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrDecryption, err)
	}
	return string(plain), nil
}

// MustNewCipher — для тестов и init-фаз: панически валит, если ключ невалидный.
// В production-коде используется NewCipher с явной обработкой ошибки.
func MustNewCipher(base64Key string) *Cipher {
	c, err := NewCipher(base64Key)
	if err != nil {
		panic(err)
	}
	return c
}
