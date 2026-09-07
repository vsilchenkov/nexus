package usecase

import (
	"context"
	"sort"
	"time"

	"nexus/internal/platform/metrics"
	"nexus/internal/web/usecase/port"
)

// topN — максимум строк в блоках top producers / top failures (§4.4 spec).
const topN = 10

// NodeProducer — строка top producers (§4.4): узел, число сообщений, доля.
//
// NodeID (§98.2) пуст, если путь не удалось однозначно сопоставить узлу:
// такого узла уже нет, путь принадлежит нескольким командам сразу, либо резолв
// недоступен. Интерфейс в этом случае показывает путь текстом.
type NodeProducer struct {
	NodePath string
	NodeID   string
	Produced uint64
	Share    float64 // доля от суммарного produced (0..1)
}

// NodeFailure — строка top failures (§4.4): узел, число ошибок, error rate.
// NodeID — как у NodeProducer (§98.2).
type NodeFailure struct {
	NodePath string
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

// ByNode — топ-узлы по числу async-сообщений и по ошибкам за период
// (since, until]. Источник — Prometheus (per-node throughput, метка node).
func (u *KafkaMonitorUsecase) ByNode(ctx context.Context, since, until time.Time) KafkaByNodeResult {
	res := KafkaByNodeResult{TopProducers: []NodeProducer{}, TopFailures: []NodeFailure{}}
	if u.prom == nil {
		return res
	}
	m, err := u.prom.NodeThroughput(ctx, since, until)
	if err != nil {
		u.logger.Warn("kafka by-node throughput failed", u.logger.Err(err))
		return res
	}
	res.TopProducers = topProducers(m)
	res.TopFailures = topFailures(m)
	res.PrometheusAvailable = true
	u.resolveNodeIDs(ctx, &res)
	return res
}

// resolveNodeIDs проставляет id узлов строкам top-блоков (§98.2).
//
// Резолв идёт ПОСЛЕ отбора top-N: путей здесь не больше 2×topN, то есть один
// дешёвый запрос вместо резолва всей карты Prometheus. Ошибка резолва экран не
// роняет — ссылки украшение, без них таблица остаётся прежней.
func (u *KafkaMonitorUsecase) resolveNodeIDs(ctx context.Context, res *KafkaByNodeResult) {
	if u.nodes == nil {
		return
	}
	want := len(res.TopProducers) + len(res.TopFailures)
	seen := make(map[string]struct{}, want)
	paths := make([]string, 0, want)
	add := func(p string) {
		// §94.8: запросы к несуществующим узлам схлопнуты Prometheus'ом в одну
		// метку-заглушку. Спрашивать про неё базу бессмысленно, а ссылка на
		// «узел <unresolved>» была бы прямой ложью.
		if p == "" || p == metrics.NodeUnresolved {
			return
		}
		if _, ok := seen[p]; ok {
			return
		}
		seen[p] = struct{}{}
		paths = append(paths, p)
	}
	for _, r := range res.TopProducers {
		add(r.NodePath)
	}
	for _, r := range res.TopFailures {
		add(r.NodePath)
	}
	if len(paths) == 0 {
		return
	}

	ids, err := u.nodes.IDsByPaths(ctx, paths)
	if err != nil {
		u.logger.Debug("kafka by-node: node id resolve failed, rows stay plain text",
			u.logger.Int("paths", len(paths)), u.logger.Err(err))
		return
	}
	for i := range res.TopProducers {
		res.TopProducers[i].NodeID = ids[res.TopProducers[i].NodePath]
	}
	for i := range res.TopFailures {
		res.TopFailures[i].NodeID = ids[res.TopFailures[i].NodePath]
	}
	// §51.9: расхождение «путей спросили N, узнали M» — первое, что нужно при
	// разборе жалобы «строка не кликается».
	u.logger.Debug("kafka by-node: node ids resolved",
		u.logger.Int("paths", len(paths)),
		u.logger.Int("resolved", len(ids)))
}

// topProducers сортирует узлы по числу отправленных сообщений (Out) и берёт
// top-N с долей от общего объёма.
func topProducers(m map[string]port.NodeThroughput) []NodeProducer {
	var total float64
	rows := make([]NodeProducer, 0, len(m))
	for node, t := range m {
		if t.Out <= 0 {
			continue
		}
		total += t.Out
		rows = append(rows, NodeProducer{NodePath: node, Produced: f2u(t.Out)})
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
// error-rate (errors/(out+errors) — доля проваленных среди обработанных).
func topFailures(m map[string]port.NodeThroughput) []NodeFailure {
	rows := make([]NodeFailure, 0)
	for node, t := range m {
		if t.Errors <= 0 {
			continue
		}
		var rate float64
		if denom := t.Out + t.Errors; denom > 0 {
			rate = t.Errors / denom
		}
		rows = append(rows, NodeFailure{NodePath: node, Failed: f2u(t.Errors), Rate: rate})
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
