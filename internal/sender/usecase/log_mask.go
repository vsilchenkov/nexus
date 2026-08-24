package usecase

import (
	"regexp"
	"sync/atomic"

	"nexus/internal/domain"
)

// compiledMask — один скомпилированный шаблон маскирования (§95).
type compiledMask struct {
	re   *regexp.Regexp
	repl string
}

// LogMaskProvider — атомарный держатель скомпилированного набора шаблонов
// маскирования логов узлов (§95). Сидится на старте чтением из PG, обновляется
// hot-reload'ом секции masking без рестарта Sender'а. Реализует chlog.LogMasker
// структурным совпадением (метод Mask). Безопасен для конкурентного доступа:
// Write вызывается из множества горутин доставки, Set — из reload-callback'а.
type LogMaskProvider struct {
	set atomic.Pointer[[]compiledMask]
}

// NewLogMaskProvider создаёт провайдер с пустым набором: до первого чтения PG
// маскирование — no-op (логи пишутся как есть). Никогда не отдаёт nil-набор.
func NewLogMaskProvider() *LogMaskProvider {
	p := &LogMaskProvider{}
	empty := []compiledMask{}
	p.set.Store(&empty)
	return p
}

// Set компилирует и атомарно заменяет набор. Некомпилируемый шаблон пропускается
// (в БД он валидируется на записи usecase'ом Web; здесь — вторая линия защиты от
// руками-правленной строки, чтобы один битый шаблон не ронял всё маскирование).
// Возвращает число применённых и пропущенных — вызывающий логирует итог.
func (p *LogMaskProvider) Set(patterns []domain.LogMaskPattern) (applied, skipped int) {
	compiled := make([]compiledMask, 0, len(patterns))
	for i := range patterns {
		re, err := regexp.Compile(patterns[i].Pattern)
		if err != nil {
			skipped++
			continue
		}
		compiled = append(compiled, compiledMask{re: re, repl: patterns[i].Replacement})
	}
	p.set.Store(&compiled)
	return len(compiled), skipped
}

// Mask применяет все шаблоны последовательно (в порядке, в котором их вернул
// reader — sort_order, затем created_at). Пустой набор или пустая строка —
// no-op. Порядок значим: замена одного шаблона может подготовить вход другому.
func (p *LogMaskProvider) Mask(s string) string {
	if s == "" {
		return s
	}
	set := p.set.Load()
	if set == nil {
		return s
	}
	for i := range *set {
		s = (*set)[i].re.ReplaceAllString(s, (*set)[i].repl)
	}
	return s
}
