package usecase

import (
	"cmp"
	"encoding/json"
	"slices"

	"nexus/internal/domain"
)

// RevertFieldStatus — статус поля строки журнала относительно текущего узла
// (§108.3).
type RevertFieldStatus string

const (
	// RevertFieldActual — сейчас == «Стало»: отмена вернёт «Было».
	RevertFieldActual RevertFieldStatus = "actual"
	// RevertFieldOverwritten — сейчас ≠ «Стало» и ≠ «Было»: поле перебито позже.
	RevertFieldOverwritten RevertFieldStatus = "overwritten"
	// RevertFieldAlready — сейчас == «Было»: отменять нечего.
	RevertFieldAlready RevertFieldStatus = "already"
	// RevertFieldInapplicable — поле не относится к типу узла (rmq_*/pull_* у
	// не-pull узла): репозиторий пишет его NULL, значение не сохранится.
	RevertFieldInapplicable RevertFieldStatus = "inapplicable"
	// RevertFieldUnrecoverable — значение в журнале не восстановимо: сводка
	// шаблона ответа, нечитаемое значение, неизвестный ключ, удалённый узел.
	RevertFieldUnrecoverable RevertFieldStatus = "unrecoverable"
	// RevertFieldSecret — креды: в журнале только факт изменения.
	RevertFieldSecret RevertFieldStatus = "secret"
)

// RevertWarningCredentials — поле связано с кредами, изменёнными в той же
// строке: вернуть его можно, а креды — нет (§108.3).
const RevertWarningCredentials = "credentials"

// RevertState — состояние строки журнала (§108.3).
type RevertState string

const (
	RevertStateActual RevertState = "actual"
	RevertStateStale  RevertState = "stale"
	RevertStateGone   RevertState = "gone"
	// RevertStateNone — сравнивать нечего (только креды или неприменимые
	// поля): строку не подсвечивать, кнопку не показывать.
	RevertStateNone RevertState = "none"
)

// RevertField — поле строки журнала с текущим значением узла.
type RevertField struct {
	Name    string
	Before  any
	After   any
	Current any // nil у секрета и у удалённого узла
	Status  RevertFieldStatus
	Warning string
	// comparable/differs — участвует ли поле в неактуальности строки и
	// отличается ли текущее значение от «Стало». Отдельно от Status: сводка
	// шаблона ответа не отменяется, но неактуальность по ней видна.
	comparable, differs bool
}

// Revertible — поле можно выбрать для отмены.
func (f RevertField) Revertible() bool {
	return f.Status == RevertFieldActual || f.Status == RevertFieldOverwritten
}

// RevertMark — разметка строки выдачи журнала для администратора (§108.7).
type RevertMark struct {
	State      RevertState
	Revertible bool
	// Overwritten — текущие значения полей, отличающихся от «Стало» (для
	// подсказки «сейчас: X» у зачёркнутого значения).
	Overwritten map[string]any
}

// isRevertMetaKey — служебные ключи details, которые полями узла не являются.
func isRevertMetaKey(k string) bool {
	return k == "reverted_from" || k == "reverted_from_at" || k == "cleared_incompatible_fields"
}

// classifyEntry сопоставляет diff строки журнала с текущим узлом cur (nil —
// узел удалён). Порядок полей — порядок реестра, затем секреты, сводка
// шаблона и неизвестные ключи.
func classifyEntry(details map[string]any, cur *domain.Node) []RevertField {
	fields := make([]RevertField, 0, len(details))
	for _, name := range orderedDetailKeys(details) {
		fields = append(fields, classifyField(name, details[name], cur))
	}
	markCredentialWarnings(fields)
	return fields
}

func classifyField(name string, raw any, cur *domain.Node) RevertField {
	if s, ok := raw.(string); ok && s == auditSecretChanged && isSecretField(name) {
		return RevertField{Name: name, Status: RevertFieldSecret}
	}
	before, after, ok := auditBeforeAfter(raw)
	rf := RevertField{Name: name, Before: before, After: after, Status: RevertFieldUnrecoverable}
	if !ok || cur == nil {
		return rf
	}
	if name == auditKeyAckSpec {
		rf.Current = ackSpecAudit(cur.AsyncAck)
		rf.comparable, rf.differs = true, rf.Current != after
		return rf
	}
	f, known := lookupNodeField(name)
	if !known {
		return rf
	}
	if !f.appliesTo(cur) {
		rf.Status = RevertFieldInapplicable
		return rf
	}
	rf.Status = compareWithCurrent(f, cur, before, after)
	if rf.Status != RevertFieldUnrecoverable {
		rf.Current = f.get(cur)
		rf.comparable, rf.differs = true, rf.Status != RevertFieldActual
	}
	return rf
}

// compareWithCurrent — статус поля реестра: значения «Было»/«Стало» против
// текущего. Нечитаемое значение (null, дробь в int32) — unrecoverable.
func compareWithCurrent(f nodeField, cur *domain.Node, before, after any) RevertFieldStatus {
	isAfter, errA := f.holds(cur, after)
	isBefore, errB := f.holds(cur, before)
	switch {
	case errA != nil || errB != nil:
		return RevertFieldUnrecoverable
	case isAfter:
		return RevertFieldActual
	case isBefore:
		return RevertFieldAlready
	default:
		return RevertFieldOverwritten
	}
}

// markCredentialWarnings помечает отменяемые поля, связанные с кредами,
// изменёнными в той же строке.
func markCredentialWarnings(fields []RevertField) {
	coupled := map[string]bool{}
	for _, s := range nodeSecretRegistry() {
		if slices.ContainsFunc(fields, func(f RevertField) bool {
			return f.Name == s.name && f.Status == RevertFieldSecret
		}) {
			for _, c := range s.coupled {
				coupled[c] = true
			}
		}
	}
	for i := range fields {
		if coupled[fields[i].Name] && fields[i].Revertible() {
			fields[i].Warning = RevertWarningCredentials
		}
	}
}

// revertState — состояние строки и есть ли в ней что отменять.
func revertState(fields []RevertField, cur *domain.Node) (RevertState, bool) {
	if cur == nil {
		return RevertStateGone, false
	}
	state := RevertStateNone
	for _, f := range fields {
		if !f.comparable {
			continue
		}
		if f.differs {
			state = RevertStateStale
			break
		}
		state = RevertStateActual
	}
	return state, slices.ContainsFunc(fields, RevertField.Revertible)
}

// markOf — разметка строки для выдачи журнала.
func markOf(fields []RevertField, cur *domain.Node) RevertMark {
	state, revertible := revertState(fields, cur)
	m := RevertMark{State: state, Revertible: revertible}
	for _, f := range fields {
		if f.differs {
			if m.Overwritten == nil {
				m.Overwritten = map[string]any{}
			}
			m.Overwritten[f.Name] = f.Current
		}
	}
	return m
}

// auditBeforeAfter достаёт пару «до/после» из значения details. Через JSON:
// из JSONB приходит map[string]any с float64, из памяти — int32 и
// map[string]string (SetStatus); разбор должен вести себя одинаково.
func auditBeforeAfter(raw any) (before, after any, ok bool) {
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, nil, false
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return nil, nil, false
	}
	before, hasBefore := m["before"]
	after, hasAfter := m["after"]
	return before, after, hasBefore && hasAfter
}

func isSecretField(name string) bool {
	return slices.ContainsFunc(nodeSecretRegistry(), func(s nodeSecret) bool { return s.name == name })
}

// orderedDetailKeys — ключи details без служебных, в порядке показа.
func orderedDetailKeys(details map[string]any) []string {
	registry, secrets := nodeFieldRegistry(), nodeSecretRegistry()
	order := make([]string, 0, len(registry)+len(secrets)+1)
	for _, f := range registry {
		order = append(order, f.name)
	}
	for _, s := range secrets {
		order = append(order, s.name)
	}
	order = append(order, auditKeyAckSpec)
	rank := func(k string) int {
		if i := slices.Index(order, k); i >= 0 {
			return i
		}
		return len(order)
	}
	keys := make([]string, 0, len(details))
	for k := range details {
		if !isRevertMetaKey(k) {
			keys = append(keys, k)
		}
	}
	slices.SortFunc(keys, func(a, b string) int {
		return cmp.Or(cmp.Compare(rank(a), rank(b)), cmp.Compare(a, b))
	})
	return keys
}
