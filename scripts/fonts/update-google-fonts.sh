#!/usr/bin/env bash
# scripts/fonts/update-google-fonts.sh — запускать из КОРНЯ репозитория.
#
# §89.1: перекачивает woff2-сабсеты Inter/JetBrains Mono из Google Fonts в
# web-ui/src/assets/fonts и генерирует рядом fonts.css с @font-face.
# Каталог woff2 перед записью очищается — «хвостов» от прошлых прогонов не
# остаётся, результат воспроизводим.
#
# Зеркало для Windows — update-google-fonts.ps1 (та же логика, тот же вывод).
set -euo pipefail

DEST="${1:-web-ui/src/assets/fonts}"
# Сабсеты, которые реально нужны двуязычному (ru/en) интерфейсу. greek/greek-ext/
# vietnamese Google отдаёт тоже, но вес в бинаре платился бы всегда, а глифы не
# понадобятся никогда.
SUBSETS="latin latin-ext cyrillic cyrillic-ext"

# UA ОБЯЗАТЕЛЕН. На дефолтном UA (curl/PowerShell) Google отдаёт ответ для
# древних браузеров: пять @font-face с .ttf, без woff2, без unicode-range и без
# деления на сабсеты. Проверяется guard'ом ниже — по unicode-range, а не по
# коду ответа: там тоже 200.
UA="Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
CSS_URL="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600&family=JetBrains+Mono:wght@400;500&display=swap"

# Свой цикл ретраев, а не флаги curl: за корпоративным MITM-прокси разовый
# «self signed certificate in certificate chain» — обычное дело, а `--retry`
# такие ошибки не повторяет вовсе; `--retry-all-errors`, который повторил бы,
# появился только в curl 7.71, тогда как в Git for Windows ездит 7.67.
fetch() {
    local url="$1" out="$2" i
    for i in 1 2 3; do
        if curl -sSfL -A "$UA" "$url" -o "$out"; then
            return 0
        fi
        sleep 1
    done
    echo "не удалось скачать $url" >&2
    return 1
}

mkdir -p "$DEST"
rm -f "$DEST"/*.woff2

fetch "$CSS_URL" "$DEST/google-css2.txt"
if ! grep -q 'unicode-range' "$DEST/google-css2.txt"; then
    echo "Google вернул ответ без unicode-range — проверьте User-Agent (см. комментарий выше)." >&2
    exit 1
fi

records=$(mktemp)
groups=$(mktemp)
trap 'rm -f "$records" "$groups"' EXIT

# Каждый блок Google'а — комментарий с именем сабсета, затем @font-face, у
# которого unicode-range идёт последней строкой; на ней и выплёвываем запись.
awk '
/^\/\* / { subset = $2; next }
/font-family:/ { if (match($0, /'"'"'[^'"'"']+'"'"'/)) family = substr($0, RSTART + 1, RLENGTH - 2) }
/font-weight:/ { weight = $2; sub(/;/, "", weight) }
/src: url\(/ { if (match($0, /url\([^)]+\)/)) url = substr($0, RSTART + 4, RLENGTH - 5) }
/unicode-range:/ {
    r = $0
    sub(/^[ \t]*unicode-range:[ \t]*/, "", r)
    sub(/;[ \t]*$/, "", r)
    slug = tolower(family); gsub(/ +/, "-", slug)
    print subset "|" slug "|" family "|" weight "|" url "|" r
}
' "$DEST/google-css2.txt" | awk -F'|' -v want=" $SUBSETS " '
index(want, " " $1 " ") { print }
' > "$records"

# Inter и JetBrains Mono — ВАРИАТИВНЫЕ шрифты: на все запрошенные веса одного
# сабсета Google отдаёт ОДИН и тот же файл, а вес задаётся только объявлением
# font-weight (33 блока css2 ссылаются всего на 13 разных URL). Поэтому имя
# файла — <slug>-<subset>, без веса: иначе в репозитории лежали бы 12 побайтово
# одинаковых копий (626 КБ вместо 226 КБ), причём незаметно — Vite схлопывает
# их по хешу содержимого, и в выводе сборки перерасход не виден.
# Если Google когда-нибудь вернётся к статическим начертаниям, у группы станет
# больше одного URL — тогда вес попадает в имя, и файлы снова разъедутся.
awk -F'|' '{ key = $2 "|" $1; if (!seen[key "|" $5]++) n[key]++ } END { for (k in n) print k "|" n[k] }' \
    "$records" > "$groups"

OUT="$DEST/fonts.css"
{
    echo "/* СГЕНЕРИРОВАН scripts/fonts/update-google-fonts.{sh,ps1} — не править руками. */"
    echo "/* Источник: $CSS_URL */"
    echo "/* Сабсеты: ${SUBSETS// /, }. Сырой ответ Google — google-css2.txt. */"
    echo "/* Файл на сабсет один: шрифты вариативные, вес задаёт font-weight. */"
    echo
} > "$OUT"

while IFS='|' read -r subset slug family weight url range; do
    if [ "$(awk -F'|' -v k="$slug|$subset" '$1 "|" $2 == k { print $3 }' "$groups")" = "1" ]; then
        file="$slug-$subset.woff2"
    else
        file="$slug-$weight-$subset.woff2"
    fi
    [ -f "$DEST/$file" ] || fetch "$url" "$DEST/$file"
    {
        echo "@font-face {"
        echo "  font-family: \"$family\";"
        echo "  font-style: normal;"
        echo "  font-weight: $weight;"
        echo "  font-display: swap;"
        echo "  src: url(\"./$file\") format(\"woff2\");"
        echo "  unicode-range: $range;"
        echo "}"
        echo
    } >> "$OUT"
done < "$records"

count=$(find "$DEST" -maxdepth 1 -name '*.woff2' | wc -l | tr -d ' ')
bytes=$(find "$DEST" -maxdepth 1 -name '*.woff2' -printf '%s\n' | awk '{s += $1} END {print s + 0}')
echo "woff2: $count файл(ов), $((bytes / 1024)) КБ; @font-face: $(grep -c '@font-face' "$OUT")"
