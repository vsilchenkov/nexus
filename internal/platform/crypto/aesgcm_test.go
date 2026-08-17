package crypto

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func validKey() string {
	// 32 нуль-байта — простой, детерминированный ключ для тестов.
	return base64.StdEncoding.EncodeToString(make([]byte, 32))
}

func TestNewCipher_ValidKey(t *testing.T) {
	if _, err := NewCipher(validKey()); err != nil {
		t.Fatalf("expected ok, got %v", err)
	}
}

func TestNewCipher_InvalidKey(t *testing.T) {
	cases := []struct {
		name string
		key  string
	}{
		{"empty", ""},
		{"too_short", base64.StdEncoding.EncodeToString([]byte("short"))},
		{"not_base64", "!not_base64!"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := NewCipher(c.key)
			if !errors.Is(err, ErrInvalidKey) {
				t.Fatalf("want ErrInvalidKey, got %v", err)
			}
		})
	}
}

func TestEncryptDecrypt_RoundTrip(t *testing.T) {
	c, _ := NewCipher(validKey())
	for _, plain := range []string{
		"",
		"a",
		"short token",
		strings.Repeat("very long credentials ", 100),
		"unicode: тестирование 🔐",
	} {
		enc, err := c.Encrypt(plain)
		if err != nil {
			t.Fatalf("encrypt %q: %v", plain, err)
		}
		dec, err := c.Decrypt(enc)
		if err != nil {
			t.Fatalf("decrypt %q (enc=%q): %v", plain, enc, err)
		}
		if dec != plain {
			t.Fatalf("round-trip mismatch: want %q got %q", plain, dec)
		}
	}
}

func TestEncrypt_NonDeterministic(t *testing.T) {
	c, _ := NewCipher(validKey())
	a, _ := c.Encrypt("secret")
	b, _ := c.Encrypt("secret")
	if a == b {
		t.Fatalf("expected different ciphertexts (nonce should be random), got identical")
	}
}

func TestDecrypt_Tampered(t *testing.T) {
	c, _ := NewCipher(validKey())
	enc, _ := c.Encrypt("hello")
	// Меняем один символ в ciphertext-секции.
	parts := strings.Split(enc, ":")
	parts[2] = parts[2][:len(parts[2])-2] + "AA"
	_, err := c.Decrypt(strings.Join(parts, ":"))
	if !errors.Is(err, ErrDecryption) {
		t.Fatalf("want ErrDecryption, got %v", err)
	}
}

func TestDecrypt_InvalidFormat(t *testing.T) {
	c, _ := NewCipher(validKey())
	cases := []string{
		"not_versioned",
		"v1:only-three-parts",
		"v2:nonce:ct:tag",
	}
	for _, in := range cases {
		_, err := c.Decrypt(in)
		if err == nil {
			t.Fatalf("expected error for %q", in)
		}
	}
}

// §90.1: смесь исторического plaintext и нового шифротекста в одной колонке.
func TestDecryptLenient(t *testing.T) {
	c, _ := NewCipher(validKey())
	encrypted, err := c.Encrypt("s3cret")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	cases := []struct {
		name          string
		in            string
		wantPlain     string
		wantEncrypted bool
	}{
		{"пусто", "", "", false},
		{"шифротекст", encrypted, "s3cret", true},
		{"legacy plaintext", "plain-password", "plain-password", false},
		// Реальные значения, которые лежат в app_settings открытым текстом:
		// у DSN есть двоеточия, но частей не четыре — не спутается с v1-форматом.
		{"legacy Sentry DSN", "https://key@sentry.example.com:9000/52", "https://key@sentry.example.com:9000/52", false},
		{"legacy строка с 4 частями, но чужой версией", "v2:a:b:c", "v2:a:b:c", false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			plain, wasEnc, err := c.DecryptLenient(tt.in)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if plain != tt.wantPlain {
				t.Fatalf("plain: want %q got %q", tt.wantPlain, plain)
			}
			if wasEnc != tt.wantEncrypted {
				t.Fatalf("wasEncrypted: want %v got %v", tt.wantEncrypted, wasEnc)
			}
		})
	}
}

// Порча и чужой ключ обязаны быть ошибкой, а не «сойти за plaintext»:
// иначе наверх ушёл бы шифротекст под видом секрета.
func TestDecryptLenient_TamperedAndForeignKey(t *testing.T) {
	c, _ := NewCipher(validKey())
	enc, _ := c.Encrypt("hello")

	t.Run("порченый tag", func(t *testing.T) {
		parts := strings.Split(enc, ":")
		parts[2] = parts[2][:len(parts[2])-2] + "AA"
		plain, wasEnc, err := c.DecryptLenient(strings.Join(parts, ":"))
		if !errors.Is(err, ErrDecryption) {
			t.Fatalf("want ErrDecryption, got %v", err)
		}
		if !wasEnc {
			t.Fatalf("порченый v1-шифротекст обязан считаться шифротекстом")
		}
		if plain != "" {
			t.Fatalf("при ошибке значение не отдаётся, got %q", plain)
		}
	})

	t.Run("чужой ключ", func(t *testing.T) {
		otherKey := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
		other, _ := NewCipher(otherKey)
		if _, _, err := other.DecryptLenient(enc); !errors.Is(err, ErrDecryption) {
			t.Fatalf("want ErrDecryption, got %v", err)
		}
	})
}
