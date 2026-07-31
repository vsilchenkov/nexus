# syntax=docker/dockerfile:1.7
# Apache Kafka + Prometheus JMX-агент (§75).
#
# Зачем свой образ. Размер топика на диске Kafka отдаёт ТОЛЬКО через JMX
# (MBean `kafka.log:type=Log,name=Size,topic=…,partition=…`): админ-протокол
# высокоуровневого segmentio/kafka-go не знает DescribeLogDirs, а kafka_exporter
# размера не экспортирует вовсе. Агент грузится в процесс брокера и сам отдаёт
# /metrics на 7071 — это снимает нужду в JMX RMI (в docker он требует
# фиксации java.rmi.server.hostname и второго порта, промах выглядит как
# «подключились, метрик нет») и в стороннем контейнере экспортёра (официального
# образа jmx_exporter не существует, только jar).
#
# Агент активируется переменной KAFKA_OPTS в compose — образ сам по себе
# остаётся обычным брокером:
#   KAFKA_OPTS: "-javaagent:/opt/jmx/agent.jar=7071:/opt/jmx/kafka.yml"
#
# Версия. На Maven Central проект публикуется по 1.0.1 включительно; более
# свежие (1.6.0 на июль 2026) выкладываются только ассетами GitHub Releases
# (`.../releases/download/v<версия>/jmx_prometheus_javaagent-<версия>.jar`).
# Дефолт — Maven Central: он проксируется корпоративными зеркалами (Nexus/
# Artifactory) и доступен в закрытых контурах, где GitHub может быть закрыт.
# Для одного правила Log Size разницы между 1.0.1 и 1.6.0 нет.
#
# JMX_AGENT_URL — точка подмены (зеркало, свежая версия с GitHub, локальный
# файл): docker compose build --build-arg JMX_AGENT_URL=<url> kafka
FROM apache/kafka:3.9.0

ARG JMX_AGENT_VERSION=1.0.1
ARG JMX_AGENT_URL=https://repo1.maven.org/maven2/io/prometheus/jmx/jmx_prometheus_javaagent/${JMX_AGENT_VERSION}/jmx_prometheus_javaagent-${JMX_AGENT_VERSION}.jar

# Каталог создаётся отдельным слоем от root, потому что оба грабля здесь
# молчаливые и оба валят брокер на старте («Error opening zip file or JAR
# manifest missing», а не «нет прав»):
#   1) `ADD --chmod=444 … /opt/jmx/agent.jar` применяет права и к СОЗДАВАЕМОМУ
#      каталогу — /opt/jmx остаётся без бита `x` и становится непроходимым;
#   2) `ADD <url>` без --chmod кладёт скачанный файл с правами 0600 (владелец
#      root), а брокер работает от appuser и прочитать jar не может.
# Отсюда: mkdir отдельно, --chmod=0644 только на файлы, USER возвращается.
USER root
RUN mkdir -p /opt/jmx
ADD --chmod=0644 ${JMX_AGENT_URL} /opt/jmx/agent.jar
COPY --chmod=0644 deploy/kafka-jmx.yml /opt/jmx/kafka.yml
USER appuser
