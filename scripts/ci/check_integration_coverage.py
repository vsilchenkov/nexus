#!/usr/bin/env python3
"""Гейт: каждый integration-тест обязан попадать хотя бы в одну группу Makefile.

Зачем. `make test-integration` гоняет тесты под-прогонами с фильтрами `-run`,
а CI запускает пакет `tests/integration` ЦЕЛИКОМ. Расхождение означает
ложно-зелёный локальный гейт: тест, не попавший ни в один фильтр, локально не
выполняется вовсе, и падение всплывает уже на теге.

Ровно это и случилось при выпуске 1.28.0: 26 тестов из 145 не матчились ни
одним фильтром, среди них `TestOneTimeTokens_MigrationDownUp_E2E`, который
сломала новая миграция 0037 (§89.9).

Скрипт не требует ни Docker, ни сети — он читает Makefile и исходники тестов.
"""

from __future__ import annotations

import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parents[2]
TESTS_DIR = ROOT / "tests" / "integration"
MAKEFILE = ROOT / "Makefile"

TEST_FUNC_RE = re.compile(r"^func (Test\w+)\(", re.M)
RUN_FLAG_RE = re.compile(r'-run "([^"]+)"')


def test_names() -> list[tuple[str, str]]:
    out: list[tuple[str, str]] = []
    for path in sorted(TESTS_DIR.glob("*_test.go")):
        for name in TEST_FUNC_RE.findall(path.read_text(encoding="utf-8")):
            out.append((name, path.name))
    return out


def run_prefixes() -> set[str]:
    prefixes: set[str] = set()
    for group in RUN_FLAG_RE.findall(MAKEFILE.read_text(encoding="utf-8")):
        for alt in group.split("|"):
            prefixes.add(alt.lstrip("^"))
    return prefixes


def main() -> int:
    names = test_names()
    if not names:
        print("не найдено ни одного integration-теста — проверьте пути", file=sys.stderr)
        return 2

    prefixes = run_prefixes()
    uncovered = [(n, f) for n, f in names if not any(n.startswith(p) for p in prefixes)]

    if uncovered:
        print(
            f"{len(uncovered)} из {len(names)} integration-тестов не попадают ни в одну "
            f"группу `-run` Makefile — локально они НЕ выполняются, а CI гоняет пакет "
            f"целиком:",
            file=sys.stderr,
        )
        for name, fname in uncovered:
            print(f"  {name}  ({fname})", file=sys.stderr)
        print(
            "\nДобавьте их в подходящую цель test-int-* (или в test-int-misc).",
            file=sys.stderr,
        )
        return 1

    print(f"покрытие групп: все {len(names)} integration-тестов попадают в под-прогоны")
    return 0


if __name__ == "__main__":
    sys.exit(main())
