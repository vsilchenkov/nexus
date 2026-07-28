package clickhouse

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	chgo "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
)

// newTestGuard — Guard без ClickHouse-соединения. Все проверки ниже обязаны
// отвечать из кеша: если кеш не сработает, probe упрётся в nil-провайдер и тест
// это увидит.
func newTestGuard(t *testing.T, id string, opts ...GuardOption) *Guard {
	t.Helper()
	return NewGuard(nil, domain.InstanceID(id), logging.NewNoop(), opts...)
}

func TestGuard_VerdictFor(t *testing.T) {
	t.Parallel()

	g := newTestGuard(t, "kz")
	claimed := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)

	tests := []struct {
		name   string
		owners []Owner
		want   Verdict
	}{
		{
			name:   "пустой маркер — неопределённость, а не «ничей»",
			owners: nil,
			want:   VerdictUnknown,
		},
		{
			name:   "наш идентификатор",
			owners: []Owner{{InstanceID: "kz", ClaimedAt: claimed}},
			want:   VerdictOwned,
		},
		{
			name:   "чужой идентификатор",
			owners: []Owner{{InstanceID: "edo", ClaimedAt: claimed}},
			want:   VerdictForeign,
		},
		{
			name:   "нода без идентификатора — чужая для kz",
			owners: []Owner{{InstanceID: "", ClaimedAt: claimed}},
			want:   VerdictForeign,
		},
		{
			name: "несколько разных идентификаторов — расщепление владения",
			owners: []Owner{
				{InstanceID: "kz", ClaimedAt: claimed},
				{InstanceID: "edo", ClaimedAt: claimed.Add(time.Second)},
			},
			want: VerdictConflict,
		},
		{
			name: "дубли своей строки конфликтом не считаются",
			owners: []Owner{
				{InstanceID: "kz", ClaimedAt: claimed},
				{InstanceID: "kz", ClaimedAt: claimed.Add(time.Second)},
			},
			want: VerdictOwned,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, g.verdictFor(tt.owners))
		})
	}
}

// Нода без идентификатора считает своим маркер с пустым instance_id — именно
// поэтому гейт первого запуска (§70.5) опирается на признак свежей PostgreSQL,
// а не на маркер: два таких развёртывания неразличимы.
func TestGuard_EmptyInstanceOwnsEmptyMarker(t *testing.T) {
	t.Parallel()

	g := newTestGuard(t, "")
	assert.Equal(t, VerdictOwned, g.verdictFor([]Owner{{InstanceID: ""}}))
	assert.Equal(t, VerdictForeign, g.verdictFor([]Owner{{InstanceID: "kz"}}))
}

func TestGuard_CacheTTL(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	g := newTestGuard(t, "kz",
		WithOwnerTTL(5*time.Minute, 30*time.Second),
		WithOwnerClock(func() time.Time { return clock() }))

	g.store("nexus_kz_default", VerdictOwned, nil)
	g.store("nexus_default", VerdictForeign, &Owner{InstanceID: ""})

	e, ok := g.cached("nexus_kz_default")
	require.True(t, ok)
	assert.Equal(t, VerdictOwned, e.verdict)

	// Отрицательный вердикт живёт меньше: исправление конфигурации должно
	// подхватываться быстро.
	now = now.Add(31 * time.Second)
	if _, ok := g.cached("nexus_default"); ok {
		t.Error("отрицательный вердикт обязан протухнуть через negativeTTL")
	}
	if _, ok := g.cached("nexus_kz_default"); !ok {
		t.Error("положительный вердикт ещё свеж")
	}

	now = now.Add(5 * time.Minute)
	if _, ok := g.cached("nexus_kz_default"); ok {
		t.Error("положительный вердикт обязан протухнуть через positiveTTL")
	}
}

func TestGuard_Invalidate(t *testing.T) {
	t.Parallel()

	g := newTestGuard(t, "kz")
	g.store("nexus_kz_default", VerdictOwned, nil)
	g.Invalidate()
	if _, ok := g.cached("nexus_kz_default"); ok {
		t.Error("после Invalidate кеш обязан быть пуст")
	}
}

// Гейты решают по закешированному вердикту — соединение не требуется.
func TestGuard_Assertions_UseCachedVerdict(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	g := newTestGuard(t, "kz")
	g.store("nexus_kz_default", VerdictOwned, nil)
	g.store("nexus_default", VerdictForeign, &Owner{InstanceID: ""})
	g.store("nexus_kz_legacy", VerdictUnclaimed, nil)
	g.store("nexus_kz_split", VerdictConflict, nil)
	g.store("nexus_kz_dark", VerdictUnknown, nil)

	require.NoError(t, g.AssertOwnsDatabase(ctx, "nexus_kz_default"))
	require.NoError(t, g.AssertOwnsTable(ctx, "nexus_kz_default.orders"))

	// Чужая БД — отказ с sentinel-ошибкой, по которой вызывающие возвращают 4xx.
	err := g.AssertOwnsDatabase(ctx, "nexus_default")
	require.ErrorIs(t, err, ErrForeignDatabase)

	// Конфликт владения — отдельная ошибка: чинится вручную, а не ретраем.
	require.ErrorIs(t, g.AssertOwnsDatabase(ctx, "nexus_kz_split"), ErrOwnershipConflict)

	// Неопределённость и «маркера нет» разрушающие операции тоже запрещают
	// (fail-closed): удалить чужие данные хуже, чем отложить обслуживание.
	require.ErrorIs(t, g.AssertOwnsDatabase(ctx, "nexus_kz_dark"), ErrForeignDatabase)
	require.ErrorIs(t, g.AssertOwnsDatabase(ctx, "nexus_kz_legacy"), ErrForeignDatabase)

	owns, err := g.OwnsTable(ctx, "nexus_kz_legacy.logs")
	require.NoError(t, err)
	assert.False(t, owns, "БД без маркера не считается своей при строгой проверке")
}

// Мягкий режим нужен, чтобы обновление ноды до §70 не ломало обслуживание
// схемы: маркеров ещё нет, а стартовые ALTER идемпотентны.
func TestGuard_MayManageTable_AllowsUnclaimed(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	g := newTestGuard(t, "kz")
	g.store("nexus_kz_default", VerdictOwned, nil)
	g.store("nexus_kz_legacy", VerdictUnclaimed, nil)
	g.store("nexus_default", VerdictForeign, &Owner{InstanceID: ""})
	g.store("nexus_kz_dark", VerdictUnknown, nil)

	for table, want := range map[string]bool{
		"nexus_kz_default.orders": true,
		"nexus_kz_legacy.orders":  true,
		"nexus_default.orders":    false,
		"nexus_kz_dark.orders":    false,
	} {
		got, err := g.MayManageTable(ctx, table)
		require.NoError(t, err, table)
		assert.Equal(t, want, got, table)
	}

	kept := g.FilterManagedTables(ctx, []string{
		"nexus_kz_default.orders",
		"nexus_default.orders", // чужая — отсеивается
		"nexus_kz_legacy.orders",
		"garbage", // невалидное имя — отсеивается
	})
	assert.Equal(t, []string{"nexus_kz_default.orders", "nexus_kz_legacy.orders"}, kept)
}

// Сбой проверки кешируется ВМЕСТЕ с ошибкой: без этого повторный вызов в
// пределах negativeTTL отдавал бы «вердикт unknown» без ошибки, и обслуживание
// схемы рапортовало бы «таблица принадлежит другой ноде» вместо «ClickHouse не
// ответил» — диагностика уводила бы в другую сторону.
func TestGuard_CachedErrorIsReturned(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	g := newTestGuard(t, "kz")
	probeErr := errors.New("clickhouse: dial refused")
	g.storeErr("nexus_kz_default", probeErr)

	v, _, err := g.Check(ctx, "nexus_kz_default")
	require.ErrorIs(t, err, probeErr, "причина сбоя обязана дойти до вызывающего")
	assert.Equal(t, VerdictUnknown, v)

	// Предикаты возвращают ту же ошибку, а не «не наша таблица».
	_, err = g.MayManageTable(ctx, "nexus_kz_default.orders")
	require.ErrorIs(t, err, probeErr)
	_, err = g.OwnsTable(ctx, "nexus_kz_default.orders")
	require.ErrorIs(t, err, probeErr)

	// Разрушающие операции всё равно запрещены (fail-closed).
	require.Error(t, g.AssertOwnsTable(ctx, "nexus_kz_default.orders"))
}

func TestDatabaseOf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		table string
		want  string
		ok    bool
	}{
		{table: "nexus_kz_default.orders", want: "nexus_kz_default", ok: true},
		{table: "db.t", want: "db", ok: true},
		{table: "nexus_default", ok: false},
		{table: ".orders", ok: false},
		{table: "db.", ok: false},
		{table: "db.t.x", ok: false},
		{table: "db.t; DROP TABLE x", ok: false},
		{table: "", ok: false},
	}
	for _, tt := range tests {
		got, ok := databaseOf(tt.table)
		assert.Equal(t, tt.ok, ok, tt.table)
		assert.Equal(t, tt.want, got, tt.table)
	}
}

// Захват опирается на код 57 у проигравшего CREATE TABLE — распознавание идёт
// строго по коду, а не по тексту сообщения.
func TestIsTableAlreadyExists(t *testing.T) {
	t.Parallel()

	assert.True(t, isTableAlreadyExists(&chgo.Exception{Code: 57}))
	assert.True(t, isTableAlreadyExists(fmt.Errorf("create marker: %w", &chgo.Exception{Code: 57})))
	assert.False(t, isTableAlreadyExists(&chgo.Exception{Code: 60}))
	assert.False(t, isTableAlreadyExists(errors.New("table already exists")))
	assert.False(t, isTableAlreadyExists(nil))
}

func TestVerdict_String(t *testing.T) {
	t.Parallel()

	for v, want := range map[Verdict]string{
		VerdictOwned:      "owned",
		VerdictForeign:    "foreign",
		VerdictUnclaimed:  "unclaimed",
		VerdictNoDatabase: "no_database",
		VerdictConflict:   "conflict",
		VerdictUnknown:    "unknown",
	} {
		assert.Equal(t, want, v.String())
	}
}
