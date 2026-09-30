// Package backupfile — зашифрованный паролем конверт файла резервной копии
// конфигурации (§110.3).
//
// Файл — JSON:
//
//	{"format": "nexus-config-backup",
//	 "protected": "<base64(JSON заголовка и параметров KDF)>",
//	 "nonce": "<base64>", "ciphertext": "<base64>"}
//
// Ключ выводится из пароля через Argon2id и шифрует gzip-сжатый payload
// алгоритмом AES-256-GCM. Байты protected — дополнительные данные (AAD): правка
// заголовка (счётчики, версия, параметры KDF) ломает расшифровку. protected
// хранится base64-строкой, а не вложенным объектом, чтобы файл пережил
// переформатирование редактором: у JSON нет канонической формы, и любое
// изменение пробелов во вложенном объекте сделало бы копию невосстановимой.
//
// Разбор недоверенного файла ограничен: параметры KDF (иначе файл заставил бы
// сервер выделить гигабайты под Argon2) и размер после распаковки.
package backupfile

import (
	"bytes"
	"compress/gzip"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Format — метка формата в файле; Version — версия конверта и снимка.
const (
	Format  = "nexus-config-backup"
	Version = 1
)

// MinPassphraseLen — минимальная длина пароля файла (в символах).
const MinPassphraseLen = 12

// MaxPayloadBytes — предел размера payload после распаковки.
const MaxPayloadBytes = 64 << 20

// Ошибки разбора и шифрования. Сообщения не несут кусков входа: файл
// недоверенный, а текст ошибки уходит в журнал и в ответ API.
var (
	ErrPassphraseTooShort = errors.New("backupfile: passphrase is too short")
	ErrNotBackupFile      = errors.New("backupfile: not a nexus configuration backup")
	ErrUnsupportedVersion = errors.New("backupfile: unsupported backup format version")
	ErrBadKDFParams       = errors.New("backupfile: key derivation parameters out of bounds")
	// ErrDecrypt — неверный пароль ЛИБО изменённый файл: GCM их не различает,
	// и различать нельзя — иначе подбор пароля получил бы подсказку.
	ErrDecrypt         = errors.New("backupfile: wrong passphrase or corrupted file")
	ErrPayloadTooLarge = errors.New("backupfile: payload exceeds size limit")
)

// Header — открытая часть файла: читается без пароля, чтобы предпросмотр
// показал «что за файл». Защищена от правки как AAD. Кто выгружал и откуда —
// внутри зашифрованного payload, не здесь.
type Header struct {
	ExportedAt       time.Time      `json:"exported_at"`
	NexusVersion     string         `json:"nexus_version"`
	SchemaVersion    uint           `json:"schema_version"`
	SourceInstanceID string         `json:"source_instance_id"`
	Counts           map[string]int `json:"counts"`
}

// KDFParams — параметры Argon2id, с которыми зашифрован файл.
type KDFParams struct {
	Name      string `json:"name"`
	Time      uint32 `json:"time"`
	MemoryKiB uint32 `json:"memory_kib"`
	Threads   uint8  `json:"threads"`
	Salt      []byte `json:"salt"`
}

// DefaultKDF — параметры новых файлов: 64 МиБ, 3 прохода, 4 потока.
func DefaultKDF() KDFParams {
	return KDFParams{Name: kdfArgon2id, Time: 3, MemoryKiB: 64 << 10, Threads: 4}
}

const (
	kdfArgon2id  = "argon2id"
	keyLen       = 32
	saltLen      = 16
	nonceLen     = 12
	maxTime      = 10
	minMemoryKiB = 8 << 10
	maxMemoryKiB = 256 << 10
	maxThreads   = 16
	maxSaltLen   = 64
)

// protected — то, что лежит в поле protected (и служит AAD).
type protected struct {
	Format  string    `json:"format"`
	Version int       `json:"version"`
	Header  Header    `json:"header"`
	KDF     KDFParams `json:"kdf"`
}

// envelope — файл целиком. []byte-поля encoding/json пишет base64.
type envelope struct {
	Format     string `json:"format"`
	Protected  []byte `json:"protected"`
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
}

// ValidatePassphrase — проверка длины пароля (в символах, не байтах).
func ValidatePassphrase(passphrase string) error {
	if utf8.RuneCountInString(passphrase) < MinPassphraseLen {
		return ErrPassphraseTooShort
	}
	return nil
}

// Seal сжимает и шифрует payload паролем с параметрами kdf (соль
// генерируется здесь, значение kdf.Salt игнорируется).
func Seal(h Header, payload []byte, passphrase string, kdf KDFParams) ([]byte, error) {
	if err := ValidatePassphrase(passphrase); err != nil {
		return nil, err
	}
	if len(payload) > MaxPayloadBytes {
		return nil, ErrPayloadTooLarge
	}
	kdf.Name = kdfArgon2id
	kdf.Salt = make([]byte, saltLen)
	nonce := make([]byte, nonceLen)
	if err := readRandom(kdf.Salt, nonce); err != nil {
		return nil, err
	}
	if err := checkKDF(kdf); err != nil {
		return nil, err
	}
	aad, err := json.Marshal(protected{Format: Format, Version: Version, Header: h, KDF: kdf})
	if err != nil {
		return nil, fmt.Errorf("backupfile: marshal header: %w", err)
	}
	plain, err := gzipBytes(payload)
	if err != nil {
		return nil, err
	}
	aead, err := newAEAD(passphrase, kdf)
	if err != nil {
		return nil, err
	}
	out, err := json.Marshal(envelope{
		Format: Format, Protected: aad, Nonce: nonce,
		Ciphertext: aead.Seal(nil, nonce, plain, aad),
	})
	if err != nil {
		return nil, fmt.Errorf("backupfile: marshal envelope: %w", err)
	}
	return out, nil
}

// ReadHeader читает открытый заголовок без пароля. Подлинность заголовка
// здесь НЕ проверена — только Open подтверждает её расшифровкой.
func ReadHeader(file []byte) (*Header, error) {
	_, p, err := parse(file)
	if err != nil {
		return nil, err
	}
	return &p.Header, nil
}

// Open проверяет, расшифровывает и распаковывает файл.
func Open(file []byte, passphrase string) (*Header, []byte, error) {
	env, p, err := parse(file)
	if err != nil {
		return nil, nil, err
	}
	if err := checkKDF(p.KDF); err != nil {
		return nil, nil, err
	}
	if len(env.Nonce) != nonceLen {
		return nil, nil, ErrNotBackupFile
	}
	aead, err := newAEAD(passphrase, p.KDF)
	if err != nil {
		return nil, nil, err
	}
	plain, err := aead.Open(nil, env.Nonce, env.Ciphertext, env.Protected)
	if err != nil {
		return nil, nil, ErrDecrypt
	}
	payload, err := gunzipBytes(plain)
	if err != nil {
		return nil, nil, err
	}
	return &p.Header, payload, nil
}

// parse разбирает конверт и защищённый заголовок и проверяет формат/версию.
func parse(file []byte) (*envelope, *protected, error) {
	var env envelope
	if err := json.Unmarshal(file, &env); err != nil || env.Format != Format || len(env.Protected) == 0 {
		return nil, nil, ErrNotBackupFile
	}
	var p protected
	if err := json.Unmarshal(env.Protected, &p); err != nil || p.Format != Format {
		return nil, nil, ErrNotBackupFile
	}
	if p.Version != Version {
		return nil, nil, fmt.Errorf("%w: %d", ErrUnsupportedVersion, p.Version)
	}
	return &env, &p, nil
}

// checkKDF — параметры KDF из недоверенного файла в разумных границах.
func checkKDF(k KDFParams) error {
	switch {
	case k.Name != kdfArgon2id,
		k.Time < 1 || k.Time > maxTime,
		k.MemoryKiB < minMemoryKiB || k.MemoryKiB > maxMemoryKiB,
		k.Threads < 1 || k.Threads > maxThreads,
		len(k.Salt) < saltLen || len(k.Salt) > maxSaltLen:
		return ErrBadKDFParams
	}
	return nil
}

func newAEAD(passphrase string, k KDFParams) (cipher.AEAD, error) {
	key := argon2.IDKey([]byte(passphrase), k.Salt, k.Time, k.MemoryKiB, k.Threads, keyLen)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("backupfile: aes: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("backupfile: gcm: %w", err)
	}
	return aead, nil
}

func readRandom(bufs ...[]byte) error {
	for _, b := range bufs {
		if _, err := rand.Read(b); err != nil {
			return fmt.Errorf("backupfile: random: %w", err)
		}
	}
	return nil
}

func gzipBytes(b []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(b); err != nil {
		return nil, fmt.Errorf("backupfile: gzip: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("backupfile: gzip: %w", err)
	}
	return buf.Bytes(), nil
}

// gunzipBytes распаковывает не больше MaxPayloadBytes: сжатый «бомбой» файл
// раздулся бы в памяти во много раз больше своего размера.
func gunzipBytes(b []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, ErrNotBackupFile
	}
	defer func() { _ = zr.Close() }() // чтение из памяти: ошибке закрытия неоткуда взяться
	out, err := io.ReadAll(io.LimitReader(zr, MaxPayloadBytes+1))
	if err != nil {
		return nil, ErrNotBackupFile
	}
	if len(out) > MaxPayloadBytes {
		return nil, ErrPayloadTooLarge
	}
	return out, nil
}
