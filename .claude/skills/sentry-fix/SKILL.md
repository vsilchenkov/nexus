---
name: sentry-fix
description: Разбор и закрытие issue Sentry шины Nexus (sentry.vozovoz.ru, проект nexus id 52) готовым скриптом — список незакрытых за период, разбор issue по видам ошибок и тегам, классификация «баг / окно выката / уход клиента / инфраструктура», правка по слою, смена статуса issue после релиза. Используй, когда пользователь даёт ссылку на Sentry или NEXUS-xx, просит проанализировать ошибки Sentry или закрыть issue.
---

# Разбор ошибок Sentry (Nexus)

Адаптация скилла `sentry-fix` из проекта vika. 1С-специфика (журнал регистрации, BSL, пути
конфигурации) не перенесена. Где Sentry подключён в коде и какой контракт маскирования нельзя
ломать — скилл [nexus-sentry](../nexus-sentry/SKILL.md); здесь — порядок работы и скрипт.

## Скрипт

`scripts/sentry.ps1` (Windows PowerShell 5.1; файл сохранён в UTF-8 **с BOM** — без него 5.1 читает
его как cp1251 и падает на разборе). Токен: `-Token` → `$env:SENTRY_TOKEN` → память
`reference_sentry_api.md` этого репозитория. В репозиторий токен не кладётся: `.claude/skills` под
git.

```powershell
# незакрытые за 7 суток, по частоте
powershell -NoProfile -File .claude/skills/sentry-fix/scripts/sentry.ps1 -List -StatsPeriod 7d
# другой запрос / больше строк
powershell -NoProfile -File .claude/skills/sentry-fix/scripts/sentry.ps1 -List -Query "is:unresolved level:error" -Limit 50
# разбор issue: номер, ссылка из браузера или shortId
powershell -NoProfile -File .claude/skills/sentry-fix/scripts/sentry.ps1 -Issue NEXUS-T
powershell -NoProfile -File .claude/skills/sentry-fix/scripts/sentry.ps1 -Issue 160559 -Raw   # JSON целиком
# смена статуса — только по подтверждению пользователя
powershell -NoProfile -File .claude/skills/sentry-fix/scripts/sentry.ps1 -Issue NEXUS-T -Resolve resolvedInNextRelease
```

Вывод маскирует токены Telegram-ботов (`bot<id>:***`): до 1.36.0 они попадали в тег `node`, и сырые
значения тегов нельзя копировать в отчёты, коммиты и ТЗ.

## Порядок работы

1. **Список** за период пользователя (ссылка из интерфейса обычно с `statsPeriod=7d`).
2. **Разбор каждой issue** (`-Issue`): смотри не заголовок, а блок «виды ошибок в последних
   событиях» и распределение тегов (`service`, `environment`, `release`, `node`, `replica`).
3. **Классификация**:
   - **окно выката** — всплески `Unavailable`/`connection refused` в минуты рестарта сервиса;
     сверь время событий с выкатами (тег `release` меняется);
   - **уход клиента** — `context canceled`/`Canceled` при оборванном запросе; это не сбой шины;
   - **мусор на входе** — невалидные пути, кодировки, размеры: должно быть 4xx, а не 5xx;
   - **инфраструктура** — ClickHouse/Kafka/PostgreSQL конкретной ноды (тег `environment`: `Prod`,
     `KZ`), проверяется на сервере (скиллы `nexus-prod`, память по KZ);
   - **баг** — всё остальное.
4. **Правка по слою** (`handler → usecase → port → adapter`), тест красный на старом коде, гейты
   CLAUDE.md. Крупная доработка — новое ТЗ в `specs/sections/`.
5. **Коммит**: в теле — shortId issue (`NEXUS-T`) и что именно было причиной.
6. **После релиза** — спроси, закрыть ли issue как `resolvedInNextRelease` (скрипт `-Resolve`).
   Инфраструктурные — `resolved` только после проверки на сервере.

## Грабли

- **Sentry группирует по стеку, а не по тексту.** В одной issue бывают разные ошибки (`Unavailable`
  выката и `Canceled` ушедшего клиента в NEXUS-S). Счётчик issue — не счётчик одной причины.
- **`statsPeriod` проектного endpoint** — только `''`, `24h`, `14d` (иначе 400 «Invalid
  stats_period»). За 7 суток — организационный `/organizations/vzv/issues/?project=52`; скрипт
  ходит туда.
- **Discover-счётчики** без `event.type:error` включают транзакции (их на порядки больше).
- **Prod и KZ** живут на разных релизах — фильтруй по `environment`, прежде чем сравнивать.
- `Invoke-RestMethod` в PowerShell 5.1 декодирует ответ без charset как ISO-8859-1 (кракозябры в
  кириллице) — скрипт читает сырые байты как UTF-8.
- Только чтение (GET) без подтверждения; смена статуса issue — по явному «да» пользователя.
