<#
.SYNOPSIS
  Полное «погружение» в тестирование стенда Nexus: создаёт узлы всех типов и
  вариантов авторизации, гоняет по N запросов на каждый и печатает сводку.

.DESCRIPTION
  Часть docs/STAND_TESTING.md. Скрипт идемпотентен по именам путей узлов
  (повторный запуск переиспользует/пересоздаёт). Узлы НЕ удаляются после
  прогона — чтобы пользователь сам проверил их в UI, логах и метриках.

  Предпосылки:
    * Поднят стек (make docker-up) — единый вход Web на :8000.
    * Запущен сервис-получатель echosrv (go run ./cmd/echosrv) и доступен
      Receiver'у по адресу -EchoUrl.
    * Для RabbitMQAsync — поднят RabbitMQ (docker compose --profile stand up -d rabbitmq).

.PARAMETER WebUrl       Базовый адрес Web (единый вход). По умолчанию http://localhost:8000
.PARAMETER EchoUrl      Адрес echosrv, видимый Receiver'у. В docker — http://host.docker.internal:9999
.PARAMETER AdminPassword Пароль admin для логина в Web.
.PARAMETER Count        Сколько запросов слать на каждый HTTP-узел (по умолчанию 500).
.PARAMETER TeamSlug     Slug команды в URL (по умолчанию default).

.EXAMPLE
  ./seed_and_test.ps1 -AdminPassword 'secret' -EchoUrl 'http://host.docker.internal:9999'
#>
param(
  [string]$WebUrl = "http://localhost:8000",
  [string]$EchoUrl = "http://host.docker.internal:9999",
  [Parameter(Mandatory = $true)][string]$AdminPassword,
  [int]$Count = 500,
  [string]$TeamSlug = "default",
  [string]$AdminLogin = "admin"
)

$ErrorActionPreference = "Stop"
$api = "$WebUrl/api"
$session = New-Object Microsoft.PowerShell.Commands.WebRequestSession

function Login {
  Write-Host "==> Логин в Web ($api/auth/login)" -ForegroundColor Cyan
  $body = @{ login = $AdminLogin; password = $AdminPassword } | ConvertTo-Json
  Invoke-RestMethod -Uri "$api/auth/login" -Method Post -Body $body `
    -ContentType "application/json" -WebSession $session | Out-Null
}

# CreateNode — POST /api/nodes; при 409 (уже существует) ищет по списку и PUT-ит.
function CreateNode([hashtable]$node) {
  $json = $node | ConvertTo-Json -Depth 6
  try {
    $created = Invoke-RestMethod -Uri "$api/nodes" -Method Post -Body $json `
      -ContentType "application/json" -WebSession $session
    Write-Host ("  + создан узел {0} ({1}/{2})" -f $node.path, $node.root_method, $node.outgoing_method) -ForegroundColor Green
    return $created
  } catch {
    if ($_.Exception.Response.StatusCode.value__ -eq 409) {
      Write-Host ("  = узел {0} уже существует — пропуск" -f $node.path) -ForegroundColor Yellow
      return $null
    }
    throw
  }
}

# Каталог узлов: покрываем request/requestAsync, методы, проброс заголовков,
# все виды outgoing-auth и RabbitMQAsync. target_url указывает на echosrv с
# нужным режимом авторизации (/noauth, /basic, /token, /empty, /status/500).
function NodeCatalog {
  @(
    @{ path = "stand/req-noauth-post";   root_method = "request";      incoming_method = "POST"; outgoing_method = "POST"; url_mode = "static"; target_url = "$EchoUrl/noauth/echo"; auth_type = "none"; clickhouse_table = "stand_req_noauth_post" },
    @{ path = "stand/req-get";           root_method = "request";      incoming_method = "GET";  outgoing_method = "GET";  url_mode = "static"; target_url = "$EchoUrl/noauth/echo"; auth_type = "none"; clickhouse_table = "stand_req_get" },
    @{ path = "stand/req-put";           root_method = "request";      incoming_method = "PUT";  outgoing_method = "PUT";  url_mode = "static"; target_url = "$EchoUrl/noauth/echo"; auth_type = "none"; clickhouse_table = "stand_req_put" },
    @{ path = "stand/req-delete";        root_method = "request";      incoming_method = "DELETE"; outgoing_method = "DELETE"; url_mode = "static"; target_url = "$EchoUrl/noauth/echo"; auth_type = "none"; clickhouse_table = "stand_req_delete" },
    @{ path = "stand/req-basic";         root_method = "request";      incoming_method = "POST"; outgoing_method = "POST"; url_mode = "static"; target_url = "$EchoUrl/basic/echo"; auth_type = "basic"; auth_credentials = "user:pass"; clickhouse_table = "stand_req_basic" },
    @{ path = "stand/req-token";         root_method = "request";      incoming_method = "POST"; outgoing_method = "POST"; url_mode = "static"; target_url = "$EchoUrl/token/echo"; auth_type = "token"; auth_credentials = "secret-token"; clickhouse_table = "stand_req_token" },
    @{ path = "stand/req-fwd-headers";   root_method = "request";      incoming_method = "POST"; outgoing_method = "POST"; url_mode = "static"; target_url = "$EchoUrl/noauth/echo"; auth_type = "none"; forward_headers = @("X-Request-Id","X-Custom"); clickhouse_table = "stand_req_fwd"; log_headers = $true },
    @{ path = "stand/req-empty";         root_method = "request";      incoming_method = "POST"; outgoing_method = "POST"; url_mode = "static"; target_url = "$EchoUrl/empty/x"; auth_type = "none"; clickhouse_table = "stand_req_empty" },
    @{ path = "stand/req-token-from-req"; root_method = "request";     incoming_method = "POST"; outgoing_method = "POST"; url_mode = "static"; target_url = "$EchoUrl/token/echo"; auth_type = "token_from_request"; auth_dynamic_source = "header"; auth_dynamic_field = "X-Token"; auth_dynamic_strip_prefix = ""; clickhouse_table = "stand_req_tfr" },
    @{ path = "stand/async-noauth";      root_method = "requestAsync"; incoming_method = "POST"; outgoing_method = "POST"; url_mode = "static"; target_url = "$EchoUrl/noauth/echo"; auth_type = "none"; clickhouse_table = "stand_async_noauth" },
    @{ path = "stand/async-token";       root_method = "requestAsync"; incoming_method = "POST"; outgoing_method = "POST"; url_mode = "static"; target_url = "$EchoUrl/token/echo"; auth_type = "token"; auth_credentials = "secret-token"; clickhouse_table = "stand_async_token" }
  )
}

# RabbitMQAsync — отдельно: требует поднятого RabbitMQ. Создаём, но нагрузку
# на него льём публикацией в очередь (см. docs/STAND_TESTING.md), не HTTP.
function RmqNode {
  @{
    path = "stand/rmq-async"; root_method = "RabbitMQAsync"; outgoing_method = "POST";
    url_mode = "static"; target_url = "$EchoUrl/noauth/echo"; auth_type = "none";
    clickhouse_table = "stand_rmq";
    rmq_host = $env:RMQ_HOST; rmq_port = 5672; rmq_vhost = "/";
    rmq_user = ($env:RMQ_USER, "guest" -ne $null)[0]; rmq_password = ($env:RMQ_PASSWORD, "guest" -ne $null)[0];
    rmq_queue = "nexus.stand"; pull_interval_sec = 2; pull_batch_size = 100; pull_prefetch = 100
  }
}

# Fire — шлёт $Count запросов на HTTP-узел через единый вход и считает ответы.
function Fire([hashtable]$node) {
  $verb = if ($node.root_method -eq "request") { "request" } else { "requestAsync" }
  $url = "$WebUrl/api/v1/$verb/$TeamSlug/$($node.path)"
  $method = $node.incoming_method
  $ok = 0; $bad = 0; $firstBody = $null
  for ($i = 0; $i -lt $Count; $i++) {
    $headers = @{ "X-Request-Id" = "stand-$i"; "X-Custom" = "v$i"; "X-Token" = "Bearer dyn-$i" }
    try {
      $hasBody = $method -ne "GET"
      if ($hasBody) {
        $resp = Invoke-WebRequest -Uri $url -Method $method -Headers $headers `
          -Body (@{ n = $i; node = $node.path } | ConvertTo-Json) -ContentType "application/json" `
          -WebSession $session -UseBasicParsing
      } else {
        $resp = Invoke-WebRequest -Uri $url -Method $method -Headers $headers `
          -WebSession $session -UseBasicParsing
      }
      if ($resp.StatusCode -ge 200 -and $resp.StatusCode -lt 300) { $ok++ } else { $bad++ }
      if ($null -eq $firstBody) { $firstBody = $resp.Content }
    } catch {
      $bad++
      if ($null -eq $firstBody) { $firstBody = $_.Exception.Message }
    }
  }
  Write-Host ("  {0,-26} method={1,-6} ok={2,-5} bad={3}" -f $node.path, $method, $ok, $bad) `
    -ForegroundColor (if ($bad -eq 0) { "Green" } else { "Yellow" })
  if ($firstBody) { Write-Host ("      первый ответ: {0}" -f ($firstBody -replace "\s+", " ").Substring(0, [Math]::Min(160, $firstBody.Length))) -ForegroundColor DarkGray }
}

# --- main ---
Login

Write-Host "==> Создание узлов" -ForegroundColor Cyan
$nodes = NodeCatalog
foreach ($n in $nodes) { CreateNode $n | Out-Null }
if ($env:RMQ_HOST) { CreateNode (RmqNode) | Out-Null } else {
  Write-Host "  (RabbitMQAsync пропущен: задайте `$env:RMQ_HOST для его создания)" -ForegroundColor Yellow
}

Write-Host ("==> Нагрузка: по {0} запросов на HTTP-узел" -f $Count) -ForegroundColor Cyan
foreach ($n in $nodes) { Fire $n }

Write-Host ""
Write-Host "Готово. Узлы НЕ удалены — проверьте в UI ($WebUrl):" -ForegroundColor Cyan
Write-Host "  * счётчики/графики на главном экране и в каждом узле;" -ForegroundColor Gray
Write-Host "  * вкладку логов узла (ClickHouse): метод, тело, заголовки, IP (должен быть IPv4);" -ForegroundColor Gray
Write-Host "  * /metrics Receiver/Sender и панели /api/metrics/*;" -ForegroundColor Gray
Write-Host "  * полный адрес узла и кнопку «Скопировать» в форме/конфиге;" -ForegroundColor Gray
Write-Host "  * dry-run (скролл результата) и адаптивность при сужении окна." -ForegroundColor Gray
