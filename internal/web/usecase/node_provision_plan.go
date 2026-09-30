package usecase

import "nexus/internal/domain"

// tableProvisionMode — что делать с таблицей логов узла при его появлении
// (§19.5). Решение вынесено в чистую функцию, потому что его принимают двое:
// сохранение узла (NodeUsecase.provisionTable) и восстановление конфигурации
// (§110.6). Разойдись они — импорт создал бы таблицу там, где форма узла её
// не создаёт (внешняя таблица §64), или наоборот.
type tableProvisionMode uint8

const (
	// tableProvisionSkip — таблицу не создавать.
	tableProvisionSkip tableProvisionMode = iota
	// tableProvisionTemplate — строго по шаблону узла: без шаблона или
	// ClickHouse узел сохранять нельзя.
	tableProvisionTemplate
	// tableProvisionDefault — по шаблону каталога «по умолчанию»: логирование
	// включено, шаблон не выбран.
	tableProvisionDefault
)

// tableProvisionPlan — решение и, для пропуска, причина (для debug-строки;
// пусто — пропуск штатный и в логе не нужен).
type tableProvisionPlan struct {
	mode       tableProvisionMode
	skipReason string
}

// planTableProvision решает судьбу таблицы логов узла:
//   - имя пустое — пропуск;
//   - имя невалидно — пропуск: такое возможно только у узла с выключенным
//     логированием (черновик «nexus_x.» из дефолта формы), и CREATE завалил бы
//     сохранение узла, ради которого логи и выключали;
//   - external_table — пропуск (§64): таблицей владеет оператор или посторонний
//     сервис-писатель, создавать её Nexus не вправе;
//   - явно выбран шаблон — строго по нему;
//   - логирование включено — по шаблону по умолчанию;
//   - иначе — пропуск (legacy/ручная таблица).
func planTableProvision(n *domain.Node) tableProvisionPlan {
	switch {
	case n.ClickHouseTable == "":
		return tableProvisionPlan{mode: tableProvisionSkip}
	case !domain.IsValidCHTableName(n.ClickHouseTable):
		return tableProvisionPlan{mode: tableProvisionSkip, skipReason: "invalid table name (logging disabled draft)"}
	case n.ExternalTable:
		return tableProvisionPlan{mode: tableProvisionSkip, skipReason: "external table"}
	case n.ClickHouseTemplateID != "":
		return tableProvisionPlan{mode: tableProvisionTemplate}
	case n.LoggingEnabled:
		return tableProvisionPlan{mode: tableProvisionDefault}
	default:
		return tableProvisionPlan{mode: tableProvisionSkip}
	}
}
