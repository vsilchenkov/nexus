package usecase_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/sender/usecase"
)

func TestLogMaskProvider_EmptySet_NoOp(t *testing.T) {
	t.Parallel()
	p := usecase.NewLogMaskProvider()
	require.Equal(t, "https://api/botTOKEN/x", p.Mask("https://api/botTOKEN/x"))
	require.Equal(t, "", p.Mask(""))
}

func TestLogMaskProvider_GroupReplacement(t *testing.T) {
	t.Parallel()
	p := usecase.NewLogMaskProvider()
	applied, skipped := p.Set([]domain.LogMaskPattern{
		{Pattern: `(bot[0-9]{3,}):[A-Za-z0-9_-]{5,}`, Replacement: "$1:***"},
	})
	require.Equal(t, 1, applied)
	require.Equal(t, 0, skipped)
	require.Equal(t,
		"https://api.telegram.org/bot123456:***/getUpdates",
		p.Mask("https://api.telegram.org/bot123456:AAExYzToken/getUpdates"),
		"группа $1 сохраняется, хвост-токен маскируется")
}

func TestLogMaskProvider_OrderMatters(t *testing.T) {
	t.Parallel()
	p := usecase.NewLogMaskProvider()
	// Первый шаблон готовит вход второму: A→B, затем B→C. Порядок = порядок среза
	// (reader отдаёт по sort_order, created_at).
	p.Set([]domain.LogMaskPattern{
		{Pattern: `A`, Replacement: "B"},
		{Pattern: `B`, Replacement: "C"},
	})
	require.Equal(t, "C", p.Mask("A"))
}

func TestLogMaskProvider_InvalidPatternSkipped(t *testing.T) {
	t.Parallel()
	p := usecase.NewLogMaskProvider()
	applied, skipped := p.Set([]domain.LogMaskPattern{
		{Pattern: `valid[0-9]+`, Replacement: "***"},
		{Pattern: `broken(`, Replacement: "***"}, // не компилируется
	})
	require.Equal(t, 1, applied, "битый шаблон пропущен, валидный применён")
	require.Equal(t, 1, skipped)
	require.Equal(t, "x=***", p.Mask("x=valid123"))
}

func TestLogMaskProvider_SetReplacesPrevious(t *testing.T) {
	t.Parallel()
	p := usecase.NewLogMaskProvider()
	p.Set([]domain.LogMaskPattern{{Pattern: `old`, Replacement: "***"}})
	p.Set([]domain.LogMaskPattern{{Pattern: `new`, Replacement: "***"}})
	require.Equal(t, "old x=***", p.Mask("old x=new"), "прежний набор заменён целиком")
}
