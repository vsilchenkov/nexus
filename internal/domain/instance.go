package domain

import "regexp"

// InstanceID — идентификатор ноды (инстанса) Nexus, §70.1.
//
// Нода — самостоятельное развёртывание шины со своими PostgreSQL/Redis/Kafka,
// которое пишет логи в ОБЩИЙ с другими нодами ClickHouse. Идентификатор задаётся
// в конфиге (`instance.id`) и служит единственным различителем ноды: суффиксом
// имени БД ClickHouse (§70.2), тегом Sentry, меткой метрик и атрибутом логов
// (§70.7). Заводить отдельные идентификаторы для наблюдаемости нельзя — разъедутся.
//
// Пустое значение валидно и означает «нода, существовавшая до §70»: имена БД
// остаются прежними (`nexus_<slug>`), теги и метки не добавляются. Это условие
// обратной совместимости, а не временное состояние.
type InstanceID string

// instanceIDPattern — 1..8 символов, первый обязательно буква. Подчёркивание
// запрещено намеренно: имя БД собирается как "nexus_<id>_<slug>", а слаг команды
// сам допускает '_' — с '_' внутри идентификатора граница между ним и слагом
// перестала бы читаться однозначно (§70.2).
var instanceIDPattern = regexp.MustCompile(`^[a-z][a-z0-9]{0,7}$`)

// chDatabasePrefix — общее начало имени БД любой команды на любой ноде.
const chDatabasePrefix = "nexus_"

// maxCHDatabaseSuffixLen — сколько символов допустимо после "nexus_" в имени БД
// команды. Совпадает с CHECK teams_ch_database_format (миграция 0029) и с
// teamCHDatabasePattern.
const maxCHDatabaseSuffixLen = 40

// Validate проверяет формат идентификатора. Пустой валиден (см. тип).
func (i InstanceID) Validate() error {
	if i == "" {
		return nil
	}
	if !instanceIDPattern.MatchString(string(i)) {
		return ErrInstanceIDFormat
	}
	return nil
}

// IsZero — нода без идентификатора (та, что существовала до §70).
func (i InstanceID) IsZero() bool { return i == "" }

func (i InstanceID) String() string { return string(i) }

// CHDatabasePrefix — префикс имён БД этой ноды: "nexus_" либо "nexus_<id>_".
// Используется UI (§70.8) для предпросмотра имени БД при создании команды.
func (i InstanceID) CHDatabasePrefix() string {
	if i.IsZero() {
		return chDatabasePrefix
	}
	return chDatabasePrefix + string(i) + "_"
}

// CHDatabase — каноническое имя ClickHouse-БД команды на этой ноде:
// "nexus_<slug>" без идентификатора, "nexus_<id>_<slug>" с ним. Единственная
// точка сборки имени БД во всём коде (§70.2).
func (i InstanceID) CHDatabase(slug string) string {
	return i.CHDatabasePrefix() + slug
}

// OwnsCHDatabase — принадлежит ли имя БД пространству имён этой ноды по одному
// лишь имени. ВНИМАНИЕ: это НЕ проверка владения — слаг команды допускает '_',
// поэтому нода без идентификатора с командой "kz_edo" и нода "kz" с командой
// "edo" дают одно и то же имя "nexus_kz_edo". Владение определяет только маркер
// в самой БД (§70.3); этот метод пригоден лишь для дешёвых предварительных
// проверок и диагностических сообщений.
func (i InstanceID) OwnsCHDatabase(db string) bool {
	prefix := i.CHDatabasePrefix()
	return len(db) > len(prefix) && db[:len(prefix)] == prefix
}

// MaxTeamSlugLen — сколько символов остаётся слагу команды после префикса.
// Без идентификатора — 40, с "kz" — 37 (40 − len("kz_")).
func (i InstanceID) MaxTeamSlugLen() int {
	return maxCHDatabaseSuffixLen - (len(i.CHDatabasePrefix()) - len(chDatabasePrefix))
}

// ValidateTeamSlug проверяет, что слаг помещается в бюджет имени БД этой ноды.
// Вызывается ПЕРЕД Team.Validate: иначе слишком длинный слаг упирался бы в
// ErrTeamCHDatabaseFormat («ch_database must match …»), из которого оператору
// непонятно, что чинить (§70.2).
func (i InstanceID) ValidateTeamSlug(slug string) error {
	if len(slug) > i.MaxTeamSlugLen() {
		return ErrTeamSlugTooLongForInstance
	}
	return nil
}

// CHDatabaseForSlug — имя БД команды для ноды без идентификатора. Сохранён как
// обёртка над InstanceID("").CHDatabase: его зовут тесты и вспомогательный код,
// которым идентификатор ноды не нужен.
func CHDatabaseForSlug(slug string) string {
	return InstanceID("").CHDatabase(slug)
}
