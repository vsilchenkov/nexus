package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestNodeGroup_Validate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		group   NodeGroup
		wantErr error
	}{
		{"simple ok", NodeGroup{Name: "Маркетплейсы"}, nil},
		// §99.3: имя — свободный текст, а не RFC 7230 token как у заголовков
		// (§24.2). Пробелы, кириллица, цифры и знаки препинания обязаны
		// проходить: группы называет человек.
		{"spaces and cyrillic ok", NodeGroup{Name: "1С Обмен"}, nil},
		{"punctuation ok", NodeGroup{Name: "СДЭК / DPD (курьеры)"}, nil},
		{"colon ok", NodeGroup{Name: "prod: маркетплейсы"}, nil},
		{"trimmed to valid", NodeGroup{Name: "  Логистика  "}, nil},
		{"max length ok", NodeGroup{Name: strings.Repeat("я", 100)}, nil},
		{"sort order max ok", NodeGroup{Name: "Гр", SortOrder: MaxNodeGroupSortOrder}, nil},

		{"empty", NodeGroup{Name: ""}, ErrNodeGroupNameLength},
		// Имя из одних пробелов не должно проходить как непустое: длина
		// считается ПОСЛЕ обрезки.
		{"whitespace only", NodeGroup{Name: "   "}, ErrNodeGroupNameLength},
		// Длина в рунах, а не байтах: 100 кириллических символов — это 200
		// байт, и побайтовая проверка отвергла бы валидное имя.
		{"too long", NodeGroup{Name: strings.Repeat("я", 101)}, ErrNodeGroupNameLength},
		{"too long after trim counts trimmed", NodeGroup{Name: "  " + strings.Repeat("a", 101) + "  "}, ErrNodeGroupNameLength},
		{"description too long", NodeGroup{Name: "Гр", Description: strings.Repeat("д", 501)}, ErrNodeGroupDescriptionLength},
		{"sort order negative", NodeGroup{Name: "Гр", SortOrder: -1}, ErrNodeGroupSortOrderRange},
		{"sort order above max", NodeGroup{Name: "Гр", SortOrder: MaxNodeGroupSortOrder + 1}, ErrNodeGroupSortOrderRange},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.group.Validate()
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Validate() = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// TestNode_Validate_GroupID: группа необязательна, но заданная обязана быть
// UUID — иначе строка дошла бы до INSERT и вернулась ошибкой типа PostgreSQL.
func TestNode_Validate_GroupID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		groupID string
		wantErr error
	}{
		{"empty ok", "", nil},
		{"uuid ok", "8f2c1a94-7d31-4b0e-9a11-2c3d4e5f6071", nil},
		{"uppercase uuid ok", "8F2C1A94-7D31-4B0E-9A11-2C3D4E5F6071", nil},
		{"garbage", "not-a-uuid", ErrNodeInvalidGroupID},
		{"truncated", "8f2c1a94-7d31-4b0e-9a11", ErrNodeInvalidGroupID},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			n := validNodeForGroupTest()
			n.GroupID = tc.groupID
			err := n.Validate()
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Validate() = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// validNodeForGroupTest — минимальный валидный узел: тест проверяет ровно одно
// поле, всё остальное обязано проходить, иначе ошибка придёт не оттуда.
func validNodeForGroupTest() Node {
	n := Node{
		Path:             "erp/orders",
		RootMethod:       RootMethodRequest,
		URLMode:          URLModeStatic,
		TargetURL:        "https://example.test/hs/orders",
		IncomingMethod:   HTTPMethodPOST,
		OutgoingMethod:   HTTPMethodPOST,
		AuthType:         AuthTypeNone,
		IncomingAuthType: IncomingAuthTypeNone,
		Status:           NodeStatusEnabled,
		TeamID:           "3f2c1a94-7d31-4b0e-9a11-2c3d4e5f6072",
		TimeoutMs:        30000,
	}
	n.SetDefaults()
	return n
}
