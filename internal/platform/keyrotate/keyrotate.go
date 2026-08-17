// Package keyrotate — перешифровка данных при смене ENCRYPTION_KEY (§5.5, §90.4).
//
// Логика живёт здесь, а не в cmd/rotate-key, чтобы её можно было покрыть
// тестами: package main из tests/ не импортируется.
//
// Общие для обеих фаз правила:
//   - идемпотентность: значение, которое уже читается новым ключом, пропускается,
//     поэтому прерванный прогон безопасно повторить;
//   - значения, лежащие открытым текстом (данные старше включения шифрования),
//     не ошибка — они шифруются новым ключом и считаются отдельным счётчиком;
//   - шифротекст, который не бьётся старым ключом, — ошибка: это чужой ключ или
//     порча, и угадывать здесь нечего.
package keyrotate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"nexus/internal/platform/crypto"
	"nexus/internal/platform/logging"
)

// nodeSecretColumns — колонки nodes, хранящие зашифрованные значения.
// rmq_password (§27) шифруется наравне с кредами узла.
var nodeSecretColumns = []string{"auth_credentials", "incoming_auth_credentials", "rmq_password"}

// appSettingsSecretPaths — пути секретов внутри JSONB app_settings.value (§90.1).
var appSettingsSecretPaths = [][]string{
	{"sentry", "dsn"},
	{"clickhouse", "password"},
	{"mail", "password"},
	{"notifications", "telegram", "bot_token"},
}

// Options — режим прогона.
type Options struct {
	// DryRun — только посчитать, ничего не писать.
	DryRun bool
	// Decrypt — обратный ход (§90.6): значения расшифровываются и остаются в БД
	// ОТКРЫТЫМ ТЕКСТОМ. Нужен ровно для одного сценария — отката кода на версию
	// без §90.1, которая шифротекст в app_settings не понимает и приняла бы его
	// за сам секрет. Ключ при этом берётся из OldCipher, NewCipher не участвует.
	Decrypt bool
}

// Stats — итог прогона. Счётчики разделены намеренно: Encrypted означает, что
// данные лежали в БД ОТКРЫТЫМИ, и оператору это важно увидеть отдельно от
// обычной перешифровки.
type Stats struct {
	Scanned     int
	Reencrypted int
	// Encrypted — значения, лежавшие открытым текстом и зашифрованные.
	Encrypted int
	// Decrypted — значения, расшифрованные обратно в plaintext (Options.Decrypt).
	Decrypted         int
	SkippedAlreadyNew int
	// SkippedPlaintext — при Decrypt: значение и так лежит открыто.
	SkippedPlaintext int
	Empty            int
}

// Add суммирует результаты фаз.
func (s *Stats) Add(other Stats) {
	s.Scanned += other.Scanned
	s.Reencrypted += other.Reencrypted
	s.Encrypted += other.Encrypted
	s.Decrypted += other.Decrypted
	s.SkippedAlreadyNew += other.SkippedAlreadyNew
	s.SkippedPlaintext += other.SkippedPlaintext
	s.Empty += other.Empty
}

// actionOf — как назвать выполненное действие в логе: оператор по одной строке
// должен понимать, зашифровали значение или, наоборот, раскрыли.
func actionOf(opts Options) string {
	if opts.Decrypt {
		return "decrypted to plaintext"
	}
	return "re-encrypted"
}

// nextValue решает судьбу одного значения: nil — оставить как есть
// (пропущено), иначе — значение, которое нужно записать.
func nextValue(oldC, newC *crypto.Cipher, val string, opts Options, st *Stats) (*string, error) {
	st.Scanned++
	if val == "" {
		st.Empty++
		return nil, nil
	}

	if opts.Decrypt {
		plain, wasEncrypted, err := oldC.DecryptLenient(val)
		if err != nil {
			return nil, err
		}
		if !wasEncrypted {
			// Уже открыто — повторный прогон обратного хода безопасен.
			st.SkippedPlaintext++
			return nil, nil
		}
		st.Decrypted++
		return &plain, nil
	}

	if _, err := newC.Decrypt(val); err == nil {
		st.SkippedAlreadyNew++
		return nil, nil
	}
	plain, wasEncrypted, err := oldC.DecryptLenient(val)
	if err != nil {
		return nil, err
	}
	next, err := newC.Encrypt(plain)
	if err != nil {
		return nil, err
	}
	if wasEncrypted {
		st.Reencrypted++
	} else {
		st.Encrypted++
	}
	return &next, nil
}

// ErrDecryptNodesForbidden — попытка расшифровать креды узлов обратно в
// plaintext. Запрещено намеренно (§90.6): все три сервиса читают эти колонки
// СТРОГИМ Decrypt (node_repo.go, sender/nodepg, receiver/nodecache), поэтому
// открытый текст в них означает, что узлы просто перестанут работать. В
// отличие от app_settings, они шифруются с самого начала (§5.5) — ни одна
// версия кода не ждёт их открытыми, и откатывать тут нечего.
var ErrDecryptNodesForbidden = errors.New(
	"keyrotate: decrypting node credentials is not supported — services read them with strict Decrypt")

// RotateNodes обрабатывает секретные колонки таблицы nodes: перешифровывает на
// новый ключ или шифрует лежащее открытым текстом. Обратный ход (Options.Decrypt)
// запрещён — см. ErrDecryptNodesForbidden.
func RotateNodes(
	ctx context.Context,
	pool *pgxpool.Pool,
	oldC, newC *crypto.Cipher,
	opts Options,
	logger logging.Logger,
) (Stats, error) {
	var st Stats
	if opts.Decrypt {
		return st, ErrDecryptNodesForbidden
	}
	for _, col := range nodeSecretColumns {
		colStats, err := rotateNodeColumn(ctx, pool, oldC, newC, col, opts, logger)
		if err != nil {
			return st, err
		}
		st.Add(colStats)
	}
	return st, nil
}

func rotateNodeColumn(
	ctx context.Context,
	pool *pgxpool.Pool,
	oldC, newC *crypto.Cipher,
	col string,
	opts Options,
	logger logging.Logger,
) (Stats, error) {
	var st Stats
	// NULL отсекается сравнением само собой: NULL <> '' даёт NULL.
	rows, err := pool.Query(ctx, "SELECT id, "+col+" FROM nodes WHERE "+col+" <> ''")
	if err != nil {
		return st, fmt.Errorf("select %s: %w", col, err)
	}
	type row struct {
		id  string
		val string
	}
	var batch []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.val); err != nil {
			rows.Close()
			return st, fmt.Errorf("scan %s: %w", col, err)
		}
		batch = append(batch, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return st, fmt.Errorf("iterate %s: %w", col, err)
	}

	for _, r := range batch {
		before := st
		next, err := nextValue(oldC, newC, r.val, opts, &st)
		if err != nil {
			return st, fmt.Errorf("rotate %s/%s: %w", col, r.id, err)
		}
		if next == nil || opts.DryRun {
			continue
		}
		if _, err := pool.Exec(ctx, "UPDATE nodes SET "+col+" = $1 WHERE id = $2", *next, r.id); err != nil {
			return st, fmt.Errorf("update %s/%s: %w", col, r.id, err)
		}
		logger.Info(actionOf(opts),
			logger.Str("column", col),
			logger.Str("id", r.id),
			logger.Any("was_plaintext", st.Encrypted > before.Encrypted))
	}
	return st, nil
}

// RotateAppSettings перешифровывает секреты singleton-строки app_settings (§90.1).
//
// Документ разбирается в map[string]any, а не в domain.AppSettings: round-trip
// через типизированную структуру молча выбросил бы поля, которых она не знает
// (ровно та грабля, о которой предупреждает комментарий в app_settings_repo.go).
//
// Запись — одним UPDATE после успешной обработки всех полей: при ошибке в
// середине документ остаётся нетронутым. updated_at/updated_by не трогаются —
// ротация ключа не операторская правка настроек.
func RotateAppSettings(
	ctx context.Context,
	pool *pgxpool.Pool,
	oldC, newC *crypto.Cipher,
	opts Options,
	logger logging.Logger,
) (Stats, error) {
	var st Stats
	var raw []byte
	if err := pool.QueryRow(ctx, `SELECT value FROM app_settings WHERE id = 1`).Scan(&raw); err != nil {
		// Строку заводит миграция 0006, но отсутствие настроек — не повод валить
		// ротацию узлов: шифровать тут попросту нечего (так же трактуют эту
		// ситуацию оба штатных читателя app_settings).
		if errors.Is(err, pgx.ErrNoRows) {
			logger.Info("rotate app_settings: singleton row is missing, nothing to do")
			return st, nil
		}
		return st, fmt.Errorf("select app_settings: %w", err)
	}
	if len(raw) == 0 {
		return st, nil
	}

	doc := map[string]any{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return st, fmt.Errorf("decode app_settings: %w", err)
	}

	changed, st, err := rotateAppSettingsDoc(doc, oldC, newC, opts, logger)
	if err != nil {
		return st, err
	}
	if !changed || opts.DryRun {
		return st, nil
	}

	payload, err := json.Marshal(doc)
	if err != nil {
		return st, fmt.Errorf("encode app_settings: %w", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE app_settings SET value = $1::jsonb WHERE id = 1`, payload); err != nil {
		return st, fmt.Errorf("update app_settings: %w", err)
	}
	return st, nil
}

// rotateAppSettingsDoc — чистая часть фазы app_settings: правит документ на
// месте и возвращает признак изменений. Вынесена ради тестов без БД.
func rotateAppSettingsDoc(
	doc map[string]any,
	oldC, newC *crypto.Cipher,
	opts Options,
	logger logging.Logger,
) (bool, Stats, error) {
	var st Stats
	changed := false
	for _, path := range appSettingsSecretPaths {
		val, ok := lookupString(doc, path)
		if !ok {
			continue
		}
		field := pathString(path)
		before := st
		next, err := nextValue(oldC, newC, val, opts, &st)
		if err != nil {
			return false, st, fmt.Errorf("rotate app_settings %s: %w", field, err)
		}
		if next == nil {
			continue
		}
		setString(doc, path, *next)
		changed = true
		logger.Info(actionOf(opts),
			logger.Str("column", "app_settings."+field),
			logger.Any("was_plaintext", st.Encrypted > before.Encrypted))
	}
	return changed, st, nil
}

// lookupString достаёт строковое значение по пути. ok=false, если пути нет или
// значение не строка (null, число — трогать нечего).
func lookupString(doc map[string]any, path []string) (string, bool) {
	cur := doc
	for i, key := range path {
		v, ok := cur[key]
		if !ok {
			return "", false
		}
		if i == len(path)-1 {
			s, isStr := v.(string)
			return s, isStr
		}
		next, isMap := v.(map[string]any)
		if !isMap {
			return "", false
		}
		cur = next
	}
	return "", false
}

// setString записывает значение по пути; промежуточные узлы к этому моменту
// заведомо существуют — вызывается только после успешного lookupString.
func setString(doc map[string]any, path []string, val string) {
	cur := doc
	for i, key := range path {
		if i == len(path)-1 {
			cur[key] = val
			return
		}
		next, ok := cur[key].(map[string]any)
		if !ok {
			return
		}
		cur = next
	}
}

func pathString(path []string) string {
	return strings.Join(path, ".")
}
