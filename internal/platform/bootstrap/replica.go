package bootstrap

import (
	"os"
	"sync"
)

// EnvReplicaName — переменная окружения, которой можно задать имя реплики явно.
// Пусто — берётся hostname процесса.
const EnvReplicaName = "NEXUS_REPLICA"

// replicaOnce кэширует результат: hostname за время жизни процесса не меняется,
// а спрашивают его и логгер, и Sentry, и (при перезагрузке настроек) reloader.
var replicaOnce = sync.OnceValue(resolveReplicaName)

// ReplicaName — имя ЭКЗЕМПЛЯРА сервиса (§93.6): что именно писало эту строку
// лога и откуда прилетело событие Sentry.
//
// Не путать с instance.id (§70.7): тот идентифицирует НОДУ — самостоятельное
// развёртывание со своими PostgreSQL/Redis/Kafka. У двух реплик одной ноды
// instance.id ОДИНАКОВ, и различить их без этого имени нечем.
//
// Источник — hostname, который docker compose выставляет равным имени сервиса
// (`web-1`, `receiver-2`). Переменная NEXUS_REPLICA перекрывает его для
// развёртываний, где hostname неинформативен (нативный запуск под systemd,
// несколько процессов на одной машине).
//
// Пустая строка — легальный результат: hostname может не определиться, и это
// не повод падать. Потребители (logsink, sentry) при пустом значении просто не
// добавляют поле/тег.
func ReplicaName() string { return replicaOnce() }

func resolveReplicaName() string {
	if v := os.Getenv(EnvReplicaName); v != "" {
		return v
	}
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return h
}
