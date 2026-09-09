#!/usr/bin/env python3
"""Создание GitHub Release для зеркала Nexus (github.com/vsilchenkov/nexus).

Аналог `create_release.py` (тот работает с GitLab), но GitHub не отдаёт API по
SSH-ключу — нужен personal access token с правом `Contents: write`. Токен берётся
из переменной окружения `GITHUB_TOKEN` и никуда не записывается.

Владелец и имя репозитория определяются по git-remote (по умолчанию `github`),
поэтому скрипт не привязан к конкретному зеркалу.

Тело релиза — markdown-файл. GitHub не резолвит относительные ссылки в описании
релиза, поэтому `--rewrite-relative` переписывает их на абсолютные: картинки — на
`raw.githubusercontent.com`, ссылки на файлы — на `github.com/<owner>/<repo>/blob`.

Usage:
    GITHUB_TOKEN=... python scripts/release/create_github_release.py \
        v1.35.1 "v1.35.1 — 100 разделов ТЗ" ANNIVERSARY.md [--rewrite-relative master]

    # проверить доступ и увидеть, что получится, ничего не создавая:
    python scripts/release/create_github_release.py --check
    GITHUB_TOKEN=... python scripts/release/create_github_release.py \
        v1.35.1 "…" ANNIVERSARY.md --rewrite-relative master --dry-run
"""
import argparse
import io
import json
import os
import re
import subprocess
import sys
import urllib.error
import urllib.request

API = "https://api.github.com"


def repo_slug(remote: str) -> str:
    """Возвращает `owner/repo` по URL указанного git-remote (ssh или https)."""
    url = subprocess.check_output(["git", "remote", "get-url", remote]).decode().strip()
    m = re.search(r"github\.com[:/]([^/]+/[^/]+?)(?:\.git)?$", url)
    if not m:
        sys.exit("не удалось разобрать github-репозиторий из remote %r: %s" % (remote, url))
    return m.group(1)


def rewrite_links(md: str, slug: str, ref: str) -> str:
    """Делает относительные ссылки абсолютными: описание релиза живёт вне дерева репозитория."""
    raw = "https://raw.githubusercontent.com/%s/%s/" % (slug, ref)
    blob = "https://github.com/%s/blob/%s/" % (slug, ref)

    def sub(m):
        bang, text, target = m.group(1), m.group(2), m.group(3).lstrip("./")
        if re.match(r"^(https?://|#|mailto:)", target):
            return m.group(0)
        base = raw if (bang or re.search(r"\.(png|jpe?g|gif|svg|webp)$", target, re.I)) else blob
        return "%s[%s](%s%s)" % (bang, text, base, target)

    return re.sub(r"(!?)\[([^\]]*)\]\(([^)\s]+)\)", sub, md)


def api(method: str, path: str, token: str, payload: dict | None = None) -> dict:
    data = json.dumps(payload).encode("utf-8") if payload is not None else None
    req = urllib.request.Request(API + path, data=data, method=method)
    req.add_header("Accept", "application/vnd.github+json")
    req.add_header("X-GitHub-Api-Version", "2022-11-28")
    req.add_header("User-Agent", "nexus-release-script")
    if token:
        req.add_header("Authorization", "Bearer " + token)
    if data is not None:
        req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req, timeout=60) as r:
            return json.load(r)
    except urllib.error.HTTPError as e:
        body = e.read().decode("utf-8", "replace")
        sys.exit("HTTP %s на %s %s: %s" % (e.code, method, path, body[:800]))


def main() -> None:
    p = argparse.ArgumentParser(description="создать GitHub Release из markdown-файла")
    p.add_argument("tag", nargs="?", help="тег, уже запушенный в GitHub")
    p.add_argument("name", nargs="?", help="заголовок релиза")
    p.add_argument("descfile", nargs="?", help="markdown-файл с телом релиза")
    p.add_argument("--remote", default="github", help="git-remote зеркала (по умолчанию github)")
    p.add_argument("--rewrite-relative", metavar="REF",
                   help="переписать относительные ссылки на абсолютные для этой ветки/тега")
    p.add_argument("--check", action="store_true", help="только проверить доступ к репозиторию")
    p.add_argument("--dry-run", action="store_true", help="показать, что будет отправлено, и выйти")
    args = p.parse_args()

    slug = repo_slug(args.remote)
    token = os.environ.get("GITHUB_TOKEN", "")

    if args.check:
        info = api("GET", "/repos/" + slug, token)
        print("репозиторий:", info["full_name"])
        print("публичный:  ", not info["private"])
        print("ветка:      ", info["default_branch"])
        print("токен:      ", "передан" if token else "НЕ передан (для создания релиза нужен)")
        return

    if not (args.tag and args.name and args.descfile):
        p.error("нужны tag, name и descfile (или --check)")
    if not token and not args.dry_run:
        sys.exit("нет GITHUB_TOKEN в окружении — без него релиз не создать")

    body = io.open(args.descfile, encoding="utf-8").read()
    if args.rewrite_relative:
        body = rewrite_links(body, slug, args.rewrite_relative)
    if len(body) > 125000:
        sys.exit("тело релиза %d символов — GitHub принимает не больше 125000" % len(body))

    if args.dry_run:
        print("репозиторий:", slug, "| тег:", args.tag, "| символов:", len(body))
        print("--- первые 600 символов тела ---")
        print(body[:600])
        return

    rel = api("POST", "/repos/%s/releases" % slug, token, {
        "tag_name": args.tag,
        "name": args.name,
        "body": body,
        "draft": False,
        "prerelease": False,
    })
    print("создан релиз", rel["tag_name"], "->", rel["html_url"])


if __name__ == "__main__":
    main()
