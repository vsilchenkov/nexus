package backup

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/domain/ackspec"
)

// nodeNotInSnapshot — поля domain.Node, которые не являются колонками nodes.
var nodeNotInSnapshot = map[string]bool{
	"TeamSlug": true, // вычисляемое при чтении (JOIN teams)
	"AsyncAck": true, // переносится через AsyncAckSpec и разбирается AckSpec
}

// TestNode_RoundTripCoversEveryField — каждое поле domain.Node, заполненное
// ненулевым значением, переживает снимок туда и обратно. Поле, добавленное в
// домен и забытое в NodeFromDomain/ToDomain, валит тест.
func TestNode_RoundTripCoversEveryField(t *testing.T) {
	t.Parallel()
	src := &domain.Node{}
	fillNonZero(t, reflect.ValueOf(src).Elem(), nodeNotInSnapshot)
	src.AsyncAck = &ackspec.Spec{}

	row, err := NodeFromDomain(src)
	require.NoError(t, err)
	raw, err := json.Marshal(row)
	require.NoError(t, err)
	var back Node
	require.NoError(t, json.Unmarshal(raw, &back))

	got := back.ToDomain()
	got.AsyncAck, err = back.AckSpec()
	require.NoError(t, err)
	src.TeamSlug = ""
	assert.Equal(t, src, got)
}

func TestNode_AckSpec(t *testing.T) {
	t.Parallel()
	spec, err := Node{}.AckSpec()
	require.NoError(t, err)
	assert.Nil(t, spec, "нет спеки — nil")

	_, err = Node{ID: "n1", AsyncAckSpec: json.RawMessage(`{"status":`)}.AckSpec()
	require.Error(t, err, "битая спека — ошибка, а не паника")
	assert.Contains(t, err.Error(), "n1")
}

// TestNode_ToDomainNonNilSlices — старый файл без списков даёт пустые срезы:
// колонки TEXT[] NOT NULL.
func TestNode_ToDomainNonNilSlices(t *testing.T) {
	t.Parallel()
	n := Node{}.ToDomain()
	assert.NotNil(t, n.URLAllowedHosts)
	assert.NotNil(t, n.ForwardHeaders)
}

func TestTables_Consistent(t *testing.T) {
	t.Parallel()
	names := map[string]bool{}
	for _, tb := range Tables() {
		assert.False(t, names[tb.Name], "таблица %s дважды", tb.Name)
		names[tb.Name] = true
		cols := map[string]bool{}
		for _, c := range tb.Exported() {
			assert.False(t, cols[c], "%s.%s дважды", tb.Name, c)
			cols[c] = true
		}
		for _, c := range tb.Excluded {
			assert.False(t, cols[c], "%s.%s и переносится, и исключена", tb.Name, c)
		}
	}
	for _, name := range ExcludedTables() {
		assert.False(t, names[name], "таблица %s и переносится, и исключена", name)
	}
}

func TestSnapshot_Validate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		mutate  func(s *Snapshot)
		wantErr string
	}{
		{"целый снимок", func(*Snapshot) {}, ""},
		{"узел в несуществующей команде", func(s *Snapshot) { s.Nodes[0].TeamID = "t-x" }, "nodes.team_id"},
		{"роль пользователя", func(s *Snapshot) { s.Users[0].Role = "root" }, "users.role"},
		{"роль в команде", func(s *Snapshot) { s.UserTeams[0].Role = "boss" }, "user_teams.role"},
		{"дубль пути в команде", func(s *Snapshot) {
			s.Nodes = append(s.Nodes, Node{ID: "n2", Path: "a", TeamID: "t1"})
		}, "nodes.team_id+path"},
		{"два шаблона по умолчанию", func(s *Snapshot) {
			s.CHTemplates = append(s.CHTemplates, CHTemplate{ID: "tpl2", IsDefault: true})
		}, "default templates"},
		{"связь с несуществующим хостом", func(s *Snapshot) { s.NodeHosts[0].HostID = "h-x" }, "node_allowed_hosts.host_id"},
		{"вид хоста", func(s *Snapshot) { s.Hosts[0].Kind = "glob" }, "host_allowlist.kind"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := validSnapshot()
			tt.mutate(s)
			err := s.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, ErrInvalidSnapshot)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestSnapshot_ValidateCapsProblems(t *testing.T) {
	t.Parallel()
	s := validSnapshot()
	for i := range 50 {
		s.NodeHosts = append(s.NodeHosts, NodeHost{NodeID: "n1", HostID: "h-x" + strings.Repeat("x", i)})
	}
	err := s.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "and 30 more")
}

func validSnapshot() *Snapshot {
	return &Snapshot{
		Teams:       []Team{{ID: "t1", Slug: "default"}},
		Users:       []User{{ID: "u1", Login: "admin", Role: "admin", DefaultTeamID: "t1"}},
		UserTeams:   []UserTeam{{UserID: "u1", TeamID: "t1", Role: "owner"}},
		CHTemplates: []CHTemplate{{ID: "tpl1", IsDefault: true}},
		NodeGroups:  []NodeGroup{{ID: "g1"}},
		Hosts:       []Host{{ID: "h1", Kind: "exact"}},
		Nodes:       []Node{{ID: "n1", Path: "a", TeamID: "t1", ClickHouseTemplateID: "tpl1", GroupID: "g1"}},
		NodeHosts:   []NodeHost{{NodeID: "n1", HostID: "h1"}},
	}
}

// fillNonZero заполняет все экспортируемые поля структуры ненулевыми
// значениями своего типа (skip — имена полей, которые не заполнять).
func fillNonZero(t *testing.T, v reflect.Value, skip map[string]bool) {
	t.Helper()
	for i := range v.NumField() {
		f, sf := v.Field(i), v.Type().Field(i)
		if skip[sf.Name] || !sf.IsExported() {
			continue
		}
		switch f.Kind() {
		case reflect.String:
			f.SetString("v-" + sf.Name)
		case reflect.Bool:
			f.SetBool(true)
		case reflect.Int32, reflect.Int, reflect.Int64:
			f.SetInt(int64(i + 1))
		case reflect.Slice:
			f.Set(reflect.ValueOf([]string{"s-" + sf.Name}))
		case reflect.Struct:
			if f.Type() != reflect.TypeFor[time.Time]() {
				t.Fatalf("fillNonZero: unsupported struct field %s", sf.Name)
			}
			f.Set(reflect.ValueOf(time.Date(2026, 1, 1+i, 0, 0, 0, 0, time.UTC)))
		default:
			t.Fatalf("fillNonZero: unsupported kind %s of field %s — дополните тест", f.Kind(), sf.Name)
		}
	}
}
