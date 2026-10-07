package kafka

import (
	"context"
	"errors"
	"testing"
	"time"

	segkafka "github.com/segmentio/kafka-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeOffsetsAPI — брокер для Offsets без сети: отвечает заранее заданным.
type fakeOffsetsAPI struct {
	meta      *segkafka.MetadataResponse
	metaErr   error
	offsets   *segkafka.ListOffsetsResponse
	offErr    error
	fetch     *segkafka.OffsetFetchResponse
	fetchErr  error
	configs   *segkafka.DescribeConfigsResponse
	configErr error
	groups    *segkafka.DescribeGroupsResponse
	groupsErr error

	fetchReq *segkafka.OffsetFetchRequest
}

func (f *fakeOffsetsAPI) Metadata(context.Context, *segkafka.MetadataRequest) (*segkafka.MetadataResponse, error) {
	return f.meta, f.metaErr
}

func (f *fakeOffsetsAPI) ListOffsets(context.Context, *segkafka.ListOffsetsRequest) (*segkafka.ListOffsetsResponse, error) {
	return f.offsets, f.offErr
}

func (f *fakeOffsetsAPI) OffsetFetch(_ context.Context, req *segkafka.OffsetFetchRequest) (*segkafka.OffsetFetchResponse, error) {
	f.fetchReq = req
	return f.fetch, f.fetchErr
}

func (f *fakeOffsetsAPI) DescribeConfigs(context.Context, *segkafka.DescribeConfigsRequest) (*segkafka.DescribeConfigsResponse, error) {
	return f.configs, f.configErr
}

func (f *fakeOffsetsAPI) DescribeGroups(context.Context, *segkafka.DescribeGroupsRequest) (*segkafka.DescribeGroupsResponse, error) {
	return f.groups, f.groupsErr
}

func topicMeta(name string, parts ...int) *segkafka.MetadataResponse {
	t := segkafka.Topic{Name: name}
	for _, p := range parts {
		t.Partitions = append(t.Partitions, segkafka.Partition{Topic: name, ID: p})
	}
	return &segkafka.MetadataResponse{Topics: []segkafka.Topic{t}}
}

func TestPartitionPosition_Lag(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		pos  PartitionPosition
		want int64
	}{
		{"committed in range", PartitionPosition{Watermark: Watermark{First: 10, Last: 100}, Committed: 40}, 60},
		{"caught up", PartitionPosition{Watermark: Watermark{First: 10, Last: 100}, Committed: 100}, 0},
		{"never committed reads from first", PartitionPosition{Watermark: Watermark{First: 10, Last: 100}, Committed: -1}, 90},
		{"committed behind retention", PartitionPosition{Watermark: Watermark{First: 50, Last: 100}, Committed: 20}, 50},
		{"empty partition", PartitionPosition{Watermark: Watermark{First: 0, Last: 0}, Committed: -1}, 0},
		{"committed ahead of last never negative", PartitionPosition{Watermark: Watermark{First: 0, Last: 5}, Committed: 7}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tt.pos.Lag())
		})
	}
}

// TestOffsets_GroupPositions: сводка трёх запросов — партиция без committed
// считается некоммиченной, ошибка одной партиции не прячет остальные.
func TestOffsets_GroupPositions(t *testing.T) {
	t.Parallel()

	partErr := segkafka.NotLeaderForPartition
	api := &fakeOffsetsAPI{
		meta: topicMeta("nexus.async.paused", 0, 1, 2),
		offsets: &segkafka.ListOffsetsResponse{Topics: map[string][]segkafka.PartitionOffsets{
			"nexus.async.paused": {
				{Partition: 0, FirstOffset: 0, LastOffset: 10},
				{Partition: 1, FirstOffset: 5, LastOffset: 8},
				{Partition: 2, Error: partErr},
			},
		}},
		fetch: &segkafka.OffsetFetchResponse{Topics: map[string][]segkafka.OffsetFetchPartition{
			"nexus.async.paused": {{Partition: 0, CommittedOffset: 4}},
		}},
	}

	got, err := (&Offsets{api: api}).GroupPositions(context.Background(), "g-paused", "nexus.async.paused")

	require.Error(t, err)
	assert.ErrorIs(t, err, partErr)
	assert.Equal(t, []PartitionPosition{
		{Partition: 0, Watermark: Watermark{First: 0, Last: 10}, Committed: 4},
		{Partition: 1, Watermark: Watermark{First: 5, Last: 8}, Committed: -1},
	}, got)
	assert.Equal(t, map[string][]int{"nexus.async.paused": {0, 1, 2}}, api.fetchReq.Topics)
}

// TestOffsets_GroupPositions_FetchError (ревизия §113): ошибка OffsetFetch
// целиком — партиции без committed в ответ не попадают. До правки они уходили с
// -1, и LagReporter публиковал lag размером со весь лог после рестарта брокера.
func TestOffsets_GroupPositions_FetchError(t *testing.T) {
	t.Parallel()

	api := &fakeOffsetsAPI{
		meta: topicMeta("nexus.async", 0, 1),
		offsets: &segkafka.ListOffsetsResponse{Topics: map[string][]segkafka.PartitionOffsets{
			"nexus.async": {
				{Partition: 0, FirstOffset: 0, LastOffset: 1_000_000},
				{Partition: 1, FirstOffset: 0, LastOffset: 2_000_000},
			},
		}},
		fetch: &segkafka.OffsetFetchResponse{Error: segkafka.GroupCoordinatorNotAvailable},
	}

	got, err := (&Offsets{api: api}).GroupPositions(context.Background(), "g", "nexus.async")

	assert.ErrorIs(t, err, segkafka.GroupCoordinatorNotAvailable)
	assert.Empty(t, got)
}

// TestOffsets_Committed_GroupError: ошибка группы целиком (координатор ещё
// грузится) — ошибка, а не пустая карта «никто ничего не коммитил».
func TestOffsets_Committed_GroupError(t *testing.T) {
	t.Parallel()

	api := &fakeOffsetsAPI{fetch: &segkafka.OffsetFetchResponse{Error: segkafka.GroupCoordinatorNotAvailable}}
	got, err := (&Offsets{api: api}).Committed(context.Background(), "g", "t", []int{0})

	assert.ErrorIs(t, err, segkafka.GroupCoordinatorNotAvailable)
	assert.Empty(t, got)
}

// TestOffsets_Partitions: ревизия §114 — «топика нет» (брокер ответил)
// отличается от сбоя (ErrTopicNotFound), при этом исходная ошибка kafka-go
// не теряется.
func TestOffsets_Partitions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		api          *fakeOffsetsAPI
		want         []int
		wantErr      bool
		wantNotFound bool
	}{
		{name: "ok", api: &fakeOffsetsAPI{meta: topicMeta("t", 0, 1)}, want: []int{0, 1}},
		{name: "missing topic", api: &fakeOffsetsAPI{meta: topicMeta("other", 0)}, wantErr: true, wantNotFound: true},
		{name: "unknown topic", api: &fakeOffsetsAPI{meta: &segkafka.MetadataResponse{Topics: []segkafka.Topic{
			{Name: "t", Error: segkafka.UnknownTopicOrPartition},
		}}}, wantErr: true, wantNotFound: true},
		{name: "other topic error", api: &fakeOffsetsAPI{meta: &segkafka.MetadataResponse{Topics: []segkafka.Topic{
			{Name: "t", Error: segkafka.LeaderNotAvailable},
		}}}, wantErr: true},
		{name: "request error", api: &fakeOffsetsAPI{metaErr: errors.New("dial")}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := (&Offsets{api: tt.api}).Partitions(context.Background(), "t")
			if tt.wantErr {
				require.Error(t, err)
				assert.Equal(t, tt.wantNotFound, errors.Is(err, ErrTopicNotFound))
				if tt.name == "unknown topic" {
					assert.ErrorIs(t, err, segkafka.UnknownTopicOrPartition)
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestOffsets_TopicRetention(t *testing.T) {
	t.Parallel()

	resp := func(entries ...segkafka.DescribeConfigResponseConfigEntry) *segkafka.DescribeConfigsResponse {
		return &segkafka.DescribeConfigsResponse{Resources: []segkafka.DescribeConfigResponseResource{
			{ResourceName: "t", ConfigEntries: entries},
		}}
	}
	tests := []struct {
		name    string
		api     *fakeOffsetsAPI
		want    time.Duration
		wantErr bool
	}{
		{"seven days", &fakeOffsetsAPI{configs: resp(segkafka.DescribeConfigResponseConfigEntry{
			ConfigName: "retention.ms", ConfigValue: "604800000"})}, 7 * 24 * time.Hour, false},
		{"unlimited", &fakeOffsetsAPI{configs: resp(segkafka.DescribeConfigResponseConfigEntry{
			ConfigName: "retention.ms", ConfigValue: "-1"})}, 0, false},
		{"garbage value", &fakeOffsetsAPI{configs: resp(segkafka.DescribeConfigResponseConfigEntry{
			ConfigName: "retention.ms", ConfigValue: "week"})}, 0, true},
		{"not reported", &fakeOffsetsAPI{configs: resp()}, 0, true},
		{"resource error", &fakeOffsetsAPI{configs: &segkafka.DescribeConfigsResponse{Resources: []segkafka.DescribeConfigResponseResource{
			{ResourceName: "t", Error: segkafka.UnknownTopicOrPartition},
		}}}, 0, true},
		{"request error", &fakeOffsetsAPI{configErr: errors.New("dial")}, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := (&Offsets{api: tt.api}).TopicRetention(context.Background(), "t")
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestOffsets_GroupAssignment (§113.6): партиции топика по client.id участника;
// чужие топики и участники без назначения (ребаланс) в карту не попадают,
// ошибка группы — ошибка, а не «назначений нет».
func TestOffsets_GroupAssignment(t *testing.T) {
	t.Parallel()
	member := func(client string, topics ...segkafka.GroupMemberTopic) segkafka.DescribeGroupsResponseMember {
		return segkafka.DescribeGroupsResponseMember{ClientID: client,
			MemberAssignments: segkafka.DescribeGroupsResponseAssignments{Topics: topics}}
	}
	tests := []struct {
		name    string
		resp    *segkafka.DescribeGroupsResponse
		err     error
		want    map[string][]int
		wantErr bool
	}{
		{
			name: "по client.id, только нужный топик",
			resp: &segkafka.DescribeGroupsResponse{Groups: []segkafka.DescribeGroupsResponseGroup{{
				GroupID: "g",
				Members: []segkafka.DescribeGroupsResponseMember{
					member("s1-0", segkafka.GroupMemberTopic{Topic: "t", Partitions: []int{0, 2}},
						segkafka.GroupMemberTopic{Topic: "other", Partitions: []int{1}}),
					member("s1-1", segkafka.GroupMemberTopic{Topic: "t", Partitions: []int{1}}),
					member("s2-0"), // ребаланс: участник без назначения
				},
			}}},
			want: map[string][]int{"s1-0": {0, 2}, "s1-1": {1}},
		},
		{
			name: "ошибка группы",
			resp: &segkafka.DescribeGroupsResponse{Groups: []segkafka.DescribeGroupsResponseGroup{{
				GroupID: "g", Error: segkafka.NotCoordinatorForGroup}}},
			wantErr: true,
		},
		{name: "группы нет в ответе", resp: &segkafka.DescribeGroupsResponse{}, wantErr: true},
		{name: "брокер недоступен", err: errors.New("dial"), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			o := &Offsets{api: &fakeOffsetsAPI{groups: tt.resp, groupsErr: tt.err}}
			got, err := o.GroupAssignment(context.Background(), "g", "t")
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
