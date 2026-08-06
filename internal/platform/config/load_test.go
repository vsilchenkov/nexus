package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExpandEnv(t *testing.T) {
	// t.Parallel() несовместим с t.Setenv (см. golang.org/issue/53686).

	t.Setenv("DBT_PRESENT", "the-value")
	// DBT_ABSENT нарочно не выставляем

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"plain", "no env here", "no env here"},
		{"present", "x=${DBT_PRESENT}", "x=the-value"},
		{"absent_no_default_empty", "x=${DBT_ABSENT}", "x="},
		{"absent_with_default", "x=${DBT_ABSENT:fallback}", "x=fallback"},
		{"present_default_ignored", "x=${DBT_PRESENT:fallback}", "x=the-value"},
		{"multiple_substitutions",
			"a=${DBT_PRESENT};b=${DBT_ABSENT:def}",
			"a=the-value;b=def"},
		{"default_with_special_chars",
			"x=${DBT_ABSENT:http://localhost:8080}",
			"x=http://localhost:8080"},
		{"empty_default",
			"x=${DBT_ABSENT:}",
			"x="},
		// Граничный кейс: $ без скобок не должно интерпретироваться.
		{"plain_dollar_kept", "amount=$5.99", "amount=$5.99"},
		// «${...}» с невалидным именем переменной (начинается с цифры) — не должен распознаваться.
		{"invalid_name_kept", "x=${1ABC}", "x=${1ABC}"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := expandEnv([]byte(tc.input))
			assert.Equal(t, tc.want, string(got))
		})
	}
}

func TestResolveConfigPath(t *testing.T) {
	// t.Parallel() несовместим с t.Setenv/Unsetenv.

	const wd = "/work/dir"

	tests := []struct {
		name     string
		flags    Flags
		envValue string
		want     string
	}{
		{
			name:  "explicit_flag_highest_priority",
			flags: Flags{ConfigPath: "/explicit/path.yml", Debug: true},
			// env тоже выставляется, но флаг должен победить
			envValue: "/from/env.yml",
			want:     "/explicit/path.yml",
		},
		{
			name:     "env_when_no_flag",
			flags:    Flags{},
			envValue: "/from/env.yml",
			want:     "/from/env.yml",
		},
		{
			name:  "debug_fallback",
			flags: Flags{Debug: true},
			want:  filepath.Join(wd, defaultDebugConfigPath),
		},
		{
			name:  "production_default",
			flags: Flags{},
			want:  filepath.Join(wd, defaultConfigPath),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.envValue != "" {
				t.Setenv(envConfigVar, tc.envValue)
			} else {
				// Изолируем подтест от чужих env (через t.Setenv("", "") нельзя — Unsetenv).
				require.NoError(t, os.Unsetenv(envConfigVar))
			}
			got := resolveConfigPath(tc.flags, wd)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestApplyDefaults_NotOverwritingNonZero(t *testing.T) {
	t.Parallel()

	// Все ненулевые значения должны быть сохранены, нулевые — заполнены defaults.
	c := &Config{
		Postgres: PostgresSection{
			Host:         "db",
			Database:     "x",
			MaxOpenConns: 7, // явное значение, не должен затереться
		},
	}
	applyDefaults(c)

	assert.Equal(t, 7, c.Postgres.MaxOpenConns, "явное значение не должно затираться")
	assert.Equal(t, 5432, c.Postgres.Port, "нулевой port должен получить default")
	assert.Equal(t, 5, c.Postgres.MaxIdleConns)
	assert.Equal(t, 30, c.Postgres.ConnMaxLifetimeMin)
	// Не выставлены — должны получить defaults.
	assert.Equal(t, 6379, c.Redis.Port)
	assert.Equal(t, 50, c.Redis.PoolSize)
	assert.Equal(t, ":8080", c.Receiver.HTTPAddr)
	assert.Equal(t, "strict", c.Web.SessionCookieSamesite)
	// §82.1: пустая секция grpc_keepalive обязана дать рабочую политику, а не
	// нули — иначе MinTime=0 отключил бы проверку темпа и вернул дефолт grpc-go
	// (5 минут) со всеми последствиями.
	assert.Equal(t, 5, c.Sender.GRPCKeepalive.EnforcementMinTimeSec)
	assert.False(t, c.Sender.GRPCKeepalive.DenyPingWithoutStream,
		"ping без активных стримов должен быть разрешён по умолчанию (флаг инвертирован намеренно)")
}

func TestValidate(t *testing.T) {
	t.Parallel()

	// Минимальный «валидный» конфиг — все обязательные поля заполнены.
	mkValid := func() *Config {
		c := &Config{
			Postgres:   PostgresSection{Host: "pg", Database: "db"},
			Redis:      RedisSection{Host: "r"},
			ClickHouse: ClickHouseSection{Host: "ch", Database: "logs"},
			Kafka:      KafkaSection{Brokers: "k:9092"},
		}
		applyDefaults(c) // выставит SessionCookieSamesite=strict
		return c
	}

	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{"nil_config", func(_ *Config) {}, ""}, // спец-кейс ниже
		{"valid", func(_ *Config) {}, ""},
		{"missing_postgres_host",
			func(c *Config) { c.Postgres.Host = "" },
			"postgres.host"},
		{"missing_postgres_database",
			func(c *Config) { c.Postgres.Database = "" },
			"postgres.database"},
		{"missing_redis_host",
			func(c *Config) { c.Redis.Host = "" },
			"redis.host"},
		{"missing_clickhouse_host",
			func(c *Config) { c.ClickHouse.Host = "" },
			"clickhouse.host"},
		{"missing_clickhouse_database",
			func(c *Config) { c.ClickHouse.Database = "" },
			"clickhouse.database"},
		{"missing_kafka_brokers",
			func(c *Config) { c.Kafka.Brokers = "" },
			"kafka.brokers"},
		{"bad_samesite",
			func(c *Config) { c.Web.SessionCookieSamesite = "wrong" },
			"session_cookie_samesite"},
		{"sentry_use_no_dsn",
			func(c *Config) { c.Sentry.Use = true; c.Sentry.Dsn = "" },
			"sentry.dsn"},
		// §70.1: идентификатор ноды. Пустой — штатное состояние ноды до §70,
		// поэтому валиден; кривой обязан валить старт, а не всплывать позже при
		// создании команды с несобираемым именем БД.
		{"instance_id_empty_ok",
			func(c *Config) { c.Instance.ID = "" },
			""},
		{"instance_id_valid",
			func(c *Config) { c.Instance.ID = "kz" },
			""},
		{"instance_id_bad_format",
			func(c *Config) { c.Instance.ID = "KZ_1" },
			"instance.id"},
		{"instance_id_too_long",
			func(c *Config) { c.Instance.ID = "abcdefghi" },
			"instance.id"},
		// §82.1: enforcement-политика keepalive не должна быть строже темпа
		// клиентских ping'ов — иначе gRPC-сервер оборвёт соединение вместе с
		// идущим по нему sync-вызовом, а в логе это будет выглядеть уходом
		// клиента. Ловим на старте, потому что по симптому не вычисляется.
		{"grpc_keepalive_min_time_out_of_range",
			func(c *Config) { c.Sender.GRPCKeepalive.EnforcementMinTimeSec = 3_601 },
			"sender.grpc_keepalive.enforcement_min_time_sec"},
		{"grpc_keepalive_stricter_than_receiver_client",
			func(c *Config) {
				c.Receiver.SenderGRPC.KeepaliveTimeSec = 30
				c.Sender.GRPCKeepalive.EnforcementMinTimeSec = 60
			},
			"receiver.sender_grpc.keepalive_time_sec"},
		{"grpc_keepalive_stricter_than_web_client",
			func(c *Config) {
				c.Web.SenderGRPC.KeepaliveTimeSec = 10
				c.Sender.GRPCKeepalive.EnforcementMinTimeSec = 30
			},
			"web.sender_grpc.keepalive_time_sec"},
		{"grpc_keepalive_equal_to_client_ok",
			func(c *Config) {
				c.Receiver.SenderGRPC.KeepaliveTimeSec = 30
				c.Sender.GRPCKeepalive.EnforcementMinTimeSec = 30
			},
			""},
		// Клиент с keepalive_time_sec=0 не пингует вообще (grpc-go подставляет
		// бесконечность) и нарушителем стать не может — политика любой строгости
		// для него безопасна.
		{"grpc_keepalive_client_disabled_ok",
			func(c *Config) {
				c.Receiver.SenderGRPC.KeepaliveTimeSec = 0
				c.Web.SenderGRPC.KeepaliveTimeSec = 0
				c.Sender.GRPCKeepalive.EnforcementMinTimeSec = 3_600
			},
			""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "nil_config" {
				err := Validate(nil)
				require.Error(t, err)
				return
			}
			c := mkValid()
			tc.mutate(c)
			err := Validate(c)
			if tc.wantErr == "" {
				assert.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
			}
		})
	}
}

func TestLoad_EndToEnd_WithEnvSubstitution(t *testing.T) {
	// t.Parallel() несовместим с t.Setenv.

	t.Setenv("DBT_E2E_PASSWORD", "secret-pw")
	require.NoError(t, os.Unsetenv("DBT_E2E_MISSING"))

	yaml := `
build:
  project_name: nexus
  version: dev

logging:
  level: 4

postgres:
  host: pg-host
  database: ${DBT_E2E_MISSING:bus_db}
  password: ${DBT_E2E_PASSWORD}

redis:
  host: redis-host

clickhouse:
  host: ch-host
  database: logs

kafka:
  brokers: kafka:9092

web:
  session_cookie_samesite: lax
`
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	require.NoError(t, os.WriteFile(path, []byte(yaml), 0o644))

	cfg, gotPath, err := Load(Flags{ConfigPath: path}, dir)
	require.NoError(t, err)

	assert.Equal(t, path, gotPath)
	assert.Equal(t, "pg-host", cfg.Postgres.Host)
	assert.Equal(t, "bus_db", cfg.Postgres.Database, "default из ${...:bus_db} применился")
	assert.Equal(t, "secret-pw", cfg.Postgres.Password, "env-substitution не сработала")
	assert.Equal(t, "lax", cfg.Web.SessionCookieSamesite)
	// applyDefaults должен был выставить, что мы не указали:
	assert.Equal(t, 5432, cfg.Postgres.Port, "default port")
	assert.Equal(t, ":8080", cfg.Receiver.HTTPAddr, "default Receiver.HTTPAddr")
}

func TestLoad_FailsOnInvalidYaml(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	require.NoError(t, os.WriteFile(path, []byte("postgres:\n  host: [unclosed"), 0o644))

	_, _, err := Load(Flags{ConfigPath: path}, dir)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "parse yaml") || strings.Contains(err.Error(), "yaml:"),
		"ошибка должна указать на parse, got: %v", err)
}

func TestLoad_FailsOnMissingFile(t *testing.T) {
	t.Parallel()

	_, _, err := Load(Flags{ConfigPath: "/no/such/file.yml"}, "/")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read config")
}

func TestLoad_FailsValidationWithCleanMessage(t *testing.T) {
	t.Parallel()

	// Заполнен только postgres.host — должна сработать validate.
	yaml := `
postgres:
  host: pg
  database: x
redis:
  host: r
clickhouse:
  host: ch
  # database намеренно опущен — validate должен указать на него
kafka:
  brokers: k:9092
`
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	require.NoError(t, os.WriteFile(path, []byte(yaml), 0o644))

	_, _, err := Load(Flags{ConfigPath: path}, dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "validate config")
	assert.Contains(t, err.Error(), "clickhouse.database")
}
