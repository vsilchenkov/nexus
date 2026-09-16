package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/clock"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// §84.7: исход последнего вызова узла для шапки страницы. Правило приоритета
// (Redis → Prometheus) общее с рабочим столом и живёт в LastOutcomeResolver:
// две копии одного правила разошлись бы, и таблица показывала бы одно, а
// страница узла другое.

func runtimeUC(status *fakeNodeStatus, prom *fakeProm, node *domain.Node) *NodeRuntimeUsecase {
	log := logging.NewNoop()
	// Интерфейсные переменные, а не конкретные типы: typed-nil в non-nil
	// интерфейсе — та же грабля, что описана в app.go.
	var st = interfaceOrNilStatus(status)
	var pm = interfaceOrNilProm(prom)
	return NewNodeRuntimeUsecase(
		&fakeNodeRepo{node: node},
		NewLastOutcomeResolver(st, pm, slugDirectory(map[string]string{"team-a": "alpha", "team-b": "beta"}), log),
		clock.NewFake(time.Unix(1786114275, 0).UTC()),
		log,
	)
}

func interfaceOrNilStatus(s *fakeNodeStatus) port.NodeStatusReader {
	if s == nil {
		return nil
	}
	return s
}

func interfaceOrNilProm(p *fakeProm) port.PromMetrics {
	if p == nil {
		return nil
	}
	return p
}

func TestNodeRuntimeUsecase_State(t *testing.T) {
	t.Parallel()
	node := &domain.Node{ID: "n1", Path: "acs_sigur", TeamID: "team-a"}
	own := port.NodeKey{Team: "alpha", Path: "acs_sigur"}

	t.Run("Redis приоритетнее Prometheus", func(t *testing.T) {
		t.Parallel()
		// Redis говорит degraded, Prometheus — down (гаудж 2). Побеждает Redis:
		// он персистентный и переживает рестарт Sender'а (§46).
		uc := runtimeUC(
			&fakeNodeStatus{m: map[string]domain.NodeOutcome{"n1": domain.NodeOutcomeDegraded}},
			&fakeProm{lastErrs: map[port.NodeKey]float64{own: 2}},
			node,
		)
		got, err := uc.State(context.Background(), "n1", "team-a")
		require.NoError(t, err)
		require.True(t, got.Available)
		require.Equal(t, domain.NodeOutcomeDegraded, got.LastOutcome)
		require.Equal(t, OutcomeSourceRedis, got.Source)
	})

	t.Run("без Redis работает Prometheus", func(t *testing.T) {
		t.Parallel()
		uc := runtimeUC(nil, &fakeProm{lastErrs: map[port.NodeKey]float64{own: 2}}, node)
		got, err := uc.State(context.Background(), "n1", "team-a")
		require.NoError(t, err)
		require.True(t, got.Available)
		require.Equal(t, domain.NodeOutcomeDown, got.LastOutcome)
		require.Equal(t, OutcomeSourcePrometheus, got.Source)
	})

	// Ключевое правило §84.7: «неизвестно» НЕ красится в ok. Зелёная плашка при
	// неизвестном состоянии успокаивает ровно тогда, когда этого делать нельзя.
	t.Run("нет ни Redis, ни Prometheus → available=false, а не ok", func(t *testing.T) {
		t.Parallel()
		uc := runtimeUC(nil, nil, node)
		got, err := uc.State(context.Background(), "n1", "team-a")
		require.NoError(t, err)
		require.False(t, got.Available)
		require.Equal(t, OutcomeSourceNone, got.Source)
	})

	t.Run("узел есть в источниках, но не по этому пути → available=false", func(t *testing.T) {
		t.Parallel()
		uc := runtimeUC(
			&fakeNodeStatus{m: map[string]domain.NodeOutcome{"другой": domain.NodeOutcomeDown}},
			&fakeProm{lastErrs: map[port.NodeKey]float64{{Team: "alpha", Path: "другой"}: 2}},
			node,
		)
		got, err := uc.State(context.Background(), "n1", "team-a")
		require.NoError(t, err)
		require.False(t, got.Available, "молчание источников про этот узел — не ok")
	})

	// §101.9: до §101 гаудж ключевался путём, и одноимённый узел соседней
	// команды красил бейдж этого узла своим исходом.
	t.Run("гаудж одноимённого узла другой команды не подменяет исход", func(t *testing.T) {
		t.Parallel()
		uc := runtimeUC(nil, &fakeProm{lastErrs: map[port.NodeKey]float64{{Team: "beta", Path: "acs_sigur"}: 2}}, node)
		got, err := uc.State(context.Background(), "n1", "team-a")
		require.NoError(t, err)
		require.False(t, got.Available)
	})

	// §101.9: гаудж старого процесса Sender (без team) пропадает с его
	// рестартом; приписывать исход по одному пути нельзя.
	t.Run("гаудж без команды не приписывается узлу", func(t *testing.T) {
		t.Parallel()
		uc := runtimeUC(nil, &fakeProm{lastErrs: map[port.NodeKey]float64{{Path: "acs_sigur"}: 2}}, node)
		got, err := uc.State(context.Background(), "n1", "team-a")
		require.NoError(t, err)
		require.False(t, got.Available)
	})

	// Ошибка источника не должна ронять запрос: она лишь исключает источник.
	t.Run("ошибка Redis деградирует на Prometheus, а не в 500", func(t *testing.T) {
		t.Parallel()
		uc := runtimeUC(
			&fakeNodeStatus{err: errors.New("redis down")},
			&fakeProm{lastErrs: map[port.NodeKey]float64{own: 1}},
			node,
		)
		got, err := uc.State(context.Background(), "n1", "team-a")
		require.NoError(t, err)
		require.True(t, got.Available)
		require.Equal(t, domain.NodeOutcomeDegraded, got.LastOutcome)
		require.Equal(t, OutcomeSourcePrometheus, got.Source)
	})

	t.Run("чужая команда неотличима от несуществующего узла", func(t *testing.T) {
		t.Parallel()
		uc := runtimeUC(nil, nil, node)
		_, err := uc.State(context.Background(), "n1", "team-b")
		require.ErrorIs(t, err, domain.ErrNodeNotFound)
	})

	t.Run("пустой teamID пропускает scope-проверку", func(t *testing.T) {
		t.Parallel()
		uc := runtimeUC(nil, nil, node)
		_, err := uc.State(context.Background(), "n1", "")
		require.NoError(t, err)
	})
}

// §84.7: рефакторинг applyLastOutcomes на общий резолвер обязан сохранить
// прежнее поведение рабочего стола — включая трактовку «неизвестно» как ok.
// Там у бейджа нет пустого состояния: каждая строка таблицы обязана иметь тон.
func TestLastOutcomeResolver_UnknownIsOKForOverview(t *testing.T) {
	t.Parallel()
	r := NewLastOutcomeResolver(nil, nil, nil, logging.NewNoop())
	got := r.Resolve(context.Background(), time.Now(), []NodeRef{{ID: "a", Path: "a"}, {ID: "b", Path: "b"}})
	require.Len(t, got, 2)
	for _, p := range []string{"a", "b"} {
		require.False(t, got[p].Known)
		require.Equal(t, domain.NodeOutcomeOK, got[p].Outcome,
			"значение по умолчанию ok — на нём держится таблица рабочего стола")
	}
}

// §101.6: узел без id в карту не попадает — ключа Redis у него нет, а по пути
// одноимённые узлы разных команд неразличимы.
func TestLastOutcomeResolver_SkipsNodeWithoutID(t *testing.T) {
	t.Parallel()
	st := &fakeNodeStatus{m: map[string]domain.NodeOutcome{"n1": domain.NodeOutcomeDown}}
	r := NewLastOutcomeResolver(st, nil, nil, logging.NewNoop())
	got := r.Resolve(context.Background(), time.Now(),
		[]NodeRef{{ID: "n1", Path: "telegram"}, {Path: "telegram"}})
	require.Equal(t, map[string]Outcome{
		"n1": {Outcome: domain.NodeOutcomeDown, Source: OutcomeSourceRedis, Known: true},
	}, got)
	require.Equal(t, []string{"n1"}, st.asked)
}

func TestLastOutcomeResolver_EmptyPaths(t *testing.T) {
	t.Parallel()
	r := NewLastOutcomeResolver(nil, nil, nil, logging.NewNoop())
	require.Empty(t, r.Resolve(context.Background(), time.Now(), nil))
}
