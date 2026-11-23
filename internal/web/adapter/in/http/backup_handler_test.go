package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain/backup"
	"nexus/internal/platform/backupfile"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase"
	"nexus/internal/web/usecase/port"
)

// fakeBackupService — usecase раздела с заранее заданными ответами.
type fakeBackupService struct {
	status     *usecase.BackupStatus
	file       *usecase.BackupFile
	err        error
	passphrase string
	preview    *usecase.BackupImportPreview
	importReq  usecase.BackupImportRequest
	result     *usecase.BackupImportResult
	applyReq   usecase.BackupApplyRequest
}

func (f *fakeBackupService) Status(context.Context) (*usecase.BackupStatus, error) {
	return f.status, f.err
}

func (f *fakeBackupService) Export(_ context.Context, _ usecase.Actor, passphrase string) (*usecase.BackupFile, error) {
	f.passphrase = passphrase
	return f.file, f.err
}

func (f *fakeBackupService) ApplyImport(_ context.Context, _ usecase.Actor, req usecase.BackupApplyRequest) (*usecase.BackupImportResult, error) {
	f.applyReq = req
	return f.result, f.err
}

func (f *fakeBackupService) PreviewImport(_ context.Context, _ usecase.Actor, req usecase.BackupImportRequest) (*usecase.BackupImportPreview, error) {
	f.importReq = req
	return f.preview, f.err
}

func backupEngine(svc backupService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewBackupHandler(svc, logging.NewNoop())
	r.GET("/api/backup/status", h.Status)
	r.POST("/api/backup/export", h.Export)
	r.POST("/api/backup/import/preview", h.PreviewImport)
	r.POST("/api/backup/import/apply", h.ApplyImport)
	return r
}

func TestBackupHandler_Export(t *testing.T) {
	t.Parallel()
	svc := &fakeBackupService{file: &usecase.BackupFile{
		Name: "nexus-backup-kz-20260929-173005.json", Data: []byte(`{"format":"nexus-config-backup"}`),
	}}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/backup/export",
		strings.NewReader(`{"passphrase":"correct horse battery"}`))
	backupEngine(svc).ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "correct horse battery", svc.passphrase)
	assert.Equal(t, `attachment; filename="nexus-backup-kz-20260929-173005.json"`, w.Header().Get("Content-Disposition"))
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, `{"format":"nexus-config-backup"}`, w.Body.String())
}

func TestBackupHandler_ExportErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		body     string
		err      error
		wantCode int
		wantKey  string
	}{
		{"нет пароля", `{}`, nil, http.StatusBadRequest, "backup.passphrase_required"},
		{"пароль в query не принимается", ``, nil, http.StatusBadRequest, "backup.passphrase_required"},
		{"короткий пароль", `{"passphrase":"short"}`, backupfile.ErrPassphraseTooShort, http.StatusBadRequest, "backup.passphrase_too_short"},
		{"снимок не восстановится", `{"passphrase":"correct horse battery"}`,
			fmt.Errorf("export: %w", backup.ErrInvalidSnapshot), http.StatusInternalServerError, "backup.snapshot_invalid"},
		{"прочая ошибка", `{"passphrase":"correct horse battery"}`, errors.New("pg down"), http.StatusInternalServerError, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/backup/export?passphrase=correct+horse+battery",
				strings.NewReader(tt.body))
			backupEngine(&fakeBackupService{err: tt.err}).ServeHTTP(w, req)
			require.Equal(t, tt.wantCode, w.Code)
			assert.Empty(t, w.Header().Get("Content-Disposition"), "при ошибке файл не отдаётся")
			if tt.wantKey == "" {
				return
			}
			var resp map[string]string
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			assert.Equal(t, tt.wantKey, resp["code"])
			assert.NotContains(t, resp["error"], "%!", "аргумент перевода подставлен")
		})
	}
}

func TestBackupHandler_Status(t *testing.T) {
	t.Parallel()
	svc := &fakeBackupService{status: &usecase.BackupStatus{
		Nodes: 0, Teams: 1, Users: 1, Fresh: true,
		CHAvailable: true, InstanceID: "kz", NexusVersion: "v1.39.0", SchemaVersion: 43,
	}}
	w := httptest.NewRecorder()
	backupEngine(svc).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/backup/status", nil))

	require.Equal(t, http.StatusOK, w.Code)
	var resp BackupStatusResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, BackupStatusResponse{
		Nodes: 0, Teams: 1, Users: 1, Clean: true, ClickHouseAvailable: true,
		InstanceID: "kz", NexusVersion: "v1.39.0", SchemaVersion: 43,
		MinPassphraseLen: backupfile.MinPassphraseLen,
	}, resp)
}

func TestBackupHandler_PreviewImport(t *testing.T) {
	t.Parallel()
	svc := &fakeBackupService{preview: &usecase.BackupImportPreview{Digest: "abc"}}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/backup/import/preview", strings.NewReader(
		`{"file":"{\"format\":\"nexus-config-backup\"}","passphrase":"correct horse battery",`+
			`"options":{"status_mode":"pause_all","adopt_unclaimed":true}}`))
	req.Host = "nexus-new.example.ru"
	backupEngine(svc).ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{"format":"nexus-config-backup"}`, string(svc.importReq.File), "файл передаётся текстом как есть")
	assert.Equal(t, usecase.BackupImportOptions{StatusMode: usecase.BackupStatusPauseAll, AdoptUnclaimed: true},
		svc.importReq.Options)
	assert.Equal(t, "nexus-new.example.ru", svc.importReq.SelfHost)
	assert.Contains(t, w.Body.String(), `"digest":"abc"`)
}

func TestBackupHandler_PreviewImportErrors(t *testing.T) {
	t.Parallel()
	body := `{"file":"x","passphrase":"correct horse battery"}`
	tests := []struct {
		name     string
		body     string
		err      error
		wantCode int
		wantKey  string
	}{
		{"нет файла", `{"passphrase":"correct horse battery"}`, nil, http.StatusBadRequest, "backup.import_request_invalid"},
		{"слишком большой", `{"file":"` + strings.Repeat("a", backupImportMaxBody) + `"}`, nil,
			http.StatusRequestEntityTooLarge, "backup.file_too_large"},
		{"нечистая цель", body, port.ErrTargetNotClean, http.StatusConflict, "backup.target_not_clean"},
		{"импорт уже идёт", body, port.ErrRestoreInProgress, http.StatusConflict, "backup.restore_in_progress"},
		{"нет ClickHouse", body, usecase.ErrBackupCHUnavailable, http.StatusConflict, "backup.clickhouse_unavailable"},
		{"неверный пароль", body, fmt.Errorf("open: %w", backupfile.ErrDecrypt), http.StatusBadRequest, "backup.decrypt_failed"},
		{"не файл копии", body, backupfile.ErrNotBackupFile, http.StatusBadRequest, "backup.not_backup_file"},
		{"схема новее", body, usecase.ErrBackupSchemaNewer, http.StatusUnprocessableEntity, "backup.schema_newer"},
		{"битый снимок", body, fmt.Errorf("%w: nodes.team_id", backup.ErrInvalidSnapshot), http.StatusUnprocessableEntity, "backup.file_invalid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			w := httptest.NewRecorder()
			backupEngine(&fakeBackupService{err: tt.err}).ServeHTTP(w,
				httptest.NewRequest(http.MethodPost, "/api/backup/import/preview", strings.NewReader(tt.body)))
			require.Equal(t, tt.wantCode, w.Code)
			var resp map[string]string
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			assert.Equal(t, tt.wantKey, resp["code"])
		})
	}
}

func TestBackupHandler_ApplyImport(t *testing.T) {
	t.Parallel()
	svc := &fakeBackupService{result: &usecase.BackupImportResult{TablesCreated: 3}}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/backup/import/apply", strings.NewReader(
		`{"file":"x","passphrase":"correct horse battery","options":{"status_mode":"as_file"},`+
			`"preview_digest":"abc","confirm_host":"nexus-new.example.ru"}`))
	req.Host = "nexus-new.example.ru:8443"
	backupEngine(svc).ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "abc", svc.applyReq.Digest)
	assert.Equal(t, "nexus-new.example.ru", svc.applyReq.ConfirmHost)
	assert.Equal(t, "nexus-new.example.ru:8443", svc.applyReq.SelfHost, "адрес сервера — из Host запроса, не из тела")
	assert.Equal(t, usecase.BackupStatusAsFile, svc.applyReq.Options.StatusMode)
	assert.Contains(t, w.Body.String(), `"tables_created":3`)
}

func TestBackupHandler_ApplyImportErrors(t *testing.T) {
	t.Parallel()
	body := `{"file":"x","passphrase":"correct horse battery","preview_digest":"abc","confirm_host":"h"}`
	tests := []struct {
		name       string
		body       string
		err        error
		wantCode   int
		wantKey    string
		wantDetail bool
	}{
		{"нет дайджеста", `{"file":"x","passphrase":"correct horse battery","confirm_host":"h"}`, nil,
			http.StatusBadRequest, "backup.import_request_invalid", false},
		{"адрес не совпал", body, usecase.ErrBackupConfirmHost, http.StatusBadRequest, "backup.confirm_host_mismatch", false},
		{"не тот предпросмотр", body, usecase.ErrBackupDigestMismatch, http.StatusConflict, "backup.digest_mismatch", false},
		{"блокировки", body, fmt.Errorf("%w: ch_database_foreign", usecase.ErrBackupBlocked), http.StatusConflict, "backup.import_blocked", false},
		{"сбой ClickHouse", body, fmt.Errorf("%w: table x: code 57", usecase.ErrBackupCHProvision),
			http.StatusInternalServerError, "backup.clickhouse_failed", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			w := httptest.NewRecorder()
			backupEngine(&fakeBackupService{err: tt.err}).ServeHTTP(w,
				httptest.NewRequest(http.MethodPost, "/api/backup/import/apply", strings.NewReader(tt.body)))
			require.Equal(t, tt.wantCode, w.Code)
			var resp map[string]string
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			assert.Equal(t, tt.wantKey, resp["code"])
			assert.Equal(t, tt.wantDetail, resp["detail"] != "")
		})
	}
}
