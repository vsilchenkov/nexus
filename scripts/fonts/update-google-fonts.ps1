#Requires -Version 5.1
# scripts/fonts/update-google-fonts.ps1 — запускать из КОРНЯ репозитория.
#
# §89.1: перекачивает woff2-сабсеты Inter/JetBrains Mono из Google Fonts в
# web-ui/src/assets/fonts и генерирует рядом fonts.css с @font-face.
# Каталог woff2 перед записью очищается — «хвостов» от прошлых прогонов не
# остаётся, результат воспроизводим.
#
# Зеркало для linux/CI — update-google-fonts.sh (та же логика, тот же вывод).
[CmdletBinding()]
param(
    [string]$Dest = "web-ui/src/assets/fonts",
    # Сабсеты, которые реально нужны двуязычному (ru/en) интерфейсу.
    # greek/greek-ext/vietnamese Google отдаёт тоже, но вес в бинаре платился бы
    # всегда, а глифы не понадобятся никогда.
    [string[]]$Subsets = @("latin", "latin-ext", "cyrillic", "cyrillic-ext")
)
$ErrorActionPreference = "Stop"
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

# UA ОБЯЗАТЕЛЕН. На дефолтном UA Google отдаёт ответ для древних браузеров:
# пять @font-face с .ttf, без woff2, без unicode-range и без деления на
# сабсеты. Проверяется guard'ом ниже — по unicode-range, а не по коду ответа:
# там тоже 200.
$UA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
      "(KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
$CssUrl = "https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600" +
          "&family=JetBrains+Mono:wght@400;500&display=swap"

# Три попытки: за корпоративным MITM-прокси разовый сбой TLS — обычное дело.
function Get-WithRetry {
    param([string]$Url, [string]$OutFile)
    for ($i = 1; $i -le 3; $i++) {
        try {
            if ($OutFile) {
                Invoke-WebRequest -Uri $Url -UserAgent $UA -OutFile $OutFile -UseBasicParsing
                return
            }
            return (Invoke-WebRequest -Uri $Url -UserAgent $UA -UseBasicParsing).Content
        } catch {
            if ($i -eq 3) { throw }
            Start-Sleep -Seconds 1
        }
    }
}

New-Item -ItemType Directory -Force -Path $Dest | Out-Null
Get-ChildItem -Path $Dest -Filter *.woff2 -ErrorAction SilentlyContinue | Remove-Item -Force

$css = Get-WithRetry -Url $CssUrl
if ($css -notmatch 'unicode-range') {
    throw "Google вернул ответ без unicode-range — проверьте User-Agent (см. комментарий выше)."
}
Set-Content -Path (Join-Path $Dest "google-css2.txt") -Value $css -Encoding utf8

# Каждый блок Google'а — комментарий с именем сабсета, затем @font-face.
$rx = [regex]'(?s)/\*\s*(?<subset>[a-z\-]+)\s*\*/\s*@font-face\s*\{(?<body>.*?)\}'
$records = @()
foreach ($m in $rx.Matches($css)) {
    $subset = $m.Groups['subset'].Value
    if ($Subsets -notcontains $subset) { continue }
    $body   = $m.Groups['body'].Value
    $family = [regex]::Match($body, "font-family:\s*'([^']+)'").Groups[1].Value
    $records += [pscustomobject]@{
        Subset = $subset
        Family = $family
        Slug   = ($family -replace '\s+', '-').ToLower()   # inter / jetbrains-mono
        Weight = [regex]::Match($body, 'font-weight:\s*(\d+)').Groups[1].Value
        Url    = [regex]::Match($body, 'src:\s*url\(([^)]+)\)').Groups[1].Value
        Range  = [regex]::Match($body, 'unicode-range:\s*([^;]+)').Groups[1].Value.Trim()
    }
}

# Inter и JetBrains Mono — ВАРИАТИВНЫЕ шрифты: на все запрошенные веса одного
# сабсета Google отдаёт ОДИН и тот же файл, а вес задаётся только объявлением
# font-weight (33 блока css2 ссылаются всего на 13 разных URL). Поэтому имя
# файла — <slug>-<subset>, без веса: иначе в репозитории лежали бы 12 побайтово
# одинаковых копий (626 КБ вместо 226 КБ), причём незаметно — Vite схлопывает
# их по хешу содержимого, и в выводе сборки перерасход не виден.
# Если Google когда-нибудь вернётся к статическим начертаниям, у группы станет
# больше одного URL — тогда вес попадает в имя, и файлы снова разъедутся.
$multi = @{}
foreach ($g in $records | Group-Object { "$($_.Slug)|$($_.Subset)" }) {
    $multi[$g.Name] = (($g.Group | Select-Object -ExpandProperty Url -Unique) | Measure-Object).Count -gt 1
}

$out = New-Object System.Collections.Generic.List[string]
$out.Add("/* СГЕНЕРИРОВАН scripts/fonts/update-google-fonts.{sh,ps1} — не править руками. */")
$out.Add("/* Источник: $CssUrl */")
$out.Add("/* Сабсеты: $($Subsets -join ', '). Сырой ответ Google — google-css2.txt. */")
$out.Add("/* Файл на сабсет один: шрифты вариативные, вес задаёт font-weight. */")
$out.Add("")

foreach ($r in $records) {
    $file = if ($multi["$($r.Slug)|$($r.Subset)"]) {
        "$($r.Slug)-$($r.Weight)-$($r.Subset).woff2"
    } else {
        "$($r.Slug)-$($r.Subset).woff2"
    }
    $path = Join-Path $Dest $file
    if (-not (Test-Path $path)) { Get-WithRetry -Url $r.Url -OutFile $path }

    $out.Add("@font-face {")
    $out.Add("  font-family: `"$($r.Family)`";")
    $out.Add("  font-style: normal;")
    $out.Add("  font-weight: $($r.Weight);")
    $out.Add("  font-display: swap;")
    $out.Add("  src: url(`"./$file`") format(`"woff2`");")
    $out.Add("  unicode-range: $($r.Range);")
    $out.Add("}")
    $out.Add("")
}
Set-Content -Path (Join-Path $Dest "fonts.css") -Value ($out -join "`n") -Encoding utf8

Get-ChildItem $Dest -Filter *.woff2 | Measure-Object Length -Sum | ForEach-Object {
    "woff2: $($_.Count) файл(ов), $([math]::Round($_.Sum / 1KB)) КБ"
}
