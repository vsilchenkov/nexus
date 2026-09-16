package nodestatus

import (
	"context"
	"fmt"
	"slices"
	"strings"

	goredis "github.com/redis/go-redis/v9"
)

// migrationMarkerKey — отметка «старые ключи исхода уже перенесены» (§101.6).
//
// Без неё перенос повторялся бы на каждом рестарте Web, пока живут старые ключи
// (до 30 суток), и воскрешал бы то, что уже снято: ручной сброс защиты (§81.4.2)
// удаляет ключ по id, и следующий рестарт вернул бы узлу старый «Down»; узел,
// созданный после обновления с путём удалённого, получил бы чужой исход.
//
// Ключ лежит вне префикса исходов — SCAN по старым ключам его не видит. Срок —
// как у ключей исхода: когда отметка истечёт, старых ключей уже не останется.
const migrationMarkerKey = "nexus:node:status_migration:101"

// migrateScanCount — подсказка SCAN'у. Ключей исходов сотни, а в том же Redis
// лежат сессии и кеш: с COUNT по умолчанию (10) проход шёл бы тысячами вызовов.
const migrateScanCount = 1000

// migrateSrc — перенос одного ключа.
//
// KEYS[1] — старый ключ по пути, KEYS[2] — новый по id. ARGV[1] — срок в
// миллисекундах на случай, если у старого ключа его нет. Возвращает 1, если
// значение перенесено.
//
// Скрипт, а не GET и SET NX с клиента: между чтением и записью Sender успевает
// записать по id свежий исход, а соседняя реплика Web (§93) — выполнить тот же
// перенос. Проверка и запись обязаны быть неделимы.
const migrateSrc = `
if redis.call('EXISTS', KEYS[2]) == 1 then
	return 0
end
local v = redis.call('GET', KEYS[1])
if not v then
	return 0
end
local ttl = redis.call('PTTL', KEYS[1])
if ttl <= 0 then
	ttl = tonumber(ARGV[1])
end
redis.call('SET', KEYS[2], v, 'PX', ttl)
return 1
`

// LegacyMigrator переносит исходы узлов со старых ключей по пути на ключи по id
// (§101.6).
//
// Старые ключи не удаляются: после отката версии их читает прежний Web
// (§101.12), а сами они истекают по TTL.
type LegacyMigrator struct {
	client *goredis.Client
	// script — поле по той же причине, что у RedisWriter: Script несёт состояние.
	script *goredis.Script
}

// NewLegacyMigrator создаёт мигратор. client обязан быть ненулевым.
func NewLegacyMigrator(client *goredis.Client) *LegacyMigrator {
	return &LegacyMigrator{client: client, script: goredis.NewScript(migrateSrc)}
}

// Migrated сообщает, выполнен ли перенос (стоит ли отметка).
func (m *LegacyMigrator) Migrated(ctx context.Context) (bool, error) {
	n, err := m.client.Exists(ctx, migrationMarkerKey).Result()
	if err != nil {
		return false, fmt.Errorf("nodestatus migration marker: %w", err)
	}
	return n > 0, nil
}

// LegacyPaths — пути узлов, у которых остался ключ исхода по пути.
//
// Ключи по id (`…:id:<uuid>`) и всё, в чём после префикса есть двоеточие,
// пропускаются: двоеточие запрещено в пути узла, значит это не старый ключ. SCAN
// может вернуть ключ дважды — результат без повторов.
func (m *LegacyMigrator) LegacyPaths(ctx context.Context) ([]string, error) {
	var paths []string
	iter := m.client.Scan(ctx, 0, keyPrefix+"*", migrateScanCount).Iterator()
	for iter.Next(ctx) {
		p := strings.TrimPrefix(iter.Val(), keyPrefix)
		if p == "" || strings.Contains(p, ":") {
			continue
		}
		paths = append(paths, p)
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("nodestatus scan legacy keys: %w", err)
	}
	slices.Sort(paths)
	return slices.Compact(paths), nil
}

// Migrate переносит значения старых ключей для пар «путь → id» и ставит отметку
// о переносе. Возвращает число перенесённых ключей.
//
// Уже записанный ключ по id не перезаписывается: он свежее старого. Отметка
// ставится только после успешного прохода — при ошибке следующий старт Web
// повторит перенос, а перенесённое скрипт не тронет.
func (m *LegacyMigrator) Migrate(ctx context.Context, idsByPath map[string]string) (int, error) {
	moved, err := m.moveKeys(ctx, idsByPath)
	if err != nil {
		return 0, err
	}
	if err := m.client.Set(ctx, migrationMarkerKey, "1", defaultTTL).Err(); err != nil {
		return moved, fmt.Errorf("nodestatus migration marker set: %w", err)
	}
	return moved, nil
}

func (m *LegacyMigrator) moveKeys(ctx context.Context, idsByPath map[string]string) (int, error) {
	pipe := m.client.Pipeline()
	cmds := make([]*goredis.Cmd, 0, len(idsByPath))
	for path, id := range idsByPath {
		if path == "" || id == "" {
			continue
		}
		// Eval, а не Run: в конвейере EVALSHA на пустом кеше скриптов вернул бы
		// NOSCRIPT уже в Exec, где повторить через EVAL некому.
		cmds = append(cmds, m.script.Eval(ctx, pipe,
			[]string{LegacyKey(path), Key(id)}, defaultTTL.Milliseconds()))
	}
	if len(cmds) == 0 {
		return 0, nil
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, fmt.Errorf("nodestatus migrate legacy keys: %w", err)
	}
	moved := 0
	for _, c := range cmds {
		if v, err := c.Int(); err == nil && v == 1 {
			moved++
		}
	}
	return moved, nil
}
