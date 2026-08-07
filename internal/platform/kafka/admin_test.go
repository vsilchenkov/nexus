package kafka

import (
	"errors"
	"log/slog"
	"testing"

	segkafka "github.com/segmentio/kafka-go"

	"nexus/internal/platform/config"
	"nexus/internal/platform/logging"
)

// stubPartitionReader — источник метаданных топиков без брокера.
type stubPartitionReader struct {
	parts []segkafka.Partition
	err   error
}

func (s stubPartitionReader) ReadPartitions(...string) ([]segkafka.Partition, error) {
	return s.parts, s.err
}

// recordingLogger — собирает записи лога: вся суть правки в том, ЧТО именно
// увидит оператор, поэтому проверяется содержимое сообщения, а не результат
// функции (она ничего не возвращает).
type recordingLogger struct {
	logging.Logger
	infos []logEntry
	warns []logEntry
}

type logEntry struct {
	msg   string
	attrs map[string]string
}

func newRecordingLogger() *recordingLogger {
	return &recordingLogger{Logger: logging.NewNoop()}
}

func (l *recordingLogger) Info(msg string, attrs ...slog.Attr) {
	l.infos = append(l.infos, entry(msg, attrs))
}

func (l *recordingLogger) Warn(msg string, attrs ...slog.Attr) {
	l.warns = append(l.warns, entry(msg, attrs))
}

func entry(msg string, attrs []slog.Attr) logEntry {
	m := make(map[string]string, len(attrs))
	for _, a := range attrs {
		m[a.Key] = a.Value.String()
	}
	return logEntry{msg: msg, attrs: m}
}

func testCfg(partitions, instances int) *config.Config {
	cfg := &config.Config{}
	cfg.Kafka.Topic.Partitions = partitions
	cfg.Kafka.Topic.ReplicationFactor = 1
	cfg.Kafka.Consumer.Instances = instances
	return cfg
}

func parts(topic string, n int) []segkafka.Partition {
	out := make([]segkafka.Partition, 0, n)
	for i := range n {
		out = append(out, segkafka.Partition{Topic: topic, ID: i})
	}
	return out
}

func TestCountPartitions(t *testing.T) {
	t.Parallel()

	got := countPartitions(append(parts("nexus.async", 8), parts("nexus.async.dlq", 4)...))

	if got["nexus.async"] != 8 {
		t.Errorf("nexus.async = %d, want 8", got["nexus.async"])
	}
	if got["nexus.async.dlq"] != 4 {
		t.Errorf("nexus.async.dlq = %d, want 4", got["nexus.async.dlq"])
	}
	if len(got) != 2 {
		t.Errorf("топиков в результате %d, want 2", len(got))
	}
}

// TestLogTopicState_ReportsActual — боевой случай 07.08.2026: партиции
// увеличены брокерной командой до 8, в конфиге осталось 4. Лог обязан
// показать ФАКТ; раньше печаталось значение конфига, и строка
// «partitions=4» уводила разбор в сторону.
func TestLogTopicState_ReportsActual(t *testing.T) {
	t.Parallel()

	log := newRecordingLogger()
	logTopicState(stubPartitionReader{parts: parts("nexus.async", 8)},
		testCfg(4, 4), log, []string{"nexus.async"})

	if len(log.infos) != 1 {
		t.Fatalf("info-записей %d, want 1", len(log.infos))
	}
	if got := log.infos[0].attrs["partitions"]; got != "8" {
		t.Errorf("в логе partitions=%q, а у топика их 8", got)
	}

	if len(log.warns) != 1 {
		t.Fatalf("расхождение факта с конфигом обязано попасть в warn, got %d", len(log.warns))
	}
	w := log.warns[0].attrs
	if w["partitions_actual"] != "8" || w["partitions_configured"] != "4" {
		t.Errorf("warn не называет обе величины: %+v", w)
	}
	// Число consumer-инстансов в том же предупреждении: именно оно решает,
	// сколько партиций реально обслуживается (активны min(instances, partitions)).
	if w["consumer_instances"] != "4" {
		t.Errorf("warn обязан назвать consumer_instances, got %+v", w)
	}
}

// TestLogTopicState_QuietWhenMatches — при совпадении предупреждать не о чем:
// warn на каждом старте обесценивает сигнал.
func TestLogTopicState_QuietWhenMatches(t *testing.T) {
	t.Parallel()

	log := newRecordingLogger()
	logTopicState(stubPartitionReader{parts: parts("nexus.async", 8)},
		testCfg(8, 8), log, []string{"nexus.async"})

	if len(log.warns) != 0 {
		t.Fatalf("warn при совпадении не нужен, got %d", len(log.warns))
	}
	if got := log.infos[0].attrs["partitions"]; got != "8" {
		t.Errorf("partitions=%q, want 8", got)
	}
}

// TestLogTopicState_ReadFailureDoesNotBreakStart — метаданные не прочитались:
// топики уже созданы, и старт сервиса не должен падать из-за диагностики.
// В этом случае честно помечаем, что напечатано значение КОНФИГА.
func TestLogTopicState_ReadFailureDoesNotBreakStart(t *testing.T) {
	t.Parallel()

	log := newRecordingLogger()
	logTopicState(stubPartitionReader{err: errors.New("broker unavailable")},
		testCfg(8, 8), log, []string{"nexus.async", "nexus.async.dlq"})

	if len(log.infos) != 2 {
		t.Fatalf("info-записей %d, want 2 (по одной на топик)", len(log.infos))
	}
	if _, ok := log.infos[0].attrs["partitions"]; ok {
		t.Error("при провале чтения нельзя выдавать конфиг за факт: ключ должен быть partitions_configured")
	}
	if got := log.infos[0].attrs["partitions_configured"]; got != "8" {
		t.Errorf("partitions_configured=%q, want 8", got)
	}
	if len(log.warns) != 1 {
		t.Fatalf("провал чтения метаданных обязан быть виден, got %d warn", len(log.warns))
	}
}
