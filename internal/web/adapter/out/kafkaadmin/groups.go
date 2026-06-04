package kafkaadmin

import (
	"context"

	kafka "github.com/segmentio/kafka-go"
)

// listGroupIDs возвращает ID всех consumer-групп кластера. Ошибка — best-effort:
// логируется, возвращается пустой список (топики покажутся без групп).
func (c *Client) listGroupIDs(ctx context.Context) []string {
	resp, err := c.kc.ListGroups(ctx, &kafka.ListGroupsRequest{})
	if err != nil {
		c.logger.Warn("kafka list groups failed", c.logger.Str("op", "web.kafkaAdmin.listGroups"), c.logger.Err(err))
		return nil
	}
	ids := make([]string, 0, len(resp.Groups))
	for _, g := range resp.Groups {
		ids = append(ids, g.GroupID)
	}
	return ids
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
