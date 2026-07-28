package usecase

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
)

// stubCHOwnership — гейт владения БД (§70.6) с заранее заданными ответами.
type stubCHOwnership struct {
	foreign map[string]bool
	err     error
	asked   []string
}

func (s *stubCHOwnership) IsForeignTable(_ context.Context, table string) (bool, error) {
	s.asked = append(s.asked, table)
	if s.err != nil {
		return false, s.err
	}
	return s.foreign[table], nil
}

// §70.6: обычный узел не может писать в БД другой ноды. Ошибка адресована
// оператору (400 + подсказка чинить поле), а не прячется в 500.
func TestNodeUC_Create_ForeignTableRejected(t *testing.T) {
	t.Parallel()

	own := &stubCHOwnership{foreign: map[string]bool{"nexus_default.hook": true}}
	uc := newNodeUC(newMemNodeRepo(), &verifyProvisioner{}, newMemCHTemplateRepo())
	uc.SetOwnership(own)

	err := uc.Create(context.Background(), SystemActor(), nodeWithTable("nexus_default.hook", true))
	require.ErrorIs(t, err, domain.ErrNodeCHTableForeignDatabase)
	assert.Equal(t, []string{"nexus_default.hook"}, own.asked)
}

// §70.6: внешняя таблица (§64) — именно тот режим, ради которого чужая БД
// разрешена: Nexus её не создаёт и не меняет, а только читает и дополняет.
func TestNodeUC_Create_ForeignTableAllowedForExternal(t *testing.T) {
	t.Parallel()

	own := &stubCHOwnership{foreign: map[string]bool{"nexus_default.hook": true}}
	uc := newNodeUC(newMemNodeRepo(), &verifyProvisioner{}, newMemCHTemplateRepo())
	uc.SetOwnership(own)

	n := nodeWithTable("nexus_default.hook", true)
	n.ExternalTable = true
	require.NoError(t, uc.Create(context.Background(), SystemActor(), n))
	assert.Empty(t, own.asked, "для внешней таблицы владение не спрашивается вовсе")
}

// Своя таблица сохраняется как обычно.
func TestNodeUC_Create_OwnTableAccepted(t *testing.T) {
	t.Parallel()

	own := &stubCHOwnership{foreign: map[string]bool{"nexus_kz_default.hook": false}}
	uc := newNodeUC(newMemNodeRepo(), &verifyProvisioner{}, newMemCHTemplateRepo())
	uc.SetOwnership(own)

	require.NoError(t, uc.Create(context.Background(), SystemActor(),
		nodeWithTable("nexus_kz_default.hook", true)))
}

// Недоступный ClickHouse не должен блокировать правку конфигурации: проверка
// владения — про чужие данные, а не про валидность запроса оператора.
func TestNodeUC_Create_OwnershipErrorDoesNotBlock(t *testing.T) {
	t.Parallel()

	own := &stubCHOwnership{err: errors.New("clickhouse down")}
	uc := newNodeUC(newMemNodeRepo(), &verifyProvisioner{}, newMemCHTemplateRepo())
	uc.SetOwnership(own)

	require.NoError(t, uc.Create(context.Background(), SystemActor(),
		nodeWithTable("nexus_kz_default.hook", true)))
}

// Черновик имени (узел с выключенными логами хранит «nexus_x.») — ещё не имя
// таблицы, спрашивать по нему владение бессмысленно.
func TestNodeUC_Create_DraftTableNameSkipsOwnership(t *testing.T) {
	t.Parallel()

	own := &stubCHOwnership{}
	uc := newNodeUC(newMemNodeRepo(), &verifyProvisioner{}, newMemCHTemplateRepo())
	uc.SetOwnership(own)

	require.NoError(t, uc.Create(context.Background(), SystemActor(),
		nodeWithTable("nexus_kz_default.", false)))
	assert.Empty(t, own.asked)
}

// Update проверяется тем же гейтом: иначе чужую таблицу можно было бы
// подставить правкой уже созданного узла.
func TestNodeUC_Update_ForeignTableRejected(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repo := newMemNodeRepo()
	uc := newNodeUC(repo, &verifyProvisioner{}, newMemCHTemplateRepo())

	n := nodeWithTable("nexus_kz_default.hook", true)
	require.NoError(t, uc.Create(ctx, SystemActor(), n))

	own := &stubCHOwnership{foreign: map[string]bool{"nexus_default.hook": true}}
	uc.SetOwnership(own)

	n.ClickHouseTable = "nexus_default.hook"
	err := uc.Update(ctx, SystemActor(), n, "")
	require.ErrorIs(t, err, domain.ErrNodeCHTableForeignDatabase)
}
