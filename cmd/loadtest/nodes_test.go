package main

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlanNodes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		httpNodes  int
		totalNodes int
		f          flags
		want       map[nodeMode]int
	}{
		{
			name:       "all zero ratios -> all sync",
			httpNodes:  10,
			totalNodes: 10,
			f:          flags{},
			want:       map[nodeMode]int{modeSync: 10},
		},
		{
			name:       "mix within budget, remainder sync",
			httpNodes:  10,
			totalNodes: 10,
			f:          flags{RatioAsync: 0.3, RatioDynamicURL: 0.2, RatioAuthToken: 0.2, RatioAuthBasic: 0.1},
			want:       map[nodeMode]int{modeAsync: 3, modeDynURL: 2, modeAuthToken: 2, modeAuthBasic: 1, modeSync: 2},
		},
		{
			name:       "overflow is clamped to httpNodes",
			httpNodes:  10,
			totalNodes: 10,
			f:          flags{RatioAsync: 0.6, RatioDynamicURL: 0.6, RatioAuthToken: 0.6},
			want:       map[nodeMode]int{modeAsync: 6, modeDynURL: 4},
		},
		{
			name:       "httpNodes below total (rmq carved out)",
			httpNodes:  8,
			totalNodes: 10,
			f:          flags{RatioAsync: 0.5},
			want:       map[nodeMode]int{modeAsync: 5, modeSync: 3},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			modes := planNodes(tt.httpNodes, tt.totalNodes, tt.f)
			assert.Len(t, modes, tt.httpNodes)
			assert.Equal(t, tt.want, modeCounts(modes))
		})
	}
}

func TestNodeCreateBody(t *testing.T) {
	t.Parallel()

	const target = "http://loadtest:9999"

	tests := []struct {
		name   string
		mode   nodeMode
		assert func(t *testing.T, body map[string]any)
	}{
		{
			name: "sync",
			mode: modeSync,
			assert: func(t *testing.T, b map[string]any) {
				assert.Equal(t, "request", b["root_method"])
				assert.Equal(t, "none", b["auth_type"])
				assert.NotContains(t, b, "url_mode")
			},
		},
		{
			name: "async",
			mode: modeAsync,
			assert: func(t *testing.T, b map[string]any) {
				assert.Equal(t, "requestAsync", b["root_method"])
			},
		},
		{
			name: "dyn-url",
			mode: modeDynURL,
			assert: func(t *testing.T, b map[string]any) {
				assert.Equal(t, "from_request", b["url_mode"])
				assert.Equal(t, "url_base", b["url_param_name"])
				// url_allowed_hosts не задаём => allow-all (domain.HostAllowed).
				assert.NotContains(t, b, "url_allowed_hosts")
			},
		},
		{
			name: "auth-token",
			mode: modeAuthToken,
			assert: func(t *testing.T, b map[string]any) {
				assert.Equal(t, "token_from_request", b["auth_type"])
				assert.Equal(t, "header", b["auth_dynamic_source"])
				assert.Equal(t, "Authorization", b["auth_dynamic_field"])
				assert.Equal(t, "Bearer ", b["auth_dynamic_strip_prefix"])
			},
		},
		{
			name: "auth-basic",
			mode: modeAuthBasic,
			assert: func(t *testing.T, b map[string]any) {
				assert.Equal(t, "basic_from_request", b["auth_type"])
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			b := nodeCreateBody(tt.mode, "loadtest/node-1", target, "nexus_kz_default.loadtest")
			assert.Equal(t, "loadtest/node-1", b["path"])
			assert.Equal(t, target, b["target_url"])
			// §70.2: имя таблицы приходит из --ch-table, а не зашито — на ноде
			// с идентификатором БД называется иначе.
			assert.Equal(t, "nexus_kz_default.loadtest", b["clickhouse_table"])
			tt.assert(t, b)
		})
	}
}

func TestRequestURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		team string
		node node
		want string
	}{
		{
			name: "sync with team",
			team: "default",
			node: node{path: "loadtest/n1", mode: modeSync},
			want: "http://r/api/v1/request/default/loadtest/n1",
		},
		{
			name: "async with team",
			team: "default",
			node: node{path: "loadtest/n2", mode: modeAsync},
			want: "http://r/api/v1/requestAsync/default/loadtest/n2",
		},
		{
			name: "sync without team",
			team: "",
			node: node{path: "loadtest/n3", mode: modeSync},
			want: "http://r/api/v1/request/loadtest/n3",
		},
		{
			name: "dyn-url appends url_base",
			team: "default",
			node: node{path: "loadtest/n4", mode: modeDynURL, urlBase: "http://loadtest:9999"},
			want: "http://r/api/v1/request/default/loadtest/n4?url_base=http%3A%2F%2Floadtest%3A9999",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, requestURL("http://r", tt.team, tt.node))
		})
	}
}

func TestBuildReport(t *testing.T) {
	t.Parallel()

	res := newResult()
	// sync: 2 ok; async: 1 ok + 1 error.
	res.add(10*time.Millisecond, false, modeSync)
	res.add(20*time.Millisecond, false, modeSync)
	res.add(30*time.Millisecond, false, modeAsync)
	res.add(40*time.Millisecond, true, modeAsync)

	rep := buildReport(res, 2*time.Second)

	assert.Equal(t, int64(4), rep.Sent)
	assert.Equal(t, int64(1), rep.Errors)
	assert.InDelta(t, 0.25, rep.ErrorRate, 1e-9)
	assert.InDelta(t, 2.0, rep.AchievedRPS, 1e-9)

	require.Contains(t, rep.Modes, "sync")
	require.Contains(t, rep.Modes, "async")
	assert.Equal(t, int64(2), rep.Modes["sync"].Sent)
	assert.Equal(t, int64(0), rep.Modes["sync"].Errors)
	assert.Equal(t, int64(2), rep.Modes["async"].Sent)
	assert.Equal(t, int64(1), rep.Modes["async"].Errors)
	assert.InDelta(t, 0.5, rep.Modes["async"].ErrorRate, 1e-9)
}

func TestRandomHexLength(t *testing.T) {
	t.Parallel()
	s := randomHex(32)
	assert.Len(t, s, 32)
	assert.Empty(t, strings.Trim(s, "0123456789abcdef"), "must be hex only")
}
