# Gate WS - Сервис WebSocket Шлюза Сообщений

## Описание

**Gate WS** — это высокопроизводительный WebSocket-шлюз для отправки и получения сообщений в распределённой системе. Сервис обеспечивает маршрутизацию сообщений на основе иерархической структуры получателей (города, филиалы, сотрудники, устройства), поддерживает real-time коммуникацию через WebSocket и предоставляет REST API для отправки сообщений.

### Основные возможности

- 🔌 **WebSocket соединения** — real-time двусторонняя коммуникация
- 🛣️ **Умная маршрутизация** — отправка сообщений по городам, филиалам, сотрудникам и устройствам
- 🔐 **Аутентификация** — Basic Auth для REST API, Bearer токены для WebSocket
- 📊 **Метрики Prometheus** — встроенная поддержка сбора метрик
- 🗄️ **Несколько БД** — поддержка PostgreSQL и MSSQL
- ⚡ **Кэширование** — встроенная поддержка Redis и in-memory кэша
- 🔍 **Swagger документация** — автоматическая документация API

---

## Архитектура

```text
┌─────────────────────────────────────────────────────────────┐
│                    REST API / WebSocket                      │
│           (Port: 8090 по умолчанию)                         │
└────────────────────┬────────────────────────────────────────┘
                     │
        ┌────────────┴────────────┐
        │                         │
   ┌────▼──────┐         ┌───────▼────┐
   │  Handler  │         │  WebSocket │
   │  (REST)   │         │ Upgrader   │
   └────┬──────┘         └───────┬────┘
        │                        │
   ┌────▼────────────────────────▼────┐
   │      Service Layer               │
   │  ├─ GateService                  │
   │  ├─ WSService                    │
   │  └─ Hub (Message Hub)            │
   └────┬─────────────────────────────┘
        │
   ┌────▼────────────────────────────┐
   │    Storage Layer                │
   │  ├─ Database (PostgreSQL/MSSQL) │
   │  ├─ Cache (Redis/Memory)        │
   │  └─ Repository                  │
   └─────────────────────────────────┘
```

---

## Структура конфигурации

Конфиг находится в файле `config/config.yml` и содержит следующие параметры:

### Server

```yaml
Server:
  Port: "8090"  # Порт сервера
```

### Authorization (JWT)

```yaml
Authorization:
  JWT:
    Expiration: 24        # Время жизни токена в часах
    Secret: "your-secret" # Секретный ключ для подписи
```

### WebSocket

```yaml
WebSocket:
  Expiration: 5  # Время жизни соединения в минутах без активности
```

### Database (PostgreSQL или MSSQL)

```yaml
DataBase:
  Type: "postgres"              # Тип БД: "postgres" или "mssql"
  Host: ""                # Хост БД
  Port: 5432                    # Порт БД
  DBName: "bus"             # Название БД
  Credintials:
    UserName: ""          # Пользователь БД
    Password: ""          # Пароль БД
```

### Redis (опционально)

```yaml
Redis:
  Use: true                 # Использовать Redis
  Addr: ""                  # Адрес Redis (пусто = локально)
  DB: 3                     # Номер БД Redis
  Credintials:
    UserName: ""            # Пользователь Redis (опционально)
    Password: ""            # Пароль Redis (опционально)
```

### Metrics

```yaml
Metrics:
  Interval: 5  # Интервал сбора метрик в секундах
```

### Logging

```yaml
Log:
  Debug: false          # Режим отладки
  Level: 5              # Уровень логирования (2-5: ошибка, предупреждение, информация, дебаг)
  OutputInFile: true    # Логирование в файл
  Dir: "logs"           # Директория логов
```

### Sentry (опционально)

```yaml
Sentry:
  Use: false                    # Использовать Sentry
  Environment: "Production"     # Окружение
  AttachStacktrace: true        # Прикреплять stack trace
  TracesSampleRate: 1.0         # Доля трассируемых запросов
  EnableTracing: true           # Включить трассировку
  Debug: true                   # Отладка Sentry
```

---

## Переменные окружения (Environment Variables)

Приложение поддерживает переопределение некоторых параметров конфигурации через переменные окружения. Это удобно для развёртывания в контейнерах Docker и облачных платформах.

Переменные окружения загружаются из файла `.env` (если он существует) и переопределяют соответствующие значения из `config.yml`.

### Поддерживаемые переменные окружения

| Переменная | Описание | Значение по умолчанию | Пример |
| --- | --- | --- | --- |
| `SENTRY_DSN` | DSN для Sentry мониторинга ошибок | (пусто) | `https://key@sentry.io/project` |
| `JWT_SECRET` | Секретный ключ для подписи JWT токенов | (пусто) | `your-super-secret-key-123` |
| `REDIS_ADDR` | Адрес Redis сервера (хост:порт) | `localhost:6379` | `redis:6379` |
| `REDIS_USR` | Имя пользователя Redis | (пусто) | `default` |
| `REDIS_PWD` | Пароль Redis | (пусто) | `your-redis-password` |
| `DB_HOST` | Хост сервера базы данных | (значение из config.yml) | `db.example.com` |
| `DB_PORT` | Порт базы данных | (значение из config.yml) | `5432` |
| `DB_USR` | Имя пользователя БД | (значение из config.yml) | `postgres` |
| `DB_PWD` | Пароль БД | (значение из config.yml) | `secure-password` |

### Приоритет конфигурации

Значения загружаются в следующем порядке (более поздние переопределяют предыдущие):

1. **Переменные окружения** (`.env` файл или системные переменные)
2. **YAML конфигурация** (`config/config.yml`)

Это означает, что переменные окружения имеют наивысший приоритет и переопределяют значения из YAML конфигурации.

## HTTP Handlers (REST API)

### 1. Swagger документация

- **URL:** `GET /`
- **Описание:** Перенаправление на Swagger UI
- **Параметры:** нет
- **Ответ:** Перенаправление на `/swagger/index.html`

### 2. Metrics (Prometheus)

- **URL:** `GET /metrics`
- **Описание:** Метрики Prometheus для мониторинга
- **Параметры:** нет
- **Ответ:** Текстовый формат Prometheus метрик

---

### Gate API (`/api/gate/*`)

Требует **Basic Auth** (username:password)

#### 2.1 Send Message

- **URL:** `POST /api/gate/sendMessage`
- **Аутентификация:** Basic Auth
- **Описание:** Отправка сообщения получателям
- **Параметры (JSON body):**

  ```json
  {
    "date": "2025-01-26T10:30:00Z",  // Время сообщения (опционально)
    "message": "Текст сообщения",     // Текст сообщения
    "recipients": {
      "citys": [
        {
          "id": "city_001",
          "name": "Москва"
        }
      ],
      "branches": [
        {
          "id": "branch_001",
          "name": "Филиал №1"
        }
      ],
      "employees": [
        {
          "id": "emp_001",
          "name": "Иван Петров"
        }
      ],
      "devices": [
        {
          "id": "device_001"
        }
      ]
    }
  }
  ```

- **Ответ (200 OK):**

  ```json
  {
    "result": "ok"
  }
  ```

- **Ошибки:**
  - `400 Bad Request` — невалидный JSON
  - `401 Unauthorized` — неверные credentials
  - `500 Internal Server Error` — ошибка при отправке

---

### Users API (`/api/users/*`)

Требует **Basic Auth** (username:password)

#### 3.1 Create User

- **URL:** `POST /api/users/createUser`
- **Аутентификация:** Basic Auth
- **Описание:** Создание нового пользователя (только для админов)
- **Параметры (JSON body):**

  ```json
  {
    "username": "newuser",
    "password": "password123",
    "role": "admin"  // или "user"
  }
  ```

- **Ответ (200 OK):**

  ```json
  {
    "result": "user_id_here"
  }
  ```

- **Ошибки:**
  - `400 Bad Request` — невалидные данные
  - `401 Unauthorized` — неверные credentials
  - `403 Forbidden` — только администраторы могут создавать пользователей
  - `500 Internal Server Error` — ошибка создания

#### 3.2 Change User Password

- **URL:** `POST /api/users/changeUserPassword`
- **Аутентификация:** Basic Auth
- **Описание:** Изменение пароля пользователя
- **Параметры (JSON body):**

  ```json
  {
    "username": "existing_user",
    "new_password": "new_password123"
  }
  ```

- **Ответ (200 OK):**

  ```json
  {
    "result": "ok"
  }
  ```

- **Ошибки:**
  - `400 Bad Request` — невалидные данные
  - `401 Unauthorized` — неверные credentials
  - `500 Internal Server Error` — ошибка изменения

---

### WebSocket API (`/ws/*`)

#### 4.1 Authorization (Get Token)

- **URL:** `POST /ws/auth`
- **Аутентификация:** Basic Auth
- **Описание:** Получение JWT токена для подключения к WebSocket
- **Параметры (JSON body):**

  ```json
  {
    "date": "2025-01-26T10:30:00Z",  // Время (опционально)
    "id": "device_001",               // ID устройства
    "city": {
      "id": "city_001",
      "name": "Москва"
    },
    "branche": {
      "id": "branch_001",
      "name": "Филиал №1"
    },
    "employee": {
      "id": "emp_001",
      "name": "Иван Петров"
    }
  }
  ```

- **Ответ (200 OK):**

  ```json
  {
    "token": "eyJhbGciOiJIUzI1NiIs..."
  }
  ```

- **Ошибки:**
  - `400 Bad Request` — невалидные данные
  - `401 Unauthorized` — неверные credentials
  - `500 Internal Server Error` — ошибка создания токена

#### 4.2 WebSocket Connection

- **URL:** `GET /ws/conn`
- **Аутентификация:** Bearer Token (из `/ws/auth`)
- **Описание:** Установка WebSocket соединения
- **Параметры:**
  - Header: `Authorization: Bearer <token>`
- **Ответ:** WebSocket соединение
- **Ошибки:**
  - `401 Unauthorized` — отсутствует или невалидный токен
  - `500 Internal Server Error` — ошибка апгрейда

#### 4.3 Ping (Health Check)

- **URL:** `GET /ws/ping`
- **Описание:** Проверка статуса сервиса
- **Параметры:** нет
- **Ответ (200 OK):**

  ```json
  {
    "result": "pong"
  }
  ```

---

## Сборка и запуск

### Сборка для Windows

```powershell
# Используя Makefile
make build-win

# Или прямо go
go build .\app\cmd\bus
```

### Генерация Swagger документации

```powershell
swag init -g app/cmd/bus/main.go
```

### Варианты запуска

**В режиме отладки:**

```powershell
go run .\app\cmd\bus\main.go -config config/config_debug.yml -debug
```

**С production конфигом:**

```powershell
bus.exe -config config/config.yml
```

**Первоночальное заполнение БД и создание пользователя admin:**

```powershell
bus.exe -config config/config.yml -install
```

**Очистка БД:**

```powershell
bus.exe -config config/config.yml -uninstall
```

**Изменение пароля пользователя:**

```powershell
go run .\app\cmd\bus\main.go -config config/config_debug.yml -ChangeUserPassword
```

### Доступные флаги командной строки

- `-config <путь>` — путь к конфигу (по умолчанию: `config/config.yml`)
- `-debug` — режим отладки (использует `config_debug.yml`)
- `-install` — Первоночальное заполнение БД
- `-uninstall` — *Очистка БД
- `-ChangeUserPassword` — изменить пароль пользователя
- `-logdir <путь>` — переопределить директорию логов

---

## Middleware и Аутентификация

### Basic Auth Middleware

Проверяет учетные данные в заголовке `Authorization: Basic <base64(username:password)>`

- Извлекает username и password
- Проверяет учетные данные в БД
- Устанавливает в контекст: `userId`, `userRole`

### Bearer Token Middleware

Проверяет JWT токен в заголовке `Authorization: Bearer <token>`

- Валидирует подпись токена
- Проверяет срок действия
- Устанавливает в контекст: `deviceId`

---

## Структура проекта

```bus/
├── app/
│   ├── build/              # Информация о версии и сборке
│   ├── cmd/
│   │   └── bus/        # Точка входа приложения
│   └── internal/
│       ├── app/            # Структура приложения
│       ├── config/         # Парсинг конфигурации
│       ├── controller/     # HTTP контроллеры и WebSocket апгрейдер
│       ├── handler/        # HTTP обработчики (handlers)
│       ├── lib/            # Утилиты (кэширование, JWT, логирование)
│       ├── models/         # Структуры данных
│       ├── service/        # Бизнес-логика
│       ├── storage/        # Работа с БД и репозитории
│       └── terminal/       # Терминальные команды
├── config/                 # Конфигурационные файлы
├── docs/                   # Swagger документация
├── logs/                   # Логи (создается при запуске)
├── docker-compose.yml      # Docker Compose конфигурация
├── Makefile               # Команды сборки
└── README.md              # Этот файл
```

## Примеры использования

### Получение токена и подключение к WebSocket (Python)

```python
import requests
import json
from websocket import create_connection

# 1. Получить токен
auth = ("admin", "password")
response = requests.post(
    "http://localhost:8090/ws/auth",
    auth=auth,
    json={
        "id": "device_001",
        "city": {"id": "city_001", "name": "Москва"},
        "branche": {"id": "branch_001", "name": "Филиал №1"},
        "employee": {"id": "emp_001", "name": "Иван Петров"}
    }
)
token = response.json()["token"]

# 2. Подключиться к WebSocket
ws = create_connection(
    f"ws://localhost:8090/ws/conn",
    header={"Authorization": f"Bearer {token}"}
)

# 3. Получать сообщения
while True:
    data = ws.recv()
    print("Received:", data)
```

### Отправка сообщения (curl)

```bash
curl -X POST http://localhost:8090/api/gate/sendMessage \
  -H "Content-Type: application/json" \
  -H "Authorization: Basic YWRtaW46cGFzc3dvcmQ=" \
  -d '{
    "message": "Hello, world!",
    "recipients": {
      "citys": [{"id": "city_001"}],
      "branches": [],
      "employees": [],
      "devices": []
    }
  }'
```

## Логирование

Логи хранятся в директории, указанной в `config.Log.Dir` (по умолчанию: `logs/`).

Уровни логирования:

- **2** — Error (Ошибка)
- **3** — Warning (Предупреждение)
- **4** — Info (Информация)
- **5** — Debug (Дебаг)

---

## Мониторинг

Метрики Prometheus доступны на `http://localhost:8090/metrics`

Собираемые метрики:

- Количество активных WebSocket соединений
- Количество обработанных сообщений (TODO)
- Время ответа API (TODO)
- Количество ошибок (TODO)
