#!/usr/bin/env python3
"""Строка «Откат» для CHANGELOG: сколько миграций отделяет две версии (§74.6).

В аварии оператору нужно одно число — сколько миграций откатывать, — и знать,
что при этом теряется. Считать это по `git ls-tree` в момент инцидента поздно,
поэтому значение готовится на выпуске релиза (DEPLOYMENT.md §9.5) и попадает в
CHANGELOG.

Usage:
    python scripts/release/rollback_info.py <от-тега> [<до-тега>]

    <от-тега>  предыдущая (та, на которую откатываются), напр. v1.20.2
    <до-тега>  выпускаемая; по умолчанию HEAD

Пример:
    python scripts/release/rollback_info.py v1.20.2 v1.21.0

Скрипт читает каталог migrations/ в обоих ревизиях через `git ls-tree` (рабочее
дерево не трогается) и помечает миграции, чей down теряет данные — по наличию
DROP TABLE / DROP COLUMN / DELETE FROM в *.down.sql. Эвристика намеренно грубая:
её задача — заставить автора релиза посмотреть на такие миграции глазами, а не
классифицировать за него.
"""
import re
import subprocess
import sys

DESTRUCTIVE = re.compile(r"\b(DROP\s+TABLE|DROP\s+COLUMN|DELETE\s+FROM)\b", re.IGNORECASE)


def git(*args: str) -> str:
    return subprocess.check_output(["git", *args], text=True, encoding="utf-8")


def migrations_at(rev: str) -> dict[int, str]:
    """{версия: имя up-файла} в каталоге migrations/ указанной ревизии."""
    try:
        listing = git("ls-tree", "--name-only", rev, "migrations/")
    except subprocess.CalledProcessError:
        sys.exit(f"ревизия не найдена: {rev}")
    out: dict[int, str] = {}
    for path in listing.splitlines():
        name = path.split("/")[-1]
        if not name.endswith(".up.sql"):
            continue
        try:
            out[int(name.split("_", 1)[0])] = name
        except ValueError:
            sys.exit(f"неожиданное имя миграции: {name}")
    return out


def down_body(rev: str, up_name: str) -> str:
    down = up_name.replace(".up.sql", ".down.sql")
    try:
        return git("show", f"{rev}:migrations/{down}")
    except subprocess.CalledProcessError:
        return ""


def main() -> None:
    # Консоль Windows по умолчанию cp1251 — вывод со стрелкой и длинным тире
    # (они же идут в CHANGELOG) на ней падает с UnicodeEncodeError.
    if hasattr(sys.stdout, "reconfigure"):
        sys.stdout.reconfigure(encoding="utf-8")  # type: ignore[attr-defined]

    if len(sys.argv) not in (2, 3):
        sys.exit("usage: rollback_info.py <от-тега> [<до-тега>]")
    prev, cur = sys.argv[1], (sys.argv[2] if len(sys.argv) == 3 else "HEAD")

    before, after = migrations_at(prev), migrations_at(cur)
    added = sorted(set(after) - set(before))
    removed = sorted(set(before) - set(after))

    print(f"{prev} → {cur}")
    if removed:
        # Миграции не удаляют — если это случилось, номера разошлись, и число
        # «откатить N» посчитано неверно.
        print(f"ВНИМАНИЕ: в {cur} исчезли миграции {removed} — проверьте историю вручную")

    if not added:
        print("\nСтрока для CHANGELOG:")
        print("**Откат:** без отката схемы (миграций нет).")
        return

    print(f"\nДобавлено миграций: {len(added)}")
    lossy: list[str] = []
    for version in added:
        name = after[version]
        marks = []
        body = down_body(cur, name)
        if not body.strip():
            marks.append("НЕТ down-файла")
            lossy.append(name)
        elif DESTRUCTIVE.search(body):
            marks.append("down теряет данные")
            lossy.append(name)
        suffix = f"  <-- {', '.join(marks)}" if marks else ""
        print(f"  {version:04d}  {name}{suffix}")

    span = f"{added[0]:04d}" if len(added) == 1 else f"{added[0]:04d}–{added[-1]:04d}"
    line = f"**Откат:** {len(added)} миграци{'я' if len(added) == 1 else 'и'} ({span})"
    if lossy:
        line += "; down теряет данные — уточните, какие"
    print("\nСтрока для CHANGELOG (уточните, что именно теряется):")
    print(line + ".")


if __name__ == "__main__":
    main()
