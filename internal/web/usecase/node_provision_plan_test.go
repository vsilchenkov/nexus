package usecase

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"nexus/internal/domain"
)

// TestPlanTableProvision — решение о таблице логов узла, общее для сохранения
// узла и восстановления конфигурации §110.
func TestPlanTableProvision(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		node       domain.Node
		want       tableProvisionMode
		wantReason bool
	}{
		{"имя пустое", domain.Node{LoggingEnabled: true}, tableProvisionSkip, false},
		{"черновое имя", domain.Node{ClickHouseTable: "nexus_x.", LoggingEnabled: false}, tableProvisionSkip, true},
		{"внешняя таблица с шаблоном", domain.Node{ClickHouseTable: "ext.t", ExternalTable: true, ClickHouseTemplateID: "tpl", LoggingEnabled: true}, tableProvisionSkip, true},
		{"явный шаблон при выключенном логировании", domain.Node{ClickHouseTable: "nexus_a.t", ClickHouseTemplateID: "tpl"}, tableProvisionTemplate, false},
		{"логирование без шаблона", domain.Node{ClickHouseTable: "nexus_a.t", LoggingEnabled: true}, tableProvisionDefault, false},
		{"логирование выключено, шаблона нет", domain.Node{ClickHouseTable: "nexus_a.t"}, tableProvisionSkip, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := planTableProvision(&tt.node)
			assert.Equal(t, tt.want, got.mode)
			assert.Equal(t, tt.wantReason, got.skipReason != "")
		})
	}
}
