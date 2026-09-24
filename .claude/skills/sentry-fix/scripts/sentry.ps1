<#
.SYNOPSIS
    Чтение issue Nexus из Sentry (sentry.vozovoz.ru) и смена их статуса.

.DESCRIPTION
    Токен — User Auth Token (НЕ DSN: DSN умеет только отправлять события).
    Порядок поиска: -Token, $env:SENTRY_TOKEN, память Claude Code этого
    репозитория (~/.claude/projects/<slug>/memory/reference_sentry_api.md).
    В репозиторий токен не кладётся: .claude/skills лежит под git.

    Вывод маскирует токены Telegram-ботов (bot<id>:<secret>): до 1.36.0 они
    попадали в тег node, и сырые значения тегов нельзя копировать дальше.

.EXAMPLE
    powershell -File .claude/skills/sentry-fix/scripts/sentry.ps1 -List -StatsPeriod 7d
    powershell -File .claude/skills/sentry-fix/scripts/sentry.ps1 -Issue NEXUS-T
    powershell -File .claude/skills/sentry-fix/scripts/sentry.ps1 -Issue "https://sentry.vozovoz.ru/organizations/vzv/issues/160559/?project=52"
    powershell -File .claude/skills/sentry-fix/scripts/sentry.ps1 -Issue 160559 -Raw
    powershell -File .claude/skills/sentry-fix/scripts/sentry.ps1 -Issue 160559 -Resolve resolvedInNextRelease
#>
[CmdletBinding()]
param(
    [string] $Issue,
    [switch] $List,
    [string] $Project = "52",
    [string] $Query = "is:unresolved",
    [string] $StatsPeriod = "14d",
    [int]    $Limit = 25,
    [ValidateSet("resolved", "resolvedInNextRelease", "ignored", "unresolved")]
    [string] $Resolve,
    [switch] $Raw,
    [string] $Token
)

$ErrorActionPreference = "Stop"
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

$baseUrl = "https://sentry.vozovoz.ru"
$org = "vzv"

function Find-MemoryToken {
    $root = (Resolve-Path (Join-Path $PSScriptRoot "..\..\..\..")).Path.TrimEnd("\")
    $slug = $root -replace '[:\\/_.]', '-'
    $file = Join-Path $HOME ".claude\projects\$slug\memory\reference_sentry_api.md"
    if (-not (Test-Path $file)) { return $null }
    $hit = Select-String -Path $file -Pattern 'sntryu_[0-9a-f]+' | Select-Object -First 1
    if ($hit) { return $hit.Matches[0].Value }
    return $null
}

if (-not $Token) { $Token = $env:SENTRY_TOKEN }
if (-not $Token) { $Token = Find-MemoryToken }
if (-not $Token) {
    Write-Error "Нет токена Sentry: передайте -Token, задайте SENTRY_TOKEN или сохраните его в памяти (reference_sentry_api.md). Нужен User Auth Token со скоупами event:read, project:read (для смены статуса — event:write)."
}
$headers = @{ Authorization = "Bearer $Token" }

function Hide-Secrets([string] $Text) {
    return ($Text -replace '(bot\d+):[A-Za-z0-9_-]{20,}', '$1:***')
}

function Out-Line([string] $Text) { Write-Output (Hide-Secrets $Text) }

# Invoke-RestMethod в Windows PowerShell 5.1 декодирует ответ без charset как
# ISO-8859-1 — кириллица превращается в кракозябры. Читаем сырые байты как UTF-8.
function Invoke-Sentry([string] $Path, [string] $Method = "GET", $Body = $null) {
    $params = @{ Uri = "$baseUrl/api/0$Path"; Headers = $headers; Method = $Method; UseBasicParsing = $true }
    if ($null -ne $Body) {
        $params.Body = [Text.Encoding]::UTF8.GetBytes(($Body | ConvertTo-Json -Depth 5))
        $params.ContentType = "application/json; charset=utf-8"
    }
    $resp = Invoke-WebRequest @params
    $text = [Text.Encoding]::UTF8.GetString($resp.RawContentStream.ToArray())
    if (-not $text) { return $null }
    return ($text | ConvertFrom-Json)
}

# Номер issue принимаем числом, ссылкой из браузера или shortId (NEXUS-T).
function Resolve-IssueId([string] $Value) {
    if ($Value -match "/issues/(\d+)") { return $Matches[1] }
    if ($Value -match '^\d+$') { return $Value }
    if ($Value -match '^[A-Za-z]+-[A-Za-z0-9]+$') {
        $sid = Invoke-Sentry "/organizations/$org/shortids/$($Value.ToUpper())/"
        return [string]$sid.groupId
    }
    Write-Error "Не понял номер issue: $Value"
}

# Нормализация текста ошибки для группировки: адреса, числа и UUID не должны
# дробить один вид ошибки на десятки строк.
function Get-ErrorKind([string] $Value) {
    $v = $Value -replace '\d{1,3}(\.\d{1,3}){3}(:\d+)?', '<addr>'
    $v = $v -replace '[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}', '<uuid>'
    $v = $v -replace '\d+(\.\d+)?', 'N'
    if ($v.Length -gt 140) { $v = $v.Substring(0, 140) }
    return $v
}

function Show-Issue([string] $Id) {
    $data = Invoke-Sentry "/issues/$Id/"
    $event = Invoke-Sentry "/issues/$Id/events/latest/"

    if ($Raw) {
        Out-Line ([pscustomobject]@{ issue = $data; event = $event } | ConvertTo-Json -Depth 12)
        return
    }

    Out-Line "== $($data.shortId) (id $Id): $($data.title)"
    Out-Line "culprit : $($data.culprit)"
    Out-Line "level   : $($data.level)   status: $($data.status)"
    Out-Line "события : $($data.count)"
    Out-Line "период  : $($data.firstSeen) .. $($data.lastSeen)"
    Out-Line "ссылка  : $($data.permalink)"
    Out-Line ""

    Out-Line "-- распределение тегов (все события issue)"
    foreach ($tag in (Invoke-Sentry "/issues/$Id/tags/")) {
        $top = ($tag.topValues | Select-Object -First 8 | ForEach-Object { "$($_.value)($($_.count))" }) -join ", "
        Out-Line ("   {0} : {1}" -f $tag.key, $top)
    }
    Out-Line ""

    # Sentry группирует по стеку, а не по тексту: в одной issue бывают разные
    # ошибки (Unavailable при выкате и Canceled от ушедшего клиента). Поэтому
    # смотрим виды ошибок по последним событиям, а не только latest.
    $recent = Invoke-Sentry "/issues/$Id/events/?full=true"
    $kinds = @{}
    foreach ($ev in $recent) {
        $exc = $ev.entries | Where-Object { $_.type -eq "exception" } | Select-Object -First 1
        $value = if ($exc) { ($exc.data.values | Select-Object -Last 1).value } else { $ev.message }
        $k = Get-ErrorKind ([string]$value)
        if ($kinds.ContainsKey($k)) { $kinds[$k]++ } else { $kinds[$k] = 1 }
    }
    Out-Line "-- виды ошибок в последних $(@($recent).Count) событиях"
    $kinds.GetEnumerator() | Sort-Object Value -Descending | ForEach-Object { Out-Line ("   {0,5}  {1}" -f $_.Value, $_.Key) }
    Out-Line ""

    Out-Line "-- событие $($event.eventID) от $($event.dateCreated)"
    if ($event.message) { Out-Line $event.message }
    foreach ($t in $event.tags) { Out-Line ("   {0} = {1}" -f $t.key, $t.value) }

    foreach ($entry in $event.entries) {
        switch ($entry.type) {
            "exception" {
                foreach ($v in $entry.data.values) {
                    Out-Line ""
                    Out-Line "-- exception $($v.type): $($v.value)"
                    foreach ($f in $v.stacktrace.frames) {
                        if ($f.inApp) { Out-Line ("     {0}:{1} {2}" -f $f.filename, $f.lineNo, $f.function) }
                    }
                }
            }
            "breadcrumbs" {
                Out-Line ""
                Out-Line "-- breadcrumbs (последние 10)"
                $entry.data.values | Select-Object -Last 10 | ForEach-Object { Out-Line ("   {0} {1} {2}" -f $_.timestamp, $_.category, $_.message) }
            }
            default {
                Out-Line ""
                Out-Line "-- entry: $($entry.type)"
                Out-Line ($entry.data | ConvertTo-Json -Depth 6)
            }
        }
    }
}

if ($Issue) {
    $id = Resolve-IssueId $Issue
    if ($Resolve) {
        Invoke-Sentry "/issues/$id/" "PUT" @{ status = $Resolve } | Out-Null
        Out-Line "Issue $id -> $Resolve"
        return
    }
    Show-Issue $id
    return
}

if (-not $List) {
    Write-Error "Укажите -Issue <номер|ссылка|NEXUS-xx> или -List."
}

# Организационный endpoint: проектный принимает statsPeriod только ''/24h/14d,
# а ссылки из интерфейса Sentry обычно с 7d.
$q = [uri]::EscapeDataString($Query)
$issues = Invoke-Sentry "/organizations/$org/issues/?project=$Project&query=$q&statsPeriod=$StatsPeriod&sort=freq&limit=$Limit"
Out-Line ("{0,-10} {1,8} {2,7}  {3,-20}  {4}" -f "shortId", "id", "count", "lastSeen", "title")
foreach ($i in $issues) {
    Out-Line ("{0,-10} {1,8} {2,7}  {3,-20}  {4}" -f $i.shortId, $i.id, $i.count, $i.lastSeen.ToString().Substring(0, [Math]::Min(19, $i.lastSeen.ToString().Length)), $i.title)
    Out-Line ("{0,-39}  culprit: {1}" -f "", $i.culprit)
}
