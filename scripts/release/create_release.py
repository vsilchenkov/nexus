#!/usr/bin/env python3
"""Создание GitLab Release для проекта Nexus (bus/nexus на gitlab.vozovoz.ru).

Аналог `create_github_release.py` (тот работает с зеркалом на GitHub): хост и путь
проекта определяются по git-remote (по умолчанию `origin`), поэтому скрипт не
привязан к адресу GitLab — переезд сервера меняет только remote. Токен —
personal access token с правом `api` — берётся из переменной окружения
`GITLAB_TOKEN`; без неё — токен, встроенный в URL remote (`oauth2:<token>@`,
старые клоны), иначе пароль хоста из хранилища учёток git — тот PAT, которым
идёт push (как в routix). Токен никуда не записывается и не печатается.
Deploy-токен (`gldt-…`, логин nexus-deploy) для API НЕ годится — им только
`git fetch`.

Тег должен уже существовать в GitLab (быть запушен). Тело релиза — markdown-файл
(секция CHANGELOG + блок обновления/отката по `release_notes_template.md`);
относительные ссылки GitLab резолвит сам, переписывать их не нужно.

Usage (GITLAB_TOKEN=... перед командой — только если учётки нет в хранилище git):
    python scripts/release/create_release.py v1.41.1 "v1.41.1" desc_1411.md

    # проверить доступ и увидеть, что получится, ничего не создавая:
    python scripts/release/create_release.py --check
    python scripts/release/create_release.py v1.41.1 "…" desc_1411.md --dry-run

    # поправить описание уже опубликованного релиза:
    python scripts/release/create_release.py v1.41.1 "…" desc_1411.md --update
"""
import argparse
import io
import json
import os
import re
import subprocess
import sys
import urllib.error
import urllib.parse
import urllib.request


def remote_url(remote: str) -> str:
    return subprocess.check_output(["git", "remote", "get-url", remote]).decode().strip()


def project_from_url(url: str) -> tuple[str, str]:
    """Возвращает (api_base, project_path) по URL remote — https или ssh."""
    patterns = (
        r"^https?://(?:[^@/]+@)?([^/:]+)(?::\d+)?/(.+?)(?:\.git)?/?$",
        r"^ssh://(?:[^@/]+@)?([^/:]+)(?::\d+)?/(.+?)(?:\.git)?/?$",
        r"^(?:[^@/]+@)?([^/:]+):(.+?)(?:\.git)?/?$",
    )
    for pat in patterns:
        m = re.match(pat, url)
        if m:
            return "https://%s/api/v4" % m.group(1), m.group(2)
    sys.exit("не удалось разобрать gitlab-проект из remote: %s" % url)


def token_from(url: str, base: str) -> str:
    """GITLAB_TOKEN из окружения, иначе oauth2-токен, встроенный в URL remote,
    иначе пароль хоста GitLab из хранилища учёток git."""
    env = os.environ.get("GITLAB_TOKEN", "")
    if env:
        return env
    m = re.search(r"oauth2:([^@]+)@", url)
    if m:
        return m.group(1)
    return token_from_credential_store(base)


def token_from_credential_store(base: str) -> str:
    """Пароль хоста GitLab из хранилища учёток git — тот PAT, которым идёт push
    (так же устроено в routix). Пустая строка, если учётки нет; ничего не
    спрашивает: интерактивный ввод выключен."""
    host = urllib.parse.urlsplit(base).hostname or ""
    filled = subprocess.run(
        ["git", "credential", "fill"],
        # Байты, а не text=True: на Windows текстовый режим шлёт \r\n, и git
        # молча не разбирает запрос.
        input=f"protocol=https\nhost={host}\n\n".encode(),
        capture_output=True,
        env={**os.environ, "GIT_TERMINAL_PROMPT": "0", "GCM_INTERACTIVE": "never"},
        check=False,
    ).stdout.decode()
    fields = dict(line.split("=", 1) for line in filled.splitlines() if "=" in line)
    return fields.get("password", "")


def api(base: str, method: str, path: str, token: str, payload: dict | None = None) -> dict:
    data = json.dumps(payload).encode("utf-8") if payload is not None else None
    req = urllib.request.Request(base + path, data=data, method=method)
    req.add_header("User-Agent", "nexus-release-script")
    if token:
        req.add_header("PRIVATE-TOKEN", token)
    if data is not None:
        req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req, timeout=60) as r:
            return json.load(r)
    except urllib.error.HTTPError as e:
        body = e.read().decode("utf-8", "replace")
        sys.exit("HTTP %s на %s %s: %s" % (e.code, method, path, body[:800]))


def main() -> None:
    p = argparse.ArgumentParser(description="создать GitLab Release из markdown-файла")
    p.add_argument("tag", nargs="?", help="тег, уже запушенный в GitLab")
    p.add_argument("name", nargs="?", help="заголовок релиза")
    p.add_argument("descfile", nargs="?", help="markdown-файл с телом релиза")
    p.add_argument("--remote", default="origin", help="git-remote GitLab (по умолчанию origin)")
    p.add_argument("--check", action="store_true", help="только проверить доступ к проекту")
    p.add_argument("--dry-run", action="store_true", help="показать, что будет отправлено, и выйти")
    p.add_argument("--update", action="store_true",
                   help="обновить уже опубликованный релиз этого тега вместо создания нового")
    args = p.parse_args()

    url = remote_url(args.remote)
    base, project = project_from_url(url)
    token = token_from(url, base)
    enc = urllib.parse.quote(project, safe="")

    if args.check:
        print("gitlab:     ", base)
        print("проект:     ", project)
        print("токен:      ", "передан" if token else "НЕ передан (для создания релиза нужен)")
        info = api(base, "GET", "/projects/" + enc, token)
        print("id проекта: ", info["id"])
        print("видимость:  ", info["visibility"])
        print("ветка:      ", info["default_branch"])
        return

    if not (args.tag and args.name and args.descfile):
        p.error("нужны tag, name и descfile (или --check)")
    if not token and not args.dry_run:
        sys.exit("нет токена: ни GITLAB_TOKEN, ни токена в URL remote, ни учётки GitLab "
                 "в хранилище git — без него релиз не создать")

    body = io.open(args.descfile, encoding="utf-8").read()
    if args.dry_run:
        print("gitlab:", base, "| проект:", project, "| тег:", args.tag, "| символов:", len(body))
        print("--- первые 600 символов тела ---")
        print(body[:600])
        return

    if args.update:
        rel = api(base, "PUT", "/projects/%s/releases/%s" % (enc, urllib.parse.quote(args.tag, safe="")),
                  token, {"name": args.name, "description": body})
        print("обновлён релиз", rel["tag_name"], "->", rel["_links"]["self"])
        return
    rel = api(base, "POST", "/projects/%s/releases" % enc, token,
              {"tag_name": args.tag, "name": args.name, "description": body})
    print("создан релиз", rel["tag_name"], "->", rel["_links"]["self"])


if __name__ == "__main__":
    main()
