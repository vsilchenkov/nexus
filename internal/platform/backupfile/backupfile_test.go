package backupfile

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testPass = "correct horse battery staple"

// cheapKDF — нижняя граница допустимых параметров: тесты не должны тратить
// по 64 МиБ и сотни миллисекунд на каждый Seal/Open.
func cheapKDF() KDFParams {
	return KDFParams{Time: 1, MemoryKiB: minMemoryKiB, Threads: 1}
}

func testHeader() Header {
	return Header{
		ExportedAt:       time.Date(2026, 9, 29, 17, 30, 0, 0, time.UTC),
		NexusVersion:     "v1.40.0",
		SchemaVersion:    43,
		SourceInstanceID: "kz",
		Counts:           map[string]int{"nodes": 142, "teams": 6},
	}
}

func sealed(t *testing.T, payload []byte) []byte {
	t.Helper()
	file, err := Seal(testHeader(), payload, testPass, cheapKDF())
	require.NoError(t, err)
	return file
}

func TestSealOpen_RoundTrip(t *testing.T) {
	t.Parallel()
	payload := []byte(`{"teams":[{"slug":"acme"}],"secret":"s3cr3t"}`)
	file := sealed(t, payload)

	assert.NotContains(t, string(file), "s3cr3t", "payload в файле зашифрован")

	h, got, err := Open(file, testPass)
	require.NoError(t, err)
	assert.Equal(t, payload, got)
	assert.Equal(t, testHeader(), *h)
}

func TestReadHeader_WithoutPassphrase(t *testing.T) {
	t.Parallel()
	h, err := ReadHeader(sealed(t, []byte("{}")))
	require.NoError(t, err)
	assert.Equal(t, 142, h.Counts["nodes"])
	assert.Equal(t, "kz", h.SourceInstanceID)
}

// TestOpen_SurvivesReformatting — файл, переформатированный редактором,
// открывается: AAD хранится строкой и от пробелов не зависит.
func TestOpen_SurvivesReformatting(t *testing.T) {
	t.Parallel()
	file := sealed(t, []byte("payload"))
	var pretty bytes.Buffer
	require.NoError(t, json.Indent(&pretty, file, "", "    "))

	_, got, err := Open(pretty.Bytes(), testPass)
	require.NoError(t, err)
	assert.Equal(t, []byte("payload"), got)
}

func TestOpen_Rejects(t *testing.T) {
	t.Parallel()
	good := sealed(t, []byte("payload"))

	tests := []struct {
		name    string
		file    func() []byte
		pass    string
		wantErr error
	}{
		{"неверный пароль", func() []byte { return good }, "wrong passphrase!", ErrDecrypt},
		{"изменены счётчики в заголовке", func() []byte {
			return mutateProtected(t, good, func(p *protected) { p.Header.Counts["nodes"] = 1 })
		}, testPass, ErrDecrypt},
		{"изменён шифротекст", func() []byte {
			return mutateEnvelope(t, good, func(e *envelope) { e.Ciphertext[0] ^= 0xFF })
		}, testPass, ErrDecrypt},
		{"не JSON", func() []byte { return []byte("PK\x03\x04 zip") }, testPass, ErrNotBackupFile},
		{"чужой JSON", func() []byte { return []byte(`{"format":"other"}`) }, testPass, ErrNotBackupFile},
		{"неизвестная версия", func() []byte {
			return mutateProtected(t, good, func(p *protected) { p.Version = 2 })
		}, testPass, ErrUnsupportedVersion},
		{"память KDF выше предела", func() []byte {
			return mutateProtected(t, good, func(p *protected) { p.KDF.MemoryKiB = 4 << 20 })
		}, testPass, ErrBadKDFParams},
		{"чужой алгоритм KDF", func() []byte {
			return mutateProtected(t, good, func(p *protected) { p.KDF.Name = "scrypt" })
		}, testPass, ErrBadKDFParams},
		{"короткий nonce", func() []byte {
			return mutateEnvelope(t, good, func(e *envelope) { e.Nonce = e.Nonce[:4] })
		}, testPass, ErrNotBackupFile},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := Open(tt.file(), tt.pass)
			assert.ErrorIs(t, err, tt.wantErr)
		})
	}
}

func TestSeal_Passphrase(t *testing.T) {
	t.Parallel()
	_, err := Seal(testHeader(), nil, "short", cheapKDF())
	assert.ErrorIs(t, err, ErrPassphraseTooShort)

	// Длина — в символах: 12 кириллических букв — это 24 байта, но 12 символов.
	assert.NoError(t, ValidatePassphrase("абвгдежзийкл"))
	assert.ErrorIs(t, ValidatePassphrase("абвгдежзийк"), ErrPassphraseTooShort)
}

// TestOpen_GzipBomb — распакованный payload больше предела отклоняется, а не
// раздувается в памяти до конца.
func TestOpen_GzipBomb(t *testing.T) {
	t.Parallel()
	kdf := cheapKDF()
	kdf.Name = kdfArgon2id
	kdf.Salt = bytes.Repeat([]byte{1}, saltLen)
	aad, err := json.Marshal(protected{Format: Format, Version: Version, Header: testHeader(), KDF: kdf})
	require.NoError(t, err)
	bomb, err := gzipBytes(make([]byte, MaxPayloadBytes+1))
	require.NoError(t, err)
	aead, err := newAEAD(testPass, kdf)
	require.NoError(t, err)
	nonce := make([]byte, nonceLen)
	file, err := json.Marshal(envelope{Format: Format, Protected: aad, Nonce: nonce,
		Ciphertext: aead.Seal(nil, nonce, bomb, aad)})
	require.NoError(t, err)

	_, _, err = Open(file, testPass)
	assert.ErrorIs(t, err, ErrPayloadTooLarge)
}

func mutateEnvelope(t *testing.T, file []byte, fn func(*envelope)) []byte {
	t.Helper()
	var e envelope
	require.NoError(t, json.Unmarshal(file, &e))
	e.Ciphertext = bytes.Clone(e.Ciphertext)
	fn(&e)
	out, err := json.Marshal(e)
	require.NoError(t, err)
	return out
}

func mutateProtected(t *testing.T, file []byte, fn func(*protected)) []byte {
	t.Helper()
	return mutateEnvelope(t, file, func(e *envelope) {
		var p protected
		require.NoError(t, json.Unmarshal(e.Protected, &p))
		fn(&p)
		b, err := json.Marshal(p)
		require.NoError(t, err)
		e.Protected = b
	})
}

// FuzzOpen — недоверенный файл никогда не роняет разбор и с неверным паролем
// не открывается.
func FuzzOpen(f *testing.F) {
	file, err := Seal(testHeader(), []byte(`{"a":1}`), testPass, cheapKDF())
	if err != nil {
		f.Fatal(err)
	}
	f.Add(file)
	f.Add([]byte(`{"format":"nexus-config-backup","protected":"e30="}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`not json`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if _, err := ReadHeader(data); err != nil {
			return
		}
		if _, _, err := Open(data, "definitely-not-the-passphrase"); err == nil {
			t.Fatalf("file opened with a wrong passphrase")
		}
	})
}
