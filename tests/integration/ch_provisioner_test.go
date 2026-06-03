//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/clickhouse"
	"nexus/internal/platform/logging"
	"nexus/internal/sender/adapter/out/chlog"
	webch "nexus/internal/web/adapter/out/clickhouse"
)

// TestCHProvisioner_CreateTable_E2E (§19, Phase F1.3): provisioner создаёт
// таблицу из дефолтного шаблона; Sender-writer пишет в неё батч, LogReader
// читает — доказывает совместимость сгенерированного DDL со схемой §4.3.
// Плюс VerifyTemplate: валидный шаблон проходит, несовместимый CODEC ловится
// «вживую».
func TestCHProvisioner_CreateTable_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	conn, cfg, cleanup := startClickHouse(t, ctx)
	defer cleanup()

	require.NoError(t, conn.Exec(ctx, "CREATE DATABASE IF NOT EXISTS nexus_default"))

	logger := logging.NewNoop()
	provider := clickhouse.StaticProvider(conn)
	prov := webch.NewTeamProvisioner(provider, logger)

	tmpl := &domain.CHTemplate{Name: "Standard logs", Spec: domain.DefaultCHTemplateSpec()}

	const table = "nexus_default.f13_logs"
	ddl, err := tmpl.RenderCreateTable(table, 0)
	require.NoError(t, err)
	require.NoError(t, prov.CreateTable(ctx, table, ddl))
	// Идемпотентность.
	require.NoError(t, prov.CreateTable(ctx, table, ddl))

	// Запись через Sender-writer + чтение через LogReader.
	writer := chlog.New(provider, cfg, logger)
	defer writer.Stop(ctx)
	rec := &domain.LogRecord{
		ID: "00000000-0000-0000-0000-0000000000aa", Type: domain.RootMethodRequest,
		URL: "https://x", Method: "POST", Request: "{}", Response: "{}",
		Status: 200, DateCreate: time.Now().UTC(), DateRequest: time.Now().UTC(),
		DateResponse: time.Now().UTC(), Duration: 1, Done: true,
		ChecksumRequest:  "00000000000000000000000000000000",
		ChecksumResponse: "00000000000000000000000000000000",
		Host:             "h", IP: "127.0.0.1", Attempts: 1, AttemptsDetails: "[]",
	}
	writer.Write(ctx, table, rec)
	require.NoError(t, writer.Flush(ctx))

	deadline := time.Now().Add(20 * time.Second)
	var n uint64
	for time.Now().Before(deadline) {
		require.NoError(t, conn.QueryRow(ctx, fmt.Sprintf("SELECT count() FROM %s", table)).Scan(&n))
		if n >= 1 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	require.EqualValues(t, 1, n, "row should be written into generated table")

	reader := webch.NewLogReader(provider, logger)
	got, err := reader.GetByID(ctx, table, rec.ID)
	require.NoError(t, err)
	require.EqualValues(t, 200, got.Status)

	// VerifyTemplate: валидный шаблон — ok.
	require.NoError(t, prov.VerifyTemplate(ctx, "nexus_default", tmpl))

	// Несовместимый CODEC (Delta на String) проходит статическую валидацию,
	// но падает в ClickHouse — VerifyTemplate должен вернуть ошибку.
	bad := &domain.CHTemplate{Name: "bad codec", Spec: domain.DefaultCHTemplateSpec()}
	bad.Spec.ColumnOverrides = []domain.CHColumnOverride{{Name: "request", Codec: "Delta"}}
	require.NoError(t, bad.Validate(), "static validation passes")
	require.Error(t, prov.VerifyTemplate(ctx, "nexus_default", bad), "live verify must fail")
}
