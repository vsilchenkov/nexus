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

// Stats — итог прогона. Reencrypted и UpgradedPlaintext разделены намеренно:
// второе означает, что данные лежали в БД открытыми, и оператору это важно
// увидеть в отчёте.
type Stats struct {
	Scanned           int
	Reencrypted       int
	UpgradedPlaintext int
	SkippedAlreadyNew int
	Empty             int
}

// Add суммирует результаты фаз.
func (s *Stats) Add(other Stats) {
	s.Scanned += other.Scanned
	s.Reencrypted += other.Reencrypted
	s.UpgradedPlaintext += other.UpgradedPlaintext
	s.SkippedAlreadyNew += other.SkippedAlreadyNew
	s.Empty += other.Empty
}

func (s *Stats) count(wasEncrypted bool) {
	if wasEncrypted {
		s.Reencrypted++
		return
	}
	s.UpgradedPlaintext++
}

// rotateValue решает судьбу одного значения: nil — оставить как есть
// (пропущено), иначе — новое значение под новым ключом.
func rotateValue(oldC, newC *crypto.Cipher, val string, st *Stats) (*string, error) {
	st.Scanned++
	if val == "" {
		st.Empty++
		return nil, nil
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
	st.count(wasEncrypted)
	return &next, nil
}

// RotateNodes перешифровывает секретные колонки таблицы nodes.
func RotateNodes(
	ctx context.Context,
	pool *pgxpool.Pool,
	oldC, newC *crypto.Cipher,
	dryRun bool,
	logger logging.Logger,
) (Stats, error) {
	var st Stats
	for _, col := range nodeSecretColumns {
		colStats, err := rotateNodeColumn(ctx, pool, oldC, newC, col, dryRun, logger)
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
	dryRun bool,
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
		next, err := rotateValue(oldC, newC, r.val, &st)
		if err != nil {
			return st, fmt.Errorf("rotate %s/%s: %w", col, r.id, err)
		}
		if next == nil {
			continue
		}
		if dryRun {
			continue
		}
		if _, err := pool.Exec(ctx, "UPDATE nodes SET "+col+" = $1 WHERE id = $2", *next, r.id); err != nil {
			return st, fmt.Errorf("update %s/%s: %w", col, r.id, err)
		}
		logger.Info("re-encrypted",
			logger.Str("column", col),
			logger.Str("id", r.id),
			logger.Any("was_plaintext", st.UpgradedPlaintext > before.UpgradedPlaintext))
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
	dryRun bool,
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

	changed, st, err := rotateAppSettingsDoc(doc, oldC, newC, logger)
	if err != nil {
		return st, err
	}
	if !changed || dryRun {
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
		next, err := rotateValue(oldC, newC, val, &st)
		if err != nil {
			return false, st, fmt.Errorf("rotate app_settings %s: %w", field, err)
		}
		if next == nil {
			continue
		}
		setString(doc, path, *next)
		changed = true
		logger.Info("re-encrypted",
			logger.Str("column", "app_settings."+field),
			logger.Any("was_plaintext", st.UpgradedPlaintext > before.UpgradedPlaintext))
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
