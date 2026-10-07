package kafkaadmin

import (
	"context"
	"fmt"

	kafka "github.com/segmentio/kafka-go"
)

// listGroupIDs возвращает ID всех consumer-групп кластера. Ошибка — ошибка
// Topics (§114.4): до §114 она глоталась, топики показывались без групп, и
// такой ответ кешировался как нормальный — lag всех групп пропадал с экрана
// на весь TTL, хотя брокер просто не ответил.
func (c *Client) listGroupIDs(ctx context.Context) ([]string, error) {
	resp, err := c.kc.ListGroups(ctx, &kafka.ListGroupsRequest{})
	if err != nil {
		return nil, fmt.Errorf("kafka list groups: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("kafka list groups: %w", resp.Error)
	}
	ids := make([]string, 0, len(resp.Groups))
	for _, g := range resp.Groups {
		ids = append(ids, g.GroupID)
	}
	return ids, nil
}

// describeMembers возвращает число участников по каждой группе. Best-effort:
// при ошибке — пустая карта (members=0 в UI).
func (c *Client) describeMembers(ctx context.Context, groupIDs []string) map[string]int {
	out := make(map[string]int, len(groupIDs))
	if len(groupIDs) == 0 {
		return out
	}
	resp, err := c.kc.DescribeGroups(ctx, &kafka.DescribeGroupsRequest{GroupIDs: groupIDs})
	if err != nil {
		c.logger.Warn("kafka describe groups failed", c.logger.Str("op", "web.kafkaAdmin.describeGroups"), c.logger.Err(err))
		return out
	}
	for _, g := range resp.Groups {
		out[g.GroupID] = len(g.Members)
	}
	return out
}
