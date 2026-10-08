package usecase

import (
	"context"
	"slices"
	"sort"
	"time"

	"nexus/internal/platform/metrics"
	"nexus/internal/web/usecase/port"
)

// topN — максимум строк в блоках top producers / top failures (§4.4 spec).
const topN = 10

// NodeProducer — строка top producers (§4.4): узел, число сообщений, доля.
//
// Team (§101.9) — слог команды из метки ряда. Пуст у строки рядов, собранных до
// §101, которые не удалось приписать узлу: путь принадлежит нескольким командам
// сразу, либо резолв недоступен.
//
// NodeID (§98.2) пуст, если строку не удалось сопоставить узлу по точной паре
// (Team, NodePath): такого узла уже нет, строка без команды, либо резолв
// недоступен. Интерфейс в этом случае показывает путь текстом.
type NodeProducer struct {
	NodePath string
	Team     string
	NodeID   string
	Produced uint64
	Share    float64 // доля от суммарного produced (0..1)
}

// NodeFailure — строка top failures (§4.4): узел, число ошибок, error rate.
// Team и NodeID — как у NodeProducer (§98.2, §101.9).
type NodeFailure struct {
	NodePath string
	Team     string
	NodeID   string
	Failed   uint64
	Rate     float64 // failed / total по узлу (0..1)
}

// KafkaByNodeResult — ответ §4.4: распределение нагрузки/ошибок по узлам.
type KafkaByNodeResult struct {
	TopProducers        []NodeProducer
	TopFailures         []NodeFailure
	PrometheusAvailable bool
}

// ByNode — топ-узлы за период (since, until]. Источник — Prometheus, ключ —
// пара меток team и node (§101.9):
//   - top producers — только async-сообщения (method="requestAsync", §113.5):
//     sync-трафика в Kafka нет;
//   - top failures — ошибки исходящих по всему трафику, sync и async: блок
//     отвечает «у кого болит», и до 1.41.0 показывал sync-узлы.
//
// Отказ любого из двух запросов — деградация всего ответа: пустой top failures
// при живом producers читался бы как «ошибок нет».
func (u *KafkaMonitorUsecase) ByNode(ctx context.Context, since, until time.Time) KafkaByNodeResult {
	res := KafkaByNodeResult{TopProducers: []NodeProducer{}, TopFailures: []NodeFailure{}}
	if u.prom == nil {
		return res
	}
	async, err := u.prom.KafkaNodeThroughput(ctx, since, until)
	if err != nil {
		u.logger.Warn("kafka by-node throughput failed", u.logger.Err(err))
		return res
	}
	all, err := u.prom.NodeFailures(ctx, since, until)
	if err != nil {
		u.logger.Warn("kafka by-node failures failed", u.logger.Err(err))
		return res
	}
	u.foldLegacyRows(ctx, async, all)
	res.TopProducers = topProducers(async)
	res.TopFailures = topFailures(all)
	res.PrometheusAvailable = true
	u.resolveNodeIDs(ctx, &res)
	return res
}

// foldLegacyRows приписывает ряды без команды (собраны до §101) узлу с тем же
// путём, если путь однозначен во всём инстансе (§101.9).
//
// Свёртка идёт ДО отбора top-N: она меняет числа и порядок строк. Ряды с
// неоднозначным путём остаются отдельной строкой без команды — экран покажет её
// текстом. Пути всех карт резолвятся одним запросом к базе; когда старые ряды
// уходят из окна, запроса нет вовсе.
func (u *KafkaMonitorUsecase) foldLegacyRows(ctx context.Context, maps ...map[port.NodeKey]port.NodeThroughput) {
	var paths []string
	for _, m := range maps {
		paths = append(paths, legacyRowPaths(m, nil)...)
	}
	slices.Sort(paths)
	paths = slices.Compact(paths)
	owners := resolveLegacyOwners(ctx, u.nodes, paths, u.logger)
	for _, m := range maps {
		for _, p := range legacyRowPaths(m, nil) {
			owner, ok := owners[p]
			if !ok {
				continue
			}
			legacy := legacyKey(p)
			m[owner.Key] = addCounters(m[owner.Key], m[legacy])
			delete(m, legacy)
		}
	}
}

// resolveNodeIDs проставляет id узлов строкам top-блоков (§98.2) по точной паре
// (команда, путь) (§101.9).
//
// Резолв идёт ПОСЛЕ отбора top-N: путей здесь не больше 2×topN, то есть один
// дешёвый запрос вместо резолва всей карты Prometheus. Ошибка резолва экран не
// роняет — ссылки украшение, без них таблица остаётся прежней.
func (u *KafkaMonitorUsecase) resolveNodeIDs(ctx context.Context, res *KafkaByNodeResult) {
	if u.nodes == nil {
		return
	}
	paths := topRowPaths(res)
	if len(paths) == 0 {
		return
	}
	refs, err := u.nodes.RefsByPaths(ctx, paths)
	if err != nil {
		u.logger.Debug("kafka by-node: node id resolve failed, rows stay plain text",
			u.logger.Int("paths", len(paths)), u.logger.Err(err))
		return
	}
	ids := make(map[port.NodeKey]string, len(refs))
	for _, r := range refs {
		ids[r.Key] = r.ID
	}
	for i := range res.TopProducers {
		p := &res.TopProducers[i]
		p.NodeID = ids[port.NodeKey{Team: p.Team, Path: p.NodePath}]
	}
	for i := range res.TopFailures {
		f := &res.TopFailures[i]
		f.NodeID = ids[port.NodeKey{Team: f.Team, Path: f.NodePath}]
	}
	// §51.9: расхождение «путей спросили N, узлов нашли M» — первое, что нужно
	// при разборе жалобы «строка не кликается».
	u.logger.Debug("kafka by-node: node ids resolved",
		u.logger.Int("paths", len(paths)),
		u.logger.Int("nodes", len(refs)))
}

// topRowPaths — пути строк top-блоков, по которым есть смысл спрашивать базу,
// каждый один раз. Строки без команды не спрашиваются: после свёртки это ряды
// до §101 с неоднозначным путём, и ссылка вела бы неизвестно куда. Заглушку
// <unresolved> (§94.8) Prometheus ставит запросам к несуществующим узлам —
// ссылка на «узел <unresolved>» была бы прямой ложью.
func topRowPaths(res *KafkaByNodeResult) []string {
	want := len(res.TopProducers) + len(res.TopFailures)
	seen := make(map[string]struct{}, want)
	paths := make([]string, 0, want)
	add := func(team, p string) {
		if team == "" || p == "" || p == metrics.NodeUnresolved {
			return
		}
		if _, ok := seen[p]; ok {
			return
		}
		seen[p] = struct{}{}
		paths = append(paths, p)
	}
	for _, r := range res.TopProducers {
		add(r.Team, r.NodePath)
	}
	for _, r := range res.TopFailures {
		add(r.Team, r.NodePath)
	}
	return paths
}

// topProducers сортирует узлы по числу отправленных сообщений (Out) и берёт
// top-N с долей от общего объёма.
func topProducers(m map[port.NodeKey]port.NodeThroughput) []NodeProducer {
	var total float64
	rows := make([]NodeProducer, 0, len(m))
	for key, t := range m {
		if t.Out <= 0 {
			continue
		}
		total += t.Out
		rows = append(rows, NodeProducer{NodePath: key.Path, Team: key.Team, Produced: f2u(t.Out)})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Produced > rows[j].Produced })
	if len(rows) > topN {
		rows = rows[:topN]
	}
	if total > 0 {
		for i := range rows {
			rows[i].Share = float64(rows[i].Produced) / total
		}
	}
	return rows
}

// topFailures сортирует узлы по числу ошибок (Errors>0) и берёт top-N с
// error-rate — долей проваленных среди исходящих. m — срез всего трафика
// (NodeFailures), а не async-срез producers.
//
// Знаменатель — Out, а не Out+Errors (§113.8): Out считает все исходящие
// Sender'а с любым статусом, ошибки — их подмножество, и до §113 они входили в
// знаменатель дважды (50 ошибок из 100 давали 33% вместо 50%). max с Errors —
// на случай, когда increase() экстраполировал ошибки выше исходящих: доля не
// превышает 100%.
func topFailures(m map[port.NodeKey]port.NodeThroughput) []NodeFailure {
	rows := make([]NodeFailure, 0)
	for key, t := range m {
		if t.Errors <= 0 {
			continue
		}
		rate := t.Errors / max(t.Out, t.Errors)
		rows = append(rows, NodeFailure{NodePath: key.Path, Team: key.Team, Failed: f2u(t.Errors), Rate: rate})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Failed > rows[j].Failed })
	if len(rows) > topN {
		rows = rows[:topN]
	}
	return rows
}

// KafkaTestResult — ответ §4.5: ping брокеров.
type KafkaTestResult struct {
	OK             bool
	Brokers        []port.BrokerPing
	KafkaAvailable bool
}

// Test — ping всех брокеров (§4.5). OK=true, если все брокеры ответили.
func (u *KafkaMonitorUsecase) Test(ctx context.Context) KafkaTestResult {
	res := KafkaTestResult{Brokers: []port.BrokerPing{}}
	if u.admin == nil {
		return res
	}
	pings, err := u.admin.Ping(ctx)
	if err != nil {
		u.logger.Warn("kafka test failed", u.logger.Err(err))
		return res
	}
	res.Brokers = pings
	res.KafkaAvailable = true
	res.OK = true
	for _, p := range pings {
		if !p.OK {
			res.OK = false
		}
	}
	return res
}
