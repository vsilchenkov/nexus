package usecase

import (
	"strings"
	"testing"

	"nexus/internal/domain"
	"nexus/internal/domain/ackspec"
)

func auditSpec(body string) *ackspec.Spec {
	return &ackspec.Spec{
		Version:     ackspec.Version,
		ContentType: ackspec.ContentTypeJSON,
		Body:        body,
		OnError:     ackspec.OnErrorDefault,
	}
}

// TestDiffNodes_AsyncAckSpec — шаблон ответа меняет то, что видит КЛИЕНТ узла.
// Включение, правка и выключение обязаны попадать в аудит: иначе «почему
// интеграция вдруг получает другой ответ» по журналу не восстановить.
func TestDiffNodes_AsyncAckSpec(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		old, new *ackspec.Spec
		wantDiff bool
	}{
		{"enabled", nil, auditSpec(`{"ok": true}`), true},
		{"disabled", auditSpec(`{"ok": true}`), nil, true},
		{"template edited", auditSpec(`{"a": 1}`), auditSpec(`{"a": 2}`), true},
		{"unchanged", auditSpec(`{"a": 1}`), auditSpec(`{"a": 1}`), false},
		{"both absent", nil, nil, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			old := &domain.Node{Path: "n", AsyncAck: tc.old}
			updated := &domain.Node{Path: "n", AsyncAck: tc.new}

			d := diffNodes(old, updated)

			_, present := d["async_ack_spec"]
			if present != tc.wantDiff {
				t.Fatalf("async_ack_spec in diff = %v, want %v (diff=%v)", present, tc.wantDiff, d)
			}
		})
	}
}

// TestAckSpecAudit_TruncatesBody — в журнале нужен факт и узнаваемый фрагмент,
// а не 8 КиБ шаблона.
func TestAckSpecAudit_TruncatesBody(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("x", ackSpecAuditMaxBody*2)
	got := ackSpecAudit(auditSpec(long))

	if len(got) > ackSpecAuditMaxBody+100 {
		t.Fatalf("audit value too long: %d chars", len(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncation must be visible, got tail %q", got[len(got)-10:])
	}
	if !strings.Contains(got, "content_type=application/json") {
		t.Errorf("audit value must carry the settings, got %q", got)
	}
	if ackSpecAudit(nil) != "" {
		t.Errorf("nil spec must render as empty value, got %q", ackSpecAudit(nil))
	}
}
