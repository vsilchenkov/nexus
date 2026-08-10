package usecase

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/domain/ackspec"
	"nexus/internal/platform/logging"
)

// §84.9: пересчёт ответа шины по записи журнала. Отрендеренный ответ приёма не
// сохраняется НИГДЕ (Receiver отдаёт его в сокет и с ClickHouse не соединён),
// поэтому журнал показывает расчёт по текущему шаблону, а не факт.

// sigurSpec — боевая спека узла acs_sigur: СКУД двигает свой курсор только по
// эхо-подтверждению с максимальным logId пакета.
func sigurSpec() *ackspec.Spec {
	return &ackspec.Spec{
		Version:     1,
		ContentType: ackspec.ContentTypeJSON,
		Body:        `{"confirmedLogId": "${ body.logs[*].logId | max }"}`,
		OnError:     ackspec.OnErrorError,
	}
}

const sigurBody = `{"logs":[{"logId":79569,"time":1786095775,"empId":"000005859"}]}`

func ackUC(node *domain.Node, rec *domain.LogRecord) *LogsUsecase {
	return NewLogsUsecase(
		&logReaderMock{getRow: rec},
		&stubNodeRepo{nodes: map[string]*domain.Node{node.ID: node}},
		logging.NewNoop(),
	)
}

func asyncRecord(body string, at time.Time) *domain.LogRecord {
	return &domain.LogRecord{
		ID:          "9c0a7ca8-9e44-4f5e-bca6-ec873409d51b",
		Type:        domain.RootMethodRequestAsync,
		Request:     body,
		RequestSize: int64(len(body)),
		DateRequest: at,
	}
}

func ackNode(root domain.RootMethod, spec *ackspec.Spec, updatedAt time.Time) *domain.Node {
	return &domain.Node{
		ID:              "n1",
		Path:            "acs_sigur",
		TeamID:          "team-a",
		RootMethod:      root,
		ClickHouseTable: "nexus_webhook.acs_sigur",
		Status:          domain.NodeStatusEnabled,
		LogRequestBody:  true,
		AsyncAck:        spec,
		UpdatedAt:       updatedAt,
	}
}

func TestLogsUsecase_AckFromLog(t *testing.T) {
	t.Parallel()
	reqAt := time.Date(2026, 8, 5, 14, 12, 3, 0, time.UTC)
	before := reqAt.Add(-time.Hour)
	after := reqAt.Add(time.Hour)

	t.Run("боевой acs_sigur: число, а не строка (правило кавычек §83)", func(t *testing.T) {
		t.Parallel()
		uc := ackUC(ackNode(domain.RootMethodRequestAsync, sigurSpec(), before), asyncRecord(sigurBody, reqAt))

		rep, err := uc.AckFromLog(context.Background(), "n1", "team-a", "log-1")
		require.NoError(t, err)
		require.True(t, rep.Applicable)
		require.True(t, rep.OK)
		// Именно 79569, а не "79569": плейсхолдер вплотную в кавычках
		// подставляется JSON-литералом вместе с ними.
		require.JSONEq(t, `{"confirmedLogId": 79569}`, rep.Body)
		require.Equal(t, `{"confirmedLogId": 79569}`, rep.Body)
	})

	t.Run("у узла выключено изменение ответа → блока нет", func(t *testing.T) {
		t.Parallel()
		uc := ackUC(ackNode(domain.RootMethodRequestAsync, nil, before), asyncRecord(sigurBody, reqAt))

		rep, err := uc.AckFromLog(context.Background(), "n1", "team-a", "log-1")
		require.NoError(t, err)
		require.False(t, rep.Applicable)
		require.Equal(t, AckNotApplicableNoSpec, rep.NotApplicableReason)
	})

	t.Run("запись прошла синхронным путём → блока нет", func(t *testing.T) {
		t.Parallel()
		rec := asyncRecord(sigurBody, reqAt)
		rec.Type = domain.RootMethodRequest
		uc := ackUC(ackNode(domain.RootMethodRequestAsync, sigurSpec(), before), rec)

		rep, err := uc.AckFromLog(context.Background(), "n1", "team-a", "log-1")
		require.NoError(t, err)
		require.False(t, rep.Applicable)
		require.Equal(t, AckNotApplicableSyncRecord, rep.NotApplicableReason)
	})

	// ГЛАВНЫЙ смысл гейта по ЗАПИСИ, а не по root_method узла: по §83.5 спека
	// переживает перевод узла в sync и продолжает работать, пока узел на паузе
	// (§3.6). У такого узла в журнале лежат записи, чей ответ шаблон РЕАЛЬНО
	// формировал, и прятать их было бы неправдой. На наивной реализации
	// «гейт по root_method» этот кейс красный.
	t.Run("узел снят с async, но запись асинхронная → блок ЕСТЬ", func(t *testing.T) {
		t.Parallel()
		uc := ackUC(ackNode(domain.RootMethodRequest, sigurSpec(), before), asyncRecord(sigurBody, reqAt))

		rep, err := uc.AckFromLog(context.Background(), "n1", "team-a", "log-1")
		require.NoError(t, err)
		require.True(t, rep.Applicable, "спека переживает смену типа узла (§83.5)")
		require.True(t, rep.OK)
	})

	t.Run("тело не логируется → блока нет, рендер не запускается", func(t *testing.T) {
		t.Parallel()
		rec := asyncRecord("", reqAt)
		uc := ackUC(ackNode(domain.RootMethodRequestAsync, sigurSpec(), before), rec)

		rep, err := uc.AckFromLog(context.Background(), "n1", "team-a", "log-1")
		require.NoError(t, err)
		require.False(t, rep.Applicable)
		require.Equal(t, AckNotApplicableBodyNotSaved, rep.NotApplicableReason)
	})

	t.Run("усечённое тело помечается: подстановка могла не найти значение", func(t *testing.T) {
		t.Parallel()
		rec := asyncRecord(sigurBody, reqAt)
		rec.RequestSize = int64(len(sigurBody)) + 1000 // сохранили меньше, чем пришло
		uc := ackUC(ackNode(domain.RootMethodRequestAsync, sigurSpec(), before), rec)

		rep, err := uc.AckFromLog(context.Background(), "n1", "team-a", "log-1")
		require.NoError(t, err)
		require.Equal(t, AckBodySourceTruncated, rep.BodySource)
	})

	// Признак консервативный: updated_at меняется от ЛЮБОЙ правки узла, не
	// только спеки. Ложная тревога дешевле молчания.
	t.Run("узел менялся после запроса → предупреждение с обеими датами", func(t *testing.T) {
		t.Parallel()
		uc := ackUC(ackNode(domain.RootMethodRequestAsync, sigurSpec(), after), asyncRecord(sigurBody, reqAt))

		rep, err := uc.AckFromLog(context.Background(), "n1", "team-a", "log-1")
		require.NoError(t, err)
		require.True(t, rep.SpecChangedAfterRequest)
		require.Equal(t, after.UnixMilli(), rep.SpecUpdatedAtMs)
		require.Equal(t, reqAt.UnixMilli(), rep.RequestAtMs)
	})

	t.Run("узел не менялся после запроса → предупреждения нет", func(t *testing.T) {
		t.Parallel()
		uc := ackUC(ackNode(domain.RootMethodRequestAsync, sigurSpec(), before), asyncRecord(sigurBody, reqAt))

		rep, err := uc.AckFromLog(context.Background(), "n1", "team-a", "log-1")
		require.NoError(t, err)
		require.False(t, rep.SpecChangedAfterRequest)
	})

	// query входящего запроса в журнале не хранится вовсе (колонка parameters
	// несёт query ИСХОДЯЩЕГО URL), поэтому ${ query.… } обязан честно упасть с
	// предупреждением, а не молча подставить чужое значение.
	t.Run("шаблон с query: ok=false и предупреждение query_not_stored", func(t *testing.T) {
		t.Parallel()
		spec := sigurSpec()
		spec.Body = `{"token": "${ query.token }"}`
		uc := ackUC(ackNode(domain.RootMethodRequestAsync, spec, before), asyncRecord(sigurBody, reqAt))

		rep, err := uc.AckFromLog(context.Background(), "n1", "team-a", "log-1")
		require.NoError(t, err)
		require.True(t, rep.Applicable)
		require.False(t, rep.OK)
		require.NotEmpty(t, rep.Reason)
		require.Contains(t, rep.Warnings, AckWarnQueryNotStored)
	})

	t.Run("политика on_error доезжает наружу", func(t *testing.T) {
		t.Parallel()
		spec := sigurSpec()
		spec.OnError = ackspec.OnErrorDefault
		uc := ackUC(ackNode(domain.RootMethodRequestAsync, spec, before), asyncRecord(sigurBody, reqAt))

		rep, err := uc.AckFromLog(context.Background(), "n1", "team-a", "log-1")
		require.NoError(t, err)
		require.Equal(t, string(ackspec.OnErrorDefault), rep.OnError)
	})

	// Битая спека в БД (правка SQL мимо валидации формы) — не 500: оператору
	// нужна причина, а не «сервер сломался».
	t.Run("невалидный шаблон в БД → ok=false, а не ошибка вызова", func(t *testing.T) {
		t.Parallel()
		spec := sigurSpec()
		spec.Body = `{"a": ${ body.` // незакрытая подстановка
		uc := ackUC(ackNode(domain.RootMethodRequestAsync, spec, before), asyncRecord(sigurBody, reqAt))

		rep, err := uc.AckFromLog(context.Background(), "n1", "team-a", "log-1")
		require.NoError(t, err)
		require.False(t, rep.OK)
		require.NotEmpty(t, rep.Message)
	})

	t.Run("чужая команда неотличима от несуществующего узла", func(t *testing.T) {
		t.Parallel()
		uc := ackUC(ackNode(domain.RootMethodRequestAsync, sigurSpec(), before), asyncRecord(sigurBody, reqAt))

		_, err := uc.AckFromLog(context.Background(), "n1", "team-b", "log-1")
		require.Error(t, err)
	})
}
