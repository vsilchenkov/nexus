package port

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestQueueOwner_Owns — правило принадлежности сообщения «Очереди» (§101.8):
// ключ совпадает с путём И (node_id свой ИЛИ конверт старый, а путь однозначен).
func TestQueueOwner_Owns(t *testing.T) {
	t.Parallel()

	const path, own = "telegram", "3f1c2d4e-0000-4000-8000-000000000001"
	strict := QueueOwner{Path: path, NodeID: own}
	legacyOK := QueueOwner{Path: path, NodeID: own, AcceptLegacy: true}

	tests := []struct {
		name   string
		owner  QueueOwner
		key    string
		nodeID string
		want   bool
	}{
		{"своё сообщение", strict, path, own, true},
		{"одноимённый узел другой команды", legacyOK, path, "9a8b7c6d-0000-4000-8000-000000000002", false},
		{"другой путь с тем же id", strict, "telegram2", own, false},
		{"старый конверт, путь однозначен", legacyOK, path, "", true},
		{"старый конверт, путь неоднозначен", strict, path, "", false},
		{"старый конверт другого пути", legacyOK, "other", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.owner.Owns(tc.key, tc.nodeID))
		})
	}
}
