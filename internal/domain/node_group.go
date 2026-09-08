package domain

import (
	"strings"
	"time"
)

// NodeGroup — запись справочника групп узлов (§99 ТЗ). Справочник общий для
// инсталляции (без team-скоупа, как заголовки §24 и разрешённые хосты §23):
// одна и та же прикладная группа встречается в разных командах, а в сквозном
// режиме §86 команднозависимый справочник дал бы на экране несколько разных
// групп с одинаковым именем.
//
// UsageCount вычисляется on-read (COUNT узлов с этим group_id), в БД не
// хранится — тот же приём, что в §24.1: хранимый счётчик требует триггера, а
// триггер даёт класс ошибок рассинхронизации.
type NodeGroup struct {
	ID          string
	Name        string
	Description string
	// SortOrder — порядок вывода группы на экране «Узлы»: меньше — выше.
	SortOrder  int32
	UsageCount int
	CreatedBy  string
	UpdatedBy  string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// MaxNodeGroupSortOrder — верхняя граница порядка сортировки. Совпадает с
// CHECK-ограничением миграции 0042: значение вне диапазона обязано отлетать в
// домене с понятной ошибкой, а не 23514 из СУБД.
const MaxNodeGroupSortOrder = 100000

// Validate проверяет длины и диапазон порядка. Имя — свободный текст (группы
// называет человек: «1С Обмен», «Курьерские службы»), поэтому формат, в отличие
// от заголовков §24.2, не ограничивается; проверяется только длина ПОСЛЕ
// обрезки пробелов, иначе имя из одних пробелов прошло бы как непустое.
//
// Та же проверка выполняется клиентом перед показом «Сохранить» (§99.3), чтобы
// UI нельзя было обойти.
func (g *NodeGroup) Validate() error {
	name := strings.TrimSpace(g.Name)
	if l := len([]rune(name)); l < 1 || l > 100 {
		return ErrNodeGroupNameLength
	}
	if len([]rune(g.Description)) > 500 {
		return ErrNodeGroupDescriptionLength
	}
	if g.SortOrder < 0 || g.SortOrder > MaxNodeGroupSortOrder {
		return ErrNodeGroupSortOrderRange
	}
	return nil
}
