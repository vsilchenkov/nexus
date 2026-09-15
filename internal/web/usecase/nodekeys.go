package usecase

import (
	"context"
	"slices"

	"nexus/internal/platform/logging"
	"nexus/internal/platform/metrics"
	"nexus/internal/web/usecase/port"
)

// Ключи per-node рядов Prometheus (§101.9).
//
// Ряд несёт пару меток (team, node), узел в PostgreSQL — team_id и путь. Слог
// узла берётся из справочника команд. Ряды, собранные до §101 (без метки team),
// приписываются узлу только при однозначном во всём инстансе пути и только для
// счётчиков: p95 и гаудж не складываются.

// teamSlugsByID — слог команды по её id. Без справочника или при его сбое —
// пустая карта: узел без слога получает нули, а не ряды одноимённого узла
// другой команды.
func teamSlugsByID(ctx context.Context, teams port.TeamRepo, logger logging.Logger) map[string]string {
	if teams == nil {
		logger.Debug("node keys: no team directory, prometheus rows are not matched to nodes")
		return map[string]string{}
	}
	list, err := teams.List(ctx)
	if err != nil {
		logger.Warn("node keys: list teams failed, prometheus rows are not matched to nodes", logger.Err(err))
		return map[string]string{}
	}
	out := make(map[string]string, len(list))
	for _, t := range list {
		out[t.ID] = t.Slug
	}
	return out
}

// legacyKey — ключ ряда до §101 с данным путём.
func legacyKey(path string) port.NodeKey { return port.NodeKey{Path: path} }

// uniquePathOwners — путь → узел для путей, которым во всём инстансе
// соответствует ровно один узел. refs обязан быть ПОЛНЫМ списком узлов с этими
// путями: по неполному однозначность не доказать.
func uniquePathOwners(refs []port.NodeKeyRef) map[string]port.NodeKeyRef {
	count := make(map[string]int, len(refs))
	for _, r := range refs {
		count[r.Key.Path]++
	}
	out := make(map[string]port.NodeKeyRef, len(refs))
	for _, r := range refs {
		if count[r.Key.Path] == 1 {
			out[r.Key.Path] = r
		}
	}
	return out
}

// resolveLegacyOwners — однозначные владельцы путей, у которых в окне есть ряды
// без команды. Пока окно захватывает выкат, это один запрос к PostgreSQL; когда
// старые ряды из окна уходят — ни одного.
//
// Fail-closed: без резолвера или при его сбое карта пуста, и ряды до §101 не
// приписываются никому — недосчитать в окне выката лучше, чем отдать трафик
// одноимённого узла чужой команде.
func resolveLegacyOwners(ctx context.Context, nodes port.NodeKeyResolver, paths []string, logger logging.Logger) map[string]port.NodeKeyRef {
	if len(paths) == 0 {
		return nil
	}
	if nodes == nil {
		logger.Debug("node keys: legacy rows left unattributed, no node resolver",
			logger.Int("paths", len(paths)))
		return nil
	}
	refs, err := nodes.RefsByPaths(ctx, paths)
	if err != nil {
		logger.Warn("node keys: legacy rows left unattributed, node resolve failed",
			logger.Int("paths", len(paths)), logger.Err(err))
		return nil
	}
	owners := uniquePathOwners(refs)
	// §51.9: «рядов без команды N, однозначных M» — первое, что нужно при разборе
	// расхождения чисел в окне выката.
	logger.Debug("node keys: legacy rows owners resolved",
		logger.Int("paths", len(paths)), logger.Int("unique", len(owners)))
	return owners
}

// legacyRowPaths — отсортированные пути рядов без команды. Заглушка
// <unresolved> (§94.8) узлом не является и в список не попадает. want (может
// быть nil) дополнительно сужает список, например до путей узлов скоупа.
func legacyRowPaths[V any](m map[port.NodeKey]V, want func(path string) bool) []string {
	var out []string
	for k := range m {
		if k.Team != "" || k.Path == "" || k.Path == metrics.NodeUnresolved {
			continue
		}
		if want != nil && !want(k.Path) {
			continue
		}
		out = append(out, k.Path)
	}
	slices.Sort(out)
	return out
}

// withLegacy — значение узла по ключу плюс ряд до §101 с тем же путём, если
// путь однозначен и принадлежит именно этому ключу.
//
// Ключ без слога не читается вовсе: это ряд до §101, и без проверки
// однозначности он мог принадлежать любому из одноимённых узлов.
func withLegacy[V any](m map[port.NodeKey]V, key port.NodeKey, owners map[string]port.NodeKeyRef, add func(V, V) V) V {
	var v V
	if key.Team == "" {
		return v
	}
	v = m[key]
	if o, ok := owners[key.Path]; ok && o.Key == key {
		if legacy, ok := m[legacyKey(key.Path)]; ok {
			v = add(v, legacy)
		}
	}
	return v
}

// addCounters складывает счётчики ряда до §101 со счётчиками узла. p95
// остаётся узла: перцентили двух распределений не складываются.
func addCounters(dst, legacy port.NodeThroughput) port.NodeThroughput {
	dst.In += legacy.In
	dst.Out += legacy.Out
	dst.Errors += legacy.Errors
	return dst
}

func addFloat(a, b float64) float64 { return a + b }
