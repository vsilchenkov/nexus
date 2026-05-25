## 12. Структура репозитория

```
/cmd
  /receiver           main для Receiver Service
  /sender             main для Sender Service
  /web                main для Web Service
  /loadtest           сценарный тест производительности (см. §10.2)
/internal
  /receiver           Gin handlers, auth, routing, кеш конфига
  /sender             gRPC server, Kafka consumer, HTTP-клиент, воркер-пул
  /config             загрузка YAML + env, валидация, перезагрузка
  /storage
    /postgres         pgxpool, миграции, репозитории
    /redis            обёртка над go-redis: кеш конфига, сессии, rate-limit, circuit-breaker
    /clickhouse       асинхронный логгер с буфером и файл-фоллбеком
    /kafka            producer + consumer обёртки
  /metrics            Prometheus instrumentation
  /web                handlers UI и REST API
  /logging            обёртки над github.com/vsilchenkov/logging
/proto                .proto файлы для gRPC
/migrations           SQL-миграции для PostgreSQL
/config
  config.yml          основной (в .gitignore)
  config_debug.yml    локальная отладка
  config.example.yml  шаблон с пояснениями (в git)
.env                  секреты (в .gitignore)
.env.example          шаблон секретов (в git)
/docs
  /receiver           swagger.json/yaml для Receiver
  /web                swagger.json/yaml для Web API
/web-ui               фронтенд (React/Vue/Svelte — на выбор)
/deploy
  /docker
    receiver.Dockerfile
    sender.Dockerfile
    web.Dockerfile
  docker-compose.yml
  docker-compose.dev.yml   override для локальной разработки
  prometheus.yml
  grafana/                 dashboards (опционально)
Makefile                Windows + Linux совместимый
README.md               описание сервиса, инструкции по запуску (см. §13.4)
TESTING.md              процедура запуска всех видов тестов (см. §13.4)
```

