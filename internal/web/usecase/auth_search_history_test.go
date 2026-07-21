package usecase

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/platform/logging"
)

// stubSearchHistoryRepo — port.SearchHistoryRepo для unit-тестов (§62):
// запоминает сохранённые/очищенные вызовы, отдаёт настраиваемые список/ошибки.
type stubSearchHistoryRepo struct {
	items    []string
	listErr  error
	saveErr  error
	clearErr error

	saved   []savedQuery
	cleared int
}

type savedQuery struct {
	query string
	keep  int
}

func (r *stubSearchHistoryRepo) ListSearchHistory(_ context.Context, _ string, _ int) ([]string, error) {
	return r.items, r.listErr
}

func (r *stubSearchHistoryRepo) SaveSearchQuery(_ context.Context, _, query string, keep int) error {
	if r.saveErr != nil {
		return r.saveErr
	}
	r.saved = append(r.saved, savedQuery{query: query, keep: keep})
	return nil
}

func (r *stubSearchHistoryRepo) ClearSearchHistory(context.Context, string) error {
	if r.clearErr != nil {
		return r.clearErr
	}
	r.cleared++
	return nil
}

func mkSearchUC(hist *stubSearchHistoryRepo) *AuthUsecase {
	uc := NewAuthUsecase(newAuthUserRepo(), newMemSessionRepo(), &nopTeamRepo{},
		NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop()),
		func() time.Duration { return time.Hour }, logging.NewNoop())
	if hist != nil {
		uc = uc.WithSearchHistory(hist)
	}
	return uc
}

// TestAuthUC_RecordSearch (§62): нормализация строки и отсев мусора.
func TestAuthUC_RecordSearch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		in        string
		wantSaved string // "" = ожидаем no-op (ничего не сохранено)
	}{
		{name: "обычная строка сохраняется", in: "parcel", wantSaved: "parcel"},
		{name: "trim пробелов", in: "  push  ", wantSaved: "push"},
		{name: "ровно 2 руны — граница включительно", in: "id", wantSaved: "id"},
		{name: "пустая → no-op", in: "", wantSaved: ""},
		{name: "только пробелы → no-op", in: "   ", wantSaved: ""},
		{name: "1 руна → no-op (короче минимума)", in: "a", wantSaved: ""},
		{name: "201 руна → no-op (длиннее максимума)", in: strings.Repeat("z", 201), wantSaved: ""},
		{name: "200 рун — граница включительно", in: strings.Repeat("z", 200), wantSaved: strings.Repeat("z", 200)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			hist := &stubSearchHistoryRepo{}
			uc := mkSearchUC(hist)
			require.NoError(t, uc.RecordSearch(context.Background(), "u1", tt.in))
			if tt.wantSaved == "" {
				assert.Empty(t, hist.saved, "мусор не сохраняется")
				return
			}
			require.Len(t, hist.saved, 1)
			assert.Equal(t, tt.wantSaved, hist.saved[0].query)
			assert.Equal(t, maxSearchHistory, hist.saved[0].keep, "keep = maxSearchHistory")
		})
	}

	t.Run("без WithSearchHistory → no-op, не ошибка", func(t *testing.T) {
		t.Parallel()
		require.NoError(t, mkSearchUC(nil).RecordSearch(context.Background(), "u1", "parcel"))
	})

	t.Run("ошибка репозитория пробрасывается", func(t *testing.T) {
		t.Parallel()
		uc := mkSearchUC(&stubSearchHistoryRepo{saveErr: errors.New("db down")})
		assert.Error(t, uc.RecordSearch(context.Background(), "u1", "parcel"))
	})
}

// TestAuthUC_SearchHistory (§62): чтение деградирует в пустой список.
func TestAuthUC_SearchHistory(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"push", "parcel"},
		mkSearchUC(&stubSearchHistoryRepo{items: []string{"push", "parcel"}}).SearchHistory(context.Background(), "u1"),
		"порядок из репозитория сохраняется")
	assert.Equal(t, []string{},
		mkSearchUC(&stubSearchHistoryRepo{listErr: errors.New("db down")}).SearchHistory(context.Background(), "u1"),
		"ошибка репозитория → пустой список, не ошибка")
	assert.Equal(t, []string{},
		mkSearchUC(nil).SearchHistory(context.Background(), "u1"),
		"без WithSearchHistory → пустой список")
	assert.Equal(t, []string{},
		mkSearchUC(&stubSearchHistoryRepo{items: nil}).SearchHistory(context.Background(), "u1"),
		"nil из репозитория нормализуется в пустой слайс (JSON [] вместо null)")
}

// TestAuthUC_ClearSearchHistory (§62): очистка проксирует в репозиторий, без
// репозитория — no-op.
func TestAuthUC_ClearSearchHistory(t *testing.T) {
	t.Parallel()

	hist := &stubSearchHistoryRepo{}
	require.NoError(t, mkSearchUC(hist).ClearSearchHistory(context.Background(), "u1"))
	assert.Equal(t, 1, hist.cleared)

	require.NoError(t, mkSearchUC(nil).ClearSearchHistory(context.Background(), "u1"),
		"без WithSearchHistory → no-op")

	assert.Error(t, mkSearchUC(&stubSearchHistoryRepo{clearErr: errors.New("db down")}).
		ClearSearchHistory(context.Background(), "u1"))
}
